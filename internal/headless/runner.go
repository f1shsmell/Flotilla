package headless

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// ErrTimeout reports a run the caller's deadline ended. It is separate from
// errFailed because the two need different discipline: a timeout says nothing
// about whether the agent's work landed, and a caller that treats it as "did
// not happen" risks doing it twice.
var ErrTimeout = errors.New("headless: the run exceeded its deadline")

// maxLineBytes bounds one line of an agent's stdout. Both dialects write one
// JSON object per line, and a tool result can be large, so this is generous
// rather than tight — but it is bounded, because an agent that prints a line
// with no newline must not be able to exhaust this process's memory.
const maxLineBytes = 8 << 20

// Runner executes one unattended run of a dialect. It is a struct rather than a
// package function so a test can substitute the process: the parse path is what
// the projection tests cover, and spawning a real agent is not something a unit
// test may do.
type Runner struct {
	// LookPath resolves the dialect's command. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// Now reads the clock. Nil uses time.Now.
	Now func() time.Time
}

// Run starts the dialect, feeds the projection line by line, and waits.
//
// The returned Result is filled even when the error is non-nil: what an agent
// printed before it failed is still what it did, and a caller that dropped it
// would have no way to say why the run failed.
func (r Runner) Run(ctx context.Context, d Dialect, t Task) (Result, error) {
	args, err := d.argv(t)
	if err != nil {
		return Result{}, err
	}

	bin := d.Command
	if r.LookPath != nil {
		resolved, err := r.LookPath(bin)
		if err != nil {
			return Result{}, fmt.Errorf("headless: %s is not on PATH: %w", bin, err)
		}
		bin = resolved
	}

	if t.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = t.Dir
	cmd.Env = t.Env
	// A prompt is the run's only input, and it arrives as an argument. Closing
	// stdin stops the agent from waiting on a pipe nobody will write to: codex
	// reads stdin when it is not told otherwise.
	cmd.Stdin = nil
	// stderr is kept out of the projection but not thrown away: a crash explains
	// itself there rather than on stdout.
	var stderr limitedBuffer

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("headless: take the agent's stdout: %w", err)
	}
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("headless: start %s: %w", bin, err)
	}

	proj, err := newProjector(d.Parser)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Result{}, err
	}

	// The projection runs on its own goroutine with no lock shared with the
	// wait: a line that takes long to parse must not stop this process from
	// reaping the child, and a child that dies mid-line must still leave the
	// lines it wrote before it.
	var (
		res  Result
		perr error
		wg   sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		perr = projectStream(stdout, proj, &res)
	}()

	// The stream is drained before the process is reaped. Wait closes the pipe
	// StdoutPipe handed over, so reaping first would cut a line the agent had
	// already written — and the agent's last line is exactly the one a caller
	// needs. Draining first is safe even on a hang: the deadline kills the
	// process, which closes the pipe, which ends the read.
	wg.Wait()
	waitErr := cmd.Wait()

	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return res, fmt.Errorf("%w (it said: %s)", ErrTimeout, summarize(stderr.String()))
	}
	if perr != nil && waitErr == nil {
		return res, perr
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			return res, fmt.Errorf("headless: %s exited %d: %s", d.Name, exit.ExitCode(), summarize(stderr.String()))
		}
		return res, fmt.Errorf("headless: wait for %s: %w", d.Name, waitErr)
	}
	return res, nil
}

// projectStream reads complete lines and hands each to the projector. A final
// line with no newline is still a line: an agent that exits right after writing
// its result must not lose it.
func projectStream(r io.Reader, proj projector, res *Result) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for scanner.Scan() {
		if err := proj.project(scanner.Bytes(), res); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		// A read that fails because the process died is not a projection
		// failure. The wait reports the death; this reports a stream this build
		// could not read, which is worth saying once rather than twice.
		if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("headless: read the agent's output: %w", err)
	}
	return nil
}

// stderrLimit bounds how much of an agent's stderr is kept for the failure
// report. The tail is what explains a crash; the head is usually a banner.
const stderrLimit = 4 << 10

// limitedBuffer keeps the last stderrLimit bytes written to it.
type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= stderrLimit {
		b.buf.Reset()
		b.buf.Write(p[n-stderrLimit:])
		return n, nil
	}
	if b.buf.Len()+n > stderrLimit {
		kept := b.buf.Bytes()[b.buf.Len()-(stderrLimit-n):]
		discarded := append([]byte(nil), kept...)
		b.buf.Reset()
		b.buf.Write(discarded)
	}
	b.buf.Write(p)
	return n, nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// summarize renders a process's stderr as one line fit for an error message.
// The whole point of keeping it is that a reader can act on it, so the newlines
// inside go: an error that spans lines gets cut wherever it is displayed.
func summarize(s string) string {
	fields := bytes.Fields([]byte(s))
	if len(fields) == 0 {
		return "no error output"
	}
	joined := bytes.Join(fields, []byte(" "))
	if len(joined) > 400 {
		return string(joined[:400]) + "…"
	}
	return string(joined)
}

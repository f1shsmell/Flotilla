package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// waitFor bounds every wait in this file. It stops a hung test from hanging the
// run; no assertion depends on a timeout to pass.
const waitFor = 20 * time.Second

// restarts is the acceptance number from the milestone plan: ten daemon deaths
// must not cost a session.
const restarts = 10

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// helperCommand re-runs this test binary as the fake agent.
func helperCommand(t *testing.T) (string, []string) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	return exe, []string{"-test.run=TestHelperEchoAgent", "-test.v=false"}
}

// newHost takes a session the way a session host does, in this process.
func newHost(t *testing.T, dir, id, token string) *host.Host {
	t.Helper()

	command, args := helperCommand(t)
	h, err := host.Start(context.Background(), host.Config{
		ID:       session.ID(id),
		Token:    token,
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	return h
}

// TestTenDaemonRestartsKeepTheSession is the W1-2 acceptance check: ten daemon
// lifetimes in a row, one session, and the agent never restarts. The daemons
// here are values rather than processes, which is what makes this fast; the
// process-level version lives in the end-to-end test.
func TestTenDaemonRestartsKeepTheSession(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	h := newHost(t, dir, "sess-survivor", token)

	reg := session.NewRegistry(dir)
	rec, err := waitRecord(t, reg, "sess-survivor")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	firstPID := 0
	for i := 0; i < restarts; i++ {
		// A fresh daemon with no memory of the previous one, which is exactly
		// what a restart is.
		d := New(Config{
			Channel:  "test",
			Token:    token,
			StateDir: dir,
			Log:      discardLogger(),
		}, nil)

		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		err := d.Reattach(ctx)
		cancel()
		if err != nil {
			t.Fatalf("restart %d: Reattach: %v", i+1, err)
		}

		fleet := d.Fleet()
		if len(fleet.Attachments) != 1 {
			t.Fatalf("restart %d: fleet holds %d sessions, want 1 (failures: %v)",
				i+1, len(fleet.Attachments), fleet.Failures)
		}

		att := fleet.Attachments[0]
		if att.Info.ID != "sess-survivor" {
			t.Fatalf("restart %d: attached to session %q", i+1, att.Info.ID)
		}
		if firstPID == 0 {
			firstPID = att.Info.PID
		} else if att.Info.PID != firstPID {
			t.Fatalf("restart %d: host pid changed from %d to %d: the session was restarted, not reattached",
				i+1, firstPID, att.Info.PID)
		}

		if att.Record.Endpoint != rec.Endpoint {
			t.Fatalf("restart %d: endpoint changed from %v to %v", i+1, rec.Endpoint, att.Record.Endpoint)
		}

		d.Detach()
	}

	// The agent answered after every restart, so it was the same live PTY
	// throughout rather than a process the daemon kept respawning.
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	output, err := talkToHost(ctx, t, dir, token, "after restarts")
	if err != nil {
		t.Fatalf("talk to the session after %d restarts: %v", restarts, err)
	}
	if !strings.Contains(output, "echo: after restarts") {
		t.Fatalf("the agent did not answer after %d restarts; output: %q", restarts, output)
	}

	if h.SessionID() != "sess-survivor" {
		t.Fatalf("host holds session %q", h.SessionID())
	}
}

// TestDaemonReportsSessionsItCanReach checks the status a client sees.
func TestDaemonReportsSessionsItCanReach(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-status", token)

	reg := session.NewRegistry(dir)
	if _, err := waitRecord(t, reg, "sess-status"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := New(Config{Channel: "test", Token: token, StateDir: dir, Log: discardLogger()}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if err := d.Reattach(ctx); err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	defer d.Detach()

	raw, err := json.Marshal(d.status())
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}

	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !st.Ready {
		t.Fatal("status reports not ready")
	}
	if len(st.Sessions) != 1 || st.Sessions[0].ID != "sess-status" {
		t.Fatalf("status = %+v, want the one live session", st.Sessions)
	}
	if !st.Sessions[0].Reachable {
		t.Fatalf("session %q reports unreachable", st.Sessions[0].ID)
	}
}

// TestAttachPrunesARecordWhoseHostIsGone pins the cleanup: a record left behind
// by a dead host must not read as a failure on every restart, forever.
func TestAttachPrunesARecordWhoseHostIsGone(t *testing.T) {
	dir := t.TempDir()
	reg := session.NewRegistry(dir)

	if err := reg.Publish(session.Record{
		ID:       "sess-gone",
		Endpoint: ipc.Address{Channel: "sess-gone", Name: "host"},
		PID:      deadPID(t),
		Started:  time.Now(),
	}); err != nil {
		t.Fatalf("publish a stale record: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	fleet, err := Attach(ctx, reg, "shared", dir, discardLogger(), attachTimeout, nil)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(fleet.Attachments) != 0 || len(fleet.Failures) != 0 {
		t.Fatalf("attach reported %d sessions and %d failures, want neither",
			len(fleet.Attachments), len(fleet.Failures))
	}
	if _, err := reg.Read("sess-gone"); !errors.Is(err, session.ErrNoRecord) {
		t.Fatalf("the stale record survived: %v", err)
	}
}

// deadPID returns the pid of a process this test has already reaped.
func deadPID(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestNoSuchTestRuns")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reap the throwaway process: %v", err)
	}
	if processAlive(pid) {
		t.Skipf("pid %d was reused before the check", pid)
	}
	return pid
}

// TestDaemonSurvivesAHostThatCannotBeReached pins the honest half of the
// boundary: a wedged host is reported, and the rest of the fleet still attaches.
func TestDaemonSurvivesAHostThatCannotBeReached(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-live", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-live"); err != nil {
		t.Fatalf("the live host never published a record: %v", err)
	}

	// A record whose host process is alive but whose endpoint answers nobody.
	// The pipe name is real, nothing listens.
	reg := session.NewRegistry(dir)
	if err := reg.Publish(session.Record{
		ID:       "sess-gone",
		Endpoint: ipc.Address{Channel: "sess-gone", Name: "host"},
		PID:      os.Getpid(),
		Started:  time.Now(),
	}); err != nil {
		t.Fatalf("publish an unreachable record: %v", err)
	}

	d := New(Config{Channel: "test", Token: token, StateDir: dir, Log: discardLogger()}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if err := d.Reattach(ctx); err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	defer d.Detach()

	fleet := d.Fleet()
	if len(fleet.Attachments) != 1 {
		t.Fatalf("attached %d sessions, want the one live host", len(fleet.Attachments))
	}
	if fleet.Attachments[0].Record.ID != "sess-live" {
		t.Fatalf("attached to %q, want sess-live", fleet.Attachments[0].Record.ID)
	}
	if len(fleet.Failures) != 1 {
		t.Fatalf("reported %d failures, want the one unreachable host", len(fleet.Failures))
	}
}

// TestReattachDoesNotStallOnAHostThatStopsAnswering pins the startup path: a
// host that accepts the pipe and then answers nothing must cost one bounded
// wait, not the whole daemon. Reattach runs before the daemon listens, so a
// stall here is a daemon that never becomes reachable.
func TestReattachDoesNotStallOnAHostThatStopsAnswering(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	addr := ipc.Address{Channel: "sess-wedged", Name: "host"}
	l, err := ipc.Listen(addr, dir)
	if err != nil {
		t.Fatalf("listen for the wedged host: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	// Accept, then say nothing: the dial succeeds and the handshake never does.
	heldc := make(chan net.Conn, 1)
	go func() {
		nc, err := l.Accept()
		if err == nil {
			heldc <- nc
		}
	}()
	t.Cleanup(func() {
		select {
		case nc := <-heldc:
			_ = nc.Close()
		default:
		}
	})

	if err := session.NewRegistry(dir).Publish(session.Record{
		ID:       "sess-wedged",
		Endpoint: addr,
		PID:      os.Getpid(),
		Started:  time.Now(),
	}); err != nil {
		t.Fatalf("publish the wedged record: %v", err)
	}

	d := New(Config{
		Channel:       "test",
		Token:         token,
		StateDir:      dir,
		Log:           discardLogger(),
		AttachTimeout: 200 * time.Millisecond,
	}, nil)

	// A hang guard, not a condition: the assertion is the elapsed time.
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	started := time.Now()
	if err := d.Reattach(ctx); err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("reattach waited %s on a host that answers nothing", elapsed.Round(time.Millisecond))
	}

	fleet := d.Fleet()
	if len(fleet.Attachments) != 0 || len(fleet.Failures) != 1 {
		t.Fatalf("fleet holds %d sessions and %d failures, want the one wedged host reported",
			len(fleet.Attachments), len(fleet.Failures))
	}
}

// talkToHost dials a session host and asks its agent to echo a line.
func talkToHost(ctx context.Context, t *testing.T, dir, token, line string) (string, error) {
	t.Helper()

	rec, err := session.NewRegistry(dir).Read(session.ID("sess-survivor"))
	if err != nil {
		return "", err
	}

	output := make(chan string, 64)
	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: token,
		Role:  "test",
		OnData: func(_ *ipc.Conn, stream uint32, payload []byte) {
			if stream == session.StreamOutput {
				output <- string(payload)
			}
		},
	})
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := conn.SendData(session.StreamInput, []byte(line+"\r\n")); err != nil {
		return "", err
	}

	var seen strings.Builder
	deadline := time.After(waitFor)
	for {
		select {
		case chunk := <-output:
			seen.WriteString(chunk)
			if strings.Contains(seen.String(), "echo: "+line) {
				return seen.String(), nil
			}
		case <-deadline:
			return seen.String(), errNoEcho
		}
	}
}

// waitRecord waits for a host to publish.
func waitRecord(t *testing.T, reg session.Registry, id session.ID) (session.Record, error) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if rec, err := reg.Read(id); err == nil {
			return rec, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return session.Record{}, session.ErrNoRecord
}

// errNoEcho reports an agent that never answered.
var errNoEcho = errors.New("the agent never echoed")

// TestSpawnFailsWhenTheHostDiesFirst pins the wait: a host that cannot start
// says so at once instead of leaving the caller to its deadline with no reason.
func TestSpawnFailsWhenTheHostDiesFirst(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}

	spawn := ExecSpawner(discardLogger())
	started := time.Now()
	_, err = spawn(context.Background(), SpawnRequest{
		Binary:   exe,
		Session:  "sess-dead",
		Mode:     session.ModePTY,
		Command:  "unused",
		Args:     []string{"-test.run=TestNoSuchTestRuns"},
		Channel:  "test",
		StateDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("a host that exits before publishing was reported as started")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the spawn waited %s on a host that was already gone", elapsed)
	}
	if !strings.Contains(err.Error(), "exited before it published") {
		t.Fatalf("error %q does not say the host died", err)
	}
}

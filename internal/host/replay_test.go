package host

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// collect returns a data sink that appends terminal output, plus a reader for
// what it has seen so far. The reader is only used from the test goroutine
// after the sink's last write, which the waits below establish.
func collect() (func(*ipc.Conn, uint32, []byte), func() string) {
	var seen strings.Builder
	return func(_ *ipc.Conn, stream uint32, payload []byte) {
			if stream == session.StreamOutput {
				seen.Write(payload)
			}
		}, func() string {
			return seen.String()
		}
}

// waitForText waits until read reports needle, or fails the test with what it
// did see. Output arrives on the connection's own goroutine, so a sentinel is
// what makes this a wait rather than a race.
func waitForText(t *testing.T, read func() string, needle string) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if strings.Contains(read(), needle) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never saw %q; output so far: %q", needle, read())
}

// TestDetachedTerminalKeepsItsOutput is the reason the buffer exists: while no
// window is attached the agent keeps producing output, and the client that
// comes back has to be shown it instead of a blank screen.
func TestDetachedTerminalKeepsItsOutput(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-replay",
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	go func() { _ = h.Serve(ctx) }()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-replay")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	// Output produced with nobody attached: this is what a window that switched
	// sessions leaves behind, and what the buffer has to hold.
	const missed = "output while nobody was watching\n"
	h.send(session.StreamOutput, []byte(missed))

	sink, read := collect()
	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		OnData: func(c *ipc.Conn, stream uint32, payload []byte) {
			sink(c, stream, payload)
		},
	})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	waitForText(t, read, missed)

	// A second gap, then a second client: the replay must still work after the
	// buffer has been drained once.
	conn.Close()
	time.Sleep(50 * time.Millisecond)

	const missedAgain = "output after the first replay\n"
	h.send(session.StreamOutput, []byte(missedAgain))

	sink2, read2 := collect()
	conn2, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		OnData: func(c *ipc.Conn, stream uint32, payload []byte) {
			sink2(c, stream, payload)
		},
	})
	if err != nil {
		t.Fatalf("dial the host again: %v", err)
	}
	t.Cleanup(conn2.Close)

	waitForText(t, read2, missedAgain)
	if strings.Contains(read2(), missed) {
		t.Fatalf("the second replay repeated the first one; saw %q", read2())
	}
}

// TestReplayIsBounded pins the buffer's cost: a terminal left running must not
// grow memory without limit, and what it keeps must start on a line boundary so
// a returning client is not fed the tail of a half-cut escape sequence.
//
// Trimming is in bulk: the buffer is allowed to pass the limit between trims,
// but never twice it, so memory is bounded while the memmove cost stays
// amortised.
func TestReplayIsBounded(t *testing.T) {
	h := &Host{}

	line := []byte(strings.Repeat("x", 99) + "\n")
	lines := 2*ptyBufferLimit/len(line) + 20
	for i := 0; i < lines; i++ {
		h.mu.Lock()
		h.bufferLocked(line)
		h.mu.Unlock()

		h.mu.Lock()
		size := len(h.out)
		h.mu.Unlock()
		if size > 2*ptyBufferLimit {
			t.Fatalf("buffer reached %d bytes after %d lines, past the %d hard bound",
				size, i+1, 2*ptyBufferLimit)
		}
	}

	h.mu.Lock()
	size := len(h.out)
	head := h.out[0]
	tail := h.out[len(h.out)-1]
	h.mu.Unlock()

	if size == 0 {
		t.Fatal("buffer trimmed everything, so a reattach would show nothing")
	}
	if head != 'x' {
		t.Fatalf("buffer starts with %q, want the head of a kept line", head)
	}
	if tail != '\n' {
		t.Fatalf("buffer ends with %q, want the tail of a kept line", tail)
	}
}

// TestAbandonedReplayDoesNotClaimTheNextOne pins the supersede path: when a
// client's replay fails after another client has taken over, the stale replay
// must not clear the new one's state nor push its undelivered bytes into the new
// client's queue, which would show the present and then the past.
func TestAbandonedReplayDoesNotClaimTheNextOne(t *testing.T) {
	h := &Host{}

	stale := &ipc.Conn{}
	current := &ipc.Conn{}

	h.mu.Lock()
	h.replayConn = current
	h.out = []byte("fresh output\n")
	h.mu.Unlock()

	h.abandonReplay(stale, []byte("stale output\n"))

	h.mu.Lock()
	owner := h.replayConn
	buffered := string(h.out)
	h.mu.Unlock()

	if owner != current {
		t.Fatal("a superseded replay cleared the live replay state")
	}
	if strings.Contains(buffered, "stale") {
		t.Fatalf("a superseded replay put its bytes into the live queue: %q", buffered)
	}
	if buffered != "fresh output\n" {
		t.Fatalf("live queue = %q, want the newer output untouched", buffered)
	}
}

// TestAbandonedReplayRestoresItsOwnBytes pins the other half: when the replay
// that fails is still the owner, what it could not deliver goes back to the
// front of the queue so the next client is not shown a screen that starts after
// it.
func TestAbandonedReplayRestoresItsOwnBytes(t *testing.T) {
	h := &Host{}

	conn := &ipc.Conn{}
	h.mu.Lock()
	h.replayConn = conn
	h.out = []byte("live\n")
	h.mu.Unlock()

	h.abandonReplay(conn, []byte("undelivered\n"))

	h.mu.Lock()
	owner := h.replayConn
	buffered := string(h.out)
	h.mu.Unlock()

	if owner != nil {
		t.Fatal("the failed replay still owns the queue")
	}
	if buffered != "undelivered\nlive\n" {
		t.Fatalf("queue = %q, want the undelivered bytes back in front", buffered)
	}
}

package host

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestAnAcpAgentThatDiesTakesItsSessionWithIt pins the fourth shape the
// keep-alive boundary must not allow: host up, agent gone. A prompt sent after
// the agent's death would fail inside the transport, so nobody would learn about
// it from the lifecycle — the session would read as whatever it last said while
// every turn failed.
//
// The agent is killed here instead of exiting on its own, which is the same
// observable event from the host's side: the process is gone and nobody asked
// the host to close.
func TestAnAcpAgentThatDiesTakesItsSessionWithIt(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)

	pid := h.acp.sess.PID()
	if pid == 0 {
		t.Fatal("the fake agent has no process id")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find the agent process %d: %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the agent: %v", err)
	}

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("the host kept serving after its agent died")
	}

	// The record goes with the host: a daemon that reattaches must not find a
	// session whose agent is already gone.
	//
	// The probe stats the directory rather than reading the record: on Windows a
	// read that races the host's own removal fails with a sharing violation and
	// keeps the file alive for the next attempt, which would turn this test into
	// the very race it is checking for.
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read the state directory: %v", err)
		}
		if len(entries) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	entries, _ := os.ReadDir(dir)
	t.Fatalf("the dead session left %v behind in %q", entries, dir)
}

// TestAnAgentThatDiesIsNotReportedAsDone pins the state an involuntary exit may
// not become: done claims a turn finished, and this build watched the process
// disappear rather than the agent declare an end.
func TestAnAgentThatDiesIsNotReportedAsDone(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	if got := h.CurrentState(); got == session.StateDone {
		t.Fatalf("a fresh session reports %q", got)
	}

	pid := h.acp.sess.PID()
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find the agent process %d: %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the agent: %v", err)
	}

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("the host kept serving after its agent died")
	}

	if got := h.CurrentState(); got == session.StateDone {
		t.Fatalf("an agent that died left the session reporting %q", got)
	}
}

// TestTheHostStillServesAfterAKnownAgentExit pins the other side of the watcher:
// a host that is closing because it was asked to must not turn that into a
// failure. Close is idempotent and the watcher only logs, so a normal stop stays
// a normal stop.
func TestTheHostStillServesAfterAKnownAgentExit(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("Close did not finish the host")
	}

	// A second Close is a no-op, which is what a watcher racing the user's own
	// stop relies on.
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestAnAgentThatExitsBeforeTheHostServesIsReported pins the startup path: an
// agent that dies during the first seconds must still be noticed, because the
// watcher starts before the accept loop.
func TestAnAgentThatExitsBeforeTheHostServesIsReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	binary := buildFakeAgent(t)

	h, err := Start(ctx, Config{
		ID:       "sess-early-exit",
		Mode:     session.ModeACP,
		Token:    "shared",
		StateDir: dir,
		Command:  binary,
		Dir:      dir,
		Policy:   acp.Auto,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Serve, kill the agent at once, and check that the death is noticed rather
	// than the host sitting on a session it cannot drive.
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()

	proc, err := os.FindProcess(h.acp.sess.PID())
	if err != nil {
		t.Fatalf("find the agent process: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the agent: %v", err)
	}

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("the host did not notice its agent dying at startup")
	}
}

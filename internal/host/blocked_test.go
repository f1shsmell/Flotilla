package host

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestAnAdapterPatternBlocksATerminalSession pins the whole path an adapter
// takes: the definition reaches the host, the read pump runs the detector, and a
// line the agent printed turns the session blocked. Without this the schema
// would be a declaration nothing acts on.
//
// The fake agent echoes what it is sent, so the prompt arrives as ordinary
// output — which is exactly what the detector sees in a real session.
func TestAnAdapterPatternBlocksATerminalSession(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-blocked",
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
		Adapter:  adapters.Adapter{Blocked: []string{"do you trust"}},
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	go func() { _ = h.Serve(ctx) }()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-blocked")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{Token: "shared", Role: "test"})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	if got := h.CurrentState(); got != session.StateUnknown {
		t.Fatalf("a fresh terminal session reports %q, want unknown", got)
	}

	if err := conn.SendData(session.StreamInput, []byte("Do you trust this folder?\r\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if h.CurrentState() == session.StateBlocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the session stayed %q after the agent printed a declared prompt", h.CurrentState())
}

// TestWithoutAPatternTheSameLineDoesNotBlock pins the off switch end to end: an
// agent that declared no patterns is never classified from its output, so the
// same line that blocked above leaves this session unknown.
func TestWithoutAPatternTheSameLineDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-unblocked",
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

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-unblocked")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{Token: "shared", Role: "test"})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	if err := conn.SendData(session.StreamInput, []byte("Do you trust this folder?\r\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}

	// Give the echo time to arrive and be read; the assertion is that it
	// changed nothing.
	time.Sleep(500 * time.Millisecond)
	if got := h.CurrentState(); got != session.StateUnknown {
		t.Fatalf("a session with no patterns reports %q, want unknown", got)
	}
}

// TestTypingClearsABlockedTerminalSession pins the way out: this build cannot
// see a terminal agent resume, so an answer moves the session to unknown rather
// than claiming work it cannot observe.
func TestTypingClearsABlockedTerminalSession(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-answer",
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
		Adapter:  adapters.Adapter{Blocked: []string{"do you trust"}},
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	go func() { _ = h.Serve(ctx) }()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-answer")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{Token: "shared", Role: "test"})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	if err := conn.SendData(session.StreamInput, []byte("Do you trust this folder?\r\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) && h.CurrentState() != session.StateBlocked {
		time.Sleep(10 * time.Millisecond)
	}
	if h.CurrentState() != session.StateBlocked {
		t.Fatalf("the session never blocked: %q", h.CurrentState())
	}

	// The user answers.
	if err := conn.SendData(session.StreamInput, []byte("y\r\n")); err != nil {
		t.Fatalf("send the answer: %v", err)
	}

	deadline = time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if h.CurrentState() != session.StateBlocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.CurrentState(); got != session.StateUnknown {
		t.Fatalf("after an answer the session reports %q, want unknown", got)
	}
}

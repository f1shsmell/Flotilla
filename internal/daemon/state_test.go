package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/session"
)

// startAutoACPHost takes an ACP session whose policy needs no answers. The
// lifecycle tests drive real turns, and a Supervised host would stop at the
// agent's permission question and wait for a client that is not there.
func startAutoACPHost(t *testing.T, dir, id, token string) *host.Host {
	t.Helper()

	h, err := host.Start(context.Background(), host.Config{
		ID:       session.ID(id),
		Mode:     session.ModeACP,
		Token:    token,
		StateDir: dir,
		Command:  buildFakeAgent(t),
		Dir:      dir,
		Policy:   acp.Auto,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the ACP host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	if _, err := waitRecord(t, session.NewRegistry(dir), session.ID(id)); err != nil {
		t.Fatalf("the ACP host never published a record: %v", err)
	}
	return h
}

// stateOf reads one session's lifecycle out of a status report.
func stateOf(t *testing.T, st Status, id session.ID) session.State {
	t.Helper()

	for _, entry := range st.Sessions {
		if entry.ID == id {
			return entry.State
		}
	}
	t.Fatalf("status does not list %q: %+v", id, st.Sessions)
	return ""
}

// waitForState polls status until the session reports want. It reads through a
// control connection of its own: a connection that has attached speaks for its
// session, so daemon.status on it would be forwarded to the host and refused.
func waitForState(t *testing.T, dir, token string, id session.ID, want session.State) {
	t.Helper()

	control := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	var last session.State
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		last = stateOf(t, fetchStatus(t, ctx, control), id)
		if last == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %q reports %q, want %q", id, last, want)
}

// TestStatusCarriesTheLifecycleTheHostReported pins the daemon's half of the
// fleet view: the host classifies the session, and a status poll answers with
// that classification instead of the one it had at attach time.
func TestStatusCarriesTheLifecycleTheHostReported(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startAutoACPHost(t, dir, "sess-life", token)
	runDaemon(t, dir, token)

	// A fresh session is unknown: nothing has classified it yet.
	control := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if got := stateOf(t, fetchStatus(t, ctx, control), "sess-life"); got != session.StateUnknown {
		t.Fatalf("a fresh session reports %q, want unknown", got)
	}

	client := dialDaemon(t, dir, token, nil, nil)
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-life"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// The turn ends as done, and the daemon must have heard it.
	waitForState(t, dir, token, "sess-life", session.StateDone)

	if _, err := client.Call(ctx, session.MethodSeen, struct{}{}); err != nil {
		t.Fatalf("session.seen: %v", err)
	}
	waitForState(t, dir, token, "sess-life", session.StateIdle)
}

// TestAnUnreachableSessionIsUnknownNotDone pins the rule that matters most for
// honesty: a host this daemon cannot reach has no lifecycle it can vouch for,
// so it reports unknown rather than repeating the last state it heard. A stale
// "done" badge would claim finished work nobody can confirm.
func TestAnUnreachableSessionIsUnknownNotDone(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	h := startAutoACPHost(t, dir, "sess-lost", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	// Give the session a known state, then take the host away.
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-lost"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitForState(t, dir, token, "sess-lost", session.StateDone)

	if err := h.Close(); err != nil {
		t.Fatalf("stop the host: %v", err)
	}

	// Either the row goes away (the host is gone, which is the stronger answer)
	// or it stays and must stop claiming done and reachable.
	control := dialDaemon(t, dir, token, nil, nil)
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		st := fetchStatus(t, ctx, control)
		found := false
		for _, entry := range st.Sessions {
			if entry.ID != "sess-lost" {
				continue
			}
			found = true
			if entry.State == session.StateDone {
				t.Fatalf("a session whose host is gone still reports done: %+v", entry)
			}
			if entry.Reachable {
				t.Fatalf("a session whose host is gone still reports reachable: %+v", entry)
			}
		}
		if !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRememberStateIgnoresASessionItDoesNotServe pins the guard on the hook: a
// stray state for an unknown session must not invent an attachment, because a
// row with no host behind it is exactly the ghost the fleet view must not show.
func TestRememberStateIgnoresASessionItDoesNotServe(t *testing.T) {
	d := New(Config{Channel: "test", Token: "shared", StateDir: t.TempDir(), Log: discardLogger()}, nil)

	d.rememberState("sess-ghost", session.StateDone)

	if got := len(d.Fleet().Attachments); got != 0 {
		t.Fatalf("a state for an unknown session created %d attachment(s)", got)
	}
}

// TestRememberStateRejectsAValueOutsideTheClosedSet pins the closed set: a host
// that sends a word this build does not know must not have it stored, because
// the UI would then render a state it cannot explain.
func TestRememberStateRejectsAValueOutsideTheClosedSet(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startAutoACPHost(t, dir, "sess-closed", token)
	d := runDaemon(t, dir, token)

	if got := len(d.Fleet().Attachments); got != 1 {
		t.Fatalf("the daemon attached to %d sessions, want 1", got)
	}

	d.rememberState("sess-closed", session.State("finished"))

	if got := d.Fleet().Attachments[0].Info.State; got != session.StateUnknown {
		t.Fatalf("a value outside the closed set was stored as %q", got)
	}
}

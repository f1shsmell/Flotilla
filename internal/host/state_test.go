package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// dialStates connects to the ACP host and reports every lifecycle change it
// sends. It exists because the state is an event: a test that only read
// CurrentState would pass while the client never learned anything.
func dialStates(
	t *testing.T,
	dir string,
	states chan<- session.State,
	onAsk func(session.PermissionQuestion),
) *ipc.Conn {
	t.Helper()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-acp")
	if err != nil {
		t.Fatalf("the ACP host never published a record: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		Handler: func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
			switch method {
			case session.MethodState:
				var update session.StateUpdate
				if err := json.Unmarshal(params, &update); err == nil && states != nil {
					states <- update.State
				}
			case session.MethodPermission:
				var q session.PermissionQuestion
				if err := json.Unmarshal(params, &q); err == nil && onAsk != nil {
					onAsk(q)
				}
			case session.MethodUpdate:
				// Updates are not this test's subject; dropping them keeps the
				// state channel from competing with a flood of agent output.
			default:
				return nil, ipc.ErrNoHandler
			}
			return struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatalf("dial the ACP host: %v", err)
	}
	t.Cleanup(conn.Close)

	return conn
}

// waitState reads until want arrives, failing with everything seen instead of
// hanging on a transition that never came.
func waitState(t *testing.T, states <-chan session.State, want session.State, seen *[]session.State) {
	t.Helper()

	deadline := time.After(waitFor)
	for {
		select {
		case got := <-states:
			*seen = append(*seen, got)
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("never saw state %q; saw %v", want, *seen)
		}
	}
}

// TestAFreshSessionIsUnknownNotIdle pins the default: a host that has not
// classified itself must not claim to be ready for input, because idle is what
// tells a caller it may prompt.
func TestAFreshSessionIsUnknownNotIdle(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	if got := h.CurrentState(); got != session.StateUnknown {
		t.Fatalf("a fresh session reports %q, want unknown", got)
	}
}

// TestAttachTellsTheClientTheStateItArrivedIn pins the first thing a window
// needs: a session that is already blocked must show as blocked on attach, not
// only after it changes again.
func TestAttachTellsTheClientTheStateItArrivedIn(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)

	states := make(chan session.State, 8)
	dialStates(t, dir, states, nil)

	select {
	case got := <-states:
		if got != session.StateUnknown {
			t.Fatalf("attach reported %q, want the current state unknown", got)
		}
	case <-time.After(waitFor):
		t.Fatal("attach never told the client what state the session is in")
	}
}

// TestATurnMovesTheStateFromWorkingToDone is the lifecycle a fleet view draws:
// a prompt makes the session working, and the agent stopping makes it done.
func TestATurnMovesTheStateFromWorkingToDone(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)

	states := make(chan session.State, 16)
	conn := dialStates(t, dir, states, nil)

	// Drain the state sent at attach so the assertions below start clean.
	waitState(t, states, session.StateUnknown, new([]session.State))

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("session.prompt: %v", err)
	}

	var seen []session.State
	waitState(t, states, session.StateWorking, &seen)
	waitState(t, states, session.StateDone, &seen)

	// The order is the assertion: done before working would mean the client was
	// told the turn finished before it started.
	if len(seen) < 2 || seen[0] != session.StateWorking {
		t.Fatalf("states arrived as %v, want working then done", seen)
	}
}

// TestAPermissionQuestionBlocksTheSession pins the state that makes the fleet
// useful: an agent waiting for an answer is blocked, not working.
func TestAPermissionQuestionBlocksTheSession(t *testing.T) {
	_, dir := startACPHost(t, acp.Supervised)

	states := make(chan session.State, 16)
	questions := make(chan session.PermissionQuestion, 4)
	conn := dialStates(t, dir, states, func(q session.PermissionQuestion) { questions <- q })

	waitState(t, states, session.StateUnknown, new([]session.State))

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "edit it"})
		done <- err
	}()

	var seen []session.State
	waitState(t, states, session.StateWorking, &seen)
	waitState(t, states, session.StateBlocked, &seen)

	select {
	case q := <-questions:
		if _, err := conn.Call(ctx, session.MethodPermissionReply, session.PermissionReply{
			ID:       q.ID,
			OptionID: q.Options[0],
		}); err != nil {
			t.Fatalf("session.permission.reply: %v", err)
		}
	case <-time.After(waitFor):
		t.Fatal("the agent asked for permission but the client never heard it")
	}

	// Answering resumes the turn, so the session is working again.
	waitState(t, states, session.StateWorking, &seen)

	if err := <-done; err != nil {
		t.Fatalf("the turn failed after the answer: %v", err)
	}
}

// TestSeenMovesDoneToIdle pins the seen semantics: the host reports done, and
// a client that has looked says so, which is the only thing that makes it idle.
func TestSeenMovesDoneToIdle(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)

	states := make(chan session.State, 16)
	conn := dialStates(t, dir, states, nil)

	waitState(t, states, session.StateUnknown, new([]session.State))

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("session.prompt: %v", err)
	}

	var seen []session.State
	waitState(t, states, session.StateDone, &seen)

	if _, err := conn.Call(ctx, session.MethodSeen, struct{}{}); err != nil {
		t.Fatalf("session.seen: %v", err)
	}

	waitState(t, states, session.StateIdle, &seen)
}

// TestAnUnchangedStateIsNotAnnounced pins the quiet path: the UI must not
// repaint for a state that did not change, and a duplicate would read as new
// activity in a log.
func TestAnUnchangedStateIsNotAnnounced(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)

	states := make(chan session.State, 16)
	conn := dialStates(t, dir, states, nil)

	waitState(t, states, session.StateUnknown, new([]session.State))

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	// A turn ends as done; marking it seen twice must produce one idle.
	if _, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("session.prompt: %v", err)
	}
	var seen []session.State
	waitState(t, states, session.StateDone, &seen)

	for i := 0; i < 3; i++ {
		if _, err := conn.Call(ctx, session.MethodSeen, struct{}{}); err != nil {
			t.Fatalf("session.seen: %v", err)
		}
	}

	waitState(t, states, session.StateIdle, &seen)

	select {
	case extra := <-states:
		t.Fatalf("an unchanged state was announced again as %q", extra)
	case <-time.After(300 * time.Millisecond):
		// Three seen calls, one idle: the rest were duplicates.
	}
}

package host

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// settle runs one turn to its end, which is where a reclaimable session sits.
func settle(t *testing.T, conn *ipc.Conn) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "work"}); err != nil {
		t.Fatalf("run a turn: %v", err)
	}
}

// TestReclaimRefusesAWorkingSession pins the first veto: a turn in flight is
// work this host cannot give up, whatever the clock says.
func TestReclaimRefusesAWorkingSession(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	h.mu.Lock()
	h.state = session.StateWorking
	h.mu.Unlock()

	resp := h.reclaim(session.ReclaimRequest{})
	if resp.Allow {
		t.Fatal("a working session was allowed to be reclaimed")
	}
	if resp.Reason != session.ReclaimBusy {
		t.Fatalf("reason = %q, want %q", resp.Reason, session.ReclaimBusy)
	}
}

// TestReclaimRefusesASessionWaitingOnAPerson pins the second veto: an answered
// question cannot be recovered from a reclaimed host.
func TestReclaimRefusesASessionWaitingOnAPerson(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	h.mu.Lock()
	h.state = session.StateBlocked
	h.mu.Unlock()

	resp := h.reclaim(session.ReclaimRequest{})
	if resp.Allow {
		t.Fatal("a blocked session was allowed to be reclaimed")
	}
	if resp.Reason != session.ReclaimWaiting {
		t.Fatalf("reason = %q, want %q", resp.Reason, session.ReclaimWaiting)
	}
}

// TestReclaimAllowsASettledQuietSession is the working case: the turn ended,
// nothing moved, and the threshold asks for no quiet period at all.
func TestReclaimAllowsASettledQuietSession(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)
	conn := dialHost(t, dir, nil, nil)
	settle(t, conn)

	if got := h.CurrentState(); got != session.StateDone {
		t.Fatalf("state after the turn = %q, want done", got)
	}
	if resp := h.reclaim(session.ReclaimRequest{}); !resp.Allow {
		t.Fatalf("a settled quiet session was refused: %+v", resp)
	}
}

// TestReclaimRefusesActivityInsideTheQuietPeriod is the threshold itself: the
// turn just ended, so a one-hour quiet period has not passed.
func TestReclaimRefusesActivityInsideTheQuietPeriod(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)
	conn := dialHost(t, dir, nil, nil)
	settle(t, conn)

	resp := h.reclaim(session.ReclaimRequest{IdleForMs: time.Hour.Milliseconds()})
	if resp.Allow {
		t.Fatal("activity inside the quiet period was reclaimed")
	}
	if resp.Reason != session.ReclaimActive {
		t.Fatalf("reason = %q, want %q", resp.Reason, session.ReclaimActive)
	}
}

// TestReclaimRefusesAChildStillWorking is the family rule: the parent's turn is
// over, but a child it registered is still working, so the family is not idle.
func TestReclaimRefusesAChildStillWorking(t *testing.T) {
	h, dir := startACPHostEnv(t, acp.Auto, 0,
		append(os.Environ(), "FLT_FAKE_SUBAGENT=running"))
	conn := dialHost(t, dir, nil, nil)
	settle(t, conn)

	if got := h.CurrentState(); got != session.StateDone {
		t.Fatalf("state after the turn = %q, want done", got)
	}
	resp := h.reclaim(session.ReclaimRequest{})
	if resp.Allow {
		t.Fatal("a family with a working child was reclaimed")
	}
	if resp.Reason != session.ReclaimChildWorking {
		t.Fatalf("reason = %q, want %q", resp.Reason, session.ReclaimChildWorking)
	}
}

// TestReclaimCountsAChildUpdateAsFamilyActivity pins the clock: an update under
// a child's own session id is a sign of life for the whole family.
func TestReclaimCountsAChildUpdateAsFamilyActivity(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	past := time.Now().Add(-2 * time.Hour)
	h.mu.Lock()
	h.lastActivity = past
	h.mu.Unlock()

	h.forwardUpdate(acp.Update{
		Kind:      acp.UpdateMessage,
		SessionID: "sess-child-1",
		Text:      "working in the child",
	})

	h.mu.Lock()
	got := h.lastActivity
	h.mu.Unlock()
	if !got.After(past) {
		t.Fatalf("a child update left the family clock at %v, want newer than %v", got, past)
	}
}

// TestReclaimRefusesAPromptAfterItAllowed pins the close's claim: once the host
// has agreed to go, a turn arriving in the gap is refused rather than run into
// a process that is already leaving.
func TestReclaimRefusesAPromptAfterItAllowed(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)
	conn := dialHost(t, dir, nil, nil)
	settle(t, conn)

	if resp := h.reclaim(session.ReclaimRequest{}); !resp.Allow {
		t.Fatalf("a settled quiet session was refused: %+v", resp)
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	_, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "after"})
	if err == nil {
		t.Fatal("a prompt ran on a host that had already agreed to close")
	}
	if got := ipc.CodeOf(err); got != ipc.CodeUnknownSession {
		t.Fatalf("prompt error code = %d, want %d", got, ipc.CodeUnknownSession)
	}
}

// TestReclaimClosesTheHostWhenItAllows pins the effect: an allow is the host
// taking itself down, so no caller has to send a second call to make it happen.
func TestReclaimClosesTheHostWhenItAllows(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)
	conn := dialHost(t, dir, nil, nil)
	settle(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	// The reply races the close it reports: losing the pipe here is the same
	// answer, so only an explicit refusal fails the test.
	if raw, err := conn.Call(ctx, session.MethodReclaim, session.ReclaimRequest{}); err == nil {
		var resp session.ReclaimResponse
		if err := json.Unmarshal(raw, &resp); err != nil || !resp.Allow {
			t.Fatalf("reclaim answered %s (err %v), want an allow", raw, err)
		}
	}

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("the host allowed the reclaim but never closed")
	}
}

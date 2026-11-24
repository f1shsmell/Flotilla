package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// waitForInbox polls the fleet until id appears in the inbox with kind, and
// returns that entry. The daemon learns a session's lifecycle from the host's
// stream, so the report it gives is eventually consistent with the turn.
func waitForInbox(t *testing.T, ctx context.Context, control *ipc.Conn, id session.ID, kind string) InboxEntry {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	var last []InboxEntry
	for time.Now().Before(deadline) {
		last = fetchStatus(t, ctx, control).Inbox
		for _, entry := range last {
			if entry.ID == id && entry.Kind == kind {
				return entry
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("inbox never listed %q as %q; last inbox = %+v", id, kind, last)
	return InboxEntry{}
}

// waitForNoInbox waits until id is gone from the inbox, which is what the
// client that looked at a finished turn should see.
func waitForNoInbox(t *testing.T, ctx context.Context, control *ipc.Conn, id session.ID) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	var last []InboxEntry
	for time.Now().Before(deadline) {
		last = fetchStatus(t, ctx, control).Inbox
		found := false
		for _, entry := range last {
			if entry.ID == id {
				found = true
				break
			}
		}
		if !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("inbox still listed %q; last inbox = %+v", id, last)
}

// TestInboxAggregatesWhatWaitsOnAPerson is the T2 acceptance check: a question
// from one session reaches both windows, and unread is per client — the window
// that looked clears its own entry and leaves the other window's alone.
func TestInboxAggregatesWhatWaitsOnAPerson(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-inbox", token)
	runDaemon(t, dir, token)

	questions := make(chan session.PermissionQuestion, 1)
	client := dialDaemonAs(t, dir, token, "window-a", func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
		if method == session.MethodPermission {
			var q session.PermissionQuestion
			if err := json.Unmarshal(params, &q); err == nil {
				questions <- q
			}
		}
		return struct{}{}, nil
	}, nil)

	windowA := dialDaemonAs(t, dir, token, "window-a", nil, nil)
	windowB := dialDaemonAs(t, dir, token, "window-b", nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-inbox"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	prompted := make(chan error, 1)
	go func() {
		_, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "edit it"})
		prompted <- err
	}()

	var q session.PermissionQuestion
	select {
	case q = <-questions:
	case <-ctx.Done():
		t.Fatal("the client never received the permission question")
	}

	// Both windows see the blocked session: waiting on an answer is a fact
	// about the session, not about who has read it.
	entry := waitForInbox(t, ctx, windowA, "sess-inbox", InboxInput)
	if entry.Title != q.Title {
		t.Fatalf("inbox title = %q, want the agent's question %q", entry.Title, q.Title)
	}
	waitForInbox(t, ctx, windowB, "sess-inbox", InboxInput)

	if _, err := client.Call(ctx, session.MethodPermissionReply, session.PermissionReply{ID: q.ID, OptionID: "allow"}); err != nil {
		t.Fatalf("answer the permission question: %v", err)
	}
	if err := <-prompted; err != nil {
		t.Fatalf("the turn after the answer: %v", err)
	}

	// The turn is now an unread finish in both windows.
	waitForInbox(t, ctx, windowA, "sess-inbox", InboxUnread)
	waitForInbox(t, ctx, windowB, "sess-inbox", InboxUnread)

	// One window looks: the entry clears for it alone.
	if _, err := client.Call(ctx, session.MethodSeen, struct{}{}); err != nil {
		t.Fatalf("session.seen: %v", err)
	}
	waitForNoInbox(t, ctx, windowA, "sess-inbox")
	waitForInbox(t, ctx, windowB, "sess-inbox", InboxUnread)
}

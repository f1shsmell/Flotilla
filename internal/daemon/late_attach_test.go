package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// collectQuestion reports every permission question a client connection
// receives, so a test can assert what one window was told.
func collectQuestion(ch chan<- session.PermissionQuestion) ipc.Handler {
	return func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
		if method == session.MethodPermission {
			var q session.PermissionQuestion
			if err := json.Unmarshal(params, &q); err == nil {
				ch <- q
			}
		}
		return struct{}{}, nil
	}
}

// TestALateWindowReceivesThePendingQuestion is the inbox click-through gate: a
// window that attaches after the agent asked must see the card it can answer.
func TestALateWindowReceivesThePendingQuestion(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-late", token)
	runDaemon(t, dir, token)

	firstQuestions := make(chan session.PermissionQuestion, 1)
	first := dialDaemonAs(t, dir, token, "window-a", collectQuestion(firstQuestions), nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := first.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-late"}); err != nil {
		t.Fatalf("attach the first window: %v", err)
	}

	prompted := make(chan error, 1)
	go func() {
		_, err := first.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "edit it"})
		prompted <- err
	}()

	var asked session.PermissionQuestion
	select {
	case asked = <-firstQuestions:
	case <-ctx.Done():
		t.Fatal("the attached window never received the question")
	}

	// A window that opens the inbox entry now attaches only after the question
	// was asked; the daemon must hand it the card it missed.
	lateQuestions := make(chan session.PermissionQuestion, 1)
	late := dialDaemonAs(t, dir, token, "window-b", collectQuestion(lateQuestions), nil)
	if _, err := late.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-late"}); err != nil {
		t.Fatalf("attach the late window: %v", err)
	}

	var replayed session.PermissionQuestion
	select {
	case replayed = <-lateQuestions:
	case <-ctx.Done():
		t.Fatal("the late window did not receive the pending question")
	}
	if replayed.ID != asked.ID || replayed.Title != asked.Title {
		t.Fatalf("replayed question = %+v, want %+v", replayed, asked)
	}

	if _, err := late.Call(ctx, session.MethodPermissionReply, session.PermissionReply{
		ID:       replayed.ID,
		OptionID: replayed.Options[0].ID,
	}); err != nil {
		t.Fatalf("answer from the late window: %v", err)
	}
	if err := <-prompted; err != nil {
		t.Fatalf("the turn after the answer: %v", err)
	}
}

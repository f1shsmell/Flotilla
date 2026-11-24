package host

import (
	"context"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestTwoQuestionsSurviveAnAttachAndTheLastAnswerResumes pins concurrent
// permission requests: answering one does not resume a turn another question
// still blocks, and a client that attaches later is handed every card. An agent
// can ask twice for parallel tool calls; losing the second leaves the turn
// waiting on a question no window can answer.
func TestTwoQuestionsSurviveAnAttachAndTheLastAnswerResumes(t *testing.T) {
	h, dir := startACPHost(t, acp.Supervised)

	firstStates := make(chan session.State, 16)
	firstQuestions := make(chan session.PermissionQuestion, 8)
	first := dialStates(t, dir, firstStates, func(q session.PermissionQuestion) { firstQuestions <- q })
	waitState(t, firstStates, session.StateUnknown, new([]session.State))

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	done := make(chan struct{}, 2)
	ask := func(title string) {
		go func() {
			_, _ = h.askClient(ctx, acp.PermissionRequest{
				Title: title,
				Kind:  "edit",
				Options: []acp.Option{
					{ID: "allow", Kind: "allow_once", Name: "Allow"},
					{ID: "reject", Kind: "reject_once", Name: "Reject"},
				},
			})
			done <- struct{}{}
		}()
	}
	ask("first")
	ask("second")

	asked := map[string]session.PermissionQuestion{}
	for len(asked) < 2 {
		select {
		case q := <-firstQuestions:
			asked[q.ID] = q
		case <-ctx.Done():
			t.Fatalf("the first client received %d of 2 questions", len(asked))
		}
	}

	// The daemon died and came back: the host keeps waiting and must hand the
	// new client every card, not only the one that arrived last.
	first.Close()
	secondStates := make(chan session.State, 16)
	secondQuestions := make(chan session.PermissionQuestion, 8)
	second := dialStates(t, dir, secondStates, func(q session.PermissionQuestion) { secondQuestions <- q })
	waitState(t, secondStates, session.StateBlocked, new([]session.State))

	replayed := map[string]session.PermissionQuestion{}
	for len(replayed) < 2 {
		select {
		case q := <-secondQuestions:
			replayed[q.ID] = q
		case <-ctx.Done():
			t.Fatalf("the new client received %d of 2 questions", len(replayed))
		}
	}
	for id := range asked {
		if _, ok := replayed[id]; !ok {
			t.Fatalf("question %q was not replayed; got %v", id, replayed)
		}
	}

	order := make([]session.PermissionQuestion, 0, 2)
	for _, q := range replayed {
		order = append(order, q)
	}

	// One answer is not the turn resuming: the second question still waits.
	if _, err := second.Call(ctx, session.MethodPermissionReply, session.PermissionReply{
		ID: order[0].ID, OptionID: "allow",
	}); err != nil {
		t.Fatalf("answer the first question: %v", err)
	}
	<-done
	if got := h.CurrentState(); got != session.StateBlocked {
		t.Fatalf("after one answer the session is %q, want still blocked", got)
	}

	if _, err := second.Call(ctx, session.MethodPermissionReply, session.PermissionReply{
		ID: order[1].ID, OptionID: "allow",
	}); err != nil {
		t.Fatalf("answer the second question: %v", err)
	}
	<-done
	waitState(t, secondStates, session.StateWorking, new([]session.State))
}

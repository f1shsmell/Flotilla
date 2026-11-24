package host

import (
	"context"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestAHostReplaysAPendingQuestionOnAttach pins the reconnect path: the agent
// asked before the client was replaced, and the new client must be able to
// answer. Without the replay a daemon restart leaves the turn blocked on a
// question no window can see.
func TestAHostReplaysAPendingQuestionOnAttach(t *testing.T) {
	_, dir := startACPHost(t, acp.Supervised)

	firstStates := make(chan session.State, 16)
	firstQuestions := make(chan session.PermissionQuestion, 2)
	first := dialStates(t, dir, firstStates, func(q session.PermissionQuestion) { firstQuestions <- q })

	waitState(t, firstStates, session.StateUnknown, new([]session.State))

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	go func() {
		_, _ = first.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "edit it"})
	}()

	waitState(t, firstStates, session.StateWorking, new([]session.State))
	waitState(t, firstStates, session.StateBlocked, new([]session.State))

	var asked session.PermissionQuestion
	select {
	case asked = <-firstQuestions:
	case <-ctx.Done():
		t.Fatal("the first client never received the question")
	}

	// The daemon died: the host keeps the session and the agent keeps waiting.
	first.Close()

	secondStates := make(chan session.State, 16)
	secondQuestions := make(chan session.PermissionQuestion, 2)
	second := dialStates(t, dir, secondStates, func(q session.PermissionQuestion) { secondQuestions <- q })

	waitState(t, secondStates, session.StateBlocked, new([]session.State))

	var replayed session.PermissionQuestion
	select {
	case replayed = <-secondQuestions:
	case <-ctx.Done():
		t.Fatal("the host did not replay the pending question to the new client")
	}
	if replayed.ID != asked.ID || replayed.Title != asked.Title {
		t.Fatalf("replayed question = %+v, want %+v", replayed, asked)
	}

	if _, err := second.Call(ctx, session.MethodPermissionReply, session.PermissionReply{
		ID:       replayed.ID,
		OptionID: replayed.Options[0].ID,
	}); err != nil {
		t.Fatalf("answer the replayed question: %v", err)
	}
	waitState(t, secondStates, session.StateDone, new([]session.State))
}

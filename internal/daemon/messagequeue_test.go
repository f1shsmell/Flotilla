package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// queueOf reads a session's message queue over the wire.
func queueOf(t *testing.T, ctx context.Context, client *ipc.Conn) session.QueueResponse {
	t.Helper()

	raw, err := client.Call(ctx, session.MethodQueue, struct{}{})
	if err != nil {
		t.Fatalf("session.queue: %v", err)
	}
	var got session.QueueResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	return got
}

// addQueued appends one message and returns the queue it joined.
func addQueued(t *testing.T, ctx context.Context, client *ipc.Conn, text string) session.QueueResponse {
	t.Helper()

	raw, err := client.Call(ctx, session.MethodQueueAdd, session.QueueAddRequest{Text: text})
	if err != nil {
		t.Fatalf("session.queue.add %q: %v", text, err)
	}
	var got session.QueueResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	return got
}

// queuedTexts is the order a test compares, without the ids the daemon gives.
func queuedTexts(q session.QueueResponse) []string {
	out := make([]string, 0, len(q.Messages))
	for _, message := range q.Messages {
		out = append(out, message.Text)
	}
	return out
}

// waitForTranscript polls a session's stored log until one user update carries
// want, and returns that log. The daemon stores what the host reports as it
// reports it, so a delivered turn lands in the log a moment after the prompt
// returns; the poll waits for the store rather than guessing at its pace.
func waitForTranscript(t *testing.T, ctx context.Context, client *ipc.Conn, want string) session.HistoryResponse {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	var last session.HistoryResponse
	for time.Now().Before(deadline) {
		raw, err := client.Call(ctx, session.MethodHistory, session.HistoryRequest{})
		if err != nil {
			t.Fatalf("session.history: %v", err)
		}
		last = session.HistoryResponse{}
		if err := json.Unmarshal(raw, &last); err != nil {
			t.Fatalf("decode the history: %v", err)
		}
		for _, update := range last.Updates {
			if update.Kind == session.KindUser && update.Text == want {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the log never showed the user message %q; it holds %d update(s)", want, len(last.Updates))
	return last
}

// TestAWindowQueuesEditsReordersAndRemovesMessages pins the queue as a list the
// window owns while a turn runs: every mutation answers with the queue as it
// stands, so the order on screen is the order the daemon will deliver.
func TestAWindowQueuesEditsReordersAndRemovesMessages(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-line", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-line"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	first := addQueued(t, ctx, client, "first")
	if len(first.Messages) != 1 || first.Messages[0].Text != "first" {
		t.Fatalf("the first queued message reads %+v", first.Messages)
	}
	firstID := first.Messages[0].ID
	addQueued(t, ctx, client, "second")

	raw, err := client.Call(ctx, session.MethodQueueEdit, session.QueueEditRequest{
		ID:   firstID,
		Text: "first, reworded",
	})
	if err != nil {
		t.Fatalf("session.queue.edit: %v", err)
	}
	var edited session.QueueResponse
	if err := json.Unmarshal(raw, &edited); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	if got := queuedTexts(edited); len(got) != 2 || got[0] != "first, reworded" || got[1] != "second" {
		t.Fatalf("the queue after an edit reads %v", got)
	}
	secondID := edited.Messages[1].ID

	raw, err = client.Call(ctx, session.MethodQueueMove, session.QueueMoveRequest{ID: secondID, To: 0})
	if err != nil {
		t.Fatalf("session.queue.move: %v", err)
	}
	var moved session.QueueResponse
	if err := json.Unmarshal(raw, &moved); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	if got := queuedTexts(moved); len(got) != 2 || got[0] != "second" || got[1] != "first, reworded" {
		t.Fatalf("the queue after a move reads %v", got)
	}

	raw, err = client.Call(ctx, session.MethodQueueRemove, session.QueueRemoveRequest{ID: secondID})
	if err != nil {
		t.Fatalf("session.queue.remove: %v", err)
	}
	var removed session.QueueResponse
	if err := json.Unmarshal(raw, &removed); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	if got := queuedTexts(removed); len(got) != 1 || got[0] != "first, reworded" {
		t.Fatalf("the queue after a removal reads %v", got)
	}

	// A message with no text would start a turn that says nothing.
	if _, err := client.Call(ctx, session.MethodQueueAdd, session.QueueAddRequest{Text: "  "}); ipc.CodeOf(err) != ipc.CodeBadRequest {
		t.Fatalf("a blank message was answered with %v, want a bad request", err)
	}
	// An edit or a removal of a message that is no longer queued is the
	// window being behind, and the daemon says so instead of pretending.
	if _, err := client.Call(ctx, session.MethodQueueEdit, session.QueueEditRequest{ID: "q-none", Text: "x"}); ipc.CodeOf(err) != ipc.CodeBadRequest {
		t.Fatalf("an edit of an unknown message was answered with %v, want a bad request", err)
	}
	if _, err := client.Call(ctx, session.MethodQueueMove, session.QueueMoveRequest{ID: firstID, To: 7}); ipc.CodeOf(err) != ipc.CodeBadRequest {
		t.Fatalf("a move past the end was answered with %v, want a bad request", err)
	}
}

// TestAQueuedMessageRunsWhenTheTurnEnds pins the delivery rule: a queued
// message is never sent while a turn runs, and the transition into done is what
// starts its own turn — with no window in the loop, because the daemon owns the
// queue.
func TestAQueuedMessageRunsWhenTheTurnEnds(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startAutoACPHost(t, dir, "sess-later", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-later"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	addQueued(t, ctx, client, "say goodbye")

	// Nothing runs yet: no turn has ended, so the message waits.
	if got := queuedTexts(queueOf(t, ctx, client)); len(got) != 1 || got[0] != "say goodbye" {
		t.Fatalf("the queue before any turn reads %v", got)
	}
	raw, err := client.Call(ctx, session.MethodHistory, session.HistoryRequest{})
	if err != nil {
		t.Fatalf("session.history: %v", err)
	}
	var before session.HistoryResponse
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatalf("decode the history: %v", err)
	}
	for _, update := range before.Updates {
		if update.Kind == session.KindUser && update.Text == "say goodbye" {
			t.Fatal("a queued message ran before any turn had ended")
		}
	}

	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("the first turn: %v", err)
	}

	// The turn ended; the queued message starts on its own and leaves the queue.
	waitForTranscript(t, ctx, client, "say goodbye")
	if got := queueOf(t, ctx, client); len(got.Messages) != 0 {
		t.Fatalf("the delivered message is still queued: %v", queuedTexts(got))
	}
}

// TestQueuedMessagesRunInOrder pins the queue as a queue: the message added
// first runs first, one whole turn each, until the queue is empty.
func TestQueuedMessagesRunInOrder(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startAutoACPHost(t, dir, "sess-chain", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-chain"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	addQueued(t, ctx, client, "first task")
	addQueued(t, ctx, client, "second task")

	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "start"}); err != nil {
		t.Fatalf("the first turn: %v", err)
	}

	stored := waitForTranscript(t, ctx, client, "second task")
	first, second := -1, -1
	for i, update := range stored.Updates {
		if update.Kind != session.KindUser {
			continue
		}
		switch update.Text {
		case "first task":
			first = i
		case "second task":
			second = i
		}
	}
	if first < 0 || second < 0 || first > second {
		t.Fatalf("the queued turns ran out of order: first at %d, second at %d", first, second)
	}
	if got := queueOf(t, ctx, client); len(got.Messages) != 0 {
		t.Fatalf("the queue still holds %v after both turns", queuedTexts(got))
	}
}

// TestAMessageQueuedAfterTheTurnEndedStillRuns closes the one race the queue
// cannot see coming: the window looked at a running turn, and the turn ended
// before the add arrived. The message must not sit until some later turn nobody
// promised.
func TestAMessageQueuedAfterTheTurnEndedStillRuns(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startAutoACPHost(t, dir, "sess-race", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-race"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("the first turn: %v", err)
	}
	waitForState(t, dir, token, "sess-race", session.StateDone)

	addQueued(t, ctx, client, "late news")

	waitForTranscript(t, ctx, client, "late news")
	if got := queueOf(t, ctx, client); len(got.Messages) != 0 {
		t.Fatalf("the late message is still queued: %v", queuedTexts(got))
	}
}

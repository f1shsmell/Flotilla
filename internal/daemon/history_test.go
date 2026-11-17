package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/transcript"
)

// allowTools answers the agent's permission questions, so a turn in a
// supervised session runs to the end instead of waiting on a client.
func allowTools(t *testing.T) ipc.Handler {
	t.Helper()

	return func(_ context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
		if method != session.MethodPermission {
			return struct{}{}, nil
		}

		var q session.PermissionQuestion
		if err := json.Unmarshal(params, &q); err != nil {
			return struct{}{}, nil
		}

		callCtx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()

		option := "allow"
		if len(q.Options) > 0 {
			option = q.Options[0]
		}
		if _, err := peer.Call(callCtx, session.MethodPermissionReply, session.PermissionReply{
			ID:       q.ID,
			OptionID: option,
		}); err != nil {
			t.Errorf("answer the permission question: %v", err)
		}
		return struct{}{}, nil
	}
}

// TestASecondWindowSeesTheLogTheFirstOneWatched is the reason the transcript is
// on disk: a window that opens a session somebody already drove is shown what
// the agent did, not an empty log.
func TestASecondWindowSeesTheLogTheFirstOneWatched(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-history", token)
	runDaemon(t, dir, token)

	first := dialDaemon(t, dir, token, allowTools(t), nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := first.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-history"}); err != nil {
		t.Fatalf("the first client could not attach: %v", err)
	}
	if _, err := first.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	first.Close()

	// A second window attaches to the same session. Whatever the host still
	// buffers is replayed as live traffic; the daemon's stored log is what makes
	// the answer complete.
	second := dialDaemon(t, dir, token, nil, nil)
	raw, err := second.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-history"})
	if err != nil {
		t.Fatalf("the second client could not attach: %v", err)
	}

	var attached session.AttachResponse
	if err := json.Unmarshal(raw, &attached); err != nil {
		t.Fatalf("decode the attach response: %v", err)
	}
	if len(attached.History) == 0 {
		t.Fatal("the second window was given no history for a session that already ran a turn")
	}

	// The log is numbered, and the numbers only go up: that is what lets the
	// window drop an update it already folded in.
	for i, msg := range attached.History {
		if msg.Seq == 0 {
			t.Fatalf("history update %d carries no sequence", i)
		}
		if i > 0 && msg.Seq <= attached.History[i-1].Seq {
			t.Fatalf("history update %d has sequence %d after %d",
				i, msg.Seq, attached.History[i-1].Seq)
		}
		if msg.Kind == "" {
			t.Fatalf("history update %d has no kind: %+v", i, msg)
		}
	}
}

// readHistoryUntilStable asks a client for a session's log until two reads in a
// row agree. The daemon stores each update as the host reports it, so a turn
// that has finished still drains into the log for a moment afterwards; a single
// read races that drain and would make the count a coin toss.
func readHistoryUntilStable(t *testing.T, client *ipc.Conn, ctx context.Context, id string) session.HistoryResponse {
	t.Helper()

	read := func() session.HistoryResponse {
		raw, err := client.Call(ctx, session.MethodHistory, session.HistoryRequest{})
		if err != nil {
			t.Fatalf("session.history: %v", err)
		}
		var got session.HistoryResponse
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode the history: %v", err)
		}
		return got
	}

	deadline := time.Now().Add(waitFor)
	previous := -1
	for time.Now().Before(deadline) {
		got := read()
		if len(got.Updates) == previous {
			return got
		}
		previous = len(got.Updates)
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the log of %q never settled", id)
	return session.HistoryResponse{}
}

// TestTheLogOutlivesTheDaemon is the other half of the same promise: the host
// holds a bounded memory buffer that dies with it, so the daemon's store has to
// hold the turn.
func TestTheLogOutlivesTheDaemon(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-survives", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, allowTools(t), nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-survives"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// Read the log through the call a window uses, so the assertion is about
	// what a client is told rather than about the file's shape.
	//
	// The turn's updates reach the daemon as notifications, and each is stored
	// as it arrives: a prompt that has returned does not mean the whole turn is
	// on disk yet. The read is retried until the count settles, because the
	// assertion is about what a restart sees, not about how fast the log is
	// written.
	stored := readHistoryUntilStable(t, client, ctx, "sess-survives")
	if len(stored.Updates) == 0 {
		t.Fatal("the daemon stored no updates for a turn that produced some")
	}

	// A restart is a new store object over the same directory, which is exactly
	// what the next daemon process does.
	reopened := transcript.NewStore(filepath.Join(dir, "transcripts"))
	t.Cleanup(func() { _ = reopened.Close() })

	page, err := reopened.Page("sess-survives", transcript.Window{})
	if err != nil {
		t.Fatalf("read the transcript after a restart: %v", err)
	}
	if len(page.Records) != len(stored.Updates) {
		t.Fatalf("a restart sees %d updates, the daemon answered with %d",
			len(page.Records), len(stored.Updates))
	}
}

// TestHistoryCanBeReadForwardFromASequence pins the reconnect shape: a client
// that already holds the first few updates asks for what came after them.
func TestHistoryCanBeReadForwardFromASequence(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-forward", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, allowTools(t), nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-forward"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	raw, err := client.Call(ctx, session.MethodHistory, session.HistoryRequest{Tail: 2})
	if err != nil {
		t.Fatalf("session.history: %v", err)
	}
	var tail session.HistoryResponse
	if err := json.Unmarshal(raw, &tail); err != nil {
		t.Fatalf("decode the history: %v", err)
	}
	if len(tail.Updates) != 2 {
		t.Fatalf("a tail of 2 returned %d updates", len(tail.Updates))
	}
	if !tail.More {
		t.Fatal("a tail of the log did not say older updates exist")
	}

	// Everything after the first update of that tail starts at the second.
	raw, err = client.Call(ctx, session.MethodHistory, session.HistoryRequest{
		After: tail.Updates[0].Seq,
	})
	if err != nil {
		t.Fatalf("session.history after a sequence: %v", err)
	}
	var later session.HistoryResponse
	if err := json.Unmarshal(raw, &later); err != nil {
		t.Fatalf("decode the later history: %v", err)
	}
	if len(later.Updates) == 0 {
		t.Fatal("asking for what came after the first update returned nothing")
	}
	if later.Updates[0].Seq != tail.Updates[1].Seq {
		t.Fatalf("the answer starts at sequence %d, want %d",
			later.Updates[0].Seq, tail.Updates[1].Seq)
	}
}

// TestAStoppedSessionKeepsItsLog pins the scope of a stop: the agent and its
// record go, what it did does not. A session somebody ended is exactly the one
// they come back to read.
func TestAStoppedSessionKeepsItsLog(t *testing.T) {
	stateDir := t.TempDir()

	d := New(Config{Channel: "test", Token: "shared", StateDir: stateDir, Log: discardLogger()}, nil)
	t.Cleanup(func() { _ = d.transcripts.Close() })

	if _, err := d.transcripts.Append("sess-gone", 1, json.RawMessage(`{"kind":"message","text":"done"}`)); err != nil {
		t.Fatalf("store an update: %v", err)
	}

	d.end("sess-gone", true)

	// Releasing one session must not take the store with it: the daemon keeps
	// serving the rest of the fleet.
	if _, err := d.transcripts.Append("probe", 1, json.RawMessage(`{"kind":"message","text":"x"}`)); err != nil {
		t.Fatalf("the store is unusable after releasing one session: %v", err)
	}

	reopened := transcript.NewStore(filepath.Join(stateDir, "transcripts"))
	t.Cleanup(func() { _ = reopened.Close() })

	page, err := reopened.Page("sess-gone", transcript.Window{})
	if err != nil {
		t.Fatalf("read the stopped session's log: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("the stopped session's log holds %d updates, want the one it wrote",
			len(page.Records))
	}
}

// TestTheLogCarriesWhatTheUserSaid is the other half of a conversation: the agent
// reports only its own side, so a log built from agent updates alone leaves out
// the message every later turn is an answer to.
func TestTheLogCarriesWhatTheUserSaid(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-said", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, allowTools(t), nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-said"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	const said = "what does this repository do?"
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: said}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	stored := readHistoryUntilStable(t, client, ctx, "sess-said")

	// The person's message comes first: everything the agent did answers it, so
	// a log that put it after the reply would read as a different conversation.
	if len(stored.Updates) == 0 {
		t.Fatal("the log is empty after a turn")
	}
	first := stored.Updates[0]
	if first.Kind != session.KindUser {
		t.Fatalf("the log starts with a %q update, want the user's message", first.Kind)
	}
	if first.Text != said {
		t.Fatalf("the stored message is %q, want %q", first.Text, said)
	}

	for i, msg := range stored.Updates {
		if msg.Kind == session.KindUser && msg.Text != said {
			t.Fatalf("update %d is a user message with text %q, want only the one that was sent", i, msg.Text)
		}
	}
}

// TestTheUserMessageIsNumberedOnce pins the log's shape: the message is stored
// exactly once, so a reattach replays the conversation and not the question
// twice.
func TestTheUserMessageIsNumberedOnce(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-once", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, allowTools(t), nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-once"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// A second window attaches to the same session: what it folds is the log the
	// first window watched, and a message stored twice would show twice.
	second := dialDaemon(t, dir, token, nil, nil)
	raw, err := second.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-once"})
	if err != nil {
		t.Fatalf("the second client could not attach: %v", err)
	}

	var attached session.AttachResponse
	if err := json.Unmarshal(raw, &attached); err != nil {
		t.Fatalf("decode the attach response: %v", err)
	}

	var said int
	var seqs []uint64
	for _, msg := range attached.History {
		if msg.Kind == session.KindUser {
			said++
		}
		seqs = append(seqs, msg.Seq)
	}
	if said != 1 {
		t.Fatalf("the log holds %d user messages, want the one that was sent", said)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("history sequence %d is followed by %d", seqs[i-1], seqs[i])
		}
	}
}

// TestTheHistoryIsBoundedForOneFrame pins the wire bound: a session with a long
// turn must not produce an answer the transport refuses.
func TestTheHistoryIsBoundedForOneFrame(t *testing.T) {
	stateDir := t.TempDir()
	d := New(Config{Channel: "test", Token: "shared", StateDir: stateDir, Log: discardLogger()}, nil)
	t.Cleanup(func() { _ = d.transcripts.Close() })

	body, err := json.Marshal(session.UpdateMessage{Kind: "message", Text: strings.Repeat("x", 4096)})
	if err != nil {
		t.Fatalf("marshal an update: %v", err)
	}
	// Every update gets its own host number: the same one twice would be a
	// replay, which the store refuses on purpose.
	for i := uint64(1); i <= 2000; i++ {
		if _, err := d.transcripts.Append("sess-big", i, body); err != nil {
			t.Fatalf("store an update: %v", err)
		}
	}

	updates, more, err := d.readHistory("sess-big", session.HistoryRequest{Tail: 100000})
	if err != nil {
		t.Fatalf("readHistory: %v", err)
	}
	if !more {
		t.Fatal("a bounded answer did not say there was more")
	}
	if len(updates) == 0 {
		t.Fatal("a bounded answer returned nothing")
	}

	encoded, err := json.Marshal(session.HistoryResponse{Updates: updates, More: more})
	if err != nil {
		t.Fatalf("marshal the answer: %v", err)
	}
	if len(encoded) > ipc.MaxFrameSize {
		t.Fatalf("the answer is %d bytes, past the %d-byte frame", len(encoded), ipc.MaxFrameSize)
	}
}

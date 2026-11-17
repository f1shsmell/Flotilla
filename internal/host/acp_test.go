package host

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// buildFakeAgent compiles the stand-in ACP agent into the test's own directory.
func buildFakeAgent(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("no go.mod above the package directory")
		}
		root = parent
	}

	target := filepath.Join(t.TempDir(), "flt-fake-acp-agent")
	if filepath.Separator == '\\' {
		target += ".exe"
	}

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, "./cmd/flt-fake-acp-agent")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the fake agent: %v: %s", err, output)
	}
	return target
}

// startACPHost takes a session in ACP mode against the fake agent.
func startACPHost(t *testing.T, policy acp.Policy) (*Host, string) {
	t.Helper()

	return startACPHostFrom(t, policy, 0)
}

// startACPHostFrom is startACPHost with the host number its first update
// continues from, which is what a reused session id needs.
func startACPHostFrom(t *testing.T, policy acp.Policy, seqBase uint64) (*Host, string) {
	t.Helper()

	dir := t.TempDir()
	binary := buildFakeAgent(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-acp",
		Mode:     session.ModeACP,
		Token:    "shared",
		StateDir: dir,
		Command:  binary,
		Dir:      dir,
		Policy:   policy,
		SeqBase:  seqBase,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start in ACP mode: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	return h, dir
}

// dialHost connects a client to the host under test. Agent updates and
// permission questions arrive as notifications, which the handler collects.
func dialHost(t *testing.T, dir string, onUpdate func(session.UpdateMessage), onAsk func(session.PermissionQuestion)) *ipc.Conn {
	t.Helper()

	reg := session.NewRegistry(dir)
	rec, err := waitForRecord(t, reg, "sess-acp")
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
			case session.MethodUpdate:
				var msg session.UpdateMessage
				if err := json.Unmarshal(params, &msg); err == nil && onUpdate != nil {
					onUpdate(msg)
				}
			case session.MethodPermission:
				var q session.PermissionQuestion
				if err := json.Unmarshal(params, &q); err == nil && onAsk != nil {
					onAsk(q)
				}
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

// TestACPHostRunsATurnEndToEnd is the W1-2 ACP gate: initialize, newSession,
// prompt, streamed updates and an end-of-turn stop reason, all through the host
// process that a client reconnects to.
func TestACPHostRunsATurnEndToEnd(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)

	updates := make(chan session.UpdateMessage, 64)
	conn := dialHost(t, dir, func(msg session.UpdateMessage) { updates <- msg }, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodInfo, struct{}{})
	if err != nil {
		t.Fatalf("session.info: %v", err)
	}
	var info session.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Mode != string(session.ModeACP) {
		t.Fatalf("info.Mode = %q, want acp", info.Mode)
	}

	raw, err = conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"})
	if err != nil {
		t.Fatalf("session.prompt: %v", err)
	}
	var resp session.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode prompt response: %v", err)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
	}

	var kinds []string
	deadline := time.After(waitFor)
	for {
		select {
		case u := <-updates:
			kinds = append(kinds, u.Kind)
			// The person's message comes first: the agent's updates answer it,
			// and a log that started with the reply would read as a different
			// conversation.
			if strings.Join(kinds, ",") == "user,message,thought,tool,tool" {
				return
			}
		case <-deadline:
			t.Fatalf("the update stream was %v, want the user's message, a message, a thought and two tool updates", kinds)
		}
	}
}

// TestACPHostNumbersPastASeed pins the reuse case the daemon's spawn path
// relies on: a host told to continue from a seed numbers its first update past
// it, so a session id whose old run is still in the transcript cannot have its
// new run mistaken for a replay of the old one.
func TestACPHostNumbersPastASeed(t *testing.T) {
	const seed = 5
	_, dir := startACPHostFrom(t, acp.Auto, seed)

	updates := make(chan session.UpdateMessage, 64)
	conn := dialHost(t, dir, func(msg session.UpdateMessage) { updates <- msg }, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("session.prompt: %v", err)
	}

	// The fake answers one turn with a message, a thought and two tool updates,
	// so the first four numbers are the seed plus one through four.
	want := uint64(seed)
	deadline := time.After(waitFor)
	for seen := 0; seen < 4; {
		select {
		case u := <-updates:
			want++
			if u.HostSeq != want {
				t.Fatalf("update %d carried host number %d, want %d", seen+1, u.HostSeq, want)
			}
			seen++
		case <-deadline:
			t.Fatalf("only %d updates arrived, want the single turn's four", seen)
		}
	}
}

// TestACPHostForwardsPermissionQuestions is the permission-card gate: the host
// relays the agent's question to the client and carries the answer back.
func TestACPHostForwardsPermissionQuestions(t *testing.T) {
	_, dir := startACPHost(t, acp.Supervised)

	questions := make(chan session.PermissionQuestion, 4)
	conn := dialHost(t, dir, func(session.UpdateMessage) {}, func(q session.PermissionQuestion) { questions <- q })

	// The host sends the question as a notification, so the client answers with
	// a call that echoes the id.
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	promptc := make(chan struct {
		raw json.RawMessage
		err error
	}, 1)
	go func() {
		raw, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "edit it"})
		promptc <- struct {
			raw json.RawMessage
			err error
		}{raw, err}
	}()

	// The question arrives on the notification channel the host registered.
	select {
	case q := <-questions:
		if len(q.Options) == 0 {
			t.Fatalf("the question offered no options: %+v", q)
		}
		if _, err := conn.Call(ctx, session.MethodPermissionReply, session.PermissionReply{
			ID:       q.ID,
			OptionID: q.Options[0],
		}); err != nil {
			t.Fatalf("session.permission.reply: %v", err)
		}
	case res := <-promptc:
		t.Fatalf("the turn finished without asking; err=%v", res.err)
	case <-time.After(waitFor):
		t.Fatal("the host never forwarded a permission question")
	}

	select {
	case res := <-promptc:
		if res.err != nil {
			t.Fatalf("session.prompt after an allowed question: %v", res.err)
		}
		var resp session.PromptResponse
		if err := json.Unmarshal(res.raw, &resp); err != nil {
			t.Fatalf("decode prompt response: %v", err)
		}
		if resp.StopReason != "end_turn" {
			t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
		}
	case <-time.After(waitFor):
		t.Fatal("the turn never finished after the permission answer")
	}
}

// TestPTYHostRejectsACPPCalls pins the mode boundary: a terminal session has no
// structure, so a prompt to it is an error rather than a silent no-op.
func TestPTYHostRejectsACPCalls(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-pty",
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	reg := session.NewRegistry(dir)
	rec, err := waitForRecord(t, reg, "sess-pty")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), waitFor)
	defer dialCancel()

	conn, err := ipc.Dial(dialCtx, rec.Endpoint, dir, ipc.Config{Token: "shared", Role: "test"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Call(dialCtx, session.MethodPrompt, session.PromptRequest{Text: "hi"}); err == nil {
		t.Fatal("a PTY session accepted a prompt call")
	}
}

// TestACPHostRecordsTheUsersMessage pins the other side of the conversation: the
// agent reports only its own updates, so the host has to put the prompt in the
// stream itself. A log that began with the reply would be an answer to nothing.
func TestACPHostRecordsTheUsersMessage(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)

	updates := make(chan session.UpdateMessage, 64)
	conn := dialHost(t, dir, func(msg session.UpdateMessage) { updates <- msg }, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	const said = "how does the relay work?"
	if _, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: said}); err != nil {
		t.Fatalf("session.prompt: %v", err)
	}

	// The message is the first thing on the stream, before the agent's own
	// output, and it carries the text the caller sent.
	select {
	case got := <-updates:
		if got.Kind != session.KindUser {
			t.Fatalf("the first update is %q, want the user's message", got.Kind)
		}
		if got.Text != said {
			t.Fatalf("the stored message is %q, want %q", got.Text, said)
		}
		if got.HostSeq == 0 {
			t.Fatal("the user's message carries no host number, so a replay would store it twice")
		}
	case <-time.After(waitFor):
		t.Fatal("the host never recorded the user's message")
	}
}

// TestChunkUpdateKeepsEveryCharacter pins the split itself: a piece never
// exceeds the budget and never cuts a rune in half.
func TestChunkUpdateKeepsEveryCharacter(t *testing.T) {
	msg := session.UpdateMessage{
		Kind: acp.UpdateMessage,
		Text: strings.Repeat("héllo wörld ", 4*updateTextBudget/len("héllo wörld ")),
	}

	parts := chunkUpdate(msg, updateTextBudget)
	if len(parts) < 4 {
		t.Fatalf("split into %d parts, want the text spread over several", len(parts))
	}

	var joined strings.Builder
	for i, part := range parts {
		if len(part.Text) > updateTextBudget {
			t.Fatalf("part %d carries %d bytes, over the %d budget", i, len(part.Text), updateTextBudget)
		}
		if !utf8.ValidString(part.Text) {
			t.Fatalf("part %d split a rune", i)
		}
		if part.Kind != msg.Kind {
			t.Fatalf("part %d lost its kind", i)
		}
		// Only the first piece starts the line; every later one is the wire
		// saying it continues it. Without the mark a client cannot tell these
		// pieces from a second message of the same kind.
		if part.Continued != (i > 0) {
			t.Fatalf("part %d has continued=%v", i, part.Continued)
		}
		joined.WriteString(part.Text)
	}
	if joined.String() != msg.Text {
		t.Fatalf("the parts reassemble to %d bytes, want %d", joined.Len(), len(msg.Text))
	}
}

// TestChunkUpdateLeavesASmallUpdateAlone keeps the common case one notification.
func TestChunkUpdateLeavesASmallUpdateAlone(t *testing.T) {
	msg := session.UpdateMessage{Kind: acp.UpdateMessage, Text: "short"}

	parts := chunkUpdate(msg, updateTextBudget)
	if len(parts) != 1 || parts[0] != msg {
		t.Fatalf("chunkUpdate returned %+v, want the message untouched", parts)
	}
}

// TestACPHostSendsAnUpdateTooLargeForOneFrame pins what used to vanish: the
// wire refused an update past the frame limit and the client never saw it.
func TestACPHostSendsAnUpdateTooLargeForOneFrame(t *testing.T) {
	h, dir := startACPHost(t, acp.Supervised)

	big := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 35000)
	if len(big) <= ipc.MaxFrameSize {
		t.Fatalf("the fixture is %d bytes, it must exceed the %d-byte frame", len(big), ipc.MaxFrameSize)
	}

	var (
		mu   sync.Mutex
		got  strings.Builder
		done = make(chan struct{}, 1)
	)

	dialHost(t, dir, func(msg session.UpdateMessage) {
		mu.Lock()
		got.WriteString(msg.Text)
		complete := got.Len() >= len(big)
		mu.Unlock()

		if complete {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}, nil)

	waitForAttach(t, h)
	h.forwardUpdate(acp.Update{Kind: acp.UpdateMessage, Text: big})

	select {
	case <-done:
	case <-time.After(waitFor):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the client assembled %d of %d bytes", got.Len(), len(big))
	}

	mu.Lock()
	defer mu.Unlock()
	if got.String() != big {
		t.Fatalf("the client assembled %d bytes, want %d", got.Len(), len(big))
	}
}

// waitForAttach blocks until the host has a client, which Serve does just after
// the handshake and therefore just after Dial returns.
func waitForAttach(t *testing.T, h *Host) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		attached := h.conn != nil
		h.mu.Unlock()
		if attached {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the host never attached the client")
}

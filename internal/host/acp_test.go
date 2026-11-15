package host

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		Handler: func(_ context.Context, method string, params json.RawMessage) (any, error) {
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
			if strings.Join(kinds, ",") == "message,thought,tool,tool" {
				return
			}
		case <-deadline:
			t.Fatalf("the update stream was %v, want message, thought and two tool updates", kinds)
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

package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestNoPermissionWaitArmsNoDeadline pins the default: a question nobody has
// answered yet is still waiting, because a timeout proves nothing about the
// user's intent. The zero config must arm no timer at all — a nil channel is
// what "forever" looks like in a select.
func TestNoPermissionWaitArmsNoDeadline(t *testing.T) {
	if timer := (Config{}).permissionTimer(); timer != nil {
		timer.Stop()
		t.Fatal("the zero config armed a permission deadline; the default waits forever")
	}

	timer := Config{PermissionWait: time.Hour}.permissionTimer()
	if timer == nil {
		t.Fatal("a named wait armed no deadline")
	}
	timer.Stop()
}

// TestAnUnansweredQuestionIsRefusedAfterTheConfiguredWait pins the explicit
// opt-in: a caller that names a wait gets a refusal when it passes, and the
// session leaves blocked — a refused question is an answered one, and a turn
// that moved on must not keep wearing the waiting badge.
func TestAnUnansweredQuestionIsRefusedAfterTheConfiguredWait(t *testing.T) {
	dir := t.TempDir()
	binary := buildFakeAgent(t)

	h, err := Start(context.Background(), Config{
		ID:             "sess-acp",
		Mode:           session.ModeACP,
		Token:          "shared",
		StateDir:       dir,
		Command:        binary,
		Dir:            dir,
		Policy:         acp.Supervised,
		PermissionWait: time.Nanosecond,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start in ACP mode: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	questions := make(chan session.PermissionQuestion, 4)
	conn := dialHost(t, dir, nil, func(q session.PermissionQuestion) { questions <- q })

	callCtx, callCancel := context.WithTimeout(context.Background(), waitFor)
	defer callCancel()

	// No reply is ever sent: the configured wait is the only thing that can
	// end this turn.
	raw, err := conn.Call(callCtx, session.MethodPrompt, session.PromptRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("the turn did not end after the configured wait: %v", err)
	}

	var resp session.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the prompt response: %v", err)
	}
	if resp.StopReason != "cancelled" {
		t.Fatalf("stop reason = %q, want cancelled: the refusal must reach the agent", resp.StopReason)
	}

	select {
	case <-questions:
	default:
		t.Fatal("the question was never asked, so the wait proved nothing")
	}

	if h.CurrentState() == session.StateBlocked {
		t.Fatal("the session still wears blocked after its question was refused")
	}
}

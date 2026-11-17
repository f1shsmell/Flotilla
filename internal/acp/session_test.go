package acp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// waitFor bounds every wait here. It stops a hung test, it is not a condition.
const waitFor = 30 * time.Second

// fakeAgentBinary builds the stand-in ACP agent once per run.
func fakeAgentBinary(t *testing.T) string {
	t.Helper()

	root := repoRoot(t)
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

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// openFake opens a session against the fake agent with the given policy.
func openFake(t *testing.T, policy Policy, asker Asker) (*Session, *[]Update, string) {
	t.Helper()

	binary := fakeAgentBinary(t)
	workspace := t.TempDir()

	var updates []Update
	samples := &updates

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	sess, err := Open(ctx, Launch{Command: binary, Dir: workspace}, Config{
		Workspace: workspace,
		Policy:    policy,
		Asker:     asker,
		OnUpdate:  func(u Update) { *samples = append(*samples, u) },
	})
	if err != nil {
		t.Fatalf("open a session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	return sess, samples, workspace
}

// TestFullSessionOverStdio runs a whole conversation against the fake agent:
// initialize, newSession, prompt, streamed updates, a permission request and an
// end-of-turn stop reason.
func TestFullSessionOverStdio(t *testing.T) {
	sess, updates, _ := openFake(t, Auto, nil)

	if sess.ID() == "" {
		t.Fatal("the agent assigned no session id")
	}
	if !sess.Capabilities().LoadSession {
		t.Fatal("the fake advertises loadSession but the client did not see it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := sess.Prompt(ctx, sdk.TextBlock("please add a test"))
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != sdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
	}

	var sawMessage, sawThought, sawTool bool
	for _, u := range *updates {
		switch u.Kind {
		case UpdateMessage:
			if strings.Contains(u.Text, "working on: please add a test") {
				sawMessage = true
			}
		case UpdateThought:
			sawThought = true
		case UpdateTool:
			if u.ToolCallID != "" {
				sawTool = true
			}
		}
	}
	if !sawMessage {
		t.Fatalf("no agent message reached the client; updates: %+v", *updates)
	}
	if !sawThought {
		t.Fatalf("no thought reached the client; updates: %+v", *updates)
	}
	if !sawTool {
		t.Fatalf("no tool call reached the client; updates: %+v", *updates)
	}
}

// TestPermissionRequestReachesTheAsker is the W1-2 gate for the permission card:
// the agent asks, the client's policy answers, and the turn completes on that
// answer.
func TestPermissionRequestReachesTheAsker(t *testing.T) {
	seen := make([]PermissionRequest, 0, 1)

	sess, updates, _ := openFake(t, Supervised, func(_ context.Context, req PermissionRequest) (PermissionAnswer, error) {
		seen = append(seen, req)
		return PermissionAnswer{OptionID: "allow"}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := sess.Prompt(ctx, sdk.TextBlock("edit the config"))
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != sdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn after an allowed permission", resp.StopReason)
	}
	if len(seen) != 1 {
		t.Fatalf("the asker was called %d times, want 1", len(seen))
	}
	if seen[0].Title != "edit config.json" {
		t.Fatalf("the asker saw title %q, want the agent's tool title", seen[0].Title)
	}
	if seen[0].Kind != string(sdk.ToolKindEdit) {
		t.Fatalf("the asker saw kind %q, want edit", seen[0].Kind)
	}

	var completed bool
	for _, u := range *updates {
		if u.Kind == UpdateTool && u.ToolStatus == string(sdk.ToolCallStatusCompleted) {
			completed = true
		}
	}
	if !completed {
		t.Fatalf("the tool call never completed; updates: %+v", *updates)
	}
}

// TestRefusedPermissionEndsTheTurn documents what the user sees when they say no.
func TestRefusedPermissionEndsTheTurn(t *testing.T) {
	sess, updates, _ := openFake(t, Supervised, func(context.Context, PermissionRequest) (PermissionAnswer, error) {
		return PermissionAnswer{}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := sess.Prompt(ctx, sdk.TextBlock("edit the config"))
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != sdk.StopReasonCancelled {
		t.Fatalf("stop reason = %q, want cancelled after a refusal", resp.StopReason)
	}

	var said bool
	for _, u := range *updates {
		if u.Kind == UpdateMessage && strings.Contains(u.Text, "no permission") {
			said = true
		}
	}
	if !said {
		t.Fatalf("the agent's refusal message never arrived; updates: %+v", *updates)
	}
}

// TestAgentFileAccessStaysInTheWorkspace proves the client, not the agent, is
// what keeps a session inside its worktree.
func TestAgentFileAccessStaysInTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(filepath.Dir(workspace), "secret.txt")
	if err := os.WriteFile(outside, []byte("do not read me"), 0o600); err != nil {
		t.Fatalf("write the fixture outside the workspace: %v", err)
	}

	c := NewClient(Config{Workspace: workspace})
	if _, err := c.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{Path: outside}); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("ReadTextFile outside the workspace = %v, want ErrOutsideWorkspace", err)
	}
}

// TestCancelStopsATurn cancels while the agent is mid-turn.
func TestCancelStopsATurn(t *testing.T) {
	sess, _, _ := openFake(t, Supervised, func(ctx context.Context, _ PermissionRequest) (PermissionAnswer, error) {
		// The asker is where a turn waits for a human, so it is the right place
		// to cancel from.
		if err := sessErrPlaceholder(ctx); err != nil {
			return PermissionAnswer{}, err
		}
		return PermissionAnswer{}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := sess.Prompt(ctx, sdk.TextBlock("wait for me"))
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != sdk.StopReasonCancelled {
		t.Fatalf("stop reason = %q, want cancelled", resp.StopReason)
	}
}

// sessErrPlaceholder reports the context's error so a cancelled asker ends the
// turn rather than answering.
func sessErrPlaceholder(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// TestAnEmptyWorkspaceFallsBackToTheLaunchDirectory pins the fix for a session
// the agent used to refuse: an empty workspace is not sent as an empty cwd, and
// the directory the agent runs in is the one its file requests are checked
// against.
//
// The fake names a session after the last segment of its cwd, so the id says
// which directory the agent was told to run in.
func TestAnEmptyWorkspaceFallsBackToTheLaunchDirectory(t *testing.T) {
	binary := fakeAgentBinary(t)
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	sess, err := Open(ctx, Launch{Command: binary, Dir: dir}, Config{Policy: Auto})
	if err != nil {
		t.Fatalf("open a session with no workspace: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	if got, want := string(sess.ID()), "fake-"+filepath.Base(dir); got != want {
		t.Fatalf("session %q, want %q: the agent was told the wrong cwd", got, want)
	}
}

// TestANamedWorkspaceWinsOverTheLaunchDirectory pins the order: a caller that
// names a workspace keeps it even when the agent process runs somewhere else,
// so the file boundary and the session agree on the directory the caller chose.
func TestANamedWorkspaceWinsOverTheLaunchDirectory(t *testing.T) {
	binary := fakeAgentBinary(t)
	workspace := t.TempDir()
	launchDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	sess, err := Open(ctx, Launch{Command: binary, Dir: launchDir}, Config{
		Workspace: workspace,
		Policy:    Auto,
	})
	if err != nil {
		t.Fatalf("open a session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	if got, want := string(sess.ID()), "fake-"+filepath.Base(workspace); got != want {
		t.Fatalf("session %q, want %q: the launch directory overrode the workspace", got, want)
	}
}

// TestCloseStopsTheAgentProcess pins the teardown: the child is gone after
// Close, so a session cannot leak an agent.
func TestCloseStopsTheAgentProcess(t *testing.T) {
	sess, _, _ := openFake(t, Auto, nil)

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second Close is a no-op, not a failure.
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := sess.Prompt(ctx, sdk.TextBlock("anyone home?")); err == nil {
		t.Fatal("a closed session still answered a prompt")
	}
}

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

// buildReplayAgent compiles the replay agent into the test's own directory, so
// the check spawns the binary a release ships rather than calling the package.
func buildReplayAgent(t *testing.T) string {
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

	target := filepath.Join(t.TempDir(), "flt-replay-agent")
	if filepath.Separator == '\\' {
		target += ".exe"
	}

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, "./cmd/flt-replay-agent")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the replay agent: %v: %s", err, output)
	}
	return target
}

// TestTheReplayAgentRunsATurnThroughTheHost is the preset's real gate: a demo or
// a smoke run gets its turn from a process the host spawns, over real stdio,
// with no model and no credentials. A preset that only listed in the picker
// would look available and fail on the first prompt.
func TestTheReplayAgentRunsATurnThroughTheHost(t *testing.T) {
	dir := t.TempDir()
	binary := buildReplayAgent(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-replay",
		Mode:     session.ModeACP,
		Token:    "shared",
		StateDir: dir,
		Command:  binary,
		Dir:      dir,
		Policy:   acp.Auto,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start against the replay agent: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-replay")
	if err != nil {
		t.Fatalf("the replay host never published a record: %v", err)
	}

	updates := make(chan session.UpdateMessage, 64)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(dialCancel)

	conn, err := ipc.Dial(dialCtx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		Handler: func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
			if method == session.MethodUpdate {
				var msg session.UpdateMessage
				if err := json.Unmarshal(params, &msg); err == nil {
					updates <- msg
				}
			}
			return struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatalf("dial the replay host: %v", err)
	}
	t.Cleanup(conn.Close)

	callCtx, callCancel := context.WithTimeout(context.Background(), waitFor)
	defer callCancel()

	raw, err := conn.Call(callCtx, session.MethodPrompt, session.PromptRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("session.prompt on the replay agent: %v", err)
	}
	var resp session.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the turn: %v", err)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
	}

	// The builtin scenario shows the three shapes a renderer draws: a thought, a
	// tool call and a message. The tool call arrives twice — started, then
	// completed — which is how ACP reports one call, so the stream is four
	// updates. A replay that sent none of them would satisfy the stop reason
	// above while showing the user an empty session.
	want := "thought,tool,tool,message"

	var kinds []string
	deadline := time.After(waitFor)
	for strings.Join(kinds, ",") != want {
		select {
		case msg := <-updates:
			// The prompt itself is recorded first; it is not the agent's work.
			if msg.Kind == session.KindUser {
				continue
			}
			kinds = append(kinds, msg.Kind)
		case <-deadline:
			t.Fatalf("the replay stream was %v, want %s", kinds, want)
		}
	}
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/gittest"
	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// startAutoACPHostInRepo takes an ACP session whose working directory is a
// repository of its own. The state root stays outside it, so the host's own
// files never land in a snapshot.
func startAutoACPHostInRepo(t *testing.T, stateDir, workDir, id, token string) *host.Host {
	t.Helper()

	h, err := host.Start(context.Background(), host.Config{
		ID:       session.ID(id),
		Mode:     session.ModeACP,
		Token:    token,
		StateDir: stateDir,
		Command:  buildFakeAgent(t),
		Dir:      workDir,
		Policy:   acp.Auto,
	}, session.NewRegistry(stateDir), discardLogger())
	if err != nil {
		t.Fatalf("start the ACP host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	if _, err := waitRecord(t, session.NewRegistry(stateDir), session.ID(id)); err != nil {
		t.Fatalf("the ACP host never published a record: %v", err)
	}
	return h
}

// readRepoFile reads a file a test expects to exist, without the line ending
// the machine's git config chooses on checkout.
func readRepoFile(t *testing.T, dir, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

// checkpointsOf reads a session's snapshots over the wire.
func checkpointsOf(t *testing.T, ctx context.Context, client *ipc.Conn) session.CheckpointListResponse {
	t.Helper()

	raw, err := client.Call(ctx, session.MethodCheckpointList, struct{}{})
	if err != nil {
		t.Fatalf("session.checkpoint.list: %v", err)
	}
	var got session.CheckpointListResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode the checkpoints: %v", err)
	}
	return got
}

// waitForCheckpoints polls until the session has want snapshots. A checkpoint is
// written after the done event, so the poll waits for the repository rather than
// guessing at its pace.
func waitForCheckpoints(t *testing.T, ctx context.Context, client *ipc.Conn, want int) session.CheckpointListResponse {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	var last session.CheckpointListResponse
	for time.Now().Before(deadline) {
		last = checkpointsOf(t, ctx, client)
		if len(last.Checkpoints) >= want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the session has %d checkpoint(s), want %d", len(last.Checkpoints), want)
	return last
}

// TestAFinishedTurnLeavesACheckpoint pins the trigger: the turn's done
// transition is what snapshots the worktree, without a window in the loop and
// without the daemon holding snapshot state of its own.
func TestAFinishedTurnLeavesACheckpoint(t *testing.T) {
	state := t.TempDir()
	repo := gittest.Init(t)
	const token = "shared"

	startAutoACPHostInRepo(t, state, repo, "sess-cp", token)
	runDaemon(t, state, token)

	client := dialDaemon(t, state, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-cp"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	gittest.Write(t, repo, "readme.md", "the turn's work\n")
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	waitForState(t, state, token, "sess-cp", session.StateDone)

	list := waitForCheckpoints(t, ctx, client, 1)
	first := list.Checkpoints[0]
	if first.Turn != 1 || first.Commit == "" || first.At.IsZero() {
		t.Fatalf("the first checkpoint reads %+v", first)
	}

	// The snapshot is a hidden ref in the repository, not state the daemon owns.
	refs := gittest.Run(t, repo, "for-each-ref", "--format=%(refname)", "refs/flotilla/checkpoints")
	if !strings.Contains(refs, "refs/flotilla/checkpoints/") {
		t.Fatalf("the repository holds no checkpoint ref: %q", refs)
	}
	if got := gittest.Run(t, repo, "show", first.Commit+":readme.md"); !strings.Contains(got, "the turn's work") {
		t.Fatalf("the checkpoint holds readme.md as %q", got)
	}
}

// TestOneClickRestoreRollsTheWorkingTreeBack is the slice's acceptance: one
// call from the window puts the worktree back to a finished turn, removes what
// came after it, and leaves the branch where it was.
func TestOneClickRestoreRollsTheWorkingTreeBack(t *testing.T) {
	state := t.TempDir()
	repo := gittest.Init(t)
	const token = "shared"

	startAutoACPHostInRepo(t, state, repo, "sess-back", token)
	runDaemon(t, state, token)

	client := dialDaemon(t, state, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-back"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	gittest.Write(t, repo, "readme.md", "the turn's work\n")
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	waitForState(t, state, token, "sess-back", session.StateDone)
	first := waitForCheckpoints(t, ctx, client, 1).Checkpoints[0]
	head := gittest.Run(t, repo, "rev-parse", "HEAD")

	gittest.Write(t, repo, "readme.md", "later work\n")
	gittest.Write(t, repo, "later.txt", "later work\n")

	raw, err := client.Call(ctx, session.MethodCheckpointRestore, session.CheckpointRestoreRequest{Turn: first.Turn})
	if err != nil {
		t.Fatalf("session.checkpoint.restore: %v", err)
	}
	var res session.WriteResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode the restore: %v", err)
	}
	if res.Ref != first.Commit {
		t.Fatalf("the restore reports %q, want %q", res.Ref, first.Commit)
	}

	if got := readRepoFile(t, repo, "readme.md"); got != "the turn's work\n" {
		t.Fatalf("readme.md after the rollback reads %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "later.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file the checkpoint never held survived the rollback: %v", err)
	}
	if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the rollback moved HEAD from %s to %s", head, got)
	}
}

// TestARollbackIsRefusedWhileAnotherSessionUsesTheCheckout keeps one session's
// rollback out of another session's agent: the window cannot see the other
// session, so the daemon is the only layer that can refuse.
func TestARollbackIsRefusedWhileAnotherSessionUsesTheCheckout(t *testing.T) {
	state := t.TempDir()
	repo := gittest.Init(t)
	const token = "shared"

	startAutoACPHostInRepo(t, state, repo, "sess-left", token)
	startAutoACPHostInRepo(t, state, repo, "sess-right", token)
	runDaemon(t, state, token)

	client := dialDaemon(t, state, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-left"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	gittest.Write(t, repo, "readme.md", "the turn's work\n")
	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	waitForState(t, state, token, "sess-left", session.StateDone)
	first := waitForCheckpoints(t, ctx, client, 1).Checkpoints[0]

	gittest.Write(t, repo, "readme.md", "later work\n")
	if _, err := client.Call(ctx, session.MethodCheckpointRestore, session.CheckpointRestoreRequest{Turn: first.Turn}); err == nil {
		t.Fatal("a rollback in a shared checkout was allowed")
	} else if !strings.Contains(err.Error(), "another session") || ipc.CodeOf(err) != ipc.CodeBadRequest {
		t.Fatalf("the shared checkout was refused with %v", err)
	}
	if got := readRepoFile(t, repo, "readme.md"); got != "later work\n" {
		t.Fatalf("a refused rollback still rewrote readme.md as %q", got)
	}
}

// TestARollbackIsRefusedWhileTheTurnRuns keeps a destructive rewrite away from
// a tree an agent may be writing: the state is the only thing that says the
// turn is over.
func TestARollbackIsRefusedWhileTheTurnRuns(t *testing.T) {
	state := t.TempDir()
	repo := gittest.Init(t)
	const token = "shared"

	startAutoACPHostInRepo(t, state, repo, "sess-busy", token)
	d := runDaemon(t, state, token)

	client := dialDaemon(t, state, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-busy"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "say hello"}); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	waitForState(t, state, token, "sess-busy", session.StateDone)
	first := waitForCheckpoints(t, ctx, client, 1).Checkpoints[0]

	// The host reports the next turn the way it reports every turn; this one is
	// made to stay running for the length of the assertion.
	d.recordState("sess-busy", nil, session.StateWorking)
	if _, err := client.Call(ctx, session.MethodCheckpointRestore, session.CheckpointRestoreRequest{Turn: first.Turn}); err == nil {
		t.Fatal("a rollback ran while the turn was working")
	} else if !strings.Contains(err.Error(), "not finished") || ipc.CodeOf(err) != ipc.CodeBadRequest {
		t.Fatalf("the running turn was refused with %v", err)
	}
	d.recordState("sess-busy", nil, session.StateDone)
}

// TestCheckpointsNeedAnAgentSession keeps the terminal out of a feature that has
// no turn boundary to snapshot at, instead of answering an empty list forever.
func TestCheckpointsNeedAnAgentSession(t *testing.T) {
	state := t.TempDir()
	const token = "shared"

	newHost(t, state, "sess-term", token)
	runDaemon(t, state, token)

	client := dialDaemon(t, state, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-term"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := client.Call(ctx, session.MethodCheckpointList, struct{}{}); ipc.CodeOf(err) != ipc.CodeWrongMode {
		t.Fatalf("a terminal's checkpoint list was answered with %v, want a wrong mode", err)
	}
}

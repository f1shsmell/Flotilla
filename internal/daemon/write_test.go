package daemon

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/gittest"
	"github.com/f1shsmell/flotilla/internal/session"
)

// initRemote makes a bare repository to push to, so a push test never reaches a
// network or the user's real remotes.
func initRemote(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.CommandContext(context.Background(), "git", "init", "--bare", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("init a bare remote: %v: %s", err, out)
	}
	return dir
}

// TestCommitCrossesTheDaemon pins the first half of the review's ending: a
// window asks the daemon to commit what the agent changed, and the daemon writes
// to the checkout it holds.
func TestCommitCrossesTheDaemon(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)

	startHostIn(t, stateDir, "sess-commit", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-commit"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// The agent leaves a change behind; the commit is asked for by name.
	gittest.Write(t, repo, "notes.txt", "one\n")

	raw, err := client.Call(ctx, session.MethodCommit, session.CommitRequest{Message: "record notes"})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	var wrote session.WriteResponse
	if err := json.Unmarshal(raw, &wrote); err != nil {
		t.Fatalf("decode the commit response: %v", err)
	}
	if wrote.Ref == "" || !strings.Contains(wrote.Summary, wrote.Ref) {
		t.Fatalf("response = %+v, want a summary naming the commit", wrote)
	}

	// The repository has to actually contain it.
	if subject := gittest.Run(t, repo, "log", "-1", "--pretty=%s"); subject != "record notes" {
		t.Fatalf("HEAD subject = %q, want the message", subject)
	}
	if status := gittest.Run(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("the tree is not clean after the commit: %q", status)
	}
}

// TestCommitRefusalReachesTheClient pins the honest failure: an empty message is
// refused by the package, and that reason is what the window is told rather than
// a generic "the call failed".
func TestCommitRefusalReachesTheClient(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)

	startHostIn(t, stateDir, "sess-empty", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-empty"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	gittest.Write(t, repo, "notes.txt", "one\n")

	_, err := client.Call(ctx, session.MethodCommit, session.CommitRequest{Message: "  "})
	if err == nil {
		t.Fatal("a commit with an empty message was accepted")
	}
	if !strings.Contains(err.Error(), "empty message") {
		t.Fatalf("error = %v, want it to name the empty message", err)
	}
}

// TestPushCrossesTheDaemon pins the second half: the branch reaches origin
// through the daemon, using the machine's own git credentials.
func TestPushCrossesTheDaemon(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)
	remote := initRemote(t)
	gittest.Run(t, repo, "remote", "add", "origin", remote)

	startHostIn(t, stateDir, "sess-push", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-push"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	raw, err := client.Call(ctx, session.MethodPush, struct{}{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	var wrote session.WriteResponse
	if err := json.Unmarshal(raw, &wrote); err != nil {
		t.Fatalf("decode the push response: %v", err)
	}
	if wrote.Ref != "main" {
		t.Fatalf("push ref = %q, want the branch", wrote.Ref)
	}

	if head := gittest.Run(t, remote, "rev-parse", "--verify", "main"); head == "" {
		t.Fatal("the branch never reached the remote")
	}
}

// TestPullRequestNeedsATitle pins the guard that keeps a round trip from being
// spent on a request the package will refuse anyway.
func TestPullRequestNeedsATitle(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)

	startHostIn(t, stateDir, "sess-pr", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-pr"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := client.Call(ctx, session.MethodPullRequest, session.PullRequestRequest{Title: "  "}); err == nil {
		t.Fatal("a pull request with an empty title was accepted")
	}
}

// TestCommitWithoutGhStillWorks pins the scope of the gh dependency: a commit is
// local and must not be blocked by a missing GitHub CLI.
func TestCommitWithoutGhStillWorks(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)

	startHostIn(t, stateDir, "sess-local", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-local"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	gittest.Write(t, repo, "notes.txt", "one\n")
	if _, err := client.Call(ctx, session.MethodCommit, session.CommitRequest{Message: "local work"}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// gh was never consulted: the commit exists on its own.
	if subject := gittest.Run(t, repo, "log", "-1", "--pretty=%s"); subject != "local work" {
		t.Fatalf("HEAD subject = %q", subject)
	}
}

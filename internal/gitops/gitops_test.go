package gitops_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/gitops"
	"github.com/f1shsmell/flotilla/internal/gittest"
)

// initRemote makes a bare repository to push to, so a push test never touches a
// network or the user's real remotes.
func initRemote(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.CommandContext(context.Background(), "git", "init", "--bare", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("init a bare remote: %v: %s", err, out)
	}
	return dir
}

func TestCommitRecordsWhatTheAgentChanged(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "notes.txt", "one\n")

	res, err := gitops.Commit(context.Background(), repo, "add notes")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Ref == "" {
		t.Fatal("Commit reported no commit id")
	}
	if !strings.Contains(res.Summary, res.Ref) {
		t.Fatalf("summary %q does not name the commit %q", res.Summary, res.Ref)
	}

	// The commit must actually be there, on HEAD, with the file in it.
	subject := gittest.Run(t, repo, "log", "-1", "--pretty=%s")
	if subject != "add notes" {
		t.Fatalf("HEAD subject = %q, want the message", subject)
	}
	if tracked := gittest.Run(t, repo, "ls-files", "notes.txt"); tracked != "notes.txt" {
		t.Fatalf("notes.txt is not in the commit: %q", tracked)
	}

	// A commit stages everything, so the tree is clean afterwards.
	if status := gittest.Run(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("the tree is not clean after a commit: %q", status)
	}
}

func TestCommitRefusesACleanTree(t *testing.T) {
	repo := gittest.Init(t)

	_, err := gitops.Commit(context.Background(), repo, "nothing")
	if !errors.Is(err, gitops.ErrNothingToCommit) {
		t.Fatalf("Commit on a clean tree = %v, want ErrNothingToCommit", err)
	}
}

// TestCommitRefusesAnEmptyMessage pins the rule that this package will not
// invent a subject: an empty message is the caller's mistake, not a reason to
// write "update files".
func TestCommitRefusesAnEmptyMessage(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "notes.txt", "one\n")

	if _, err := gitops.Commit(context.Background(), repo, "   "); err == nil {
		t.Fatal("Commit accepted an empty message")
	}

	// Nothing was staged, so the change is still the user's to decide about.
	status := gittest.Run(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "notes.txt") {
		t.Fatalf("an empty message still touched the index: %q", status)
	}
}

func TestCommitRefusesAPathOutsideARepository(t *testing.T) {
	_, err := gitops.Commit(context.Background(), t.TempDir(), "message")
	if !errors.Is(err, gitops.ErrNotARepository) {
		t.Fatalf("Commit outside a repository = %v, want ErrNotARepository", err)
	}
}

// TestPushSendsTheBranchAndSetsUpstream pins both halves: the branch reaches the
// remote, and the second push has an upstream to use.
func TestPushSendsTheBranchAndSetsUpstream(t *testing.T) {
	repo := gittest.Init(t)
	remote := initRemote(t)
	gittest.Run(t, repo, "remote", "add", "origin", remote)

	res, err := gitops.Push(context.Background(), repo)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !strings.Contains(res.Summary, "main") {
		t.Fatalf("summary = %q, want the branch name", res.Summary)
	}

	// The remote has the branch, and the local branch now tracks it.
	if head := gittest.Run(t, remote, "rev-parse", "--verify", "main"); head == "" {
		t.Fatal("the branch never reached the remote")
	}
	if upstream := gittest.Run(t, repo, "rev-parse", "--abbrev-ref", "main@{upstream}"); upstream != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", upstream)
	}
}

// TestPushReportsARejectedPush pins the honest failure: a remote that moved on
// must produce an error with git's explanation, never a silent "pushed".
func TestPushReportsARejectedPush(t *testing.T) {
	repo := gittest.Init(t)
	remote := initRemote(t)
	gittest.Run(t, repo, "remote", "add", "origin", remote)
	if _, err := gitops.Push(context.Background(), repo); err != nil {
		t.Fatalf("first push: %v", err)
	}

	// Someone else commits to the same branch on the remote.
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.CommandContext(context.Background(), "git", "clone", remote, other).CombinedOutput(); err != nil {
		t.Fatalf("clone the remote: %v: %s", err, out)
	}
	// A commit needs a change to commit; writing one first is what a colleague
	// would have done.
	gittest.Write(t, other, "theirs.txt", "theirs\n")
	gittest.Commit(t, other, "someone else's work")
	gittest.Run(t, other, "push")

	// A local commit now cannot be pushed without a merge.
	gittest.Write(t, repo, "mine.txt", "mine\n")
	gittest.Commit(t, repo, "my work")

	if _, err := gitops.Push(context.Background(), repo); err == nil {
		t.Fatal("Push reported success for a rejected push")
	}
}

// TestPullRequestNeedsGh pins the missing-binary path: a machine without gh gets
// a named error, not a raw exec failure the caller has to parse.
func TestPullRequestNeedsGh(t *testing.T) {
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("gh is installed here, so the missing-binary path cannot be reached")
	}

	repo := gittest.Init(t)
	if _, err := gitops.PullRequest(context.Background(), repo, "title", "body", "main"); !errors.Is(err, gitops.ErrGhMissing) {
		t.Fatalf("PullRequest without gh = %v, want ErrGhMissing", err)
	}
}

// TestPullRequestRefusesAnEmptyTitle pins the same rule as Commit: a pull
// request without a title is not something this package will invent.
func TestPullRequestRefusesAnEmptyTitle(t *testing.T) {
	repo := gittest.Init(t)

	if _, err := gitops.PullRequest(context.Background(), repo, "  ", "body", "main"); err == nil {
		t.Fatal("PullRequest accepted an empty title")
	}
}

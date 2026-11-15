package diff_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/diff"
	"github.com/f1shsmell/flotilla/internal/gittest"
)

func TestUnstagedReportsAnEditedFile(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "readme.md", "hello\nworld\n")

	files, err := diff.Files(context.Background(), diff.Request{Repo: repo, Scope: diff.Unstaged})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("files = %+v, want the one edited file", files)
	}
	if got := files[0]; got.Path != "readme.md" || got.Status != "modified" || got.Added != 1 {
		t.Fatalf("file = %+v, want readme.md modified with one added line", got)
	}

	patch, err := diff.Patch(context.Background(), diff.Request{Repo: repo, Scope: diff.Unstaged}, "readme.md")
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !strings.Contains(patch, "+world") {
		t.Fatalf("patch does not carry the added line:\n%s", patch)
	}
}

// TestUnstagedReportsAnUntrackedFile pins the case git will not diff: an agent's
// new file is the most interesting line in a review.
func TestUnstagedReportsAnUntrackedFile(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "notes/new.txt", "one\ntwo\n")

	files, err := diff.Files(context.Background(), diff.Request{Repo: repo, Scope: diff.Unstaged})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	var found *diff.File
	for i := range files {
		if files[i].Path == "notes/new.txt" {
			found = &files[i]
		}
	}
	if found == nil {
		t.Fatalf("files = %+v, want the untracked file", files)
	}
	if !found.Untracked || found.Status != "added" {
		t.Fatalf("file = %+v, want an untracked addition", *found)
	}

	patch, err := diff.Patch(context.Background(), diff.Request{Repo: repo, Scope: diff.Unstaged}, "notes/new.txt")
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !strings.Contains(patch, "+one") || !strings.Contains(patch, "+two") {
		t.Fatalf("patch does not carry the whole file:\n%s", patch)
	}
}

func TestBranchSeesOnlyThisBranch(t *testing.T) {
	repo := gittest.Init(t)

	gittest.Run(t, repo, "checkout", "-b", "feat/work")
	gittest.Write(t, repo, "readme.md", "hello\nbranch\n")
	gittest.Commit(t, repo, "branch work")

	files, err := diff.Files(context.Background(), diff.Request{Repo: repo, Scope: diff.Branch})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "readme.md" {
		t.Fatalf("branch files = %+v, want the branch's own commit", files)
	}

	// Nothing is uncommitted, so the working tree scope is empty.
	unstaged, err := diff.Files(context.Background(), diff.Request{Repo: repo, Scope: diff.Unstaged})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(unstaged) != 0 {
		t.Fatalf("unstaged files = %+v, want none after a commit", unstaged)
	}
}

// TestTurnSeesEverythingSinceItsBaseline pins the scope a review of one turn
// needs: what the agent committed and what it left uncommitted.
func TestTurnSeesEverythingSinceItsBaseline(t *testing.T) {
	repo := gittest.Init(t)
	baseline := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))

	gittest.Write(t, repo, "committed.txt", "committed\n")
	gittest.Commit(t, repo, "the agent committed this")
	gittest.Write(t, repo, "readme.md", "hello\nleft uncommitted\n")

	files, err := diff.Files(context.Background(), diff.Request{
		Repo:  repo,
		Scope: diff.Turn,
		Base:  baseline,
	})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths["committed.txt"] || !paths["readme.md"] {
		t.Fatalf("turn files = %+v, want both the commit and the edit", files)
	}
}

func TestPatchRefusesAPathThatLeavesTheRepository(t *testing.T) {
	repo := gittest.Init(t)

	for _, path := range []string{"../outside.txt", "C:/Windows/win.ini", ""} {
		_, err := diff.Patch(context.Background(), diff.Request{Repo: repo, Scope: diff.Unstaged}, path)
		if !errors.Is(err, diff.ErrOutsideRepo) {
			t.Fatalf("Patch(%q) = %v, want ErrOutsideRepo", path, err)
		}
	}
}

func TestUnknownScopeIsRefused(t *testing.T) {
	repo := gittest.Init(t)

	_, err := diff.Files(context.Background(), diff.Request{Repo: repo, Scope: diff.Scope("everything")})
	if !errors.Is(err, diff.ErrUnknownScope) {
		t.Fatalf("Files with an unknown scope = %v, want ErrUnknownScope", err)
	}
}

func TestTurnWithoutABaselineIsRefused(t *testing.T) {
	repo := gittest.Init(t)

	_, err := diff.Files(context.Background(), diff.Request{Repo: repo, Scope: diff.Turn})
	if !errors.Is(err, diff.ErrUnknownScope) {
		t.Fatalf("Files for a turn without a base = %v, want ErrUnknownScope", err)
	}
}

func TestNotARepositoryIsRefused(t *testing.T) {
	_, err := diff.Files(context.Background(), diff.Request{Repo: t.TempDir(), Scope: diff.Unstaged})
	if !errors.Is(err, diff.ErrNotARepository) {
		t.Fatalf("Files outside a repository = %v, want ErrNotARepository", err)
	}
}

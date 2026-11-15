package worktree_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/worktree"
)

// initRepo makes a repository with one commit, a .gitignore that keeps .env out
// of git, and a .worktreeinclude that asks for it anyway.
func initRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	git("init", "-b", "main")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("readme.md", "hello\n")
	write(".gitignore", ".env\n")
	write(".worktreeinclude", ".env\nmissing.txt\n")

	git("add", ".")
	git("commit", "-m", "init")

	// Written after the commit, the way a real ignored secret is.
	write(".env", "SECRET=1\n")
	return dir
}

func TestCreateMakesAWorktreeOnItsOwnBranch(t *testing.T) {
	repo := initRepo(t)

	created, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   repo,
		Branch: "feat/one",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.Dir != filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-feat-one") {
		t.Fatalf("worktree landed at %q, want a sibling named after the branch", created.Dir)
	}
	if _, err := os.Stat(filepath.Join(created.Dir, ".git")); err != nil {
		t.Fatalf("the worktree has no .git: %v", err)
	}
	if branch := gitOutput(t, created.Dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "feat/one" {
		t.Fatalf("the worktree is on %q, want feat/one", branch)
	}
	if _, err := os.Stat(filepath.Join(created.Dir, "readme.md")); err != nil {
		t.Fatalf("the worktree has no readme: %v", err)
	}
}

func TestCreateCopiesWhatWorktreeincludeNames(t *testing.T) {
	repo := initRepo(t)

	created, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   repo,
		Branch: "feat/included",
		Dir:    filepath.Join(t.TempDir(), "wt"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(created.Included) != 1 || created.Included[0] != ".env" {
		t.Fatalf("included = %v, want the .env the file names", created.Included)
	}
	if len(created.Skipped) != 1 || created.Skipped[0] != "missing.txt" {
		t.Fatalf("skipped = %v, want the entry that does not exist", created.Skipped)
	}

	payload, err := os.ReadFile(filepath.Join(created.Dir, ".env"))
	if err != nil {
		t.Fatalf("read the copied .env: %v", err)
	}
	if string(payload) != "SECRET=1\n" {
		t.Fatalf(".env holds %q, want the repository's copy", payload)
	}
}

func TestCreateRefusesADestinationThatExists(t *testing.T) {
	repo := initRepo(t)
	taken := t.TempDir()

	_, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   repo,
		Branch: "feat/taken",
		Dir:    taken,
	})
	if !errors.Is(err, worktree.ErrDirExists) {
		t.Fatalf("Create into an existing directory = %v, want ErrDirExists", err)
	}
}

func TestCreateRefusesABranchGitWouldNotTake(t *testing.T) {
	repo := initRepo(t)

	_, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   repo,
		Branch: "bad..name",
	})
	if !errors.Is(err, worktree.ErrBadBranch) {
		t.Fatalf("Create with a bad branch = %v, want ErrBadBranch", err)
	}
}

func TestCreateRefusesSomethingThatIsNotARepository(t *testing.T) {
	_, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   t.TempDir(),
		Branch: "feat/nowhere",
	})
	if !errors.Is(err, worktree.ErrNotARepository) {
		t.Fatalf("Create outside a repository = %v, want ErrNotARepository", err)
	}
}

// TestCreateRefusesAnIncludeThatEscapes pins the sensitive half: a line in
// .worktreeinclude is a path from the repository, and nothing else.
func TestCreateRefusesAnIncludeThatEscapes(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte("../outside.txt\n"), 0o600); err != nil {
		t.Fatalf("write .worktreeinclude: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "wt")
	_, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   repo,
		Branch: "feat/escape",
		Dir:    dir,
	})
	if !errors.Is(err, worktree.ErrOutsideRepo) {
		t.Fatalf("Create with an escaping entry = %v, want ErrOutsideRepo", err)
	}

	// The failure must not leave a worktree or a branch behind.
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a failed create left its worktree on disk")
	}
	if branches := gitOutput(t, repo, "branch", "--list", "feat/escape"); strings.TrimSpace(branches) != "" {
		t.Fatalf("a failed create left branch %q behind", branches)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

package worktree_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/gittest"
	"github.com/f1shsmell/flotilla/internal/worktree"
)

func TestCreateMakesAWorktreeOnItsOwnBranch(t *testing.T) {
	repo := gittest.Init(t)

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
	if branch := gittest.Run(t, created.Dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "feat/one" {
		t.Fatalf("the worktree is on %q, want feat/one", branch)
	}
	if _, err := os.Stat(filepath.Join(created.Dir, "readme.md")); err != nil {
		t.Fatalf("the worktree has no readme: %v", err)
	}
}

func TestCreateCopiesWhatWorktreeincludeNames(t *testing.T) {
	repo := gittest.Init(t)
	// The repository's file names one entry that exists and one that does not.
	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte(".env\nmissing.txt\n"), 0o600); err != nil {
		t.Fatalf("write .worktreeinclude: %v", err)
	}

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
	repo := gittest.Init(t)
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
	repo := gittest.Init(t)

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
	repo := gittest.Init(t)
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
	if branches := gittest.Run(t, repo, "branch", "--list", "feat/escape"); strings.TrimSpace(branches) != "" {
		t.Fatalf("a failed create left branch %q behind", branches)
	}
}

// linkDir makes a directory link, which needs a privilege a plain test run may
// not hold: a junction is the fallback that works on Windows without it.
func linkDir(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err == nil {
		return
	}

	cmd := exec.CommandContext(context.Background(), "cmd", "/c", "mklink", "/J", link, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("this machine cannot create links: %v: %s", err, output)
	}
}

// TestCreateRefusesAnIncludeThatLinksOutside pins the other way out of the
// repository: a name that stays inside but is a link to somewhere else.
func TestCreateRefusesAnIncludeThatLinksOutside(t *testing.T) {
	repo := gittest.Init(t)

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("not yours\n"), 0o600); err != nil {
		t.Fatalf("write the outside file: %v", err)
	}
	linkDir(t, outside, filepath.Join(repo, "linked"))

	if err := os.WriteFile(filepath.Join(repo, ".worktreeinclude"), []byte("linked\n"), 0o600); err != nil {
		t.Fatalf("write .worktreeinclude: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "wt")
	_, err := worktree.Create(context.Background(), worktree.Spec{
		Repo:   repo,
		Branch: "feat/linked",
		Dir:    dir,
	})
	if !errors.Is(err, worktree.ErrOutsideRepo) {
		t.Fatalf("Create with a link out of the repository = %v, want ErrOutsideRepo", err)
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Fatal("a failed create left its worktree on disk")
	}
}

// Package worktree creates git worktrees, so two sessions can work on one
// repository without sharing a checkout.
//
// It is a sensitive path: every operation runs git against a user's repository,
// so nothing here guesses a path, and a name that could escape the repository is
// refused rather than cleaned up.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotARepository reports a path that is not the root of a git repository.
var ErrNotARepository = errors.New("worktree: not a git repository")

// ErrDirExists reports a destination that is already taken.
var ErrDirExists = errors.New("worktree: destination exists")

// ErrBadBranch reports a branch name git would not accept.
var ErrBadBranch = errors.New("worktree: invalid branch name")

// ErrOutsideRepo reports a .worktreeinclude entry that leaves the repository.
var ErrOutsideRepo = errors.New("worktree: entry escapes the repository")

// Spec is one worktree to create.
type Spec struct {
	// Repo is the repository's main worktree, the one with the real .git.
	Repo string
	// Branch is the branch to create and check out.
	Branch string
	// Base is the commit-ish to branch from. Empty means HEAD.
	Base string
	// Dir is where the worktree goes. Empty means a sibling of Repo named
	// after it and the branch.
	Dir string
}

// Created is what Create made.
type Created struct {
	// Dir is the worktree's path.
	Dir string
	// Branch is the branch checked out in it.
	Branch string
	// Included are the .worktreeinclude entries that were copied.
	Included []string
	// Skipped are the entries that were named but do not exist.
	Skipped []string
}

// Create adds a worktree on a new branch and copies what .worktreeinclude names
// into it. A failure to copy is reported, not swallowed: a worktree missing its
// .env is a session that cannot run.
func Create(ctx context.Context, spec Spec) (Created, error) {
	repo, err := repositoryRoot(spec.Repo)
	if err != nil {
		return Created{}, err
	}
	if spec.Branch == "" {
		return Created{}, fmt.Errorf("create a worktree in %q: empty branch: %w", repo, ErrBadBranch)
	}
	if err := checkBranch(ctx, repo, spec.Branch); err != nil {
		return Created{}, err
	}

	dir := spec.Dir
	if dir == "" {
		dir = siblingDir(repo, spec.Branch)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return Created{}, fmt.Errorf("resolve the worktree path %q: %w", spec.Dir, err)
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		return Created{}, fmt.Errorf("create a worktree at %q: %w", dir, ErrDirExists)
	}

	base := spec.Base
	if base == "" {
		base = "HEAD"
	}

	// --no-track keeps the new branch from adopting an upstream it does not have.
	if output, err := run(ctx, repo, "worktree", "add", "--no-track", "-b", spec.Branch, dir, base); err != nil {
		return Created{}, fmt.Errorf("add the worktree %q: %w: %s", dir, err, strings.TrimSpace(output))
	}

	included, skipped, err := copyIncluded(repo, dir)
	if err != nil {
		// A half-made worktree is worse than none: the caller asked for a
		// checkout it can work in, and leaving one behind also leaves a branch.
		discard(ctx, repo, dir, spec.Branch)
		return Created{}, fmt.Errorf("copy .worktreeinclude into %q: %w", dir, err)
	}

	return Created{Dir: dir, Branch: spec.Branch, Included: included, Skipped: skipped}, nil
}

// repositoryRoot checks that repo is the root of a working tree and returns its
// absolute path.
func repositoryRoot(repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("open the repository: empty path: %w", ErrNotARepository)
	}

	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", fmt.Errorf("resolve the repository %q: %w", repo, err)
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return "", fmt.Errorf("%q: %w", abs, ErrNotARepository)
	}
	return abs, nil
}

// checkBranch asks git whether the name is a branch it would accept, so the
// error names the branch rather than surfacing from worktree add.
func checkBranch(ctx context.Context, repo, branch string) error {
	if _, err := run(ctx, repo, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("branch %q: %w", branch, ErrBadBranch)
	}
	return nil
}

// siblingDir is where a worktree goes when the caller names no path: beside the
// repository, named after it and the branch.
func siblingDir(repo, branch string) string {
	name := filepath.Base(repo) + "-" + strings.ReplaceAll(branch, "/", "-")
	return filepath.Join(filepath.Dir(repo), name)
}

// copyIncluded copies the paths .worktreeinclude names, which is how ignored
// files such as .env reach a new worktree. A missing entry is reported as
// skipped; an entry that leaves the repository is an error.
func copyIncluded(repo, dest string) (included, skipped []string, err error) {
	payload, err := os.ReadFile(filepath.Join(repo, ".worktreeinclude"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read .worktreeinclude: %w", err)
	}

	// The repository's own path may itself be reached through a link, so both
	// sides of every containment check are compared after resolution.
	root := resolve(repo)

	for _, line := range strings.Split(string(payload), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}

		src := filepath.Join(repo, entry)
		if !inside(root, resolve(src)) {
			return nil, nil, fmt.Errorf("entry %q: %w", entry, ErrOutsideRepo)
		}

		info, statErr := os.Stat(src)
		if errors.Is(statErr, os.ErrNotExist) {
			skipped = append(skipped, entry)
			continue
		}
		if statErr != nil {
			return nil, nil, fmt.Errorf("inspect %q: %w", entry, statErr)
		}

		target := filepath.Join(dest, entry)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, nil, fmt.Errorf("create the directory for %q: %w", entry, err)
		}

		if info.IsDir() {
			if err := os.CopyFS(target, os.DirFS(src)); err != nil {
				return nil, nil, fmt.Errorf("copy the directory %q: %w", entry, err)
			}
		} else if err := copyFile(src, target); err != nil {
			return nil, nil, err
		}

		included = append(included, entry)
	}

	return included, skipped, nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %q: %w", dest, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %q: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %q: %w", dest, err)
	}
	return nil
}

// resolve returns path with every link along it followed. A path that does not
// exist is resolved as far as it goes, which is what a containment check needs:
// a link that points outside the repository is exactly the escape to catch.
func resolve(path string) string {
	if resolved, err := canonical(path); err == nil {
		return resolved
	}

	dir := filepath.Dir(path)
	if dir == path {
		return path
	}
	return filepath.Join(resolve(dir), filepath.Base(path))
}

// inside reports whether path is root or below it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// discard removes a worktree and the branch Create made for it.
func discard(ctx context.Context, repo, dir, branch string) {
	_, _ = run(ctx, repo, "worktree", "remove", "--force", dir)
	_, _ = run(ctx, repo, "branch", "-D", branch)
}

// run executes git in dir and returns its combined output.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

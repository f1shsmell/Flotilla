// Package gitops performs the writes a review ends with: commit what the agent
// changed, push the branch, and open a pull request.
//
// It shells out to the user's own git and gh, so credentials stay where the user
// already put them. Nothing here stores, reads or forwards a token: a push works
// because the machine's git credential helper works, and a pull request because
// gh is already logged in.
//
// It is a sensitive path. Every call writes to a user's repository, so the
// operations are the three named ones and nothing else: no reset, no force push,
// no branch deletion.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotARepository reports a path that is not inside a git working tree.
var ErrNotARepository = errors.New("gitops: not a git repository")

// ErrNothingToCommit reports a clean working tree. It is not a failure of the
// operation; it means there is no commit to make.
var ErrNothingToCommit = errors.New("gitops: nothing to commit")

// ErrGhMissing reports that the GitHub CLI is not installed, which is what a
// pull request needs and a commit does not.
var ErrGhMissing = errors.New("gitops: gh is not installed")

// Result is what one write produced.
type Result struct {
	// Summary is one line for a log or a toast: "committed 8a1f0c2", "opened #12".
	Summary string `json:"summary"`
	// Output is git's or gh's own text, kept because a failure's explanation
	// usually lives there and not in the error code.
	Output string `json:"output,omitempty"`
	// Ref names what the action produced: a commit id, a branch, a pull request
	// URL. Empty when the action has no such handle.
	Ref string `json:"ref,omitempty"`
}

// Commit stages every change in the working tree and commits it with message.
// An empty message is refused: a commit without one is worse than no commit,
// and this package must not invent a summary the user did not read.
func Commit(ctx context.Context, repo, message string) (Result, error) {
	root, err := repositoryRoot(ctx, repo)
	if err != nil {
		return Result{}, err
	}

	message = strings.TrimSpace(message)
	if message == "" {
		return Result{}, fmt.Errorf("commit: empty message")
	}

	status, err := run(ctx, root, "status", "--porcelain")
	if err != nil {
		return Result{}, fmt.Errorf("read the status of %q: %w", root, err)
	}
	if strings.TrimSpace(status) == "" {
		return Result{}, fmt.Errorf("commit in %q: %w", root, ErrNothingToCommit)
	}

	if _, err := run(ctx, root, "add", "-A"); err != nil {
		return Result{}, fmt.Errorf("stage the changes in %q: %w", root, err)
	}

	// --cleanup=strip removes the comment lines git would otherwise keep when a
	// message is passed on the command line, which keeps the first line the
	// subject whatever the editor would have done.
	if _, err := run(ctx, root, "commit", "-m", message, "--cleanup=strip"); err != nil {
		return Result{}, fmt.Errorf("commit in %q: %w", root, err)
	}

	sha, err := run(ctx, root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("read the new commit in %q: %w", root, err)
	}
	sha = strings.TrimSpace(sha)

	return Result{Summary: "committed " + sha, Ref: sha}, nil
}

// Push sends the current branch to origin, setting the upstream when the branch
// has none. It never forces: a rejected push means someone else pushed, and that
// is a decision for the user, not for a button.
func Push(ctx context.Context, repo string) (Result, error) {
	root, err := repositoryRoot(ctx, repo)
	if err != nil {
		return Result{}, err
	}

	branch, err := run(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("read the branch of %q: %w", root, err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return Result{}, fmt.Errorf("push from %q: detached HEAD", root)
	}

	// An upstream is what makes a bare "git push" unambiguous. Asking first is
	// cheap and turns "the branch has no upstream" into one working push.
	if _, err := run(ctx, root, "rev-parse", "--abbrev-ref", branch+"@{upstream}"); err != nil {
		if _, err := run(ctx, root, "push", "-u", "origin", branch); err != nil {
			return Result{}, fmt.Errorf("push %q to origin: %w", branch, err)
		}
		return Result{Summary: "pushed " + branch + " (new upstream)", Ref: branch}, nil
	}

	if _, err := run(ctx, root, "push"); err != nil {
		return Result{}, fmt.Errorf("push %q: %w", branch, err)
	}
	return Result{Summary: "pushed " + branch, Ref: branch}, nil
}

// PullRequest opens a pull request for the current branch with gh. It is the
// only operation here that needs gh, and it fails with ErrGhMissing rather than
// a raw exec error when the binary is not there.
func PullRequest(ctx context.Context, repo, title, body, base string) (Result, error) {
	root, err := repositoryRoot(ctx, repo)
	if err != nil {
		return Result{}, err
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return Result{}, fmt.Errorf("open a pull request: empty title")
	}

	args := []string{"pr", "create", "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base)
	}

	output, err := runGh(ctx, root, args...)
	if err != nil {
		return Result{}, fmt.Errorf("open a pull request from %q: %w", root, err)
	}

	// gh prints the new pull request's URL on stdout, which is the handle a
	// caller wants to show: everything else about it is a guess.
	url := firstURL(output)
	if url == "" {
		return Result{Summary: "opened a pull request", Output: output}, nil
	}
	return Result{Summary: "opened " + url, Ref: url, Output: output}, nil
}

// repositoryRoot checks that repo is inside a working tree and returns the
// absolute path, so the caller's relative path cannot move later.
func repositoryRoot(ctx context.Context, repo string) (string, error) {
	if strings.TrimSpace(repo) == "" {
		return "", fmt.Errorf("open the repository: empty path: %w", ErrNotARepository)
	}
	if _, err := run(ctx, repo, "rev-parse", "--show-toplevel"); err != nil {
		return "", fmt.Errorf("%q: %w", repo, ErrNotARepository)
	}
	return repo, nil
}

// firstURL returns the first line that looks like a URL, which is how gh
// reports the pull request it created.
func firstURL(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}

// run executes git in dir and returns its standard output. Standard error is
// folded into the error: git explains a refusal there, and losing it would turn
// "not a fast-forward" into a bare exit status.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	return exec1(ctx, "git", dir, args...)
}

// runGh executes gh in dir, reporting a missing binary as ErrGhMissing.
func runGh(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := exec1(ctx, "gh", dir, args...)
	if err != nil && errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("gh: %w", ErrGhMissing)
	}
	return output, err
}

func exec1(ctx context.Context, binary, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w: %s",
			binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}

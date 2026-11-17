// Package diff reads what changed in a repository, in the three scopes a review
// needs: the working tree, the branch against where it left its base, and
// everything since a session started.
//
// It is a sensitive path: every call runs git against a user's repository, and a
// path from a client is refused unless it stays inside it.
package diff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/f1shsmell/flotilla/internal/ipc"
)

// Scope names what a review compares.
type Scope string

const (
	// Unstaged is the working tree against the index: what the agent has edited
	// and not committed, plus files git does not track yet.
	Unstaged Scope = "unstaged"
	// Branch is what the branch has committed since it left its base.
	Branch Scope = "branch"
	// Turn is everything since a commit the caller names: commits and edits
	// together, which is what one turn can produce.
	Turn Scope = "turn"
)

// ErrNotARepository reports a path that is not the root of a git repository.
var ErrNotARepository = ipc.Class(errors.New("diff: not a git repository"), ipc.CodeNotARepository)

// ErrOutsideRepo reports a path that leaves the repository.
var ErrOutsideRepo = ipc.Class(errors.New("diff: path escapes the repository"), ipc.CodeOutsideRepo)

// ErrUnknownScope reports a scope this build does not read.
var ErrUnknownScope = ipc.Class(errors.New("diff: unknown scope"), ipc.CodeUnknownScope)

// File is one changed path, with the counts a list needs.
type File struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	// Binary marks a file git will not count lines for.
	Binary bool `json:"binary,omitempty"`
	// Untracked marks a file git does not know yet.
	Untracked bool `json:"untracked,omitempty"`
}

// Request is one review's question.
type Request struct {
	// Repo is the worktree to read.
	Repo string
	// Scope decides what is compared.
	Scope Scope
	// Base is the commit a branch left from, or the commit a turn started at.
	// Empty means the repository's default branch for Branch, and is an error
	// for Turn.
	Base string
}

// Files lists what changed in the request's scope.
func Files(ctx context.Context, req Request) ([]File, error) {
	repo, err := repositoryRoot(ctx, req.Repo)
	if err != nil {
		return nil, err
	}

	args, err := diffArgs(ctx, repo, req)
	if err != nil {
		return nil, err
	}

	numstat, err := run(ctx, repo, append(args, "--numstat")...)
	if err != nil {
		return nil, fmt.Errorf("read the diff of %q: %w", repo, err)
	}

	statuses, err := statusMap(ctx, repo, args)
	if err != nil {
		return nil, err
	}

	files, err := parseNumstat(numstat)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if status, ok := statuses[files[i].Path]; ok {
			files[i].Status = status
		}
	}

	if req.Scope == Unstaged {
		untracked, err := untrackedFiles(ctx, repo)
		if err != nil {
			return nil, err
		}
		files = append(files, untracked...)
	}

	return files, nil
}

// Patch returns one file's unified diff, ready to render line by line.
func Patch(ctx context.Context, req Request, path string) (string, error) {
	repo, err := repositoryRoot(ctx, req.Repo)
	if err != nil {
		return "", err
	}

	safe, err := insidePath(repo, path)
	if err != nil {
		return "", err
	}

	args, err := diffArgs(ctx, repo, req)
	if err != nil {
		return "", err
	}

	// An untracked file has no diff to ask git for: it is all additions.
	if req.Scope == Unstaged {
		tracked, err := run(ctx, repo, "ls-files", "--error-unmatch", "--", safe)
		if err != nil || strings.TrimSpace(tracked) == "" {
			return untrackedPatch(repo, safe)
		}
	}

	patch, err := run(ctx, repo, append(args, "--", safe)...)
	if err != nil {
		return "", fmt.Errorf("read the diff of %q: %w", safe, err)
	}
	return patch, nil
}

// diffArgs is the git invocation a scope needs, without the output format.
func diffArgs(ctx context.Context, repo string, req Request) ([]string, error) {
	switch req.Scope {
	case Unstaged:
		return []string{"diff"}, nil
	case Branch:
		base := req.Base
		if base == "" {
			var err error
			if base, err = defaultBranch(ctx, repo); err != nil {
				return nil, err
			}
		}
		// Three dots compares against the merge base, so commits the base branch
		// gained after this branch left do not show up as this branch's work.
		return []string{"diff", base + "...HEAD"}, nil
	case Turn:
		if req.Base == "" {
			return nil, fmt.Errorf("read a turn's diff: no starting commit: %w", ErrUnknownScope)
		}
		return []string{"diff", req.Base}, nil
	default:
		return nil, fmt.Errorf("scope %q: %w", req.Scope, ErrUnknownScope)
	}
}

// defaultBranch names the branch this one left, falling back to the two names
// repositories actually use.
func defaultBranch(ctx context.Context, repo string) (string, error) {
	if head, err := run(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimSpace(head); name != "" {
			return name, nil
		}
	}

	for _, name := range []string{"main", "master"} {
		if _, err := run(ctx, repo, "rev-parse", "--verify", name); err == nil {
			return name, nil
		}
	}

	return "", fmt.Errorf("find the base branch of %q: %w", repo, ErrNotARepository)
}

// parseNumstat turns git's per-file counts into files. A rename shows as
// "old => new" or "{old => new}"; both become one file with two paths.
func parseNumstat(output string) ([]File, error) {
	var files []File

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("read a numstat line %q: %w", line, ErrUnknownScope)
		}

		file := File{Status: "modified"}

		if fields[0] == "-" || fields[1] == "-" {
			file.Binary = true
		} else {
			added, err := strconv.Atoi(fields[0])
			if err != nil {
				return nil, fmt.Errorf("read the added count in %q: %w", line, err)
			}
			removed, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("read the removed count in %q: %w", line, err)
			}
			file.Added, file.Removed = added, removed
		}

		// The counts say how much changed, not what happened: git's own status
		// does, so it is read separately and applied by the caller.
		file.Path, file.OldPath = splitRename(fields[2])
		if file.OldPath != "" {
			file.Status = "renamed"
		}

		files = append(files, file)
	}

	return files, nil
}

// statusMap asks git what happened to each path. An edited file that only gained
// lines is a modification, which counts alone cannot tell from an addition.
func statusMap(ctx context.Context, repo string, args []string) (map[string]string, error) {
	output, err := run(ctx, repo, append(args, "--name-status")...)
	if err != nil {
		return nil, fmt.Errorf("read the status of %q: %w", repo, err)
	}

	statuses := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}

		path := fields[len(fields)-1]
		switch fields[0][0] {
		case 'A':
			statuses[path] = "added"
		case 'M', 'T':
			statuses[path] = "modified"
		case 'D':
			statuses[path] = "deleted"
		case 'R':
			statuses[path] = "renamed"
		case 'C':
			statuses[path] = "added"
		}
	}
	return statuses, nil
}

// splitRename reads git's two rename spellings.
func splitRename(path string) (current, previous string) {
	if !strings.Contains(path, "=>") {
		return path, ""
	}

	open := strings.Index(path, "{")
	if open < 0 {
		parts := strings.SplitN(path, " => ", 2)
		if len(parts) != 2 {
			return path, ""
		}
		return strings.TrimSpace(parts[1]), strings.TrimSpace(parts[0])
	}

	close := strings.Index(path[open:], "}")
	if close < 0 {
		return path, ""
	}
	prefix := strings.TrimSuffix(path[:open], " ")
	suffix := path[open+close+1:]
	parts := strings.SplitN(path[open+1:open+close], " => ", 2)
	if len(parts) != 2 {
		return path, ""
	}

	current = prefix + strings.TrimSpace(parts[1]) + suffix
	previous = prefix + strings.TrimSpace(parts[0]) + suffix
	return filepath.ToSlash(current), filepath.ToSlash(previous)
}

// untrackedFiles lists what git does not track, which a review has to show:
// an agent's new file is the most interesting line in it.
func untrackedFiles(ctx context.Context, repo string) ([]File, error) {
	output, err := run(ctx, repo, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list the untracked files of %q: %w", repo, err)
	}

	var files []File
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		files = append(files, File{Path: path, Status: "added", Untracked: true})
	}
	return files, nil
}

// untrackedPatch writes the diff git cannot: a new file is all additions.
func untrackedPatch(repo, path string) (string, error) {
	payload, err := os.ReadFile(filepath.Join(repo, path))
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}

	lines := strings.Split(strings.TrimSuffix(string(payload), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n", path, path, path)
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		b.WriteString("+" + line + "\n")
	}
	return b.String(), nil
}

// repositoryRoot checks that repo is the root of a working tree.
func repositoryRoot(ctx context.Context, repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("open the repository: empty path: %w", ErrNotARepository)
	}

	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", fmt.Errorf("resolve the repository %q: %w", repo, err)
	}
	if _, err := run(ctx, abs, "rev-parse", "--show-toplevel"); err != nil {
		return "", fmt.Errorf("%q: %w", abs, ErrNotARepository)
	}
	return abs, nil
}

// insidePath refuses a path from a client that would leave the repository.
func insidePath(repo, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("read a diff: empty path: %w", ErrOutsideRepo)
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q: %w", path, ErrOutsideRepo)
	}

	cleaned := filepath.Clean(path)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q: %w", path, ErrOutsideRepo)
	}
	if !inside(repo, filepath.Join(repo, cleaned)) {
		return "", fmt.Errorf("path %q: %w", path, ErrOutsideRepo)
	}
	return filepath.ToSlash(cleaned), nil
}

// inside reports whether path is root or below it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// run executes git in dir and returns its standard output. Standard error is
// kept out of it: git warns on stderr (line endings, for one), and a warning is
// not a line of output.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		return string(output), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}

package gitops

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
)

// A session's turns leave hidden refs in its worktree, one per finished turn.
// The refs travel with the repository and need no store: the daemon reads the
// snapshots back from git, so a restarted daemon loses nothing.
const checkpointPrefix = "refs/flotilla/checkpoints"

// ErrNoCheckpoint reports a turn the session has no snapshot for.
var ErrNoCheckpoint = ipc.Class(errors.New("gitops: no such checkpoint"), ipc.CodeBadRequest)

// Checkpoint is one finished turn's snapshot.
type Checkpoint struct {
	// Turn counts the session's snapshots from 1 in the order they were taken.
	Turn int
	// Commit is the snapshot's commit; its tree is the working tree it saw.
	Commit string
	// At is when the snapshot was taken.
	At time.Time
}

// Capture records the session's working tree as the snapshot of one finished
// turn. It writes an index of its own, a tree, a commit and the hidden ref;
// HEAD, the branch and the user's index are untouched. A tree identical to the
// previous snapshot reuses it and reports created false.
func Capture(ctx context.Context, repo, session string) (Checkpoint, bool, error) {
	scope, err := checkpointScope(session)
	if err != nil {
		return Checkpoint{}, false, err
	}
	root, err := worktreeRoot(ctx, repo)
	if err != nil {
		return Checkpoint{}, false, err
	}
	existing, err := checkpointsIn(ctx, root, scope)
	if err != nil {
		return Checkpoint{}, false, err
	}

	// The temporary index keeps the user's staged work out of the snapshot and
	// leaves their index file unlocked for their own git commands.
	temp, err := os.MkdirTemp("", "flotilla-checkpoint-")
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("checkpoint in %q: %w", root, err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(temp, "index")}

	if _, err := runEnv(ctx, root, env, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		if _, err := runEnv(ctx, root, env, "read-tree", "HEAD"); err != nil {
			return Checkpoint{}, false, fmt.Errorf("read HEAD in %q: %w", root, err)
		}
	} else if _, err := runEnv(ctx, root, env, "read-tree", "--empty"); err != nil {
		return Checkpoint{}, false, fmt.Errorf("start an empty index in %q: %w", root, err)
	}
	if _, err := runEnv(ctx, root, env, "add", "-A"); err != nil {
		return Checkpoint{}, false, fmt.Errorf("stage the working tree in %q: %w", root, err)
	}
	tree, err := runEnv(ctx, root, env, "write-tree")
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("write the tree of %q: %w", root, err)
	}
	tree = strings.TrimSpace(tree)

	last := Checkpoint{}
	if len(existing) > 0 {
		last = existing[len(existing)-1]
		same, err := run(ctx, root, "rev-parse", last.Commit+"^{tree}")
		if err == nil && strings.TrimSpace(same) == tree {
			return last, false, nil
		}
	}

	turn := last.Turn + 1
	args := []string{"commit-tree", tree, "-m", fmt.Sprintf("flotilla checkpoint: turn %d", turn)}
	if _, err := run(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		args = append(args, "-p", "HEAD")
	}
	commit, err := run(ctx, root, args...)
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("record the checkpoint in %q: %w", root, err)
	}
	commit = strings.TrimSpace(commit)
	if _, err := run(ctx, root, "update-ref", checkpointRef(scope, turn), commit); err != nil {
		return Checkpoint{}, false, fmt.Errorf("name the checkpoint in %q: %w", root, err)
	}
	return Checkpoint{Turn: turn, Commit: commit, At: time.Now()}, true, nil
}

// Checkpoints lists one session's snapshots, oldest first. Another session's
// snapshots in the same checkout belong to that session and never appear.
func Checkpoints(ctx context.Context, repo, session string) ([]Checkpoint, error) {
	scope, err := checkpointScope(session)
	if err != nil {
		return nil, err
	}
	root, err := worktreeRoot(ctx, repo)
	if err != nil {
		return nil, err
	}
	return checkpointsIn(ctx, root, scope)
}

// Restore rewrites the working tree and index from one turn's snapshot. HEAD
// stays where it is: a rollback is uncommitted change, not rewritten history.
// Files the snapshot never held are removed; ignored files stay, because the
// snapshot never promised them.
func Restore(ctx context.Context, repo, session string, turn int) (Result, error) {
	scope, err := checkpointScope(session)
	if err != nil {
		return Result{}, err
	}
	root, err := worktreeRoot(ctx, repo)
	if err != nil {
		return Result{}, err
	}

	ref := checkpointRef(scope, turn)
	commit, err := run(ctx, root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return Result{}, fmt.Errorf("restore turn %d in %q: %w", turn, root, ErrNoCheckpoint)
	}
	commit = strings.TrimSpace(commit)

	if _, err := run(ctx, root, "read-tree", "--reset", "-u", commit); err != nil {
		return Result{}, fmt.Errorf("restore turn %d in %q: %w", turn, root, err)
	}
	// What is untracked now was created after the snapshot, because the snapshot
	// captured every untracked file the moment it was taken.
	output, err := run(ctx, root, "clean", "-fd")
	if err != nil {
		return Result{}, fmt.Errorf("remove the files turn %d never held in %q: %w", turn, root, err)
	}

	return Result{
		Summary: fmt.Sprintf("restored turn %d", turn),
		Ref:     commit,
		Output:  strings.TrimSpace(output),
	}, nil
}

// Root returns the working tree root that holds repo, with the platform's path
// separators.
func Root(ctx context.Context, repo string) (string, error) {
	return worktreeRoot(ctx, repo)
}

// worktreeRoot checks that repo is inside a working tree and returns the
// checkout's root, so a session started in a subdirectory still names, lists
// and restores the whole repository.
func worktreeRoot(ctx context.Context, repo string) (string, error) {
	if strings.TrimSpace(repo) == "" {
		return "", fmt.Errorf("open the repository: empty path: %w", ErrNotARepository)
	}
	output, err := run(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%q: %w", repo, ErrNotARepository)
	}
	root := strings.TrimSpace(output)
	if root == "" {
		return "", fmt.Errorf("%q: %w", repo, ErrNotARepository)
	}
	return filepath.FromSlash(root), nil
}

// checkpointsIn reads one session's refs. Ref names cannot hold a space, so it
// is the separator the fields need.
func checkpointsIn(ctx context.Context, root, scope string) ([]Checkpoint, error) {
	prefix := checkpointPrefix + "/" + scope + "/"
	output, err := run(ctx, root, "for-each-ref",
		"--format=%(refname) %(objectname) %(committerdate:unix)", prefix)
	if err != nil {
		return nil, fmt.Errorf("list the checkpoints in %q: %w", root, err)
	}

	var list []Checkpoint
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		turn, err := strconv.Atoi(strings.TrimPrefix(fields[0], prefix))
		if err != nil || turn <= 0 {
			continue
		}
		at := time.Time{}
		if seconds, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			at = time.Unix(seconds, 0).UTC()
		}
		list = append(list, Checkpoint{Turn: turn, Commit: fields[1], At: at})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Turn < list[j].Turn })
	return list, nil
}

// checkpointRef names one snapshot. The encoded session id is one ref segment:
// base64url has no separator, and every character in it is legal in a ref.
func checkpointRef(scope string, turn int) string {
	return fmt.Sprintf("%s/%s/%d", checkpointPrefix, scope, turn)
}

// checkpointScope encodes a session id into the ref segment that namespaces its
// snapshots.
func checkpointScope(session string) (string, error) {
	if session == "" {
		return "", errors.New("checkpoint: empty session id")
	}
	return base64.RawURLEncoding.EncodeToString([]byte(session)), nil
}

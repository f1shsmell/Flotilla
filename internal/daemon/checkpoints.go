package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/gitops"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/worktree"
)

// errCheckpointNeedsAgent reports checkpoints asked for on a session whose mode
// has no turns. A terminal leaves no turn boundary to snapshot at.
var errCheckpointNeedsAgent = ipc.Class(errors.New("daemon: only an agent session has checkpoints"), ipc.CodeWrongMode)

// errCheckpointUnsettled reports a rollback asked for while the session's state
// is not a settled turn. The agent may hold the files, and unknown is not a
// state a destructive rewrite may treat as safe.
var errCheckpointUnsettled = ipc.Class(errors.New("daemon: the turn is not finished"), ipc.CodeBadRequest)

// errCheckpointShared reports a rollback in a checkout another attached session
// drives. The window that asked cannot see that session, so the rewrite would
// land under an agent nobody asked it to disturb.
var errCheckpointShared = ipc.Class(errors.New("daemon: another session uses this checkout"), ipc.CodeBadRequest)

// checkpointTimeout bounds a snapshot, so a slow or wedged checkout costs the
// turn its rollback point instead of holding the queue's next message behind it.
const checkpointTimeout = 30 * time.Second

// captureCheckpoint snapshots a finished turn. It runs before the queue's next
// turn starts, so a checkpoint names the tree its own turn left behind rather
// than a mix of two.
func (d *Daemon) captureCheckpoint(id session.ID) {
	att := d.lookup(id)
	if att == nil || att.Info.Mode != string(session.ModeACP) || att.Info.Dir == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	cp, created, err := gitops.Capture(ctx, att.Info.Dir, string(id))
	if err != nil {
		// A session outside a repository has nothing to snapshot; any other
		// failure costs one turn its rollback point, which the list then lacks.
		if !errors.Is(err, gitops.ErrNotARepository) {
			d.cfg.Log.Warn("checkpoint a finished turn failed", "session", id, "err", err.Error())
		}
		return
	}
	if created {
		d.cfg.Log.Info("checkpointed a finished turn", "session", id, "turn", cp.Turn, "ref", cp.Commit)
	}
}

// listCheckpoints answers a session's snapshots from the repository they live in.
func (d *Daemon) listCheckpoints(ctx context.Context, id session.ID) (any, error) {
	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointList, id, ErrUnknownSession)
	}
	if att.Info.Mode != string(session.ModeACP) {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointList, id, errCheckpointNeedsAgent)
	}

	list, err := gitops.Checkpoints(ctx, att.Info.Dir, string(id))
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointList, id, err)
	}
	out := session.CheckpointListResponse{Checkpoints: make([]session.Checkpoint, 0, len(list))}
	for _, cp := range list {
		out.Checkpoints = append(out.Checkpoints, session.Checkpoint{Turn: cp.Turn, Commit: cp.Commit, At: cp.At})
	}
	return out, nil
}

// restoreCheckpoint rolls the session's working tree back to one finished turn.
func (d *Daemon) restoreCheckpoint(ctx context.Context, id session.ID, params json.RawMessage) (any, error) {
	var req session.CheckpointRestoreRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodCheckpointRestore, err)
	}

	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointRestore, id, ErrUnknownSession)
	}
	if att.Info.Mode != string(session.ModeACP) {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointRestore, id, errCheckpointNeedsAgent)
	}

	root, err := gitops.Root(ctx, att.Info.Dir)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointRestore, id, err)
	}

	// The state and the other sessions holding this checkout are read under one
	// lock, so the decision is not split across two looks at the fleet.
	d.mu.Lock()
	state, served := d.stateUnderLock(id)
	holder := d.checkoutHolderUnderLock(id, root)
	d.mu.Unlock()
	if !served {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointRestore, id, ErrUnknownSession)
	}
	if state != session.StateDone && state != session.StateIdle {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointRestore, id, errCheckpointUnsettled)
	}
	if holder != "" {
		return nil, fmt.Errorf("%s on session %q (held by %q): %w",
			session.MethodCheckpointRestore, id, holder, errCheckpointShared)
	}

	res, err := gitops.Restore(ctx, att.Info.Dir, string(id), req.Turn)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCheckpointRestore, id, err)
	}
	d.cfg.Log.Info("session restored a checkpoint", "session", id, "turn", req.Turn, "ref", res.Ref)
	return session.WriteResponse{Summary: res.Summary, Ref: res.Ref, Output: res.Output}, nil
}

// checkoutHolderUnderLock names another attached session that works in root or
// around it. The caller holds d.mu.
func (d *Daemon) checkoutHolderUnderLock(id session.ID, root string) session.ID {
	for i := range d.fleet.Attachments {
		att := &d.fleet.Attachments[i]
		if att.Record.ID == id || att.Info.Dir == "" {
			continue
		}
		if within(root, att.Info.Dir) || within(att.Info.Dir, root) {
			return att.Record.ID
		}
	}
	return ""
}

// within reports whether child is parent or below it. One checkout can be
// spelled more than one way — a short name, a junction — so a spelling
// difference is resolved through the filesystem before it reads as two trees.
func within(parent, child string) bool {
	if relWithin(parent, child) {
		return true
	}
	resolvedParent, ok := resolvePath(parent)
	if !ok {
		return false
	}
	resolvedChild, ok := resolvePath(child)
	if !ok {
		return false
	}
	return relWithin(resolvedParent, resolvedChild)
}

// resolvePath gives the spelling the filesystem keeps for a path that exists:
// absolute, with short names expanded and junctions and symlinks followed.
func resolvePath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	resolved, err := worktree.Canonical(abs)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// relWithin reports whether child is parent or below it as written; a session
// started in a subdirectory is as affected by a rollback as one at the root.
func relWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if runtime.GOOS == "windows" {
		parent = strings.ToLower(parent)
		child = strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

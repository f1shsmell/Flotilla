package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/worktree"
)

// Allowed methods on the daemon connection.
const (
	// MethodStatus reports the fleet this daemon currently serves.
	MethodStatus = "daemon.status"
	// MethodSessionStart spawns a session host.
	MethodSessionStart = "session.start"
	// MethodSessionStop ends a session from the control plane. A client that
	// already attached asks its own connection instead; both reach the same end.
	MethodSessionStop = session.MethodStop
)

func (d *Daemon) handleCall(ctx context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
	// A bound connection speaks for one session, so every call on it belongs to
	// that session rather than to the control plane.
	if id, ok := d.relay.sessionOf(peer); ok {
		return d.relayCall(ctx, id, method, params)
	}

	switch method {
	case MethodStatus:
		return d.status(), nil
	case MethodSessionStart:
		return d.startSession(ctx, params)
	case MethodSessionStop:
		return d.stopSession(ctx, params)
	case session.MethodAttach:
		return d.attachClient(peer, params)
	default:
		return nil, fmt.Errorf("%w: %s", errUnknownMethod, method)
	}
}

func (d *Daemon) status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()

	st := Status{
		Channel:  d.cfg.Channel,
		PID:      os.Getpid(),
		Ready:    true,
		Sessions: make([]StatusEntry, 0, len(d.fleet.Attachments)),
	}

	for i := range d.fleet.Attachments {
		att := d.fleet.Attachments[i]
		reachable := att.Conn.Alive()

		// A host this daemon cannot reach has no lifecycle it can vouch for.
		// Reporting the last state it sent would be a stale label, which
		// FLOTILLA.md forbids: an unreachable session is unknown, never done.
		state := att.Info.State
		if !reachable || !state.Valid() {
			state = session.StateUnknown
		}

		st.Sessions = append(st.Sessions, StatusEntry{
			ID:        att.Record.ID,
			Mode:      session.Mode(att.Info.Mode),
			HostPID:   att.Info.PID,
			Command:   att.Info.Command,
			Dir:       att.Info.Dir,
			State:     state,
			Reachable: reachable,
		})
	}

	for _, f := range d.fleet.Failures {
		st.Failures = append(st.Failures, f.Error())
	}
	return st
}

func (d *Daemon) startSession(ctx context.Context, params json.RawMessage) (any, error) {
	var req StartRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", MethodSessionStart, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: empty session id: %w", MethodSessionStart, session.ErrNoRecord)
	}
	if req.Command == "" {
		return nil, fmt.Errorf("%s: empty command", MethodSessionStart)
	}

	// A start for a session this daemon already serves is a retry, not a second
	// host: the caller's id chose the session, so it names one agent.
	if att := d.lookup(req.ID); att != nil {
		return StartResponse{
			ID:       req.ID,
			HostPID:  att.Info.PID,
			Endpoint: att.Record.Endpoint,
			Dir:      att.Info.Dir,
		}, nil
	}

	dir, included, skipped, err := worktreeFor(ctx, req)
	if err != nil {
		return nil, err
	}
	req.Dir = dir
	// A worktree outlives a failed start: it holds a branch and whatever
	// .worktreeinclude copied, so it is kept and named rather than deleted.
	kept := ""
	if req.Worktree != nil {
		kept = fmt.Sprintf(" (its worktree is at %q)", dir)
	}

	// The turn scope compares against where the session started, so the commit
	// is taken now, before the agent touches anything.
	if head, err := gitHead(ctx, dir); err == nil {
		d.remember(req.ID, head)
	}

	endpoint, err := d.spawn(ctx, SpawnRequest{
		Binary:   d.cfg.HostBinary,
		Session:  req.ID,
		Mode:     req.Mode,
		Command:  req.Command,
		Args:     req.Args,
		Dir:      req.Dir,
		Policy:   req.Policy,
		Cols:     req.Cols,
		Rows:     req.Rows,
		Channel:  d.cfg.Channel,
		StateDir: d.cfg.StateDir,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn session %q: %w%s", req.ID, err, kept)
	}

	att, err := AttachOne(ctx, d.reg, req.ID, d.cfg.Token, d.cfg.StateDir, d.hostHooks(req.ID))
	if err != nil {
		return nil, fmt.Errorf("attach the new session %q: %w%s", req.ID, err, kept)
	}

	d.mu.Lock()
	d.fleet.Attachments = append(d.fleet.Attachments, att)
	d.mu.Unlock()
	d.watch(att)

	d.cfg.Log.Info("session started",
		"session", req.ID, "host_pid", att.Info.PID, "endpoint", endpoint.String(), "dir", dir)

	return StartResponse{
		ID:       req.ID,
		HostPID:  att.Info.PID,
		Endpoint: endpoint,
		Dir:      dir,
		Included: included,
		Skipped:  skipped,
	}, nil
}

// gitHead reports the commit a directory is on, or an empty string when it is
// not a repository.
func gitHead(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		return "", errors.New("no directory")
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read the commit of %q: %w", dir, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// worktreeFor creates the worktree a request asks for and reports where the
// session should run. Without a request the caller's own directory stands.
func worktreeFor(ctx context.Context, req StartRequest) (dir string, included, skipped []string, err error) {
	if req.Worktree == nil {
		return req.Dir, nil, nil, nil
	}

	created, err := worktree.Create(ctx, worktree.Spec{
		Repo:   req.Worktree.Repo,
		Branch: req.Worktree.Branch,
		Base:   req.Worktree.Base,
		Dir:    req.Worktree.Dir,
	})
	if err != nil {
		return "", nil, nil, fmt.Errorf("create a worktree for session %q: %w", req.ID, err)
	}

	return created.Dir, created.Included, created.Skipped, nil
}

func (d *Daemon) lookup(id session.ID) *Attachment {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == id {
			return &d.fleet.Attachments[i]
		}
	}
	return nil
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/worktree"
)

// Allowed methods on the daemon connection.
const (
	// MethodStatus reports the fleet this daemon currently serves.
	MethodStatus = "daemon.status"
	// MethodAgents lists the agents this machine can offer, with their switches.
	MethodAgents = "daemon.agents"
	// MethodAgentSet changes one agent's switches.
	MethodAgentSet = "daemon.agent.set"
	// MethodSessionStart spawns a session host.
	MethodSessionStart = "session.start"
	// MethodSessionStop ends a session from the control plane. A client that
	// already attached asks its own connection instead; both reach the same end.
	MethodSessionStop = session.MethodStop
	// MethodSessionArchive reads a session that is over. The daemon answers it
	// itself: the host that ran the agent is gone, and the log it left behind
	// is the daemon's.
	MethodSessionArchive = session.MethodArchive
)

func (d *Daemon) handleCall(ctx context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
	// A bound connection speaks for one session, so every call on it belongs to
	// that session rather than to the control plane.
	if id, ok := d.relay.sessionOf(peer); ok {
		return d.relayCall(ctx, peer, id, method, params)
	}

	switch method {
	case MethodStatus:
		return d.status(peer), nil
	case MethodAgents:
		return d.agents.List(), nil
	case MethodAgentSet:
		return d.setAgent(params)
	case MethodSessionStart:
		return d.startSession(ctx, params)
	case MethodSessionStop:
		return d.stopSession(ctx, params)
	case MethodSessionArchive:
		return d.archive(params)
	case session.MethodAttach:
		return d.attachClient(ctx, peer, params)
	default:
		return nil, fmt.Errorf("%w: %s", errUnknownMethod, method)
	}
}

// setAgent applies one switch change. The request names the agent, so a request
// that names none cannot be guessed at.
func (d *Daemon) setAgent(params json.RawMessage) (any, error) {
	var req SetAgentRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", MethodAgentSet, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: empty agent id: %w", MethodAgentSet, agent.ErrNoAgent)
	}

	entry, err := d.agents.Set(req.ID, req.Change)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MethodAgentSet, err)
	}
	return entry, nil
}

// status reports the fleet to one client. A finished turn that client has
// already looked at is idle for it and stays done for every other: `seen` is per
// client, so the daemon — the only layer that knows which client asked — is what
// projects it. A caller with no connection (a test, a raw CLI) sees what the
// host said and is served every field.
//
// A peer that did not name the diagnostics capability is not refused: the extra
// diagnostic fields are dropped instead. Nothing branches on them, so an older
// window reads a quieter report rather than no report at all. This is the first
// place the capability set actually withholds something, which is what keeps the
// gate from being decoration.
func (d *Daemon) status(peer *ipc.Conn) Status {
	client := ""
	diagnostics := true
	if peer != nil {
		client = peer.Client()
		diagnostics = ipc.Has(peer.PeerCapabilities(), ipc.CapabilityDiagnostics)
	}

	st := d.fleetStatus(client, diagnostics)
	// The archive is read with the fleet lock released: it lists the disk, and
	// a poll must not hold the fleet while a slow disk answers. It is a field of
	// its own rather than a sixth lifecycle value — a finished run has no
	// process, and folding it into `sessions` would let a reader act on it.
	st.Archived = d.archivedEntries()
	return st
}

// fleetStatus projects the sessions this daemon serves for one client.
func (d *Daemon) fleetStatus(client string, diagnostics bool) Status {
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
		state = projectSeen(state, d.relay.hasSeen(att.Record.ID, client))

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

	if diagnostics {
		for _, f := range d.fleet.Failures {
			st.Failures = append(st.Failures, f.Error())
		}
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

	// A session id can be reused after its first host is gone, and the daemon
	// keeps that run's transcript. The new host has to number past it: from one
	// again, its first update would look like a replay of the old run's and be
	// dropped. A log that cannot be read is not a refusal — the session still
	// starts, it just numbers from one.
	seqBase, err := d.transcripts.LastHostSeq(string(req.ID))
	if err != nil {
		d.cfg.Log.Warn("cannot read a session's last host number", "session", req.ID, "err", err.Error())
		seqBase = 0
	}

	// The adapter's operational knowledge comes from the catalogue, not from
	// the caller: the request names a command, and the definition that belongs
	// to that command is what decides how a blocked screen is recognised. A
	// caller cannot hand the host patterns of its own.
	adapter, _ := d.agents.AdapterForCommand(req.Command)

	// A bundled agent ships beside this build rather than on PATH, and the host
	// spawns it with exec, which does not look there. The name is turned into
	// the path the build was tested with before the spawn names it; anything
	// this build does not ship comes back unchanged.
	command := d.agents.Resolve(req.Command)

	endpoint, err := d.spawn(ctx, SpawnRequest{
		Binary:         d.cfg.HostBinary,
		Session:        req.ID,
		Mode:           req.Mode,
		Command:        command,
		Args:           req.Args,
		Dir:            req.Dir,
		Policy:         req.Policy,
		PermissionWait: d.cfg.PermissionWait,
		SeqBase:        seqBase,
		Cols:           req.Cols,
		Rows:           req.Rows,
		Adapter:        adapter,
		Channel:        d.cfg.Channel,
		StateDir:       d.cfg.StateDir,
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

	// What this session is about is written down once it is running: the
	// archive reads it after the host is gone, and a record taken before the
	// spawn would claim a run that never started.
	d.recordStart(req.ID, session.Mode(att.Info.Mode), att.Info.Command, att.Info.Dir)

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

// lookup returns the attachment for id. The value is a copy, not a pointer into
// the fleet: a caller reads it long after the lock is released, and the host's
// own state changes underneath it.
func (d *Daemon) lookup(id session.ID) *Attachment {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == id {
			snapshot := d.fleet.Attachments[i]
			return &snapshot
		}
	}
	return nil
}

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/f1shsmell/flotilla/internal/session"
)

// Allowed methods on the daemon connection.
const (
	// MethodStatus reports the fleet this daemon currently serves.
	MethodStatus = "daemon.status"
	// MethodSessionStart spawns a session host.
	MethodSessionStart = "session.start"
)

func (d *Daemon) handleCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodStatus:
		return d.status(), nil
	case MethodSessionStart:
		return d.startSession(ctx, params)
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
		st.Sessions = append(st.Sessions, StatusEntry{
			ID:       att.Record.ID,
			Mode:     session.Mode(att.Info.Mode),
			HostPID:  att.Info.PID,
			Command:  att.Info.Command,
			Dir:      att.Info.Dir,
			Attached: att.Conn.Alive(),
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
		return StartResponse{ID: req.ID, HostPID: att.Info.PID, Endpoint: att.Record.Endpoint}, nil
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
		return nil, fmt.Errorf("spawn session %q: %w", req.ID, err)
	}

	att, err := AttachOne(ctx, d.reg, req.ID, d.cfg.Token, d.cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("attach the new session %q: %w", req.ID, err)
	}

	d.mu.Lock()
	d.fleet.Attachments = append(d.fleet.Attachments, att)
	d.mu.Unlock()

	d.cfg.Log.Info("session started",
		"session", req.ID, "host_pid", att.Info.PID, "endpoint", endpoint.String())

	return StartResponse{ID: req.ID, HostPID: att.Info.PID, Endpoint: endpoint}, nil
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

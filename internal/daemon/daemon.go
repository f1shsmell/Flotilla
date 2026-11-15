// Package daemon is the control plane. It holds no agent handles: it discovers
// session hosts, attaches to them over IPC, and reattaches after its own
// restart.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// errUnknownMethod reports a call this daemon does not serve.
var errUnknownMethod = errors.New("daemon: unknown method")

// Config is what a daemon needs to serve a fleet.
type Config struct {
	// Channel names this daemon's endpoint and isolates its install.
	Channel string
	// Token authenticates every connection on this channel.
	Token string
	// StateDir holds host records; it is also the socket directory on Unix.
	StateDir string
	// HostBinary is the flt-session-host executable spawned for new sessions.
	HostBinary string
	// Log receives the daemon's diagnostics.
	Log *slog.Logger
}

// Status is what the daemon reports to a client.
type Status struct {
	Channel  string        `json:"channel"`
	PID      int           `json:"pid"`
	Ready    bool          `json:"ready"`
	Sessions []StatusEntry `json:"sessions"`
	Failures []string      `json:"failures,omitempty"`
}

// StatusEntry is one session in a status report.
type StatusEntry struct {
	ID session.ID `json:"id"`
	// Mode is "pty" or "acp": the client renders the two differently.
	Mode     session.Mode `json:"mode"`
	HostPID  int          `json:"host_pid"`
	Command  string       `json:"command"`
	Dir      string       `json:"dir"`
	Attached bool         `json:"attached"`
}

// StartRequest asks the daemon for a session.
type StartRequest struct {
	// ID is the session id. The caller chooses it, so a retry names one session.
	ID session.ID `json:"id"`
	// Mode is "pty" or "acp". Empty means pty, the fallback that always works.
	Mode session.Mode `json:"mode,omitempty"`
	// Command and Args name the agent to run.
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// Dir is the agent's working directory.
	Dir string `json:"dir,omitempty"`
	// Policy is the ACP permission policy. Empty means supervised.
	Policy string `json:"policy,omitempty"`
	// Cols and Rows size the initial PTY.
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
}

// StartResponse reports the session the daemon created and where it listens.
type StartResponse struct {
	ID       session.ID  `json:"id"`
	HostPID  int         `json:"host_pid"`
	Endpoint ipc.Address `json:"endpoint"`
}

// Daemon is the control plane. Its only state is the fleet it can reach, and it
// rebuilds that from disk on every start.
type Daemon struct {
	cfg   Config
	reg   session.Registry
	addr  ipc.Address
	spawn Spawner

	mu    sync.Mutex
	fleet Fleet
}

// New builds a daemon. Call Run to serve.
func New(cfg Config, spawn Spawner) *Daemon {
	return &Daemon{
		cfg:   cfg,
		reg:   session.NewRegistry(cfg.StateDir),
		addr:  ipc.Address{Channel: cfg.Channel, Name: "daemon"},
		spawn: spawn,
	}
}

// Addr reports the endpoint this daemon serves.
func (d *Daemon) Addr() ipc.Address { return d.addr }

// Fleet reports the sessions this daemon currently serves.
func (d *Daemon) Fleet() Fleet {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fleet
}

// Run reattaches to every published host, then serves until ctx expires.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Reattach(ctx); err != nil {
		return err
	}

	l, err := ipc.Listen(d.addr, d.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", d.addr, err)
	}
	defer func() { _ = l.Close() }()

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	d.cfg.Log.Info("daemon listening", "endpoint", d.addr.String(), "pid", os.Getpid())

	for {
		nc, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				d.Detach()
				return nil
			}
			return fmt.Errorf("accept on %s: %w", d.addr, err)
		}

		conn, err := ipc.Serve(nc, ipc.Config{
			Token:   d.cfg.Token,
			Role:    "daemon",
			Handler: d.handleCall,
		})
		if err != nil {
			d.cfg.Log.Warn("rejected an ipc client", "err", err.Error())
			continue
		}
		d.cfg.Log.Info("client connected", "peer", conn.Peer())
	}
}

// Reattach rebuilds the fleet from the records on disk. A daemon restart costs
// nothing but this scan: the hosts were never waiting on the daemon.
func (d *Daemon) Reattach(ctx context.Context) error {
	fleet, err := Attach(ctx, d.reg, d.cfg.Token, d.cfg.StateDir, d.cfg.Log)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.fleet = fleet
	d.mu.Unlock()

	d.cfg.Log.Info("fleet reattached",
		"sessions", len(fleet.Attachments), "failures", len(fleet.Failures))
	return nil
}

// Detach closes every host connection. The hosts keep running; only this
// daemon's view of them goes away.
func (d *Daemon) Detach() {
	d.mu.Lock()
	fleet := d.fleet
	d.fleet = Fleet{}
	d.mu.Unlock()

	fleet.Close()
}

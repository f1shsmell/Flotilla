// Package daemon is the control plane. It holds no agent handles: it discovers
// session hosts, attaches to them over IPC, and reattaches after its own
// restart.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/comments"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/transcript"
)

// errUnknownMethod reports a call this daemon does not serve.
var errUnknownMethod = ipc.Class(errors.New("daemon: unknown method"), ipc.CodeUnknownMethod)

// Config is what a daemon needs to serve a fleet.
type Config struct {
	// Channel names this daemon's endpoint and isolates its install.
	Channel string
	// Token authenticates every connection on this channel.
	Token string
	// StateDir holds host records; it is also the socket directory on Unix.
	StateDir string
	// ConfigDir holds adapters and switches; empty means StateDir/config.
	ConfigDir string
	// HostBinary is the flt-session-host executable spawned for new sessions.
	HostBinary string
	// PermissionWait bounds how long a spawned host waits for a permission
	// answer. Zero waits forever.
	PermissionWait time.Duration
	// AttachTimeout bounds the wait for one host during a fleet scan. Zero uses
	// the default: a host that accepts the pipe and then stops answering must
	// not hold the scan, which runs before the daemon listens.
	AttachTimeout time.Duration
	// Log receives the daemon's diagnostics.
	Log *slog.Logger
}

// attachTimeout is the default bound on the wait for one host.
const attachTimeout = 5 * time.Second

func (c Config) attachTimeout() time.Duration {
	if c.AttachTimeout > 0 {
		return c.AttachTimeout
	}
	return attachTimeout
}

// Status is what the daemon reports to a client.
type Status struct {
	Channel  string        `json:"channel"`
	PID      int           `json:"pid"`
	Ready    bool          `json:"ready"`
	Sessions []StatusEntry `json:"sessions"`
	// Archived lists the sessions this daemon no longer serves but still has a
	// log for. They are not sessions: nothing holds an agent handle, and the
	// reader can look but not continue. A restart that lost a host moves it
	// here rather than dropping the run out of sight.
	Archived []session.ArchivedEntry `json:"archived,omitempty"`
	Failures []string                `json:"failures,omitempty"`
}

// StatusEntry is one session in a status report.
type StatusEntry struct {
	ID session.ID `json:"id"`
	// Mode is "pty" or "acp": the client renders the two differently.
	Mode    session.Mode `json:"mode"`
	HostPID int          `json:"host_pid"`
	Command string       `json:"command"`
	Dir     string       `json:"dir"`
	// State is the lifecycle the host last reported: working, blocked, done,
	// idle or unknown. A session this daemon cannot classify is unknown, and
	// unknown is never read as complete.
	State session.State `json:"state"`
	// Reachable is true while this daemon still holds its connection to the
	// session's host. A session whose host died, or that another client
	// attached to directly, reports false.
	Reachable bool `json:"reachable"`
}

// SetAgentRequest changes one agent's switches. The id names the agent, so a
// request without one is refused rather than applied to a guess.
type SetAgentRequest struct {
	ID     string       `json:"id"`
	Change agent.Change `json:"change"`
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
	// Worktree, when set, creates a worktree and runs the session there, so two
	// sessions can work on one repository without sharing a checkout.
	Worktree *WorktreeRequest `json:"worktree,omitempty"`
}

// WorktreeRequest asks for a fresh worktree for one session.
type WorktreeRequest struct {
	// Repo is the repository's main worktree.
	Repo string `json:"repo"`
	// Branch is the branch to create and check out.
	Branch string `json:"branch"`
	// Base is the commit-ish to branch from; empty means HEAD.
	Base string `json:"base,omitempty"`
	// Dir is where the worktree goes; empty means a sibling of Repo.
	Dir string `json:"dir,omitempty"`
}

// StartResponse reports the session the daemon created and where it listens.
type StartResponse struct {
	ID       session.ID  `json:"id"`
	HostPID  int         `json:"host_pid"`
	Endpoint ipc.Address `json:"endpoint"`
	// Dir is where the agent runs: the worktree when one was asked for.
	Dir string `json:"dir,omitempty"`
	// Included and Skipped report what .worktreeinclude copied and what it
	// named but could not find.
	Included []string `json:"worktree_included,omitempty"`
	Skipped  []string `json:"worktree_skipped,omitempty"`
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
	relay *relay
	// baselines is the commit each session started at, which is what the turn
	// scope compares against. It is memory only: a daemon restart forgets it.
	baselines map[session.ID]string
	// comments are the reviewer's notes, which outlive the daemon. They sit
	// beside the session records: both are the daemon's per-session state, and
	// the registry skips a directory when it scans for records.
	comments comments.Store
	// transcripts are the agent updates of every session this daemon has seen,
	// kept on disk. They are the daemon's because it is the only process that
	// sees each update exactly once, and because a log has to outlive the host
	// that produced it.
	transcripts *transcript.Store
	// agents is the catalogue this machine can offer; it holds no handles.
	agents *agent.Catalog
}

// New builds a daemon. Call Run to serve.
func New(cfg Config, spawn Spawner) *Daemon {
	// A nil spawner defaults to the real one: session.start must not be able
	// to panic the daemon.
	if spawn == nil {
		spawn = ExecSpawner(cfg.Log)
	}

	configDir := cfg.ConfigDir
	if configDir == "" {
		configDir = filepath.Join(cfg.StateDir, "config")
	}

	d := &Daemon{
		cfg:   cfg,
		reg:   session.NewRegistry(cfg.StateDir),
		addr:  ipc.Address{Channel: cfg.Channel, Name: "daemon"},
		spawn: spawn,
		// A frame that could not be delivered is worth a debug line: the client
		// learns from its own connection closing, and this is what a postmortem
		// reads when it asks why one window stopped updating.
		relay: newRelay(func(err error) {
			cfg.Log.Debug("deliver a queued frame failed", "err", err.Error())
		}),
		baselines:   make(map[session.ID]string),
		comments:    comments.NewStore(filepath.Join(cfg.StateDir, "comments")),
		transcripts: transcript.NewStore(filepath.Join(cfg.StateDir, "transcripts")),
		agents:      agent.New(configDir),
	}
	// A read turn stays read across a daemon restart: the seen table lives on
	// disk beside the records it annotates. A table that cannot be read is a
	// warning, not a refusal to serve.
	if err := d.relay.persistSeenTo(filepath.Join(cfg.StateDir, "seen.json")); err != nil {
		cfg.Log.Warn("cannot load the seen table", "err", err.Error())
	}
	return d
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
		// A channel this process cannot bind is a channel a live daemon already
		// serves. Saying so, rather than "access denied", is what tells the GUI
		// that the daemon it tried to start is not needed.
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

		// The handshake runs off the loop: while it is being served the pipe has
		// no pending instance, and a client dialling in that window is told the
		// endpoint does not exist.
		go func(nc net.Conn) {
			conn, err := ipc.Serve(nc, ipc.Config{
				Token:   d.cfg.Token,
				Role:    "daemon",
				Handler: d.handleCall,
				OnData:  d.clientData,
			})
			if err != nil {
				d.cfg.Log.Warn("rejected an ipc client", "err", err.Error())
				return
			}
			d.cfg.Log.Info("client connected", "peer", conn.Peer())

			// The window's name is what the per-client seen map is keyed by, and it is
			// remembered for as long as the daemon runs — bounded by a table
			// size, not by connection lifetime. A poll that failed and
			// reconnected must not turn a turn the user already read back into
			// an unread badge.
			d.relay.track(conn.Client())
		}(nc)
	}
}

// Reattach rebuilds the fleet from the records on disk. A daemon restart costs
// nothing but this scan: the hosts were never waiting on the daemon.
func (d *Daemon) Reattach(ctx context.Context) error {
	fleet, err := Attach(ctx, d.reg, d.cfg.Token, d.cfg.StateDir, d.cfg.Log, d.cfg.attachTimeout(), d.hostHooks)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.fleet = fleet
	d.mu.Unlock()

	for _, att := range fleet.Attachments {
		d.watch(att)
	}

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

	// The transcript handles go with the daemon. On Windows a file still open
	// when the process exits is left to the cleanup of a machine that may not
	// come back; the logs themselves stay, which is the point of writing them.
	if err := d.transcripts.Close(); err != nil {
		d.cfg.Log.Debug("cannot close the transcripts", "err", err.Error())
	}

	d.relay.closeSeen()
}

// Package daemon is the control plane. It holds no agent handles: it discovers
// session hosts, attaches to them over IPC, and reattaches after its own
// restart.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/orch"
	"github.com/f1shsmell/flotilla/internal/outbox"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
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
	// ReclaimIdle is how quiet an agent family must be before its host is
	// reclaimed. Zero disables reclamation.
	ReclaimIdle time.Duration
	// ReclaimSweep is how often the daemon looks for idle families. Zero uses
	// the default of a minute.
	ReclaimSweep time.Duration
	// AttachTimeout bounds the wait for one host during a fleet scan. Zero uses
	// the default: a host that accepts the pipe and then stops answering must
	// not hold the scan, which runs before the daemon listens.
	AttachTimeout time.Duration
	// TaskExecutor runs one unattended task. Nil uses the real headless runner;
	// a test substitutes one so the queue can be drained without starting an
	// agent.
	TaskExecutor orch.Executor
	// TaskMaxRuns bounds how many times one task may start. Zero uses the
	// orchestrator's default.
	TaskMaxRuns int
	// Log receives the daemon's diagnostics.
	Log *slog.Logger
}

// attachTimeout is the default bound on the wait for one host.
const attachTimeout = 5 * time.Second

// statusReadTimeout bounds the disk reads a status poll makes. A poll is a
// question with a caller waiting on it, and a database that stopped answering
// must not hold the answer: the next poll gets the queue instead.
const statusReadTimeout = 2 * time.Second

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
	// Tasks lists the unattended jobs this daemon knows about: queued, running,
	// waiting on a person, or settled. They are not sessions — nothing holds an
	// agent handle and no window can attach — so they ride beside the fleet
	// rather than inside it, and a reader that ignores this field loses nothing.
	Tasks []TaskEntry `json:"tasks,omitempty"`
	// Inbox is the work waiting on a person, projected per client.
	Inbox    []InboxEntry `json:"inbox,omitempty"`
	Failures []string     `json:"failures,omitempty"`
}

// TaskEntry is one unattended job in a status report.
//
// The fields are the ones a person needs to decide what to do next: what the
// job is, where it stands, what it cost, and why it stopped. The prompt is
// included because a list of opaque ids would make the queue unreadable, and it
// is the only thing that says which job is which.
type TaskEntry struct {
	ID string `json:"id"`
	// Dialect names the headless dialect that runs it, e.g. "claude".
	Dialect string `json:"dialect"`
	// State is queued, running, waiting, done, failed or cancelled. A state this
	// build does not know is reported as it stands rather than mapped onto the
	// nearest one.
	State string `json:"state"`
	// Prompt is the instruction the agent receives.
	Prompt string `json:"prompt"`
	// Dir is where it runs; empty means the daemon's own directory.
	Dir string `json:"dir,omitempty"`
	// Runs is how many times it was started, and SpentUSD is what those runs
	// reported costing. An unpriced run leaves SpentUSD where it was.
	Runs     int     `json:"runs"`
	SpentUSD float64 `json:"spent_usd,omitempty"`
	// BudgetUSD bounds what it may spend; zero means unbounded.
	BudgetUSD float64 `json:"budget_usd,omitempty"`
	// LastError is why it last stopped, for a person to read.
	LastError string `json:"last_error,omitempty"`
}

// Kinds an inbox entry can carry.
const (
	// InboxInput is a session blocked on an answer from the user.
	InboxInput = "input"
	// InboxUnread is a finished turn the asking client has not looked at.
	InboxUnread = "unread"
)

// InboxEntry is one item waiting on a person. It names a live session so a
// click can attach straight to it; it is not a session and holds no handle.
type InboxEntry struct {
	ID session.ID `json:"id"`
	// Kind is InboxInput when the session waits on an answer, InboxUnread when a turn is unread.
	Kind string `json:"kind"`
	// Title is the agent's own question when it asked one; a terminal leaves it empty.
	Title   string `json:"title,omitempty"`
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
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
	// Protected marks a session pinned against idle reclamation.
	Protected bool `json:"protected"`
	// Steer is whether a message sent mid-turn can reach the running turn.
	Steer bool `json:"steer"`
	// Parent names the session that started this one; a person's has none.
	Parent session.ID `json:"parent,omitempty"`
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
	// Parent names the session that asked for this one; a person leaves it empty.
	Parent session.ID `json:"parent,omitempty"`
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
	// protected is the set of sessions never reclaimed as idle. A pin outlives
	// the daemon, so it is loaded from and written to the state database.
	protected map[session.ID]bool
	// reclaiming names the sessions whose host is being asked to close. A pin,
	// a window or a queued message arriving meanwhile is refused rather than
	// accepted into a session that is already leaving.
	reclaiming map[session.ID]bool
	// state is the daemon's durable state: the transcript of every session it
	// has seen, the reviewer's notes, the seen table and the events behind
	// them. It is the daemon's because it is the only process that sees each
	// update exactly once, and because all of it has to outlive the host that
	// produced it.
	state *store.Store
	// tasks runs the unattended queue. It is nil when the database could not be
	// opened: a daemon that cannot record a task must not start one.
	tasks *orch.Orchestrator
	// effects drains the work the store says is owed, so a task's output lands
	// even if the daemon dies between recording the task and writing the output.
	effects *outbox.Worker
	// background holds the loops Run started, so Detach can wait for them
	// before it closes the database they read.
	background sync.WaitGroup
	// bgMu guards the two fields below. They are written by startBackground and
	// read by stopBackground, which are called from different goroutines when a
	// daemon is detached while it is still starting.
	bgMu     sync.Mutex
	bgCancel context.CancelFunc
	// bgFailures is what a loop said when it last stopped. It is reported in
	// Status.Failures, because a daemon whose worker died is a daemon that is
	// quietly not doing its job, and silence there is worse than an error.
	bgFailures map[string]string
	// bgOnce guards starting and stopping, so a daemon that is detached twice
	// does not close the database under a loop it already stopped.
	bgStart sync.Once
	bgStop  sync.Once
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

	// The database is the daemon's state. A store that cannot be opened is not
	// something a daemon can serve around: every read below needs it.
	state, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		cfg.Log.Error("cannot open the state database", "err", err.Error())
		state = nil
	}

	// A build before the database kept its state in files beside it. They are
	// imported once, on the first start of a build that has a database, and
	// left in place afterwards: a user's history is not something an importer
	// is trusted to move.
	if state != nil {
		report, err := state.Migrate(context.Background(), legacyLayout(cfg.StateDir))
		switch {
		case err != nil:
			cfg.Log.Warn("cannot import the pre-database state", "err", err.Error())
		case report.Ran:
			cfg.Log.Info("imported the pre-database state",
				"transcripts", report.Transcripts, "clients", report.Clients,
				"seen", report.Seen, "comments", report.Comments,
				"problems", len(report.Problems))
		}
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
		baselines:  make(map[session.ID]string),
		protected:  make(map[session.ID]bool),
		reclaiming: make(map[session.ID]bool),
		state:      state,
		agents:     agent.New(configDir),
	}
	// A read turn stays read across a daemon restart: the seen table is loaded
	// from the database beside the records it annotates. A table that cannot be
	// read is a warning, not a refusal to serve.
	if err := d.relay.persistSeenTo(state); err != nil {
		cfg.Log.Warn("cannot load the seen table", "err", err.Error())
	}
	// A pin is a user's decision about a session, so it outlives the daemon that
	// recorded it. A failed load leaves the fleet unpinned, which is the safe
	// direction: the worst it does is reclaim what the user meant to keep.
	if state != nil {
		pins, err := state.ProtectedIDs(context.Background())
		if err != nil {
			cfg.Log.Warn("cannot load the protected sessions", "err", err.Error())
		}
		for _, id := range pins {
			d.protected[session.ID(id)] = true
		}
	}

	// The queue and the outbox are built here rather than in Run so a test can
	// enqueue a task and drain it through the same objects the daemon serves
	// from. A database that failed to open leaves both nil: a daemon that
	// cannot record a task must not start one, and every caller below checks.
	if state != nil {
		// The worker is built first because the queue's only notification hook
		// points at it. Building them the other way round would need a forward
		// reference or a second wake channel, and the hook is what makes "a task
		// finished" reach the worker that stores its output.
		effects, err := outbox.New(outbox.Config{Store: state, Log: cfg.Log})
		if err != nil {
			cfg.Log.Warn("cannot build the effect worker", "err", err.Error())
		} else {
			// The one effect this build produces: a run's output. It is
			// registered here, next to the worker, so a reader can see every
			// effect name in one place.
			effects.Register(store.OutputEffect, storeOutputHandler(state))
			d.effects = effects
		}

		queueCfg := orch.Config{
			Store:    state,
			Executor: cfg.TaskExecutor,
			MaxRuns:  cfg.TaskMaxRuns,
			Log:      cfg.Log,
		}
		if effects != nil {
			// Every committed task change rings the worker: an effect queued by a
			// finished run is owed now, and nothing else would tell the worker to
			// look. The call is a non-blocking channel send, so it is safe from
			// the orchestrator's own goroutine.
			queueCfg.OnChange = effects.Wake
		}

		tasks, err := orch.New(queueCfg)
		switch {
		case err != nil:
			cfg.Log.Warn("cannot build the task queue", "err", err.Error())
		default:
			d.tasks = tasks
		}
	}
	return d
}

// storeOutputHandler runs the effect that stores what one run produced. It
// decodes the payload the orchestrator queued and writes the body, replacing any
// earlier copy of the same run: that is what makes the effect safe to re-run
// after a crash between the write and its receipt.
func storeOutputHandler(state *store.Store) outbox.Handler {
	return func(ctx context.Context, payload json.RawMessage) error {
		var out store.TaskOutputPayload
		if err := json.Unmarshal(payload, &out); err != nil {
			return fmt.Errorf("decode the output effect: %w", err)
		}
		return state.WriteTaskOutput(ctx, store.TaskOutput{
			TaskID: out.TaskID,
			Run:    out.Run,
			Body:   out.Body,
		})
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
		// A channel this process cannot bind is a channel a live daemon already
		// serves. Saying so, rather than "access denied", is what tells the GUI
		// that the daemon it tried to start is not needed.
		return fmt.Errorf("listen on %s: %w", d.addr, err)
	}
	defer func() { _ = l.Close() }()

	// The queue is repaired only once this process really serves the channel.
	// Listen is what decides that: a daemon that lost the race must not have
	// touched the queue at all, because parking a running row and running tasks
	// belong to the daemon holding the lock, not to the one that just lost it.
	d.startBackground()

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
	// The background loops stop first and are waited for: they read the
	// database, and closing it under them would turn their next query into an
	// error nobody asked for. The order is the same one Run's failure path
	// takes, so a daemon that never listened shuts down the same way.
	d.stopBackground()

	d.mu.Lock()
	fleet := d.fleet
	d.fleet = Fleet{}
	d.mu.Unlock()

	fleet.Close()

	// The database closes with the daemon. WAL leaves the data on disk; the
	// handle going is what lets a later start open it cleanly on Windows.
	if d.state != nil {
		if err := d.state.Close(); err != nil {
			d.cfg.Log.Debug("cannot close the state database", "err", err.Error())
		}
	}

	d.relay.closeSeen()
}

// startBackground repairs the queue and starts the two loops that serve it. It
// is safe to call twice: the second call does nothing.
//
// The repair is not optional and not deferred. A task left saying running is a
// run whose outcome this build cannot vouch for; parking it is what stops the
// next client's enqueue from sitting behind work that will never finish.
func (d *Daemon) startBackground() {
	d.bgStart.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		d.bgMu.Lock()
		d.bgCancel = cancel
		d.bgFailures = make(map[string]string)
		d.bgMu.Unlock()

		if d.tasks != nil {
			recovered, err := d.tasks.Recover(ctx)
			switch {
			case err != nil:
				d.cfg.Log.Warn("cannot repair the task queue", "err", err.Error())
			case len(recovered) > 0:
				d.cfg.Log.Warn("parked tasks the previous daemon died under",
					"tasks", len(recovered))
			}
		}

		// Each loop reports its own death. A daemon whose worker stopped is a
		// daemon that is quietly not doing its job, and the failure rides in
		// Status.Failures so a window can say so rather than showing a queue
		// that never moves.
		if d.effects != nil {
			d.background.Add(1)
			go func() {
				defer d.background.Done()
				if err := d.effects.Run(ctx); err != nil {
					d.recordLoopFailure("effects", err)
				}
			}()
		}

		if d.tasks != nil {
			d.background.Add(1)
			go func() {
				defer d.background.Done()
				if err := d.tasks.Run(ctx); err != nil {
					d.recordLoopFailure("tasks", err)
				}
			}()
		}

		if d.cfg.ReclaimIdle > 0 {
			d.background.Add(1)
			go func() {
				defer d.background.Done()
				d.reclaimLoop(ctx)
			}()
		}
	})
}

// recordLoopFailure keeps what a stopped loop said, so a status report can show
// it. It is a stop, not a crash, when the daemon is the one that cancelled the
// loop: a stopped loop is not a failure to report.
func (d *Daemon) recordLoopFailure(loop string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	d.cfg.Log.Error("a background loop stopped", "loop", loop, "err", err.Error())

	d.bgMu.Lock()
	defer d.bgMu.Unlock()
	if d.bgFailures == nil {
		d.bgFailures = make(map[string]string)
	}
	d.bgFailures[loop] = err.Error()
}

// loopFailures reports what the background loops said when they stopped. It is
// read by the status projection, which is the only place a person can see that
// the queue is no longer being served.
func (d *Daemon) loopFailures() []string {
	d.bgMu.Lock()
	defer d.bgMu.Unlock()

	if len(d.bgFailures) == 0 {
		return nil
	}
	// Sorted so two polls of an unchanged daemon report the same list: a status
	// report that reordered itself would look like something happened.
	names := make([]string, 0, len(d.bgFailures))
	for name := range d.bgFailures {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, fmt.Sprintf("the %s loop stopped: %s", name, d.bgFailures[name]))
	}
	return out
}

// stopBackground stops the loops and waits for them. Safe to call twice, and
// safe to call without a start: a daemon whose database failed to open has
// nothing to stop.
func (d *Daemon) stopBackground() {
	d.bgStop.Do(func() {
		d.bgMu.Lock()
		cancel := d.bgCancel
		d.bgMu.Unlock()

		if cancel != nil {
			cancel()
		}
		d.background.Wait()
	})
}

// legacyLayout names where a build before the database kept its state, relative
// to the daemon's state directory. It is one function because the importer and
// the test that seeds those files have to agree on the same paths.
func legacyLayout(stateDir string) store.LegacyLayout {
	return store.LegacyLayout{
		Transcripts: filepath.Join(stateDir, "transcripts"),
		Seen:        filepath.Join(stateDir, "seen.json"),
		Comments:    filepath.Join(stateDir, "comments"),
	}
}

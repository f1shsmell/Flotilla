package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Spawner starts a session host process. It is an interface because the daemon
// tests substitute one that starts the host in-process: the real spawn, the
// detach and the restart are exactly what those tests must not depend on.
type Spawner func(ctx context.Context, req SpawnRequest) (ipc.Address, error)

// SpawnRequest is what a client's new-session request turns into.
type SpawnRequest struct {
	// Binary is the flt-session-host executable to launch.
	Binary string
	// Session is the id the new host must hold.
	Session session.ID
	// Mode is "pty" or "acp"; empty means pty.
	Mode session.Mode
	// Command and Args name the agent the host must run.
	Command string
	Args    []string
	// Dir is the agent's working directory.
	Dir string
	// Policy is the ACP permission policy, ignored for a PTY session.
	Policy string
	// SeqBase is the host number the first update continues from; zero starts at 1.
	SeqBase uint64
	// Cols and Rows size the initial PTY.
	Cols int
	Rows int
	// Adapter is the operational knowledge the agent declared: what a blocked
	// screen looks like, how it reads a paste, and how it is resumed. It is
	// filled from the catalogue by agent id, never by the caller.
	Adapter adapters.Adapter
	// Channel selects the endpoint namespace and the token file.
	Channel string
	// StateDir holds the host records and the Unix sockets.
	StateDir string
}

// recordPollInterval is how often a spawn waits for the host record. It is
// short because a host publishes before it starts listening.
const recordPollInterval = 10 * time.Millisecond

// ExecSpawner launches flt-session-host as a detached child and waits for its
// record, so a caller never dials a host that is still starting.
func ExecSpawner(log *slog.Logger) Spawner {
	return func(ctx context.Context, req SpawnRequest) (ipc.Address, error) {
		if req.Binary == "" {
			return ipc.Address{}, errors.New("spawn: no session-host binary configured")
		}

		mode := req.Mode
		if mode == "" {
			mode = session.ModePTY
		}

		args := []string{
			"--session", string(req.Session),
			"--mode", string(mode),
			"--command", req.Command,
			"--channel", req.Channel,
			"--cols", fmt.Sprint(req.Cols),
			"--rows", fmt.Sprint(req.Rows),
		}
		if req.Dir != "" {
			args = append(args, "--dir", req.Dir)
		}
		if req.Policy != "" {
			args = append(args, "--policy", req.Policy)
		}
		if req.SeqBase > 0 {
			args = append(args, "--seq-base", fmt.Sprint(req.SeqBase))
		}
		if !req.Adapter.Empty() {
			// The definition travels as JSON on the command line rather than in a
			// file: the host is one process for one session, and a file would
			// outlive the session it describes. Empty means the agent declared
			// nothing, and the flag is left out entirely.
			encoded, err := json.Marshal(req.Adapter)
			if err != nil {
				return ipc.Address{}, fmt.Errorf("encode the adapter for session %q: %w", req.Session, err)
			}
			args = append(args, "--adapter", string(encoded))
		}
		if len(req.Args) > 0 {
			args = append(args, "--")
			args = append(args, req.Args...)
		}

		// The host outlives the daemon, so it is not tied to the daemon's
		// cancellation: WithoutCancel keeps the request's values while dropping
		// its deadline, and the child gets its own process group so an interrupt
		// on the daemon's console never reaches it.
		cmd := exec.CommandContext(context.WithoutCancel(ctx), req.Binary, args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		setDetached(cmd)

		if err := cmd.Start(); err != nil {
			return ipc.Address{}, fmt.Errorf("start %s: %w", req.Binary, err)
		}
		log.Info("session host spawned", "session", req.Session, "pid", cmd.Process.Pid)

		// The host publishes before it listens, so a record is the only proof it
		// came up. A host that dies first must say so now, not after the caller's
		// deadline.
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		if err := waitForRecord(ctx, session.NewRegistry(req.StateDir), req.Session, exited); err != nil {
			return ipc.Address{}, err
		}
		return ipc.Address{Channel: string(req.Session), Name: "host"}, nil
	}
}

// waitForRecord blocks until the host has published its record, so the daemon
// attaches to a listening host instead of racing its startup. A host that exits
// first ends the wait with its own error.
func waitForRecord(ctx context.Context, reg session.Registry, id session.ID, exited <-chan error) error {
	for {
		if _, err := reg.Read(id); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for session %q to publish: %w", id, ctx.Err())
		case err := <-exited:
			return fmt.Errorf("session %q exited before it published: %w (its stderr is the daemon's)", id, err)
		case <-time.After(recordPollInterval):
		}
	}
}

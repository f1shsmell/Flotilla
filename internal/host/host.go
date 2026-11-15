// Package host runs the per-session process that owns the agent handles. It is
// the only code that ever holds a ConPTY, which is why a daemon restart does
// not cost a live session.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/charmbracelet/x/xpty"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Config is everything a host needs to take a session.
type Config struct {
	// ID is the session this host owns.
	ID session.ID
	// Token authenticates both directions of the IPC connection.
	Token string
	// StateDir holds the socket file on Unix; Windows ignores it.
	StateDir string
	// Command and Args name the agent to run under the PTY.
	Command string
	Args    []string
	// Dir is the agent's working directory, normally a session worktree.
	Dir string
	// Env is the agent's environment. Nil inherits this process's.
	Env []string
	// Cols and Rows size the initial PTY. Zero falls back to 80x24.
	Cols int
	Rows int
	// Ctx is the parent context for the agent process. It is detached from the
	// caller's cancellation, so a daemon restart never stops an agent.
	Ctx context.Context
}

// Ctx is an optional parent context for the agent process. A nil Ctx means
// context.Background, so tests need not thread one through.
func (c Config) ctxFor() context.Context {
	if c.Ctx != nil {
		return c.Ctx
	}
	return context.Background()
}

// Host is one running session: a PTY-backed agent plus the endpoint its client
// reconnects to.
type Host struct {
	cfg    Config
	log    *slog.Logger
	pty    xpty.Pty
	cmd    *exec.Cmd
	reg    session.Registry
	addr   ipc.Address
	donec  chan struct{}
	exitc  chan error
	stopOn sync.Once

	mu   sync.Mutex
	conn *ipc.Conn
}

// Start spawns the agent under a PTY and publishes the host record. The host is
// not listening yet: Serve opens the endpoint.
func Start(cfg Config, reg session.Registry, log *slog.Logger) (*Host, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("start host: empty session id: %w", session.ErrNoRecord)
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("start host %q: empty command", cfg.ID)
	}

	cols, rows := cfg.Cols, cfg.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}

	ptmx, err := xpty.NewPty(cols, rows)
	if err != nil {
		return nil, fmt.Errorf("create pty: %w", err)
	}

	h := &Host{
		cfg:   cfg,
		log:   log,
		pty:   ptmx,
		reg:   reg,
		addr:  ipc.Address{Channel: string(cfg.ID), Name: "host"},
		donec: make(chan struct{}),
		exitc: make(chan error, 1),
	}

	// The agent's lifetime is the host's, not the caller's: a cancelled context
	// stops Serve, and Close is what stops the agent afterwards.
	cmd := exec.CommandContext(context.WithoutCancel(cfg.ctxFor()), cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = cfg.Env

	if err := ptmx.Start(cmd); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("start %q on the pty: %w", cfg.Command, err)
	}
	h.cmd = cmd

	go h.pumpOutput()

	log.Info("session host started",
		"session", cfg.ID, "pid", os.Getpid(), "command", cfg.Command)

	return h, nil
}

// SessionID reports the session this host holds.
func (h *Host) SessionID() session.ID { return h.cfg.ID }

// Addr reports the endpoint this host serves.
func (h *Host) Addr() ipc.Address { return h.addr }

// Done closes when the host has stopped.
func (h *Host) Done() <-chan struct{} { return h.donec }

// Serve accepts clients until ctx expires or the agent exits. A reconnect is
// just another Accept: the PTY and the agent never move.
func (h *Host) Serve(ctx context.Context) error {
	l, err := ipc.Listen(h.addr, h.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", h.addr, err)
	}
	defer func() { _ = l.Close() }()

	// Publish only once the endpoint is listening: a record is a promise that
	// dialling it works, and a daemon that reads it must not race a startup.
	if err := h.reg.Publish(session.Record{
		ID:       h.cfg.ID,
		Endpoint: h.addr,
		PID:      os.Getpid(),
		Started:  time.Now(),
	}); err != nil {
		return fmt.Errorf("publish the record for session %q: %w", h.cfg.ID, err)
	}
	h.log.Info("session published", "session", h.cfg.ID, "endpoint", h.addr.String())

	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-h.donec:
			_ = l.Close()
		}
	}()

	go h.watchAgent(ctx)

	for {
		nc, err := l.Accept()
		if err != nil {
			select {
			case <-h.donec:
				return nil
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("accept on %s: %w", h.addr, err)
		}

		conn, err := ipc.Serve(nc, ipc.Config{
			Token:   h.cfg.Token,
			Role:    "host",
			Session: string(h.cfg.ID),
			Handler: h.handleCall,
			OnData:  h.handleData,
		})
		if err != nil {
			h.log.Warn("rejected an ipc client", "session", h.cfg.ID, "err", err.Error())
			continue
		}

		h.attach(conn)
		h.log.Info("client attached", "session", h.cfg.ID, "peer", conn.Peer())
	}
}

// attach makes conn the live client, replacing any previous one. A session
// outlives a client by design, so a new attach wins without asking.
func (h *Host) attach(conn *ipc.Conn) {
	h.mu.Lock()
	previous := h.conn
	h.conn = conn
	h.mu.Unlock()

	if previous != nil {
		previous.Close()
	}

	go func() {
		<-conn.Done()
		h.mu.Lock()
		if h.conn == conn {
			h.conn = nil
		}
		h.mu.Unlock()
		h.log.Info("client detached", "session", h.cfg.ID)
	}()
}

func (h *Host) handleCall(_ context.Context, method string, _ json.RawMessage) (any, error) {
	if method != session.MethodInfo {
		return nil, ipc.ErrNoHandler
	}

	cols, rows, err := h.pty.Size()
	if err != nil {
		cols, rows = 0, 0
	}
	return session.Info{
		ID:      h.cfg.ID,
		PID:     os.Getpid(),
		Command: h.cfg.Command,
		Dir:     h.cfg.Dir,
		Cols:    cols,
		Rows:    rows,
	}, nil
}

func (h *Host) handleData(stream uint32, payload []byte) {
	if stream != session.StreamInput {
		return
	}
	if _, err := h.pty.Write(payload); err != nil {
		h.log.Debug("write to the agent failed", "session", h.cfg.ID, "err", err.Error())
	}
}

// Close stops the agent, removes the host record and releases the PTY.
func (h *Host) Close() error {
	var errs []error

	h.stopOn.Do(func() {
		close(h.donec)

		h.mu.Lock()
		conn := h.conn
		h.conn = nil
		h.mu.Unlock()
		if conn != nil {
			conn.Close()
		}

		if h.cmd != nil && h.cmd.Process != nil {
			if err := h.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				errs = append(errs, fmt.Errorf("kill the agent: %w", err))
			}
		}
		if err := h.pty.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close the pty: %w", err))
		}
		if err := h.reg.Unpublish(h.cfg.ID); err != nil {
			errs = append(errs, err)
		}
	})

	return errors.Join(errs...)
}

// pumpOutput reads the PTY and pushes every chunk to the attached client. With
// no client the bytes are dropped: the agent keeps working while detached, its
// scrollback does not.
func (h *Host) pumpOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := h.pty.Read(buf)
		if n > 0 {
			h.send(session.StreamOutput, buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				h.log.Debug("pty read ended", "session", h.cfg.ID, "err", err.Error())
			}
			return
		}
	}
}

func (h *Host) send(stream uint32, payload []byte) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		return
	}
	// A failed send means this client is gone. The PTY keeps running and the
	// next client attaches to the same agent.
	_ = conn.SendData(stream, payload)
}

// watchAgent reaps the agent and closes the host when it exits. xpty.WaitProcess
// is the supported wait on Windows, where exec.Cmd.Wait does not work on ConPTY.
func (h *Host) watchAgent(ctx context.Context) {
	err := xpty.WaitProcess(ctx, h.cmd)
	h.exitc <- err
	h.log.Info("agent exited", "session", h.cfg.ID, "err", errText(err))
	_ = h.Close()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

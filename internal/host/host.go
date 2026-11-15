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
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/charmbracelet/x/xpty"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Config is everything a host needs to take a session.
type Config struct {
	// ID is the session this host owns.
	ID session.ID
	// Mode is how this session talks to its agent: a ConPTY or ACP stdio.
	Mode session.Mode
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
	// Policy decides what an ACP agent may do without asking.
	Policy acp.Policy
	// Ctx is the parent context for the agent process. It is detached from the
	// caller's cancellation, so a daemon restart never stops an agent.
	Ctx context.Context
}

// withDefaults fills in the values a caller may leave out.
func (c Config) withDefaults() Config {
	if c.Mode == "" {
		c.Mode = session.ModePTY
	}
	if c.Cols <= 0 || c.Rows <= 0 {
		c.Cols, c.Rows = 80, 24
	}
	if c.Ctx == nil {
		c.Ctx = context.Background()
	}
	return c
}

// Host is one running session: a PTY-backed agent plus the endpoint its client
// reconnects to.
type Host struct {
	cfg    Config
	log    *slog.Logger
	pty    xpty.Pty
	cmd    *exec.Cmd
	acp    *acpSession
	reg    session.Registry
	addr   ipc.Address
	donec  chan struct{}
	exitc  chan error
	stopOn sync.Once

	mu   sync.Mutex
	conn *ipc.Conn
}

// Start spawns the agent and publishes the host record. The host is not
// listening yet: Serve opens the endpoint.
func Start(ctx context.Context, cfg Config, reg session.Registry, log *slog.Logger) (*Host, error) {
	cfg = cfg.withDefaults()
	if cfg.ID == "" {
		return nil, fmt.Errorf("start host: empty session id: %w", session.ErrNoRecord)
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("start host %q: empty command", cfg.ID)
	}
	if !cfg.Mode.Valid() {
		return nil, fmt.Errorf("start host %q: unknown mode %q", cfg.ID, cfg.Mode)
	}

	h := &Host{
		cfg:   cfg,
		log:   log,
		reg:   reg,
		addr:  ipc.Address{Channel: string(cfg.ID), Name: "host"},
		donec: make(chan struct{}),
		exitc: make(chan error, 1),
	}

	if cfg.Mode == session.ModeACP {
		if err := h.startACP(ctx); err != nil {
			return nil, fmt.Errorf("start the acp session: %w", err)
		}
		log.Info("session host started", "session", cfg.ID, "pid", os.Getpid(),
			"mode", string(cfg.Mode), "command", cfg.Command)
		return h, nil
	}

	ptmx, err := xpty.NewPty(cfg.Cols, cfg.Rows)
	if err != nil {
		return nil, fmt.Errorf("create pty: %w", err)
	}
	h.pty = ptmx

	// The agent's lifetime is the host's, not the caller's: a cancelled context
	// stops Serve, and Close is what stops the agent afterwards.
	cmd := exec.CommandContext(context.WithoutCancel(cfg.Ctx), cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = cfg.Env

	if err := ptmx.Start(cmd); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("start %q on the pty: %w", cfg.Command, err)
	}
	h.cmd = cmd

	go h.pumpOutput()

	log.Info("session host started", "session", cfg.ID, "pid", os.Getpid(),
		"mode", string(cfg.Mode), "command", cfg.Command)

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

	// A PTY session has a child process to reap; an ACP session's agent is
	// waited on by the ACP layer, so there is nothing to watch here.
	if h.cmd != nil {
		go h.watchAgent(ctx)
	}

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

		// The handshake runs off the loop: while it is being served the pipe has
		// no pending instance, and a client dialling in that window is told the
		// endpoint does not exist.
		go func(nc net.Conn) {
			conn, err := ipc.Serve(nc, ipc.Config{
				Token:   h.cfg.Token,
				Role:    "host",
				Session: string(h.cfg.ID),
				Handler: h.handleCall,
				OnData:  h.handleData,
			})
			if err != nil {
				h.log.Warn("rejected an ipc client", "session", h.cfg.ID, "err", err.Error())
				return
			}

			h.attach(conn)
			h.log.Info("client attached", "session", h.cfg.ID, "peer", conn.Peer())
		}(nc)
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

	// A client that arrives after a gap sees what it missed while detached.
	h.replayBuffered(conn)

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

func (h *Host) handleCall(ctx context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
	switch method {
	case session.MethodInfo:
		return h.info(), nil
	case session.MethodPrompt:
		req, err := decodeParams[session.PromptRequest](params)
		if err != nil {
			return nil, err
		}
		return h.promptACP(ctx, req)
	case session.MethodCancel:
		return struct{}{}, h.cancelACP(ctx)
	case session.MethodResize:
		req, err := decodeParams[session.ResizeRequest](params)
		if err != nil {
			return nil, err
		}
		return struct{}{}, h.resize(req)
	case session.MethodPermissionReply:
		reply, err := decodeParams[session.PermissionReply](params)
		if err != nil {
			return nil, err
		}
		h.answerPermission(reply)
		return struct{}{}, nil
	default:
		return nil, ipc.ErrNoHandler
	}
}

// info reports what this host holds.
func (h *Host) info() session.Info {
	cols, rows := h.cfg.Cols, h.cfg.Rows
	if h.pty != nil {
		if w, hgt, err := h.pty.Size(); err == nil {
			cols, rows = w, hgt
		}
	}

	return session.Info{
		ID:      h.cfg.ID,
		PID:     os.Getpid(),
		Mode:    string(h.cfg.Mode),
		Command: h.cfg.Command,
		Dir:     h.cfg.Dir,
		Cols:    cols,
		Rows:    rows,
	}
}

// resize sizes the terminal. A structured session has no terminal, so it is the
// same refusal as a prompt sent to a PTY session.
func (h *Host) resize(req session.ResizeRequest) error {
	if h.pty == nil {
		return fmt.Errorf("session %q has no terminal: %w", h.cfg.ID, errWrongMode)
	}
	if req.Cols <= 0 || req.Rows <= 0 {
		return fmt.Errorf("session %q: resize to %dx%d: %w", h.cfg.ID, req.Cols, req.Rows, errBadSize)
	}
	if err := h.pty.Resize(req.Cols, req.Rows); err != nil {
		return fmt.Errorf("resize the pty: %w", err)
	}
	return nil
}

func (h *Host) handleData(_ *ipc.Conn, stream uint32, payload []byte) {
	if stream != session.StreamInput || h.pty == nil {
		return
	}
	if _, err := h.pty.Write(payload); err != nil {
		h.log.Debug("write to the agent failed", "session", h.cfg.ID, "err", err.Error())
	}
}

// Close stops the agent, removes the host record and releases its handles.
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
		if h.pty != nil {
			if err := h.pty.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close the pty: %w", err))
			}
		}
		errs = append(errs, h.closeACP()...)

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

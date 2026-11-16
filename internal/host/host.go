// Package host runs the per-session process that owns the agent handles. It is
// the only code that ever holds a ConPTY, which is why a daemon restart does
// not cost a live session.
package host

import (
	"bytes"
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
	// out holds the PTY bytes a client has not seen yet: what arrived while
	// nobody was attached, and what arrived while a replay was still being
	// written. A terminal's scrollback lives in its client, so without this a
	// detach would silently start the next client on a blank screen while the
	// agent kept working.
	out []byte
	// replayConn is the client a replay is being written to, or nil when none
	// is running. While it is set, new output queues instead of racing the
	// snapshot. It is a connection rather than a bool because an attach can
	// supersede one mid-replay: the old replay must not clear the new one's
	// state or push its stale bytes into the new client's queue.
	replayConn *ipc.Conn
	// state is the session's lifecycle as this host knows it. The host is the
	// only place that can tell working from blocked: it sees the turn start,
	// the permission question go out, and the turn end. Anything it cannot
	// classify is unknown, never done.
	state session.State
}

// ptyBufferLimit bounds the terminal output held for the next client. It is a
// screenful's worth of scrollback, not a transcript: past this the oldest bytes
// go, because a reattach should show the recent past at a bounded cost.
const ptyBufferLimit = 256 * 1024

// sendScrollback hands the buffered terminal output to the current client on
// request. It is how a window that starts watching an already-running session
// gets the screen it missed: the host has been writing to the daemon all along,
// so only the host still has those bytes.
//
// The buffer is left in place rather than cleared here: another window may ask
// next, and the write path trims it anyway. Sending is guarded by the replay
// flag so live output cannot overtake the snapshot.
func (h *Host) sendScrollback() {
	h.mu.Lock()
	conn := h.conn
	if conn == nil || h.replayConn != nil || len(h.out) == 0 {
		h.mu.Unlock()
		return
	}
	h.replayConn = conn
	pending := h.out
	h.mu.Unlock()

	h.replayOutput(conn, pending)
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

// stateLocked reports the lifecycle, defaulting to unknown. A host that has not
// classified itself is not idle: idle would claim it is ready for input.
func (h *Host) stateLocked() session.State {
	if h.state == "" {
		return session.StateUnknown
	}
	return h.state
}

// setState records a lifecycle change and tells the attached client. A state
// that did not change is not announced: the UI must not repaint for nothing,
// and a duplicate would read as new activity.
func (h *Host) setState(next session.State) {
	h.mu.Lock()
	previous := h.stateLocked()
	if previous == next {
		h.mu.Unlock()
		return
	}
	h.state = next
	conn := h.conn
	h.mu.Unlock()

	h.log.Info("session state", "session", h.cfg.ID, "from", string(previous), "to", string(next))

	if conn == nil {
		return
	}
	// A failed notify means this client is gone; the state stays on the host
	// and the next attach reads it from Info.
	if err := conn.Notify(session.MethodState, session.StateUpdate{ID: h.cfg.ID, State: next}); err != nil {
		h.log.Debug("forward a state change failed", "session", h.cfg.ID, "err", err.Error())
	}
}

// CurrentState reports the lifecycle without touching process state.
func (h *Host) CurrentState() session.State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stateLocked()
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

	// A terminal's scrollback lives in its client, so the bytes a returning
	// client missed are replayed. The snapshot is taken under the same lock
	// that promotes this connection, and output produced from here on queues
	// behind it: a live chunk overtaking the replay would show the past after
	// the present.
	var pending []byte
	if h.pty != nil {
		h.replayConn = conn
		pending = h.out
		h.out = nil
	}
	h.mu.Unlock()

	if previous != nil {
		previous.Close()
	}

	if h.pty != nil {
		h.replayOutput(conn, pending)
	} else {
		// Structured updates are replayed as notifications, not as bytes.
		h.replayBuffered(conn)
	}

	// The state the session is in now goes to the new client at once: waiting
	// for the next change would leave a blocked session looking idle until the
	// user answered the question that is waiting for them.
	h.notifyState(conn)

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

// notifyState sends the current lifecycle to one client.
func (h *Host) notifyState(conn *ipc.Conn) {
	state := h.CurrentState()
	if err := conn.Notify(session.MethodState, session.StateUpdate{ID: h.cfg.ID, State: state}); err != nil {
		h.log.Debug("send the state to a new client failed", "session", h.cfg.ID, "err", err.Error())
	}
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
	case session.MethodReplay:
		h.sendScrollback()
		return struct{}{}, nil
	case session.MethodSeen:
		// The client has looked at the finished work, which is what separates
		// done from idle. Seen is per client: one window looking must not
		// clear another window's badge.
		h.setState(session.StateIdle)
		return struct{}{}, nil
	case session.MethodStop:
		// Stopping closes this connection too, so it runs off the handler: a
		// Close inside the call would pull the pipe out from under its own
		// reply. The caller asked for the session to end and needs no answer.
		go func() { _ = h.Close() }()
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
		State:   h.CurrentState(),
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
	// Terminal output is always copied into the replay buffer, and also sent
	// live to the attached client so the daemon's subscribers see it without
	// waiting for a replay. While a replay is being written, the live copy is
	// withheld: the replay already carries these bytes, and sending them twice
	// would make the client render them twice.
	buffered := stream == session.StreamOutput
	if buffered {
		h.bufferLocked(payload)
	}
	queued := buffered && h.replayConn != nil
	h.mu.Unlock()

	if conn == nil || queued {
		return
	}
	// A failed send means this client is gone. The PTY keeps running and the
	// next client attaches to the same agent.
	_ = conn.SendData(stream, payload)
}

// bufferLocked appends terminal output to the replay buffer, keeping the most
// recent bytes. h.mu must be held.
//
// Every terminal chunk is kept, whether or not a client is attached: the
// daemon's connection is not the same thing as a window watching, and a window
// that starts watching an already-running session needs the screen the agent
// produced before it looked. The buffer is bounded, so this is recent
// scrollback rather than a transcript.
// Trimming is aligned to a line break: a terminal fed half an escape sequence
// renders it as garbage, and the cost of keeping a few extra bytes is one scan
// of a buffer that is already bounded.
//
// Trimming happens in bulk, down to the point where half the limit is free
// again. Copying the whole buffer on every chunk would cost a ~256 KB memmove
// per PTY read under sustained output, which is exactly when it hurts.
func (h *Host) bufferLocked(payload []byte) {
	h.out = append(h.out, payload...)
	if len(h.out) < 2*ptyBufferLimit {
		return
	}

	over := len(h.out) - ptyBufferLimit
	start := over
	if cut := bytes.IndexByte(h.out[start:], '\n'); cut >= 0 {
		start += cut + 1
	} else {
		// No line break after the cut: every kept byte would be the tail of a
		// line whose head is gone, so keeping none is the honest choice.
		start = len(h.out)
	}

	// Copy rather than reslice: the old backing array would otherwise pin its
	// full size for the life of the session as the buffer slides forward.
	h.out = append(make([]byte, 0, len(h.out)-start), h.out[start:]...)
}

// replayOutput writes what the terminal missed to a freshly attached client.
// pending is the snapshot taken by attach; anything produced while it is being
// written is fetched afterwards and sent in order, so the client sees neither a
// gap nor a duplicate.
//
// All state changes check that conn is still the connection being replayed: an
// attach that supersedes this one must not have its replay state cleared, nor
// its queue polluted with bytes meant for a client that is gone.
func (h *Host) replayOutput(conn *ipc.Conn, pending []byte) {
	for {
		for len(pending) > 0 {
			chunk := pending
			if len(chunk) > ipc.MaxFrameSize {
				chunk = chunk[:ipc.MaxFrameSize]
			}
			if err := conn.SendData(session.StreamOutput, chunk); err != nil {
				h.abandonReplay(conn, pending)
				return
			}
			pending = pending[len(chunk):]
		}

		// Clearing the replay state under the same lock that checks the buffer
		// is what makes the handover exact: send either queues here or goes
		// straight out, never both.
		h.mu.Lock()
		if h.replayConn != conn {
			// Another client owns the replay now; its snapshot covers what this
			// one had left, so these bytes are not ours to send or restore.
			h.mu.Unlock()
			return
		}
		pending = h.out
		h.out = nil
		if len(pending) == 0 {
			h.replayConn = nil
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
	}
}

// abandonReplay gives back the bytes a failed replay could not deliver, so the
// next client is not shown a screen that starts after them. It only takes over
// state it still owns: a replay superseded by a newer attach leaves that
// attach's queue untouched.
func (h *Host) abandonReplay(conn *ipc.Conn, pending []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.replayConn != conn {
		return
	}
	h.replayConn = nil
	h.out = append(pending, h.out...)
	if over := len(h.out) - ptyBufferLimit; over > 0 {
		// Trim on a line break for the same reason bufferLocked does: a
		// terminal fed half an escape sequence renders it as garbage.
		if cut := bytes.IndexByte(h.out[over:], '\n'); cut >= 0 {
			over += cut + 1
		} else {
			over = len(h.out)
		}
		h.out = append(make([]byte, 0, len(h.out)-over), h.out[over:]...)
	}
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

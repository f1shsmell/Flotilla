package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/version"
)

// shutdownGrace is how long Close waits for the agent to exit after its stdin
// closes, before it kills the process.
const shutdownGrace = 3 * time.Second

// Launch describes the agent process to start.
type Launch struct {
	// Command and Args launch the agent. It must speak ACP over stdio.
	Command string
	Args    []string
	// Dir is the working directory, normally the session worktree.
	Dir string
	// Env is the agent's environment. Nil inherits this process's.
	Env []string
	// MCPServers are tool servers the agent should start for this session.
	MCPServers []sdk.McpServer
	// ResumeID reopens a saved conversation; empty creates a new one.
	ResumeID string
}

// Session is one ACP conversation: the agent process, the client side of the
// connection, and the session id the agent assigned.
type Session struct {
	client  *Client
	conn    *sdk.ClientSideConnection
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	agentID sdk.SessionId
	init    sdk.InitializeResponse
	// created is what the agent answered when the session was opened. It is kept
	// because it carries the two things a client projects into controls — the
	// modes and the configuration options — and a caller that dropped it could
	// only learn them by sending a turn, which costs tokens.
	created sdk.NewSessionResponse
	exitc   chan error
	// exited closes when the agent process is gone, whichever side ended it.
	// Close reads the status from exitc; a host that wants to notice an agent
	// dying on its own watches this instead, because exitc has one reader.
	exited  chan struct{}
	exitErr atomic.Pointer[error]

	mu     sync.Mutex
	closed bool

	// surfMu guards the live surface: modes and options start as what the agent
	// announced and are replaced when it reports a change. created itself is
	// never mutated, so a reader that only wants the opening answer can have it.
	surfMu  sync.Mutex
	modes   *sdk.SessionModeState
	options []sdk.SessionConfigOption
	// onSurface folds a change into the live state and tells the client. It is
	// set at open, and the setters call it for an agent that answers in the
	// reply instead of sending a notification.
	onSurface func(SurfaceChange)
}

// workspaceOf returns the first candidate a session can run in, falling back to
// this process's own directory. The agent rejects an empty cwd, and create and
// resume have to be told the same one, so the fallback happens here rather than
// at either call site.
func workspaceOf(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// Open starts the agent, initializes the connection, and opens a session. The
// returned Session is ready for Prompt.
func Open(ctx context.Context, launch Launch, cfg Config) (*Session, error) {
	if launch.Command == "" {
		return nil, errors.New("open an acp session: empty command")
	}

	// One workspace serves the process, the session and the file boundary, so a
	// caller that names none still gets a session the agent will accept.
	cfg.Workspace = workspaceOf(cfg.Workspace, launch.Dir)
	if cfg.Workspace == "" {
		return nil, errors.New("open an acp session: no working directory")
	}

	// The agent's lifetime is the session's, not the caller's: a cancelled
	// context ends the call, and Close ends the agent.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), launch.Command, launch.Args...)
	cmd.Dir = cfg.Workspace
	cmd.Env = launch.Env
	detachAgent(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open the agent's stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open the agent's stdout: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start %q: %w", launch.Command, err)
	}

	s := &Session{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		exitc:  make(chan error, 1),
		exited: make(chan struct{}),
	}

	// One goroutine owns Wait, so Close never waits twice and a reaped process
	// is never killed a second time. The status goes to exitc for Close and the
	// event goes to exited for whoever needs to notice a death that happened
	// while nobody was closing anything.
	go func() {
		err := cmd.Wait()
		if err != nil {
			s.exitErr.Store(&err)
		}
		close(s.exited)
		s.exitc <- err
	}()

	// The surface is tracked here before the caller sees the change, so a read
	// that follows a set's notification always reports the new value.
	wrapped := cfg
	wrapped.OnSurface = func(change SurfaceChange) {
		s.applySurface(change)
		if cfg.OnSurface != nil {
			cfg.OnSurface(change)
		}
	}
	s.onSurface = wrapped.OnSurface
	client := NewClient(wrapped)
	s.client = client
	s.conn = sdk.NewClientSideConnection(client, stdin, stdout)

	initResp, err := s.conn.Initialize(ctx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientCapabilities: sdk.ClientCapabilities{
			Fs: sdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			// An agent announces child sessions only to a client that
			// advertised them. The ACP field is unstable: drop this line when the
			// SDK removes it, and keep the updates when it stabilizes.
			Subagents: &sdk.SubagentCapabilities{},
		},
		ClientInfo: &sdk.Implementation{Name: "flotilla", Version: version.String()},
	})
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("initialize the agent: %w", err)
	}
	if initResp.ProtocolVersion != sdk.ProtocolVersionNumber {
		_ = s.Close()
		return nil, fmt.Errorf("agent speaks ACP %d, this build speaks %d: %w",
			initResp.ProtocolVersion, sdk.ProtocolVersionNumber, ErrProtocol)
	}
	s.init = initResp

	if launch.ResumeID != "" {
		return s.resume(ctx, launch.ResumeID, cfg.Workspace, launch.MCPServers)
	}

	created, err := s.conn.NewSession(ctx, sdk.NewSessionRequest{
		Cwd:        cfg.Workspace,
		McpServers: nonNilServers(launch.MCPServers),
	})
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("open a session with the agent: %w", err)
	}
	s.agentID = created.SessionId
	// The reader goroutine can already be folding a surface change, which falls
	// back to created; the write takes the same lock those reads take.
	s.surfMu.Lock()
	s.created = created
	s.surfMu.Unlock()

	return s, nil
}

// nonNilServers sends an empty list rather than a null for a session that was
// given no tool servers: the protocol spells it as a list, and an agent that
// reads null as "inherit mine" is not one this build can reason about.
func nonNilServers(servers []sdk.McpServer) []sdk.McpServer {
	if servers == nil {
		return []sdk.McpServer{}
	}
	return servers
}

// resume reopens a saved conversation on a freshly launched agent. The resume
// answer carries the same surface a new session would, so it is folded into
// created exactly like one: every reader of the opening answer keeps working.
func (s *Session) resume(ctx context.Context, id, workspace string, servers []sdk.McpServer) (*Session, error) {
	if !s.SupportsResume() {
		_ = s.Close()
		return nil, fmt.Errorf("resume session %q: %w", id, ErrUnsupported)
	}
	resumed, err := s.conn.ResumeSession(ctx, sdk.ResumeSessionRequest{
		SessionId:  sdk.SessionId(id),
		Cwd:        workspace,
		McpServers: nonNilServers(servers),
	})
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("resume session %q: %w", id, err)
	}
	s.agentID = sdk.SessionId(id)
	s.surfMu.Lock()
	s.created = sdk.NewSessionResponse{
		SessionId:     s.agentID,
		Modes:         resumed.Modes,
		ConfigOptions: resumed.ConfigOptions,
	}
	s.surfMu.Unlock()
	return s, nil
}

// ID reports the session id the agent assigned.
func (s *Session) ID() sdk.SessionId { return s.agentID }

// PID reports the agent process id, or 0 when the process never started. It is
// for diagnostics and for a caller that has to take the agent away.
func (s *Session) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Exited closes when the agent process is gone, whether it ended on its own or
// because Close stopped it. A host watches this so an agent that died cannot
// leave a session that looks alive and fails every prompt.
func (s *Session) Exited() <-chan struct{} { return s.exited }

// ExitErr reports why the agent exited, or nil when it had not yet. It is only
// meaningful after Exited has closed.
func (s *Session) ExitErr() error {
	if err := s.exitErr.Load(); err != nil {
		return *err
	}
	return nil
}

// Capabilities reports what the agent said it supports.
func (s *Session) Capabilities() sdk.AgentCapabilities { return s.init.AgentCapabilities }

// SupportsResume reports whether the agent advertised session/resume, which is
// what decides whether a session can survive its agent's death.
func (s *Session) SupportsResume() bool {
	return s.init.AgentCapabilities.SessionCapabilities.Resume != nil
}

// Modes reports the agent's current mode state, or nil when it offers none. It
// starts as what the session opened with and follows the agent's own updates.
func (s *Session) Modes() *sdk.SessionModeState {
	s.surfMu.Lock()
	defer s.surfMu.Unlock()
	if s.modes != nil {
		// A change that arrived before the opening response folded no available
		// modes; the response's list is still the truth about what is offered.
		if len(s.modes.AvailableModes) == 0 && s.created.Modes != nil {
			next := *s.modes
			next.AvailableModes = s.created.Modes.AvailableModes
			return &next
		}
		return s.modes
	}
	return s.created.Modes
}

// ConfigOptions reports the agent's current configuration options. Empty means
// the agent offers none, which is a fact a client shows rather than a failure
// to hide.
func (s *Session) ConfigOptions() []sdk.SessionConfigOption {
	s.surfMu.Lock()
	defer s.surfMu.Unlock()
	if s.options != nil {
		return s.options
	}
	return s.created.ConfigOptions
}

// applySurface folds one reported change into the live state. A change carries
// either the whole option list or a new current mode; the opening answer stays
// untouched, so the fallback readers above keep working after a change.
func (s *Session) applySurface(change SurfaceChange) {
	s.surfMu.Lock()
	defer s.surfMu.Unlock()

	if change.Options != nil {
		s.options = change.Options
	}
	if change.ModeID != "" {
		available := s.created.Modes
		if s.modes != nil {
			available = s.modes
		}
		next := &sdk.SessionModeState{CurrentModeId: sdk.SessionModeId(change.ModeID)}
		if available != nil {
			next.AvailableModes = available.AvailableModes
		}
		s.modes = next
	}
}

// SetMode asks the agent to switch to one of its modes.
func (s *Session) SetMode(ctx context.Context, modeID string) error {
	if _, err := s.conn.SetSessionMode(ctx, sdk.SetSessionModeRequest{
		SessionId: s.agentID,
		ModeId:    sdk.SessionModeId(modeID),
	}); err != nil {
		return fmt.Errorf("set mode %q: %w", modeID, err)
	}
	return nil
}

// noteSurface folds a change the agent reported in a reply rather than in a
// notification. It takes the same path, so a client hears it and a read that
// follows reports the new value.
func (s *Session) noteSurface(change SurfaceChange) {
	if change.Options == nil {
		return
	}
	if s.onSurface != nil {
		s.onSurface(change)
		return
	}
	s.applySurface(change)
}

// SetConfigValue asks the agent to pick one value of a select option. The
// reply carries the full option list — some agents (opencode) report changes
// nowhere else — so it is folded into the surface here.
func (s *Session) SetConfigValue(ctx context.Context, configID, value string) error {
	resp, err := s.conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		ValueId: &sdk.SetSessionConfigOptionValueId{
			SessionId: s.agentID,
			ConfigId:  sdk.SessionConfigId(configID),
			Value:     sdk.SessionConfigValueId(value),
		},
	})
	if err != nil {
		return fmt.Errorf("set config %q to %q: %w", configID, value, err)
	}
	s.noteSurface(SurfaceChange{Options: resp.ConfigOptions})
	return nil
}

// SetConfigBool asks the agent to flip a boolean option, folding the reply's
// option list like SetConfigValue.
func (s *Session) SetConfigBool(ctx context.Context, configID string, value bool) error {
	resp, err := s.conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		Boolean: &sdk.SetSessionConfigOptionBoolean{
			SessionId: s.agentID,
			ConfigId:  sdk.SessionConfigId(configID),
			Value:     value,
		},
	})
	if err != nil {
		return fmt.Errorf("set config %q to %t: %w", configID, value, err)
	}
	s.noteSurface(SurfaceChange{Options: resp.ConfigOptions})
	return nil
}

// Prompt runs one turn and returns why it stopped.
func (s *Session) Prompt(ctx context.Context, blocks ...sdk.ContentBlock) (sdk.PromptResponse, error) {
	resp, err := s.conn.Prompt(ctx, sdk.PromptRequest{SessionId: s.agentID, Prompt: blocks})
	if err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("prompt the agent: %w", err)
	}
	return resp, nil
}

// steerMethod is the extension method the steering adapters implement. It is
// advertised at initialize, not assumed: an agent that did not is not asked.
const steerMethod = "_session/steering"

// SteerSupported reports whether the agent advertised steering. An agent that
// did not can only take a message as the next prompt.
func (s *Session) SteerSupported() bool {
	var v struct {
		Supported bool `json:"supported"`
	}
	return s.init.Meta.Get("steering", &v) == nil && v.Supported
}

// steerIdlePromptRequired opts into the adapters' host-owned idle fallback: a
// steer that finds no running turn answers "promptRequired" instead of starting
// a turn of the adapter's own that the host could not track.
var steerIdlePromptRequired = sdk.Meta{"steering": json.RawMessage(`{"idleBehavior":"promptRequired"}`)}

// Steer injects one message into the running turn and reports whether the turn
// took it. A false answer means the adapter did not apply the message — no turn
// was running, or the attempt failed — and the caller queues it instead.
func (s *Session) Steer(ctx context.Context, text string) (bool, error) {
	raw, err := s.conn.CallExtension(ctx, steerMethod, struct {
		SessionID sdk.SessionId      `json:"sessionId"`
		Prompt    []sdk.ContentBlock `json:"prompt"`
		Meta      sdk.Meta           `json:"_meta,omitempty"`
	}{
		SessionID: s.agentID,
		Prompt:    []sdk.ContentBlock{sdk.TextBlock(text)},
		Meta:      steerIdlePromptRequired,
	})
	if err != nil {
		return false, fmt.Errorf("steer the running turn: %w", err)
	}
	var resp struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(raw, &resp)
	switch resp.Outcome {
	case "promptRequired", "failed":
		return false, nil
	}
	return true, nil
}

// Cancel asks the agent to stop the current turn.
func (s *Session) Cancel(ctx context.Context) error {
	if err := s.conn.Cancel(ctx, sdk.CancelNotification{SessionId: s.agentID}); err != nil {
		return fmt.Errorf("cancel the turn: %w", err)
	}
	return nil
}

// Close ends the conversation: stdin first so the agent can exit on its own,
// then its pipes, and only a process that outstays the grace period is killed.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	var errs []error

	if s.stdin != nil {
		if err := s.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, fmt.Errorf("close the agent's stdin: %w", err))
		}
	}

	select {
	case err := <-s.exitc:
		// A non-zero exit is the agent's own decision, not a close failure;
		// only a wait error this build cannot interpret is reported.
		var exitErr *exec.ExitError
		if err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.As(err, &exitErr) {
			errs = append(errs, fmt.Errorf("wait for the agent: %w", err))
		}
	case <-time.After(shutdownGrace):
		if s.cmd != nil && s.cmd.Process != nil {
			if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				errs = append(errs, fmt.Errorf("stop the agent: %w", err))
			}
		}
		// Reap the process this call just killed; its exit status is the kill's.
		<-s.exitc //nolint:errcheck // the exit status of a killed agent is not a Close failure
	}

	if s.stdout != nil {
		if err := s.stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, fmt.Errorf("close the agent's stdout: %w", err))
		}
	}

	return errors.Join(errs...)
}

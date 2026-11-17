package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/coder/acp-go-sdk"
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
	exitc   chan error
	// exited closes when the agent process is gone, whichever side ended it.
	// Close reads the status from exitc; a host that wants to notice an agent
	// dying on its own watches this instead, because exitc has one reader.
	exited  chan struct{}
	exitErr atomic.Pointer[error]

	mu     sync.Mutex
	closed bool
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

	client := NewClient(cfg)
	s.client = client
	s.conn = sdk.NewClientSideConnection(client, stdin, stdout)

	initResp, err := s.conn.Initialize(ctx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientCapabilities: sdk.ClientCapabilities{
			Fs: sdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		},
		ClientInfo: &sdk.Implementation{Name: "flotilla", Version: "0.0.0-dev"},
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

	created, err := s.conn.NewSession(ctx, sdk.NewSessionRequest{
		Cwd:        cfg.Workspace,
		McpServers: []sdk.McpServer{},
	})
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("open a session with the agent: %w", err)
	}
	s.agentID = created.SessionId

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
// what decides whether this session can survive its host.
func (s *Session) SupportsResume() bool {
	return s.init.AgentCapabilities.SessionCapabilities.Resume != nil
}

// Prompt runs one turn and returns why it stopped.
func (s *Session) Prompt(ctx context.Context, blocks ...sdk.ContentBlock) (sdk.PromptResponse, error) {
	resp, err := s.conn.Prompt(ctx, sdk.PromptRequest{SessionId: s.agentID, Prompt: blocks})
	if err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("prompt the agent: %w", err)
	}
	return resp, nil
}

// Cancel asks the agent to stop the current turn.
func (s *Session) Cancel(ctx context.Context) error {
	if err := s.conn.Cancel(ctx, sdk.CancelNotification{SessionId: s.agentID}); err != nil {
		return fmt.Errorf("cancel the turn: %w", err)
	}
	return nil
}

// Resume asks the agent to resume this session id after a reconnect. It is only
// attempted when the agent advertised the capability.
func (s *Session) Resume(ctx context.Context) error {
	if !s.SupportsResume() {
		return fmt.Errorf("agent does not advertise resume: %w", ErrUnsupported)
	}
	if _, err := s.conn.ResumeSession(ctx, sdk.ResumeSessionRequest{
		SessionId:  s.agentID,
		Cwd:        s.client.cfg.Workspace,
		McpServers: []sdk.McpServer{},
	}); err != nil {
		return fmt.Errorf("resume session %q: %w", s.agentID, err)
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

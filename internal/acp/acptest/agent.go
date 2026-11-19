package acptest

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	sdk "github.com/coder/acp-go-sdk"
)

// Agent is an ACP agent with no model behind it. It answers a prompt with a
// message, a thought, a tool call and a permission request, so the client side
// can be driven end to end without a vendor CLI or credentials.
type Agent struct {
	conn *sdk.AgentSideConnection

	mu       sync.Mutex
	sessions map[sdk.SessionId]*SessionState
}

// SessionState is the fake's per-session bookkeeping.
type SessionState struct {
	id     sdk.SessionId
	cancel context.CancelFunc
	// askPermission makes the next turn request permission.
	askPermission bool
	// modes and options are the live surface a set call changes. They are
	// guarded by the agent's mutex, like every other field here.
	modes   *sdk.SessionModeState
	options []sdk.SessionConfigOption
}

var (
	_ sdk.Agent       = (*Agent)(nil)
	_ sdk.AgentLoader = (*Agent)(nil)
)

// NewAgent returns an agent that advertises resume support, which is what the
// host probes for after a crash.
func NewAgent() *Agent {
	return &Agent{sessions: make(map[sdk.SessionId]*SessionState)}
}

// SetAgentConnection receives the connection the SDK builds.
func (a *Agent) SetAgentConnection(conn *sdk.AgentSideConnection) { a.conn = conn }

// Initialize advertises the capabilities this fake supports.
func (a *Agent) Initialize(context.Context, sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	return sdk.InitializeResponse{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		AgentCapabilities: sdk.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: sdk.SessionCapabilities{
				Resume: &sdk.SessionResumeCapabilities{},
			},
		},
	}, nil
}

// Authenticate accepts anything.
func (a *Agent) Authenticate(context.Context, sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	return sdk.AuthenticateResponse{}, nil
}

// Logout does nothing.
func (a *Agent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}

// NewSession records a session and returns its id, along with the two things a
// client projects into controls. The fake announces them so the zero-token
// handshake probe has something to read: an agent that announced nothing
// would make "the probe read nothing" indistinguishable from "the probe is
// broken".
func (a *Agent) NewSession(_ context.Context, params sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	id := sdk.SessionId("fake-" + lastSegment(params.Cwd))

	models := sdk.SessionConfigSelectOptionsUngrouped{
		{Value: "fake-small", Name: "Small"},
		{Value: "fake-large", Name: "Large"},
	}
	thoughts := sdk.SessionConfigSelectOptionsUngrouped{
		{Value: "low", Name: "Low"},
		{Value: "high", Name: "High"},
	}
	modes := &sdk.SessionModeState{
		AvailableModes: []sdk.SessionMode{
			{Id: "plan", Name: "Plan"},
			{Id: "code", Name: "Code"},
		},
		CurrentModeId: "code",
	}
	options := []sdk.SessionConfigOption{
		{Select: &sdk.SessionConfigOptionSelect{
			Id:           "model",
			Name:         "Model",
			Type:         "select",
			CurrentValue: "fake-small",
			Options:      sdk.SessionConfigSelectOptions{Ungrouped: &models},
		}},
		{Select: &sdk.SessionConfigOptionSelect{
			Id:           "thought_level",
			Name:         "Thinking",
			Type:         "select",
			CurrentValue: "low",
			Options:      sdk.SessionConfigSelectOptions{Ungrouped: &thoughts},
		}},
		{Boolean: &sdk.SessionConfigOptionBoolean{
			Id:           "auto_approve",
			Name:         "Auto-approve",
			Type:         "boolean",
			CurrentValue: true,
		}},
	}

	a.mu.Lock()
	a.sessions[id] = &SessionState{id: id, askPermission: true, modes: modes, options: options}
	a.mu.Unlock()

	return sdk.NewSessionResponse{
		SessionId:     id,
		Modes:         modes,
		ConfigOptions: options,
	}, nil
}

// LoadSession replays nothing: the fake has no history to load.
func (a *Agent) LoadSession(_ context.Context, _ sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	return sdk.LoadSessionResponse{}, nil
}

// ResumeSession records that the id is live again.
func (a *Agent) ResumeSession(_ context.Context, params sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	a.mu.Lock()
	a.sessions[params.SessionId] = &SessionState{id: params.SessionId}
	a.mu.Unlock()

	return sdk.ResumeSessionResponse{}, nil
}

// Cancel stops the current turn.
func (a *Agent) Cancel(_ context.Context, params sdk.CancelNotification) error {
	a.mu.Lock()
	s := a.sessions[params.SessionId]
	a.mu.Unlock()

	if s != nil && s.cancel != nil {
		s.cancel()
	}
	return nil
}

// Prompt runs a canned turn: a message, a thought, a tool call, an optional
// permission request, and a closing message.
func (a *Agent) Prompt(ctx context.Context, params sdk.PromptRequest) (sdk.PromptResponse, error) {
	a.mu.Lock()
	s, ok := a.sessions[params.SessionId]
	a.mu.Unlock()
	if !ok {
		return sdk.PromptResponse{}, fmt.Errorf("unknown session %q", params.SessionId)
	}

	turnCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	s.cancel = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		s.cancel = nil
		a.mu.Unlock()
		cancel()
	}()

	prompted := promptText(params.Prompt)
	if err := a.conn.SessionUpdate(turnCtx, sdk.SessionNotification{
		SessionId: s.id,
		Update:    sdk.UpdateAgentMessageText("working on: " + prompted),
	}); err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
	}

	if err := a.conn.SessionUpdate(turnCtx, sdk.SessionNotification{
		SessionId: s.id,
		Update:    sdk.UpdateAgentThoughtText("thinking about: " + prompted),
	}); err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
	}

	if err := a.conn.SessionUpdate(turnCtx, sdk.SessionNotification{
		SessionId: s.id,
		Update: sdk.StartToolCall(
			sdk.ToolCallId("call-1"),
			"reading the workspace",
			sdk.WithStartKind(sdk.ToolKindRead),
			sdk.WithStartStatus(sdk.ToolCallStatusPending),
		),
	}); err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
	}

	a.mu.Lock()
	ask := s.askPermission
	a.mu.Unlock()

	if ask {
		resp, err := a.conn.RequestPermission(turnCtx, sdk.RequestPermissionRequest{
			SessionId: s.id,
			ToolCall: sdk.ToolCallUpdate{
				ToolCallId: sdk.ToolCallId("call-1"),
				Title:      sdk.Ptr("edit config.json"),
				Kind:       sdk.Ptr(sdk.ToolKindEdit),
				Status:     sdk.Ptr(sdk.ToolCallStatusPending),
			},
			Options: []sdk.PermissionOption{
				{Kind: sdk.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: sdk.PermissionOptionId("allow")},
				{Kind: sdk.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: sdk.PermissionOptionId("reject")},
			},
		})
		if err != nil {
			return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
		}

		if resp.Outcome.Cancelled != nil {
			if err := a.conn.SessionUpdate(turnCtx, sdk.SessionNotification{
				SessionId: s.id,
				Update:    sdk.UpdateAgentMessageText("stopped: no permission"),
			}); err != nil {
				return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
			}
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		}
	}

	if err := a.conn.SessionUpdate(turnCtx, sdk.SessionNotification{
		SessionId: s.id,
		Update: sdk.UpdateToolCall(
			sdk.ToolCallId("call-1"),
			sdk.WithUpdateStatus(sdk.ToolCallStatusCompleted),
		),
	}); err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
	}

	if turnCtx.Err() != nil {
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	}

	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

// SetSessionMode records the new mode and announces it. The notification goes
// out before the response, so a client that reads the surface after its set
// call returns always sees the value it asked for.
func (a *Agent) SetSessionMode(ctx context.Context, params sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	a.mu.Lock()
	s := a.sessions[params.SessionId]
	if s != nil && s.modes != nil {
		next := *s.modes
		next.CurrentModeId = params.ModeId
		s.modes = &next
	}
	a.mu.Unlock()
	if s == nil {
		return sdk.SetSessionModeResponse{}, fmt.Errorf("unknown session %q", params.SessionId)
	}

	if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: params.SessionId,
		Update: sdk.SessionUpdate{
			CurrentModeUpdate: &sdk.SessionCurrentModeUpdate{CurrentModeId: params.ModeId},
		},
	}); err != nil {
		return sdk.SetSessionModeResponse{}, fmt.Errorf("announce the mode change: %w", err)
	}
	return sdk.SetSessionModeResponse{}, nil
}

// SetSessionConfigOption records the new value and announces the whole option
// list, which is what the protocol's update carries. Like the mode set, the
// notification precedes the response.
func (a *Agent) SetSessionConfigOption(ctx context.Context, params sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	var sessionID sdk.SessionId
	switch {
	case params.ValueId != nil:
		sessionID = params.ValueId.SessionId
	case params.Boolean != nil:
		sessionID = params.Boolean.SessionId
	default:
		return sdk.SetSessionConfigOptionResponse{}, fmt.Errorf("set config option: no value")
	}

	a.mu.Lock()
	s := a.sessions[sessionID]
	var options []sdk.SessionConfigOption
	if s != nil {
		// The announced list is copied, never edited in place: the notification
		// marshals it after the unlock, and a concurrent set must not race it.
		options = make([]sdk.SessionConfigOption, len(s.options))
		for i, option := range s.options {
			switch {
			case option.Select != nil:
				next := *option.Select
				if params.ValueId != nil && next.Id == params.ValueId.ConfigId {
					next.CurrentValue = params.ValueId.Value
				}
				options[i] = sdk.SessionConfigOption{Select: &next}
			case option.Boolean != nil:
				next := *option.Boolean
				if params.Boolean != nil && next.Id == params.Boolean.ConfigId {
					next.CurrentValue = params.Boolean.Value
				}
				options[i] = sdk.SessionConfigOption{Boolean: &next}
			default:
				options[i] = option
			}
		}
		s.options = options
	}
	a.mu.Unlock()
	if s == nil {
		return sdk.SetSessionConfigOptionResponse{}, fmt.Errorf("unknown session %q", sessionID)
	}

	if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
		SessionId: sessionID,
		Update: sdk.SessionUpdate{
			ConfigOptionUpdate: &sdk.SessionConfigOptionUpdate{ConfigOptions: options},
		},
	}); err != nil {
		return sdk.SetSessionConfigOptionResponse{}, fmt.Errorf("announce the option change: %w", err)
	}
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

// CloseSession forgets the session.
func (a *Agent) CloseSession(_ context.Context, params sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	a.mu.Lock()
	delete(a.sessions, params.SessionId)
	a.mu.Unlock()
	return sdk.CloseSessionResponse{}, nil
}

// ListSessions reports every live session.
func (a *Agent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	infos := make([]sdk.SessionInfo, 0, len(a.sessions))
	for id := range a.sessions {
		infos = append(infos, sdk.SessionInfo{SessionId: id, Cwd: "."})
	}
	return sdk.ListSessionsResponse{Sessions: infos}, nil
}

// Serve runs an agent over this process's stdio until the peer disconnects. It
// is what the session tests spawn as a subprocess, so the wire is exercised the
// same way a real agent CLI would exercise it.
func Serve(in io.Reader, out io.Writer) {
	agent := NewAgent()
	conn := sdk.NewAgentSideConnection(agent, out, in)
	agent.SetAgentConnection(conn)
	<-conn.Done()
}

// Main runs the fake agent on stdio and exits when the peer disconnects. A
// build of `acptest.Main` is a stand-in ACP agent binary.
func Main() {
	Serve(os.Stdin, os.Stdout)
}

func promptText(blocks []sdk.ContentBlock) string {
	for _, b := range blocks {
		if b.Text != nil {
			return b.Text.Text
		}
	}
	return ""
}

func lastSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

package acptest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// steerMethod is the extension method both real adapters implement; the fake
// speaks it only when FLT_FAKE_STEER picks that variant.
const steerMethod = "_session/steering"

// Agent is an ACP agent with no model behind it. It answers a prompt with a
// message, a thought, a tool call and a permission request, so the client side
// can be driven end to end without a vendor CLI or credentials.
type Agent struct {
	conn *sdk.AgentSideConnection

	mu       sync.Mutex
	sessions map[sdk.SessionId]*SessionState
	// subagents is what the client advertised at initialize. The fake announces
	// a child only to a client that accepts them, like a real agent.
	subagents bool
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
	// steer receives one mid-turn message while a turn waits for it, and is nil
	// when no turn is running or the variant does not steer.
	steer chan string
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

// SetAgentConnection receives the connection the SDK builds. Its receive loop
// is already running, so the write takes the lock the turn's reads take.
func (a *Agent) SetAgentConnection(conn *sdk.AgentSideConnection) {
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
}

// connection reads the connection under the lock SetAgentConnection takes.
func (a *Agent) connection() *sdk.AgentSideConnection {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn
}

// Initialize advertises the capabilities this fake supports. FLT_FAKE_NO_RESUME
// selects the variant that never advertised resume, whose death ends a session.
func (a *Agent) Initialize(_ context.Context, params sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	a.mu.Lock()
	a.subagents = params.ClientCapabilities.Subagents != nil
	a.mu.Unlock()

	caps := sdk.AgentCapabilities{
		LoadSession: true,
		SessionCapabilities: sdk.SessionCapabilities{
			Resume: &sdk.SessionResumeCapabilities{},
		},
	}
	if os.Getenv("FLT_FAKE_NO_RESUME") != "" {
		caps.SessionCapabilities.Resume = nil
	}
	resp := sdk.InitializeResponse{
		ProtocolVersion:   sdk.ProtocolVersionNumber,
		AgentCapabilities: caps,
	}
	if os.Getenv("FLT_FAKE_STEER") != "" {
		resp.Meta = sdk.Meta{"steering": json.RawMessage(`{"supported":true}`)}
	}
	return resp, nil
}

// HandleExtensionMethod serves the steering extension. With no turn running it
// answers the way both adapters do when the request opts into the host-owned
// idle fallback; the legacy answer is what a request without the opt-in gets.
func (a *Agent) HandleExtensionMethod(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != steerMethod {
		return nil, fmt.Errorf("unknown extension method %q", method)
	}
	var req struct {
		SessionID sdk.SessionId      `json:"sessionId"`
		Prompt    []sdk.ContentBlock `json:"prompt"`
		Meta      struct {
			Steering struct {
				IdleBehavior string `json:"idleBehavior"`
			} `json:"steering"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode a steer: %w", err)
	}

	a.mu.Lock()
	s := a.sessions[req.SessionID]
	var steer chan string
	if s != nil {
		steer = s.steer
	}
	a.mu.Unlock()
	if steer == nil {
		if req.Meta.Steering.IdleBehavior == "promptRequired" {
			return map[string]any{"outcome": "promptRequired", "reason": "noRunningTurn"}, nil
		}
		return map[string]any{"outcome": "startedNewTurn"}, nil
	}

	select {
	case steer <- promptText(req.Prompt):
		return map[string]any{"outcome": "injected"}, nil
	default:
		return nil, fmt.Errorf("session %q already took a steer this turn", req.SessionID)
	}
}

// Authenticate accepts anything.
func (a *Agent) Authenticate(context.Context, sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	return sdk.AuthenticateResponse{}, nil
}

// Logout does nothing.
func (a *Agent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}

// fakeSurface is the modes and options the fake announces, fresh on every
// call so a session's state is never shared with another's.
func fakeSurface() (*sdk.SessionModeState, []sdk.SessionConfigOption) {
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
	return modes, options
}

// NewSession records a session and returns its id, along with the two things a
// client projects into controls. The fake announces them so the zero-token
// handshake probe has something to read: an agent that announced nothing
// would make "the probe read nothing" indistinguishable from "the probe is
// broken".
func (a *Agent) NewSession(_ context.Context, params sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	// FLT_FAKE_REQUIRE_ENV names a variable this agent refuses a session without,
	// which is how a launch test proves a declared environment reached the
	// process rather than staying in the client.
	if name := os.Getenv("FLT_FAKE_REQUIRE_ENV"); name != "" && os.Getenv(name) == "" {
		return sdk.NewSessionResponse{}, fmt.Errorf("%s is not set in this agent's environment", name)
	}

	// FLT_FAKE_CAPTURE records what the client sent and which scope this agent
	// process itself received, which is how a test reads the injection without
	// a model behind it.
	if path := os.Getenv("FLT_FAKE_CAPTURE"); path != "" {
		record := struct {
			Request sdk.NewSessionRequest `json:"request"`
			Env     map[string]string     `json:"env"`
		}{
			Request: params,
			Env: map[string]string{
				"FLT_SESSION_ID": os.Getenv("FLT_SESSION_ID"),
				"FLT_CHANNEL":    os.Getenv("FLT_CHANNEL"),
				"FLT_POLICY":     os.Getenv("FLT_POLICY"),
			},
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return sdk.NewSessionResponse{}, fmt.Errorf("encode the captured new-session request: %w", err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			return sdk.NewSessionResponse{}, fmt.Errorf("write the captured new-session request: %w", err)
		}
	}

	id := sdk.SessionId("fake-" + lastSegment(params.Cwd))
	modes, options := fakeSurface()

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

// ResumeSession records that the id is live again and answers with the same
// surface a new session gets, plus a "resumed" marker: a client can tell a
// reopened conversation from a created one by reading it. FLT_FAKE_RESUME_FAIL
// selects the variant that refuses, which is how a test drives the failure path.
func (a *Agent) ResumeSession(_ context.Context, params sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	if os.Getenv("FLT_FAKE_RESUME_FAIL") != "" {
		return sdk.ResumeSessionResponse{}, fmt.Errorf("resume refused by the fake")
	}
	modes, options := fakeSurface()
	options = append(options, sdk.SessionConfigOption{Boolean: &sdk.SessionConfigOptionBoolean{
		Id:           "resumed",
		Name:         "Resumed",
		Type:         "boolean",
		CurrentValue: true,
	}})

	a.mu.Lock()
	a.sessions[params.SessionId] = &SessionState{id: params.SessionId, askPermission: true, modes: modes, options: options}
	a.mu.Unlock()

	return sdk.ResumeSessionResponse{Modes: modes, ConfigOptions: options}, nil
}

// clientAcceptsSubagents reports whether the client advertised the subagent
// capability at initialize.
func (a *Agent) clientAcceptsSubagents() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.subagents
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
	conn := a.connection()

	turnCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	s.cancel = cancel
	if os.Getenv("FLT_FAKE_STEER") != "" {
		s.steer = make(chan string, 1)
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		s.cancel = nil
		s.steer = nil
		a.mu.Unlock()
		cancel()
	}()

	prompted := promptText(params.Prompt)
	if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
		SessionId: s.id,
		Update:    sdk.UpdateAgentMessageText("working on: " + prompted),
	}); err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
	}

	// The steering variant holds the turn open for one mid-turn message, so a
	// client that steers gets an observable effect instead of a race.
	if os.Getenv("FLT_FAKE_STEER") != "" {
		a.mu.Lock()
		steer := s.steer
		a.mu.Unlock()
		select {
		case text := <-steer:
			if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
				SessionId: s.id,
				Update:    sdk.UpdateAgentMessageText("steered: " + text),
			}); err != nil {
				return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
			}
		case <-time.After(10 * time.Second):
		case <-turnCtx.Done():
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		}
	}

	if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
		SessionId: s.id,
		Update:    sdk.UpdateAgentThoughtText("thinking about: " + prompted),
	}); err != nil {
		return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
	}

	// A child session's update: the outer sessionId names the child, which is
	// the only attribution ACP v1 carries. FLT_FAKE_CHILD_UPDATE selects the
	// variant, because a client that lost the id cannot be told apart from an
	// agent that never had one.
	if os.Getenv("FLT_FAKE_CHILD_UPDATE") != "" {
		if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
			SessionId: sdk.SessionId("sess-child-1"),
			Update:    sdk.UpdateAgentMessageText("working in the child"),
		}); err != nil {
			return sdk.PromptResponse{}, fmt.Errorf("send a child update: %w", err)
		}
	}

	// A child session the agent registers: what it is for, then its own work
	// under its own id, then the state change that says it finished.
	// FLT_FAKE_SUBAGENT selects the variant ("running" keeps the child alive
	// past the turn), and it fires only when the client advertised the
	// capability.
	if variant := os.Getenv("FLT_FAKE_SUBAGENT"); variant != "" && a.clientAcceptsSubagents() {
		child := sdk.SessionId("sess-child-1")
		steps := []sdk.SessionNotification{
			{SessionId: s.id, Update: sdk.SessionUpdate{SubagentUpdate: &sdk.SessionSubagentUpdate{
				SessionId:   child,
				Title:       sdk.PatchSet("Test investigator"),
				Description: sdk.PatchSet("Investigates the repo."),
				State:       sdk.PatchSet(sdk.StateUpdate{Running: &sdk.StateUpdateRunning{}}),
			}}},
			{SessionId: child, Update: sdk.UpdateAgentMessageText("working in the child")},
		}
		if variant != "running" {
			steps = append(steps, sdk.SessionNotification{
				SessionId: s.id,
				Update: sdk.SessionUpdate{SubagentUpdate: &sdk.SessionSubagentUpdate{
					SessionId: child,
					State: sdk.PatchSet(sdk.StateUpdate{Idle: &sdk.StateUpdateIdle{
						None: &sdk.StateUpdateIdleNone{},
					}}),
				}},
			})
		}
		for _, step := range steps {
			if err := conn.SessionUpdate(turnCtx, step); err != nil {
				return sdk.PromptResponse{}, fmt.Errorf("update a subagent: %w", err)
			}
		}
	}

	if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
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
		resp, err := conn.RequestPermission(turnCtx, sdk.RequestPermissionRequest{
			SessionId: s.id,
			ToolCall: sdk.ToolCallUpdate{
				ToolCallId: sdk.ToolCallId("call-1"),
				Title:      sdk.PatchSet("edit config.json"),
				Kind:       sdk.PatchSet(sdk.ToolKindEdit),
				Status:     sdk.PatchSet(sdk.ToolCallStatusPending),
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
			if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
				SessionId: s.id,
				Update:    sdk.UpdateAgentMessageText("stopped: no permission"),
			}); err != nil {
				return sdk.PromptResponse{}, fmt.Errorf("send an update: %w", err)
			}
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		}
	}

	if err := conn.SessionUpdate(turnCtx, sdk.SessionNotification{
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

	if err := a.connection().SessionUpdate(ctx, sdk.SessionNotification{
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
// notification precedes the response. FLT_FAKE_SILENT_CONFIG answers with the
// list and sends no notification, which is how opencode behaves.
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

	if os.Getenv("FLT_FAKE_SILENT_CONFIG") == "" {
		if err := a.connection().SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: sessionID,
			Update: sdk.SessionUpdate{
				ConfigOptionUpdate: &sdk.SessionConfigOptionUpdate{ConfigOptions: options},
			},
		}); err != nil {
			return sdk.SetSessionConfigOptionResponse{}, fmt.Errorf("announce the option change: %w", err)
		}
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

// DeleteSession forgets the session, like CloseSession: the fake keeps no
// history behind it, so there is nothing separate for a delete to drop.
func (a *Agent) DeleteSession(_ context.Context, params sdk.DeleteSessionRequest) (sdk.DeleteSessionResponse, error) {
	a.mu.Lock()
	delete(a.sessions, params.SessionId)
	a.mu.Unlock()
	return sdk.DeleteSessionResponse{}, nil
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

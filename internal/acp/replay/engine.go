package replay

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	sdk "github.com/coder/acp-go-sdk"
)

// Agent answers prompts from a recorded scenario. It is a real ACP agent on the
// wire, so a session driven by it reaches the daemon, the host and the renderer
// through the same path a live agent does; only the model is missing.
type Agent struct {
	conn     *sdk.AgentSideConnection
	scenario Scenario

	mu    sync.Mutex
	turns int
	// tools remembers the calls already announced, because ACP sends a tool
	// twice — once started, once updated — and the second must not create a
	// second line in the client's timeline.
	tools map[sdk.ToolCallId]bool
}

var (
	_ sdk.Agent       = (*Agent)(nil)
	_ sdk.AgentLoader = (*Agent)(nil)
)

// NewAgent returns an agent that answers from s.
func NewAgent(s Scenario) *Agent {
	return &Agent{scenario: s, tools: make(map[sdk.ToolCallId]bool)}
}

// SetAgentConnection receives the connection the SDK builds.
func (a *Agent) SetAgentConnection(conn *sdk.AgentSideConnection) { a.conn = conn }

// Initialize advertises what the recorded agent advertised. A client that
// probes for resume has to see the same answer it would have got live, or a
// replay would exercise a different code path than the run it stands for.
func (a *Agent) Initialize(context.Context, sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	caps := sdk.AgentCapabilities{LoadSession: a.scenario.Capabilities.LoadSession}
	if a.scenario.Capabilities.Resume {
		caps.SessionCapabilities = sdk.SessionCapabilities{Resume: &sdk.SessionResumeCapabilities{}}
	}
	return sdk.InitializeResponse{
		ProtocolVersion:   sdk.ProtocolVersionNumber,
		AgentCapabilities: caps,
	}, nil
}

// Authenticate accepts anything: a replay needs no credentials, which is the
// point of having it.
func (a *Agent) Authenticate(context.Context, sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	return sdk.AuthenticateResponse{}, nil
}

// Logout does nothing.
func (a *Agent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}

// NewSession names the session after its workspace, so a replayed window shows
// an id that reads like the one a live agent would have chosen.
func (a *Agent) NewSession(_ context.Context, params sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	return sdk.NewSessionResponse{SessionId: sdk.SessionId("replay-" + baseName(params.Cwd))}, nil
}

// LoadSession has nothing to restore: a scenario is the whole conversation.
func (a *Agent) LoadSession(context.Context, sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	return sdk.LoadSessionResponse{}, nil
}

// ResumeSession accepts the id it is given, which is what a client checking a
// resumed session expects to hear.
func (a *Agent) ResumeSession(context.Context, sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, nil
}

// Cancel ends the turn being streamed. The stream checks the context between
// updates, so a cancel stops the replay where a live agent would have stopped.
func (a *Agent) Cancel(context.Context, sdk.CancelNotification) error { return nil }

// SetSessionMode accepts any mode.
func (a *Agent) SetSessionMode(context.Context, sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	return sdk.SetSessionModeResponse{}, nil
}

// SetSessionConfigOption accepts any option.
func (a *Agent) SetSessionConfigOption(context.Context, sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	return sdk.SetSessionConfigOptionResponse{}, nil
}

// CloseSession does nothing: a scenario holds no per-session state worth
// clearing.
func (a *Agent) CloseSession(context.Context, sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	return sdk.CloseSessionResponse{}, nil
}

// ListSessions reports none: a replay serves the session the client opened.
func (a *Agent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}

// Prompt streams one recorded turn. Turns are answered in order; past the last
// one the final turn is repeated, because a demo must not break when somebody
// types twice.
func (a *Agent) Prompt(ctx context.Context, params sdk.PromptRequest) (sdk.PromptResponse, error) {
	turn := a.nextTurn()

	for _, update := range turn.Updates {
		// A cancelled context ends the turn cleanly: the client asked to stop,
		// so there is nothing to report as a failure.
		select {
		case <-ctx.Done():
			return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
		default:
		}
		if err := a.conn.SessionUpdate(ctx, sdk.SessionNotification{
			SessionId: params.SessionId,
			Update:    a.toSDK(update),
		}); err != nil {
			return sdk.PromptResponse{}, fmt.Errorf("replay update %q: %w", update.Kind, err)
		}
	}

	reason := turn.StopReason
	if reason == "" {
		reason = string(sdk.StopReasonEndTurn)
	}
	return sdk.PromptResponse{StopReason: sdk.StopReason(reason)}, nil
}

// nextTurn hands out the turn this prompt answers. A scenario with no turn is
// refused at load time; a caller that builds one directly gets nothing rather
// than an index panic.
func (a *Agent) nextTurn() Turn {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.scenario.Turns) == 0 {
		return Turn{StopReason: StopEndTurn}
	}

	index := a.turns
	if index >= len(a.scenario.Turns) {
		index = len(a.scenario.Turns) - 1
	}
	a.turns++
	return a.scenario.Turns[index]
}

// toSDK builds the SDK update one recorded update describes. A tool call is
// announced the first time it is seen and updated after that, which is the
// order the client's timeline depends on.
func (a *Agent) toSDK(u Update) sdk.SessionUpdate {
	switch u.Kind {
	case "thought":
		return sdk.UpdateAgentThoughtText(u.Text)
	case "plan":
		return sdk.UpdatePlan(sdk.PlanEntry{Content: u.Text})
	case "tool":
		id := sdk.ToolCallId(u.ToolCallID)
		a.mu.Lock()
		started := a.tools[id]
		a.tools[id] = true
		a.mu.Unlock()

		if !started {
			return sdk.StartToolCall(id, u.ToolTitle,
				sdk.WithStartStatus(sdk.ToolCallStatusPending))
		}
		return sdk.UpdateToolCall(id, sdk.WithUpdateStatus(sdk.ToolCallStatus(u.ToolStatus)))
	default:
		return sdk.UpdateAgentMessageText(u.Text)
	}
}

// Serve runs the agent over this process's stdio until the client disconnects.
// It is what the session host spawns, so a replayed turn crosses the same wire
// a live one does.
func Serve(in io.Reader, out io.Writer, s Scenario) {
	agent := NewAgent(s)
	conn := sdk.NewAgentSideConnection(agent, out, in)
	agent.SetAgentConnection(conn)
	<-conn.Done()
}

// ScenarioFor resolves the scenario a run answers from: the file named on the
// command line, or the builtin one when nobody names a file. The builtin is why
// the preset works with nothing prepared: a demo or a smoke run starts a
// session without a fixture to record first.
func ScenarioFor(args []string) (Scenario, error) {
	if len(args) == 0 {
		return Builtin(), nil
	}
	return Load(args[0])
}

// Main runs a scenario on stdio. The path comes from argv rather than the
// environment so a preset can name it as an argument and two sessions cannot
// race over one variable.
func Main(args []string) int {
	s, err := ScenarioFor(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	Serve(os.Stdin, os.Stdout, s)
	return 0
}

// baseName is the last segment of a path, on either separator, so a session id
// built from a workspace reads the same on every platform.
func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

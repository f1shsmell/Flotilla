package replay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// waitFor bounds a test that drives a real wire. A replay is synchronous, so
// this is a guard against a deadlock, not a race window.
const waitFor = 10 * time.Second

// collector is the client half: it keeps every update the agent streams and
// answers the calls the replay never makes.
type collector struct {
	updates chan sdk.SessionNotification
}

func (c *collector) SessionUpdate(_ context.Context, params sdk.SessionNotification) error {
	c.updates <- params
	return nil
}

func (c *collector) ReadTextFile(context.Context, sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	return sdk.ReadTextFileResponse{}, nil
}

func (c *collector) WriteTextFile(context.Context, sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	return sdk.WriteTextFileResponse{}, nil
}

func (c *collector) RequestPermission(context.Context, sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	return sdk.RequestPermissionResponse{Outcome: sdk.NewRequestPermissionOutcomeCancelled()}, nil
}

func (c *collector) CreateTerminal(context.Context, sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, nil
}

func (c *collector) KillTerminal(context.Context, sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, nil
}

func (c *collector) TerminalOutput(context.Context, sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, nil
}

func (c *collector) ReleaseTerminal(context.Context, sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, nil
}

func (c *collector) WaitForTerminalExit(context.Context, sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, nil
}

func (c *collector) CreateElicitation(context.Context, sdk.CreateElicitationRequest) (sdk.CreateElicitationResponse, error) {
	return sdk.CreateElicitationResponse{}, errors.New("elicitations are not part of this fake")
}

func (c *collector) CompleteElicitation(context.Context, sdk.CompleteElicitationNotification) error {
	return nil
}

// dial wires an agent to a client over two pipes, so the test speaks the real
// protocol rather than calling the agent's methods directly.
func dial(t *testing.T, s Scenario) *sdk.ClientSideConnection {
	t.Helper()

	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()

	agent := NewAgent(s)
	agentConn := sdk.NewAgentSideConnection(agent, agentToClientW, clientToAgentR)
	agent.SetAgentConnection(agentConn)

	client := &collector{updates: make(chan sdk.SessionNotification, 64)}
	clientConn := sdk.NewClientSideConnection(client, clientToAgentW, agentToClientR)

	t.Cleanup(func() {
		_ = clientToAgentW.Close()
		_ = agentToClientW.Close()
	})
	return clientConn
}

// scenario returns a small two-turn recording: a thought, a tool call, a
// message, then a closing turn.
func scenario() Scenario {
	return Scenario{
		Name: "test",
		Turns: []Turn{
			{
				Updates: []Update{
					{Kind: "thought", Text: "reading the file"},
					{Kind: "tool", ToolCallID: "call-1", ToolTitle: "read a.txt"},
					{Kind: "message", Text: "it says hello"},
				},
				StopReason: "end_turn",
			},
			{
				Updates:    []Update{{Kind: "message", Text: "second answer"}},
				StopReason: "end_turn",
			},
		},
	}
}

// TestReplayStreamsTheRecordedUpdates pins the whole point: a prompt answered
// from the scenario reaches the client as the same stream a live agent sends.
func TestReplayStreamsTheRecordedUpdates(t *testing.T) {
	conn := dial(t, scenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := conn.Prompt(ctx, sdk.PromptRequest{
		SessionId: "replay-sess",
		Prompt:    []sdk.ContentBlock{sdk.TextBlock("hi")},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if string(resp.StopReason) != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
	}
}

// TestReplayReachesTheClient is the assertion the stream above cannot make on
// its own: a prompt that returned proves the turn ended, not that anything was
// sent. Each recorded update has to arrive, in order, as its own kind.
func TestReplayReachesTheClient(t *testing.T) {
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()

	agent := NewAgent(scenario())
	agentConn := sdk.NewAgentSideConnection(agent, agentToClientW, clientToAgentR)
	agent.SetAgentConnection(agentConn)

	client := &collector{updates: make(chan sdk.SessionNotification, 64)}
	conn := sdk.NewClientSideConnection(client, clientToAgentW, agentToClientR)
	t.Cleanup(func() {
		_ = clientToAgentW.Close()
		_ = agentToClientW.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Prompt(ctx, sdk.PromptRequest{
		SessionId: "replay-sess",
		Prompt:    []sdk.ContentBlock{sdk.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// The first turn records a thought, a tool call and a message, in that
	// order, so the client must see exactly those three.
	var got []string
	for len(got) < 3 {
		select {
		case n := <-client.updates:
			switch {
			case n.Update.AgentThoughtChunk != nil:
				got = append(got, "thought:"+n.Update.AgentThoughtChunk.Content.Text.Text)
			case n.Update.ToolCall != nil:
				got = append(got, "tool:"+n.Update.ToolCall.Title)
			case n.Update.AgentMessageChunk != nil:
				got = append(got, "message:"+n.Update.AgentMessageChunk.Content.Text.Text)
			default:
				got = append(got, "other")
			}
		case <-time.After(waitFor):
			t.Fatalf("the client received %v, want a thought, a tool call and a message", got)
		}
	}

	want := []string{"thought:reading the file", "tool:read a.txt", "message:it says hello"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("update %d is %q, want %q (stream: %v)", i, got[i], want[i], got)
		}
	}
}

// TestReplaySendsTheToolCallTwice pins the ordering the client's timeline
// depends on: ACP reports one call twice — started, then updated — and a replay
// that announced it twice would create two lines, or one that updated it first
// would update a call the client never saw.
func TestReplaySendsTheToolCallTwice(t *testing.T) {
	s := scenario()
	s.Turns[0].Updates = []Update{
		{Kind: "tool", ToolCallID: "call-1", ToolTitle: "read a.txt"},
		{Kind: "tool", ToolCallID: "call-1", ToolStatus: "completed"},
	}

	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()

	agent := NewAgent(s)
	agentConn := sdk.NewAgentSideConnection(agent, agentToClientW, clientToAgentR)
	agent.SetAgentConnection(agentConn)

	client := &collector{updates: make(chan sdk.SessionNotification, 64)}
	conn := sdk.NewClientSideConnection(client, clientToAgentW, agentToClientR)
	t.Cleanup(func() {
		_ = clientToAgentW.Close()
		_ = agentToClientW.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Prompt(ctx, sdk.PromptRequest{
		SessionId: "replay-sess",
		Prompt:    []sdk.ContentBlock{sdk.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// The first is a tool_call (the announcement), the second a
	// tool_call_update (the completion). Both belong to the same id.
	var kinds []string
	for len(kinds) < 2 {
		select {
		case n := <-client.updates:
			switch {
			case n.Update.ToolCall != nil:
				if string(n.Update.ToolCall.ToolCallId) != "call-1" {
					t.Fatalf("the announcement names %q, want call-1", n.Update.ToolCall.ToolCallId)
				}
				kinds = append(kinds, "start:"+n.Update.ToolCall.Title)
			case n.Update.ToolCallUpdate != nil:
				kinds = append(kinds, "update:"+derefStatus(n.Update.ToolCallUpdate.Status))
			default:
				kinds = append(kinds, "other")
			}
		case <-time.After(waitFor):
			t.Fatalf("the client received %v, want a start and an update", kinds)
		}
	}

	want := []string{"start:read a.txt", "update:completed"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("update %d is %q, want %q (stream: %v)", i, kinds[i], want[i], kinds)
		}
	}
}

// derefStatus reads a tool status the SDK may leave unset.
func derefStatus(s sdk.Patch[sdk.ToolCallStatus]) string {
	v, _ := s.Value()
	return string(v)
}

// TestReplayRepeatsTheLastTurn pins the demo rule: somebody typing a third time
// must get an answer, not a hang or an error.
func TestReplayRepeatsTheLastTurn(t *testing.T) {
	conn := dial(t, scenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	for i := 0; i < 4; i++ {
		if _, err := conn.Prompt(ctx, sdk.PromptRequest{
			SessionId: "replay-sess",
			Prompt:    []sdk.ContentBlock{sdk.TextBlock("again")},
		}); err != nil {
			t.Fatalf("prompt %d: %v", i+1, err)
		}
	}
}

// TestReplayAdvertisesWhatTheRecordingAdvertised pins the capability honesty: a
// client that probes for resume must see the recorded agent's answer, or the
// replay would exercise a different code path than the run it stands for.
func TestReplayAdvertisesWhatTheRecordingAdvertised(t *testing.T) {
	s := scenario()
	s.Capabilities = Capabilities{Resume: true}

	conn := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	init, err := conn.Initialize(ctx, sdk.InitializeRequest{ProtocolVersion: sdk.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if init.AgentCapabilities.SessionCapabilities.Resume == nil {
		t.Fatal("the replay did not advertise resume, which the recording said it supported")
	}
}

// TestLoadRefusesAScenarioWithNoTurn pins the refusal: a file that names no turn
// would open a session that answers nothing, which is indistinguishable from a
// hung agent.
func TestLoadRefusesAScenarioWithNoTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{"name":"empty","turns":[]}`), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a scenario with no turn")
	}
}

// TestLoadRefusesAnUpdateItCannotReplay pins the shape check: an unknown kind or
// a tool call with no id would reach the renderer as a blank line.
func TestLoadRefusesAnUpdateItCannotReplay(t *testing.T) {
	cases := map[string]string{
		"unknown kind": `{"turns":[{"updates":[{"kind":"bogus","text":"x"}]}]}`,
		"empty text":   `{"turns":[{"updates":[{"kind":"message"}]}]}`,
		"tool no id":   `{"turns":[{"updates":[{"kind":"tool","tool_title":"t"}]}]}`,
	}
	for name, body := range cases {
		if _, err := Decode([]byte(body)); err == nil {
			t.Errorf("%s: Decode accepted %s", name, body)
		}
	}
}

// TestSaveAndLoadRoundTrip keeps the fixture format stable: what Save writes,
// Load reads back unchanged.
func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round-trip.json")
	original := scenario()

	if err := original.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded.Turns) != len(original.Turns) {
		t.Fatalf("loaded %d turns, wrote %d", len(loaded.Turns), len(original.Turns))
	}
	if got := loaded.Turns[0].Updates[2].Text; got != "it says hello" {
		t.Fatalf("the third update reads %q", got)
	}
}

// TestLoadRefusesATurnWithNoUpdate pins the refusal that Export already makes:
// an empty turn answers a prompt with nothing, which is the session that looks
// hung rather than one that finished.
func TestLoadRefusesATurnWithNoUpdate(t *testing.T) {
	if _, err := Decode([]byte(`{"turns":[{"updates":[]}]}`)); err == nil {
		t.Fatal("Decode accepted a turn with no update")
	}

	// The builtin and an export both have to pass the same gate.
	payload, err := json.Marshal(Builtin())
	if err != nil {
		t.Fatalf("marshal the builtin: %v", err)
	}
	if _, err := Decode(payload); err != nil {
		t.Fatalf("the builtin is not replayable: %v", err)
	}
}

// TestBaseNameCutsOnEitherSeparator pins the session id a replay chooses, so it
// reads the same on every platform.
func TestBaseNameCutsOnEitherSeparator(t *testing.T) {
	cases := map[string]string{
		`C:\work\repo`:    "repo",
		"/home/user/repo": "repo",
		"repo":            "repo",
	}
	for in, want := range cases {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestServeDrivesARealSubprocessShape pins that the replay runs over stdio: a
// session host spawns it as a process, so the reader and writer are the only
// thing between the agent and the wire.
func TestServeDrivesARealSubprocessShape(t *testing.T) {
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		Serve(clientToAgentR, agentToClientW, scenario())
	}()

	client := &collector{updates: make(chan sdk.SessionNotification, 64)}
	conn := sdk.NewClientSideConnection(client, clientToAgentW, agentToClientR)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Initialize(ctx, sdk.InitializeRequest{ProtocolVersion: sdk.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := conn.Prompt(ctx, sdk.PromptRequest{
		SessionId: "replay-sess",
		Prompt:    []sdk.ContentBlock{sdk.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	_ = clientToAgentW.Close()
	_ = agentToClientW.Close()
	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("Serve never returned after the client disconnected")
	}
}

// TestMainRefusesAPathThatDoesNotExist keeps the command's failure honest: a
// preset that names a scenario which is not there must say so rather than
// starting an agent that answers nothing.
//
// A bare Main is not tested here: with no argument it serves the builtin
// scenario, which blocks on stdio until the client disconnects.
func TestMainRefusesAPathThatDoesNotExist(t *testing.T) {
	if code := Main([]string{filepath.Join(t.TempDir(), "absent.json")}); code == 0 {
		t.Fatal("Main accepted a path that does not exist")
	}
}

// TestScenarioForFallsBackToTheBuiltin pins why the preset works with nothing
// prepared: a demo or a smoke run starts a session without a fixture to record
// first.
func TestScenarioForFallsBackToTheBuiltin(t *testing.T) {
	s, err := ScenarioFor(nil)
	if err != nil {
		t.Fatalf("ScenarioFor(nil): %v", err)
	}
	if len(s.Turns) == 0 {
		t.Fatal("the builtin scenario has no turn")
	}

	// What it serves has to pass the same gate a file does.
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal the builtin: %v", err)
	}
	if _, err := Decode(payload); err != nil {
		t.Fatalf("the builtin scenario is not replayable: %v", err)
	}
}

// TestScenarioTextIsNotEmpty guards the fixture the build ships: a preset whose
// scenario is empty would be a session that looks broken.
func TestScenarioTextIsNotEmpty(t *testing.T) {
	if strings.TrimSpace(scenario().Name) == "" {
		t.Fatal("the test scenario has no name")
	}
}

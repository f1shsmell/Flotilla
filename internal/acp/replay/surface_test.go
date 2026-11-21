package replay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/session"
)

// dialAgent wires the agent to a client that keeps every notification, so a
// test can assert what the agent announced, not only what a call returned.
func dialAgent(t *testing.T, s Scenario) (*Agent, *sdk.ClientSideConnection, *collector) {
	t.Helper()

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
	return agent, conn, client
}

// dialUpdates is dialAgent for tests that only read the wire.
func dialUpdates(t *testing.T, s Scenario) (*sdk.ClientSideConnection, *collector) {
	t.Helper()
	_, conn, client := dialAgent(t, s)
	return conn, client
}

// surfaceScenario records an agent that let the client adjust its permission
// mode, its model and one toggle: the three shapes the composer bar renders.
func surfaceScenario() Scenario {
	s := scenario()
	s.Capabilities.Resume = true
	s.Surface = []session.Control{
		{
			ID: "mode", Name: "Permission mode", Kind: session.ControlMode, Current: "default",
			Choices: []session.Choice{
				{Value: "default", Name: "Ask first"},
				{Value: "acceptEdits", Name: "Accept edits"},
			},
		},
		{
			ID: "model", Name: "Model", Kind: session.ControlSelect, Current: "sonnet",
			Choices: []session.Choice{
				{Value: "sonnet", Name: "Sonnet"},
				{Value: "opus", Name: "Opus"},
			},
		},
		{ID: "fast", Name: "Fast answers", Kind: session.ControlBoolean, Current: "false"},
	}
	return s
}

func TestReplayAnnouncesTheRecordedSurface(t *testing.T) {
	conn := dial(t, surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: "/w", McpServers: []sdk.McpServer{}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if len(resp.ConfigOptions) != 3 {
		t.Fatalf("announced %d options, want the three recorded controls", len(resp.ConfigOptions))
	}

	byID := map[string]sdk.SessionConfigOption{}
	for _, option := range resp.ConfigOptions {
		switch {
		case option.Select != nil:
			byID[string(option.Select.Id)] = option
		case option.Boolean != nil:
			byID[string(option.Boolean.Id)] = option
		}
	}

	mode := byID["mode"].Select
	if mode == nil || mode.Category == nil || *mode.Category != sdk.SessionConfigOptionCategoryMode {
		t.Fatalf("mode option = %+v, want a mode-category select", byID["mode"])
	}
	if string(mode.CurrentValue) != "default" {
		t.Fatalf("mode current = %q, want default", mode.CurrentValue)
	}
	model := byID["model"].Select
	if model == nil || string(model.CurrentValue) != "sonnet" {
		t.Fatalf("model option = %+v, want current sonnet", byID["model"])
	}
	if model.Options.Ungrouped == nil || len(*model.Options.Ungrouped) != 2 {
		t.Fatalf("model choices = %+v, want the two recorded values", model.Options)
	}
	fast := byID["fast"].Boolean
	if fast == nil || fast.CurrentValue {
		t.Fatalf("fast option = %+v, want a boolean starting false", byID["fast"])
	}
}

func TestReplaySetMovesTheSurfaceAndAnnouncesIt(t *testing.T) {
	conn, client := dialUpdates(t, surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	open, err := conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: "/w", McpServers: []sdk.McpServer{}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	resp, err := conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		ValueId: &sdk.SetSessionConfigOptionValueId{
			SessionId: open.SessionId,
			ConfigId:  "model",
			Value:     "opus",
		},
	})
	if err != nil {
		t.Fatalf("set model: %v", err)
	}

	// The notification precedes the response, so the next one on the wire is the
	// moved surface rather than a stale list.
	select {
	case n := <-client.updates:
		if n.Update.ConfigOptionUpdate == nil {
			t.Fatalf("notification = %+v, want a config option update", n.Update)
		}
		var current string
		for _, option := range n.Update.ConfigOptionUpdate.ConfigOptions {
			if option.Select != nil && option.Select.Id == "model" {
				current = string(option.Select.CurrentValue)
			}
		}
		if current != "opus" {
			t.Fatalf("announced model current = %q, want opus", current)
		}
	case <-ctx.Done():
		t.Fatal("the set never announced the moved surface")
	}

	var returned string
	for _, option := range resp.ConfigOptions {
		if option.Select != nil && option.Select.Id == "model" {
			returned = string(option.Select.CurrentValue)
		}
	}
	if returned != "opus" {
		t.Fatalf("set response model current = %q, want opus", returned)
	}
}

func TestReplaySetBooleanMovesTheToggle(t *testing.T) {
	conn, client := dialUpdates(t, surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	open, err := conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: "/w", McpServers: []sdk.McpServer{}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		Boolean: &sdk.SetSessionConfigOptionBoolean{
			SessionId: open.SessionId,
			ConfigId:  "fast",
			Value:     true,
		},
	}); err != nil {
		t.Fatalf("set fast: %v", err)
	}

	select {
	case n := <-client.updates:
		if n.Update.ConfigOptionUpdate == nil {
			t.Fatalf("notification = %+v, want a config option update", n.Update)
		}
		var found bool
		for _, option := range n.Update.ConfigOptionUpdate.ConfigOptions {
			if option.Boolean != nil && option.Boolean.Id == "fast" {
				found = true
				if !option.Boolean.CurrentValue {
					t.Fatal("announced fast still false after setting true")
				}
			}
		}
		if !found {
			t.Fatal("the announcement dropped the fast toggle entirely")
		}
	case <-ctx.Done():
		t.Fatal("the boolean set never announced the moved surface")
	}
}

func TestReplayRefusesAValueTheControlDoesNotOffer(t *testing.T) {
	conn := dial(t, surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	open, err := conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: "/w", McpServers: []sdk.McpServer{}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		ValueId: &sdk.SetSessionConfigOptionValueId{
			SessionId: open.SessionId,
			ConfigId:  "model",
			Value:     "bogus",
		},
	}); err == nil {
		t.Fatal("a value outside the recorded choices was accepted")
	}
}

func TestReplayResumeCarriesTheSurface(t *testing.T) {
	conn := dial(t, surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	open, err := conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: "/w", McpServers: []sdk.McpServer{}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		ValueId: &sdk.SetSessionConfigOptionValueId{
			SessionId: open.SessionId,
			ConfigId:  "model",
			Value:     "opus",
		},
	}); err != nil {
		t.Fatalf("set model: %v", err)
	}
	resumed, err := conn.ResumeSession(ctx, sdk.ResumeSessionRequest{SessionId: open.SessionId, Cwd: "/w"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(resumed.ConfigOptions) != 3 {
		t.Fatalf("resume announced %d options, want the surface a new session gets", len(resumed.ConfigOptions))
	}
	// A resume after a set must carry the moved value, not the recording's
	// original: the client projects what the agent now offers.
	for _, option := range resumed.ConfigOptions {
		if option.Select != nil && option.Select.Id == "model" && string(option.Select.CurrentValue) != "opus" {
			t.Fatalf("resumed model current = %q, want the opus the set moved to", option.Select.CurrentValue)
		}
	}
}

func TestDecodeRefusesABadSurface(t *testing.T) {
	cases := map[string]string{
		"unknown kind":        `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"dial","current":"1"}]}`,
		"select no choices":   `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"select","current":"1"}]}`,
		"current not offered": `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"select","current":"2","choices":[{"value":"1","name":"One"}]}]}`,
		"boolean not a bool":  `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"boolean","current":"yes"}]}`,
		"no name":             `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","kind":"boolean","current":"true"}]}`,
		"duplicate control":   `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"boolean","current":"true"},{"id":"a","name":"B","kind":"boolean","current":"false"}]}`,
		"duplicate choice":    `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"select","current":"1","choices":[{"value":"1","name":"One"},{"value":"1","name":"Uno"}]}]}`,
		"empty choice value":  `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"select","current":"","choices":[{"value":"","name":"One"}]}]}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(payload)); !errors.Is(err, ErrInvalidScenario) {
				t.Fatalf("Decode(%s) = %v, want ErrInvalidScenario", name, err)
			}
		})
	}
}

// TestSetRejectionsCarryIdentity pins the sentinel a caller classifies by: a
// refused set must say what kind of refusal it was, not only prose.
func TestSetRejectionsCarryIdentity(t *testing.T) {
	agent, _, _ := dialAgent(t, surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	cases := []sdk.SetSessionConfigOptionRequest{
		{ValueId: &sdk.SetSessionConfigOptionValueId{SessionId: "s", ConfigId: "nope", Value: "x"}},
		{ValueId: &sdk.SetSessionConfigOptionValueId{SessionId: "s", ConfigId: "model", Value: "bogus"}},
		{ValueId: &sdk.SetSessionConfigOptionValueId{SessionId: "s", ConfigId: "fast", Value: "yes"}},
		{},
	}
	for i, req := range cases {
		if _, err := agent.SetSessionConfigOption(ctx, req); !errors.Is(err, ErrInvalidSetting) {
			t.Fatalf("case %d: err = %v, want ErrInvalidSetting", i, err)
		}
	}
}

// TestWriteBeforeTheConnectionIsRefused pins the guard for the window between
// the SDK starting its receive loop and handing the connection over.
func TestWriteBeforeTheConnectionIsRefused(t *testing.T) {
	agent := NewAgent(surfaceScenario())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := agent.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{
		ValueId: &sdk.SetSessionConfigOptionValueId{SessionId: "s", ConfigId: "model", Value: "opus"},
	}); !errors.Is(err, errNoConnection) {
		t.Fatalf("set before connection: err = %v, want errNoConnection", err)
	}
	if _, err := agent.Prompt(ctx, sdk.PromptRequest{SessionId: "s"}); !errors.Is(err, errNoConnection) {
		t.Fatalf("prompt before connection: err = %v, want errNoConnection", err)
	}

	// A refused prompt must not spend a turn: the first prompt after the
	// connection lands still opens with turn one's thought.
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	agentConn := sdk.NewAgentSideConnection(agent, agentToClientW, clientToAgentR)
	agent.SetAgentConnection(agentConn)
	client := &collector{updates: make(chan sdk.SessionNotification, 64)}
	conn := sdk.NewClientSideConnection(client, clientToAgentW, agentToClientR)
	t.Cleanup(func() {
		_ = clientToAgentW.Close()
		_ = agentToClientW.Close()
	})
	if _, err := conn.Prompt(ctx, sdk.PromptRequest{
		SessionId: "s",
		Prompt:    []sdk.ContentBlock{sdk.TextBlock("hi")},
	}); err != nil {
		t.Fatalf("prompt after the connection: %v", err)
	}
	select {
	case n := <-client.updates:
		if n.Update.AgentThoughtChunk == nil || n.Update.AgentThoughtChunk.Content.Text.Text != "reading the file" {
			t.Fatalf("first update = %+v, want turn one's thought: the refusal spent a turn", n.Update)
		}
	case <-ctx.Done():
		t.Fatal("the prompt after the connection never streamed")
	}
}

// TestDecodeRefusesMixedGrouping pins the wire's oneOf: the ACP select options
// carry either a flat list or grouped sections, never both, so a recording that
// mixes them would lose half its choices in the announcement — including,
// possibly, the value the control reports as current.
func TestDecodeRefusesMixedGrouping(t *testing.T) {
	payload := `{"turns":[{"updates":[{"kind":"message","text":"x"}]}],"surface":[{"id":"a","name":"A","kind":"select","current":"1","choices":[{"value":"1","name":"One"},{"value":"2","name":"Two","group":"G"}]}]}`
	if _, err := Decode([]byte(payload)); !errors.Is(err, ErrInvalidScenario) {
		t.Fatalf("Decode(mixed grouping) = %v, want ErrInvalidScenario", err)
	}
}

// TestReplayAnnouncesGroupedChoicesAsGroups pins the grouped half of the oneOf:
// sections reach the client with their labels, so the renderer rebuilds them.
func TestReplayAnnouncesGroupedChoicesAsGroups(t *testing.T) {
	s := surfaceScenario()
	s.Surface[1].Current = "opus"
	s.Surface[1].Choices = []session.Choice{
		{Value: "opus", Name: "Opus", Group: "Large"},
		{Value: "haiku", Name: "Haiku", Group: "Large"},
	}
	conn := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	resp, err := conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: "/w", McpServers: []sdk.McpServer{}})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	for _, option := range resp.ConfigOptions {
		if option.Select == nil || option.Select.Id != "model" {
			continue
		}
		if option.Select.Options.Grouped == nil || len(*option.Select.Options.Grouped) != 1 {
			t.Fatalf("grouped choices = %+v, want the one recorded group", option.Select.Options.Grouped)
		}
		if option.Select.Options.Ungrouped != nil {
			t.Fatalf("ungrouped choices = %+v, want none beside a grouped list", option.Select.Options.Ungrouped)
		}
		return
	}
	t.Fatal("the announcement carried no model select")
}

// TestBuiltinOffersAComposerBar pins the zero-config demo: a preset session
// shows the same composer bar a live agent shows, so the controls are
// discoverable without preparing a fixture first.
func TestBuiltinOffersAComposerBar(t *testing.T) {
	s := Builtin()
	kinds := map[string]bool{}
	for _, control := range s.Surface {
		kinds[control.Kind] = true
	}
	for _, kind := range []string{session.ControlMode, session.ControlSelect, session.ControlBoolean} {
		if !kinds[kind] {
			t.Fatalf("the builtin surface has no %s control (surface: %+v)", kind, s.Surface)
		}
	}
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal the builtin: %v", err)
	}
	if _, err := Decode(payload); err != nil {
		t.Fatalf("the builtin surface is not replayable: %v", err)
	}
}

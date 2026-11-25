package host

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// dialSurface connects a client that collects surface notifications and agent
// updates separately: a control change must arrive as a surface event, never as
// a line in the transcript.
func dialSurface(t *testing.T, dir string) (*ipc.Conn, <-chan session.SurfaceResponse, <-chan session.UpdateMessage) {
	t.Helper()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-acp")
	if err != nil {
		t.Fatalf("the ACP host never published a record: %v", err)
	}

	surfaces := make(chan session.SurfaceResponse, 8)
	updates := make(chan session.UpdateMessage, 64)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		Handler: func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
			switch method {
			case session.MethodSurfaceUpdate:
				var resp session.SurfaceResponse
				if err := json.Unmarshal(params, &resp); err == nil {
					surfaces <- resp
				}
			case session.MethodUpdate:
				var msg session.UpdateMessage
				if err := json.Unmarshal(params, &msg); err == nil {
					updates <- msg
				}
			default:
				return nil, ipc.ErrNoHandler
			}
			return struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatalf("dial the ACP host: %v", err)
	}
	t.Cleanup(conn.Close)

	return conn, surfaces, updates
}

// fetchSurface asks the host what the agent currently exposes.
func fetchSurface(t *testing.T, conn *ipc.Conn) []session.Control {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodSurface, struct{}{})
	if err != nil {
		t.Fatalf("ask for the surface: %v", err)
	}
	var resp session.SurfaceResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the surface: %v", err)
	}
	return resp.Controls
}

// controlByID finds one control, failing the test when it is absent.
func controlByID(t *testing.T, controls []session.Control, id string) session.Control {
	t.Helper()

	for _, control := range controls {
		if control.ID == id {
			return control
		}
	}
	t.Fatalf("the surface has no control %q: %+v", id, controls)
	return session.Control{}
}

// setSurface writes one control value through the host.
func setSurface(t *testing.T, conn *ipc.Conn, id, value string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := conn.Call(ctx, session.MethodSurfaceSet, session.SurfaceSetRequest{ID: id, Value: value}); err != nil {
		t.Fatalf("set %s to %s: %v", id, value, err)
	}
}

// TestSurfaceListsWhatTheAgentAnnounced pins the read side: the modes and
// config options the agent announced at session open become controls a client
// can render, with the mode first and the options in the agent's own order.
func TestSurfaceListsWhatTheAgentAnnounced(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)
	conn, _, _ := dialSurface(t, dir)

	controls := fetchSurface(t, conn)
	if len(controls) != 4 {
		t.Fatalf("the fake announces a mode and three options, got %+v", controls)
	}

	mode := controls[0]
	if mode.ID != "mode" || mode.Kind != session.ControlMode || mode.Current != "code" {
		t.Fatalf("the first control = %+v, want the current mode", mode)
	}
	if len(mode.Choices) != 2 || mode.Choices[0].Value != "plan" || mode.Choices[1].Value != "code" {
		t.Fatalf("mode choices = %+v, want plan and code", mode.Choices)
	}

	model := controlByID(t, controls, "model")
	if model.Kind != session.ControlSelect || model.Current != "fake-small" || len(model.Choices) != 2 {
		t.Fatalf("model control = %+v, want a select on fake-small with two choices", model)
	}
	controlByID(t, controls, "thought_level")

	toggle := controlByID(t, controls, "auto_approve")
	if toggle.Kind != session.ControlBoolean || toggle.Current != "true" || len(toggle.Choices) != 0 {
		t.Fatalf("boolean control = %+v, want kind boolean current true and no choices", toggle)
	}
}

// TestSettingABooleanComesBack pins the boolean write path end to end: the flip
// reaches the agent and returns as a surface event carrying "false".
func TestSettingABooleanComesBack(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)
	conn, surfaces, _ := dialSurface(t, dir)

	setSurface(t, conn, "auto_approve", "false")

	select {
	case resp := <-surfaces:
		if toggle := controlByID(t, resp.Controls, "auto_approve"); toggle.Current != "false" {
			t.Fatalf("the surface event reports auto_approve %q, want false", toggle.Current)
		}
	case <-time.After(waitFor):
		t.Fatal("no surface event arrived after the toggle was set")
	}

	if toggle := controlByID(t, fetchSurface(t, conn), "auto_approve"); toggle.Current != "false" {
		t.Fatalf("a fresh read reports auto_approve %q, want false", toggle.Current)
	}
}

// TestSurfaceSetRefusesABadBooleanValue pins the value boundary: a boolean
// takes "true" or "false", and anything else is a bad request, not a guess.
func TestSurfaceSetRefusesABadBooleanValue(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)
	conn, _, _ := dialSurface(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	_, err := conn.Call(ctx, session.MethodSurfaceSet, session.SurfaceSetRequest{ID: "auto_approve", Value: "yes"})
	if err == nil {
		t.Fatal("a boolean accepted a value that is neither true nor false")
	}
	if code := ipc.CodeOf(err); code != ipc.CodeBadRequest {
		t.Fatalf("refusal code = %d, want CodeBadRequest (%d)", code, ipc.CodeBadRequest)
	}
}

// TestSettingAModeComesBackAsASurfaceEvent pins the write side end to end: the
// value reaches the agent, the agent's own notification updates the host, and
// the client hears about it as a surface event — not as a transcript line.
func TestSettingAModeComesBackAsASurfaceEvent(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)
	conn, surfaces, updates := dialSurface(t, dir)

	setSurface(t, conn, "mode", "plan")

	select {
	case resp := <-surfaces:
		mode := controlByID(t, resp.Controls, "mode")
		if mode.Current != "plan" {
			t.Fatalf("the surface event reports mode %q, want plan", mode.Current)
		}
	case <-time.After(waitFor):
		t.Fatal("no surface event arrived after the mode was set")
	}

	if mode := controlByID(t, fetchSurface(t, conn), "mode"); mode.Current != "plan" {
		t.Fatalf("a fresh read reports mode %q, want plan", mode.Current)
	}

	select {
	case msg := <-updates:
		t.Fatalf("a control change landed in the transcript: %+v", msg)
	default:
	}
}

// TestSettingASelectOptionComesBack pins the same path for a config option,
// which travels a different ACP call than a mode.
func TestSettingASelectOptionComesBack(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)
	conn, surfaces, _ := dialSurface(t, dir)

	setSurface(t, conn, "model", "fake-large")

	select {
	case resp := <-surfaces:
		if model := controlByID(t, resp.Controls, "model"); model.Current != "fake-large" {
			t.Fatalf("the surface event reports model %q, want fake-large", model.Current)
		}
	case <-time.After(waitFor):
		t.Fatal("no surface event arrived after the option was set")
	}

	if model := controlByID(t, fetchSurface(t, conn), "model"); model.Current != "fake-large" {
		t.Fatalf("a fresh read reports model %q, want fake-large", model.Current)
	}
}

// TestAResponseOnlySetMovesTheSurface pins the agent that reports a change in
// the set answer alone: opencode sends no notification, so the host must fold
// the returned list itself or every later choice is made against a stale one.
func TestAResponseOnlySetMovesTheSurface(t *testing.T) {
	_, dir := startACPHostEnv(t, acp.Auto, 0, []string{"FLT_FAKE_SILENT_CONFIG=1"})
	conn, surfaces, _ := dialSurface(t, dir)

	setSurface(t, conn, "model", "fake-large")

	select {
	case resp := <-surfaces:
		if model := controlByID(t, resp.Controls, "model"); model.Current != "fake-large" {
			t.Fatalf("the surface event reports model %q, want fake-large", model.Current)
		}
	case <-time.After(waitFor):
		t.Fatal("no surface event arrived after a response-only set")
	}

	if model := controlByID(t, fetchSurface(t, conn), "model"); model.Current != "fake-large" {
		t.Fatalf("a fresh read reports model %q, want fake-large", model.Current)
	}
}

// TestSurfaceSetRefusesAnUnknownControl pins the boundary: a value aimed at a
// control the agent never announced is a bad request, not a silent no-op.
func TestSurfaceSetRefusesAnUnknownControl(t *testing.T) {
	_, dir := startACPHost(t, acp.Auto)
	conn, _, _ := dialSurface(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	_, err := conn.Call(ctx, session.MethodSurfaceSet, session.SurfaceSetRequest{ID: "nope", Value: "x"})
	if err == nil {
		t.Fatal("an unknown control was accepted")
	}
	if code := ipc.CodeOf(err); code != ipc.CodeBadRequest {
		t.Fatalf("refusal code = %d, want CodeBadRequest (%d)", code, ipc.CodeBadRequest)
	}
}

// TestPTYHostHasNoSurface pins the mode boundary: a terminal agent announces
// nothing structured, so the call is unknown rather than an empty list that
// would render as "this agent has no settings".
func TestPTYHostHasNoSurface(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-pty-surface",
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	go func() { _ = h.Serve(ctx) }()

	rec, err := waitForRecord(t, session.NewRegistry(dir), "sess-pty-surface")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}
	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{Token: "shared", Role: "test"})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	_, err = conn.Call(ctx, session.MethodSurface, struct{}{})
	if err == nil {
		t.Fatal("a PTY session answered a surface call")
	}
	if code := ipc.CodeOf(err); code != ipc.CodeUnknownMethod {
		t.Fatalf("refusal code = %d, want CodeUnknownMethod (%d)", code, ipc.CodeUnknownMethod)
	}
}

// TestProjectControlsPrefersAModeCategoryOption pins the precedence rule: an
// agent that exposes its mode as a config option has one mode control, not two,
// and it is the option — the newer API — that speaks for the mode.
func TestProjectControlsPrefersAModeCategoryOption(t *testing.T) {
	category := sdk.SessionConfigOptionCategoryMode
	modes := &sdk.SessionModeState{
		AvailableModes: []sdk.SessionMode{{Id: "old", Name: "Old"}},
		CurrentModeId:  "old",
	}
	options := []sdk.SessionConfigOption{
		{Select: &sdk.SessionConfigOptionSelect{
			Id: "mode", Name: "Mode", Type: "select", Category: &category,
			CurrentValue: "new",
			Options: sdk.SessionConfigSelectOptions{
				Ungrouped: &sdk.SessionConfigSelectOptionsUngrouped{{Value: "new", Name: "New"}},
			},
		}},
	}

	controls := projectControls(modes, options)
	if len(controls) != 1 {
		t.Fatalf("a mode option and a mode state produced %d controls, want 1: %+v", len(controls), controls)
	}
	if controls[0].Kind != session.ControlMode || controls[0].Current != "new" {
		t.Fatalf("the mode control = %+v, want the config option's value", controls[0])
	}
}

// TestProjectControlsFlattensGroupsAndBooleans pins the two shapes a renderer
// meets beyond a flat select: grouped choices keep their group label, and a
// boolean becomes a control whose current value is "true" or "false".
func TestProjectControlsFlattensGroupsAndBooleans(t *testing.T) {
	groups := sdk.SessionConfigSelectOptionsGrouped{
		{Group: "fast", Name: "Fast", Options: []sdk.SessionConfigSelectOption{{Value: "a", Name: "A"}}},
		{Group: "slow", Name: "Slow", Options: []sdk.SessionConfigSelectOption{{Value: "b", Name: "B"}}},
	}
	options := []sdk.SessionConfigOption{
		{Select: &sdk.SessionConfigOptionSelect{
			Id: "grouped", Name: "Grouped", Type: "select",
			CurrentValue: "a",
			Options:      sdk.SessionConfigSelectOptions{Grouped: &groups},
		}},
		{Boolean: &sdk.SessionConfigOptionBoolean{
			Id: "toggle", Name: "Toggle", Type: "boolean", CurrentValue: true,
		}},
	}

	controls := projectControls(nil, options)
	if len(controls) != 2 {
		t.Fatalf("want two controls, got %+v", controls)
	}

	grouped := controls[0]
	if len(grouped.Choices) != 2 || grouped.Choices[0].Group != "Fast" || grouped.Choices[1].Group != "Slow" {
		t.Fatalf("grouped choices = %+v, want the group labels kept", grouped.Choices)
	}

	toggle := controls[1]
	if toggle.Kind != session.ControlBoolean || toggle.Current != "true" {
		t.Fatalf("boolean control = %+v, want kind boolean current true", toggle)
	}
}

// TestProjectControlsEmptyAgent pins the cautious default: an agent that
// announced nothing has no controls, and the renderer shows no selectors.
func TestProjectControlsEmptyAgent(t *testing.T) {
	if controls := projectControls(nil, nil); len(controls) != 0 {
		t.Fatalf("an agent that announced nothing produced %+v", controls)
	}
}

// TestProjectControlsKeepsOneModeID pins the collision rule: an option already
// named "mode" owns that ID, so the legacy mode state must not prepend a second
// control with it — two controls sharing an ID break rendering and writes.
func TestProjectControlsKeepsOneModeID(t *testing.T) {
	modes := &sdk.SessionModeState{
		AvailableModes: []sdk.SessionMode{{Id: "old", Name: "Old"}},
		CurrentModeId:  "old",
	}
	options := []sdk.SessionConfigOption{
		{Select: &sdk.SessionConfigOptionSelect{
			Id: "mode", Name: "Mode", Type: "select",
			CurrentValue: "a",
			Options: sdk.SessionConfigSelectOptions{
				Ungrouped: &sdk.SessionConfigSelectOptionsUngrouped{{Value: "a", Name: "A"}},
			},
		}},
	}

	controls := projectControls(modes, options)
	if len(controls) != 1 {
		t.Fatalf("an option named mode and a mode state produced %d controls, want 1: %+v", len(controls), controls)
	}
	if controls[0].Kind != session.ControlSelect || controls[0].Current != "a" {
		t.Fatalf("the mode-named control = %+v, want the config option", controls[0])
	}
}

// TestLegacyModeRoutesOnlyWhenOffered pins the write side of the same rule: the
// legacy set_mode path is open only when the projection actually shows a legacy
// mode control, so a write can never aim at a control the client cannot see.
func TestLegacyModeRoutesOnlyWhenOffered(t *testing.T) {
	category := sdk.SessionConfigOptionCategoryMode
	modes := &sdk.SessionModeState{CurrentModeId: "code"}
	modeOption := sdk.SessionConfigOption{Select: &sdk.SessionConfigOptionSelect{
		Id: "agent_mode", Category: &category,
	}}
	namedOption := sdk.SessionConfigOption{Select: &sdk.SessionConfigOptionSelect{Id: "mode"}}
	namedToggle := sdk.SessionConfigOption{Boolean: &sdk.SessionConfigOptionBoolean{Id: "mode"}}

	cases := []struct {
		name    string
		modes   *sdk.SessionModeState
		options []sdk.SessionConfigOption
		want    bool
	}{
		{"no mode state", nil, nil, false},
		{"mode state alone", modes, nil, true},
		{"unrelated options", modes, []sdk.SessionConfigOption{{Select: &sdk.SessionConfigOptionSelect{Id: "model"}}}, true},
		{"mode-category option", modes, []sdk.SessionConfigOption{modeOption}, false},
		{"select named mode", modes, []sdk.SessionConfigOption{namedOption}, false},
		{"boolean named mode", modes, []sdk.SessionConfigOption{namedToggle}, false},
	}
	for _, tc := range cases {
		if got := legacyModeOffered(tc.modes, tc.options); got != tc.want {
			t.Errorf("%s: legacyModeOffered = %t, want %t", tc.name, got, tc.want)
		}
	}
}

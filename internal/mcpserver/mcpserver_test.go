package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/session"
)

// fakeFleet is the daemon as the tools see it: every method records what the
// tool asked for, so the test checks the request as well as the answer.
type fakeFleet struct {
	mu      sync.Mutex
	status  daemon.Status
	agents  agent.Listing
	started []daemon.StartRequest
	prompts []string
	stopped []session.ID
	answer  daemon.PromptResult
}

func (f *fakeFleet) Status(context.Context) (daemon.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeFleet) Agents(context.Context) (agent.Listing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agents, nil
}

func (f *fakeFleet) StartSession(_ context.Context, req daemon.StartRequest) (daemon.StartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, req)
	dir := req.Dir
	if req.Worktree != nil {
		dir = "/worktrees/" + req.Worktree.Branch
	}
	return daemon.StartResponse{ID: req.ID, HostPID: 4242, Dir: dir}, nil
}

func (f *fakeFleet) StopSession(_ context.Context, id session.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fakeFleet) PromptSession(_ context.Context, id session.ID, text string) (daemon.PromptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, string(id)+": "+text)
	return f.answer, nil
}

// scopedStatus is one fleet with a worker chain and an unrelated session.
func scopedStatus() daemon.Status {
	return daemon.Status{
		Channel: "test",
		Ready:   true,
		Sessions: []daemon.StatusEntry{
			{ID: "root", State: session.StateWorking, Command: "self"},
			{ID: "child", Parent: "root", State: session.StateIdle, Command: "worker", Dir: "/w"},
			{ID: "grandchild", Parent: "child", State: session.StateDone, Command: "worker"},
			{ID: "other", State: session.StateIdle, Command: "not-mine"},
		},
	}
}

func agentListing() agent.Listing {
	return agent.Listing{Agents: []agent.Entry{
		{
			Preset: agent.Preset{
				ID:      "claude-agent-acp",
				Command: "npx",
				Args:    []string{"@zed-industries/claude-agent-acp"},
				Mode:    session.ModeACP,
			},
			Installed: true,
			Enabled:   true,
		},
		{Preset: agent.Preset{ID: "missing", Command: "nope"}, Installed: false, Enabled: true},
		{Preset: agent.Preset{ID: "off", Command: "off"}, Installed: true, Enabled: false},
	}}
}

// connectTools serves fleet over an in-memory transport and returns a client
// holding a session with it.
func connectTools(t *testing.T, fleet Fleet, opts Options) *mcp.ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = New(fleet, opts).Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect to the tool server: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// result is one tool call's answer.
type result struct {
	res  *mcp.CallToolResult
	text string
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) result {
	t.Helper()

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	out := result{res: res}
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			out.text += text.Text
		}
	}
	return out
}

func (r result) decode(t *testing.T, out any) {
	t.Helper()
	if r.res.IsError {
		t.Fatalf("tool failed: %s", r.text)
	}
	if err := json.Unmarshal([]byte(r.text), out); err != nil {
		t.Fatalf("decode tool output %q: %v", r.text, err)
	}
}

func (r result) failure(t *testing.T, want string) {
	t.Helper()
	if !r.res.IsError {
		t.Fatalf("tool succeeded, want an error: %s", r.text)
	}
	if want != "" && !strings.Contains(r.text, want) {
		t.Fatalf("error = %q, want it to mention %q", r.text, want)
	}
}

// TestStatusIsScopedToTheSession is the visibility gate: an agent's own tool
// shows its session and its workers, never the rest of the machine.
func TestStatusIsScopedToTheSession(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus()}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	var out statusOutput
	call(t, cs, "fleet_status", nil).decode(t, &out)

	if out.Scope != "root" {
		t.Errorf("scope = %q, want root", out.Scope)
	}
	got := make(map[string]sessionView, len(out.Sessions))
	for _, s := range out.Sessions {
		got[s.ID] = s
	}
	for _, want := range []string{"root", "child", "grandchild"} {
		if _, ok := got[want]; !ok {
			t.Errorf("status is missing %q: %+v", want, out.Sessions)
		}
	}
	if _, ok := got["other"]; ok {
		t.Errorf("status shows an unrelated session: %+v", out.Sessions)
	}
	if !got["root"].Self {
		t.Errorf("the serving session is not marked: %+v", got["root"])
	}
	if got["child"].Parent != "root" {
		t.Errorf("worker parent = %q, want root", got["child"].Parent)
	}
}

// TestUnscopedStatusServesTheFleet keeps `flt mcp serve` useful for a person:
// with no FLT_SESSION_ID there is no scope to enforce.
func TestUnscopedStatusServesTheFleet(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus(), answer: daemon.PromptResult{StopReason: "end_turn", Answer: "ok"}}
	cs := connectTools(t, fleet, Options{})

	var out statusOutput
	call(t, cs, "fleet_status", nil).decode(t, &out)
	if len(out.Sessions) != 4 {
		t.Errorf("unscoped status = %+v, want the whole fleet", out.Sessions)
	}

	var prompt promptOutput
	call(t, cs, "session_prompt", map[string]any{"id": "other", "text": "hi"}).decode(t, &prompt)
	if prompt.Answer != "ok" {
		t.Errorf("prompt answer = %+v", prompt)
	}
}

// TestStartResolvesTheAgentAndNamesTheParent pins what session_start sends: the
// catalogue's own command, arguments and mode, the worktree the caller asked
// for, and the parent lineage the fleet view walks.
func TestStartResolvesTheAgentAndNamesTheParent(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus(), agents: agentListing()}
	cs := connectTools(t, fleet, Options{SessionID: "root", Policy: "auto"})

	var out startOutput
	call(t, cs, "session_start", map[string]any{
		"agent":  "claude-agent-acp",
		"id":     "worker-1",
		"repo":   "C:/repo",
		"policy": "auto",
	}).decode(t, &out)

	if out.ID != "worker-1" || out.HostPID != 4242 || out.Dir != "/worktrees/worker/worker-1" {
		t.Errorf("start output = %+v", out)
	}

	if len(fleet.started) != 1 {
		t.Fatalf("started %d sessions, want one", len(fleet.started))
	}
	req := fleet.started[0]
	if req.Parent != "root" {
		t.Errorf("start parent = %q, want root", req.Parent)
	}
	if req.Mode != session.ModeACP || req.Command != "npx" {
		t.Errorf("start did not resolve the agent: %+v", req)
	}
	if len(req.Args) != 1 || req.Args[0] != "@zed-industries/claude-agent-acp" {
		t.Errorf("start args = %q", req.Args)
	}
	if req.Policy != "auto" {
		t.Errorf("start policy = %q, want auto", req.Policy)
	}
	if req.Worktree == nil || req.Worktree.Repo != "C:/repo" || req.Worktree.Branch != "worker/worker-1" {
		t.Errorf("start worktree = %+v", req.Worktree)
	}
}

// TestStartRefusesWhatTheMachineCannotRun names the reasons instead of starting
// a session that would fail later.
func TestStartRefusesWhatTheMachineCannotRun(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus(), agents: agentListing()}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	call(t, cs, "session_start", map[string]any{"agent": "nobody"}).failure(t, "unknown agent")
	call(t, cs, "session_start", map[string]any{"agent": "missing"}).failure(t, "not installed")
	call(t, cs, "session_start", map[string]any{"agent": "off"}).failure(t, "disabled")
	call(t, cs, "session_start", map[string]any{"agent": "claude-agent-acp", "dir": "/x", "repo": "C:/r"}).
		failure(t, "two different places")
	call(t, cs, "session_start", map[string]any{"agent": "claude-agent-acp"}).
		failure(t, "where the worker runs")

	if len(fleet.started) != 0 {
		t.Errorf("refused starts still reached the daemon: %+v", fleet.started)
	}
}

// TestStartDoesNotOutgrowTheCallersPolicy is the permission gate: a worker may
// not run with more autonomy than the session whose tools started it.
func TestStartDoesNotOutgrowTheCallersPolicy(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus(), agents: agentListing()}
	cs := connectTools(t, fleet, Options{SessionID: "root", Policy: "supervised"})

	call(t, cs, "session_start", map[string]any{
		"agent": "claude-agent-acp", "repo": "C:/repo", "policy": "full-access",
	}).failure(t, "may not run with policy")
	call(t, cs, "session_start", map[string]any{
		"agent": "claude-agent-acp", "repo": "C:/repo", "policy": "auto",
	}).failure(t, "may not run with policy")

	if len(fleet.started) != 0 {
		t.Fatalf("a wider worker reached the daemon: %+v", fleet.started)
	}

	var out startOutput
	call(t, cs, "session_start", map[string]any{
		"agent": "claude-agent-acp", "repo": "C:/repo", "policy": "supervised",
	}).decode(t, &out)
	if len(fleet.started) != 1 || fleet.started[0].Policy != "supervised" {
		t.Errorf("the equal policy did not reach the daemon: %+v", fleet.started)
	}
}

// TestUnscopedStartMaySetAnyPolicy keeps the cap where it belongs: a person's
// own server is not running inside a permission gate to escape.
func TestUnscopedStartMaySetAnyPolicy(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus(), agents: agentListing()}
	cs := connectTools(t, fleet, Options{})

	var out startOutput
	call(t, cs, "session_start", map[string]any{
		"agent": "claude-agent-acp", "dir": "/w", "policy": "full-access",
	}).decode(t, &out)
	if len(fleet.started) != 1 || fleet.started[0].Policy != "full-access" {
		t.Errorf("unscoped start = %+v", fleet.started)
	}
}

// TestStartRefusesAnAmbiguousCommand keeps a shared launcher from silently
// picking the first agent that happens to run it.
func TestStartRefusesAnAmbiguousCommand(t *testing.T) {
	listing := agent.Listing{Agents: []agent.Entry{
		{Preset: agent.Preset{ID: "one", Command: "npx"}, Installed: true, Enabled: true},
		{Preset: agent.Preset{ID: "two", Command: "npx"}, Installed: true, Enabled: true},
	}}
	fleet := &fakeFleet{status: scopedStatus(), agents: listing}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	call(t, cs, "session_start", map[string]any{"agent": "npx", "repo": "C:/repo"}).
		failure(t, "use the id")
	if len(fleet.started) != 0 {
		t.Errorf("an ambiguous command still started a session: %+v", fleet.started)
	}
}

// TestToolsRefuseTheSessionItself pins the self guard: a session cannot prompt
// or stop the turn these very tools are running inside.
func TestToolsRefuseTheSessionItself(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus()}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	call(t, cs, "session_prompt", map[string]any{"id": "root", "text": "hi"}).failure(t, "cannot act on itself")
	call(t, cs, "session_stop", map[string]any{"id": "root"}).failure(t, "cannot act on itself")

	if len(fleet.prompts) != 0 || len(fleet.stopped) != 0 {
		t.Errorf("a self call reached the daemon: prompts=%v stopped=%v", fleet.prompts, fleet.stopped)
	}
}

// TestToolsRefuseAnotherFleet keeps the scope a boundary rather than a display
// filter: an id outside the subtree never reaches the daemon.
func TestToolsRefuseAnotherFleet(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus()}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	call(t, cs, "session_prompt", map[string]any{"id": "other", "text": "hi"}).failure(t, "not this session's worker")
	call(t, cs, "session_stop", map[string]any{"id": "other"}).failure(t, "not this session's worker")

	if len(fleet.prompts) != 0 || len(fleet.stopped) != 0 {
		t.Errorf("an out-of-scope call reached the daemon: prompts=%v stopped=%v", fleet.prompts, fleet.stopped)
	}
}

// TestPromptRunsAWorkerTurn is the working case, which also proves a
// grandchild is reachable: the walk is transitive, not one level.
func TestPromptRunsAWorkerTurn(t *testing.T) {
	fleet := &fakeFleet{
		status: scopedStatus(),
		answer: daemon.PromptResult{
			StopReason:      "end_turn",
			Answer:          "the worker's answer",
			AnswerTruncated: true,
		},
	}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	var out promptOutput
	call(t, cs, "session_prompt", map[string]any{"id": "grandchild", "text": "do the thing"}).decode(t, &out)

	if out.StopReason != "end_turn" || out.Answer != "the worker's answer" || !out.AnswerTruncated {
		t.Errorf("prompt output = %+v", out)
	}
	if len(fleet.prompts) != 1 || fleet.prompts[0] != "grandchild: do the thing" {
		t.Errorf("prompt reached the daemon as %q", fleet.prompts)
	}
}

// TestPromptRefusesABlockedWorker keeps the tool from answering a card that
// belongs to a person.
func TestPromptRefusesABlockedWorker(t *testing.T) {
	st := scopedStatus()
	st.Sessions[1].State = session.StateBlocked
	fleet := &fakeFleet{status: st}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	call(t, cs, "session_prompt", map[string]any{"id": "child", "text": "hi"}).failure(t, "permission question")
	if len(fleet.prompts) != 0 {
		t.Errorf("a blocked worker was prompted: %v", fleet.prompts)
	}
}

// TestStopEndsAWorker is the stop path.
func TestStopEndsAWorker(t *testing.T) {
	fleet := &fakeFleet{status: scopedStatus()}
	cs := connectTools(t, fleet, Options{SessionID: "root"})

	var out stopOutput
	call(t, cs, "session_stop", map[string]any{"id": "child"}).decode(t, &out)
	if out.ID != "child" {
		t.Errorf("stop output = %+v", out)
	}
	if len(fleet.stopped) != 1 || fleet.stopped[0] != "child" {
		t.Errorf("stop reached the daemon as %v", fleet.stopped)
	}
}

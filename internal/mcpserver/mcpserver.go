// Package mcpserver exposes one session's part of the fleet as MCP tools, so an
// agent can start workers, message them and stop them instead of being only
// managed itself.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/version"
)

// Fleet is what the tools ask of the daemon. The CLI passes a Dialer; tests
// pass a fake.
type Fleet interface {
	Status(ctx context.Context) (daemon.Status, error)
	Agents(ctx context.Context) (agent.Listing, error)
	StartSession(ctx context.Context, req daemon.StartRequest) (daemon.StartResponse, error)
	StopSession(ctx context.Context, id session.ID) error
	PromptSession(ctx context.Context, id session.ID, text string) (daemon.PromptResult, error)
}

// Options says whose fleet the server acts for.
type Options struct {
	// SessionID is FLT_SESSION_ID; empty serves the whole fleet, as a person's
	// own `flt mcp serve` does.
	SessionID session.ID
	// Policy is FLT_POLICY: the autonomy the serving session was given.
	Policy string
}

// New builds the tool server. Serve runs it.
func New(fleet Fleet, opts Options) *mcp.Server {
	tools := &tools{fleet: fleet, opts: opts}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "flotilla",
		Version: version.String(),
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "fleet_status",
		Description: "List the sessions this one may act on: itself and the workers it started, " +
			"with their state, command and directory.",
	}, tools.status)

	mcp.AddTool(server, &mcp.Tool{
		Name: "session_start",
		Description: "Start a worker session running an agent this machine has. Name repo to run it in a " +
			"fresh worktree of that repository, or dir to run it in place. The worker becomes visible to this session.",
	}, tools.start)

	mcp.AddTool(server, &mcp.Tool{
		Name: "session_prompt",
		Description: "Send one message to a worker this session started and wait for its turn to end. " +
			"Returns the agent's answer text and why the turn stopped. The worker cannot ask permission " +
			"questions: unattended tool calls are refused unless the worker was started with policy auto.",
	}, tools.prompt)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_stop",
		Description: "Stop a worker this session started. Its agent process ends and the session leaves the fleet.",
	}, tools.stop)

	return server
}

// Serve runs the tools over this process's stdio until the peer disconnects.
// Stdout belongs to the protocol, so nothing else may be printed there.
func Serve(ctx context.Context, fleet Fleet, opts Options) error {
	if err := New(fleet, opts).Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("serve the fleet tools: %w", err)
	}
	return nil
}

type tools struct {
	fleet Fleet
	opts  Options
}

type statusInput struct{}

// sessionView is one session as a tool caller reads it. It is narrower than the
// daemon's own report on purpose: an agent needs to know what exists and where
// it stands, not the host pids and diagnostics of the whole machine.
type sessionView struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
	Parent  string `json:"parent,omitempty"`
	// Self marks the session the tools are acting for.
	Self bool `json:"self,omitempty"`
}

type statusOutput struct {
	// Scope is the session the view is limited to; empty means the whole fleet.
	Scope    string        `json:"scope,omitempty"`
	Sessions []sessionView `json:"sessions"`
}

func (t *tools) status(ctx context.Context, _ *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, statusOutput, error) {
	st, err := t.fleet.Status(ctx)
	if err != nil {
		return nil, statusOutput{}, fmt.Errorf("read the fleet status: %w", err)
	}
	if t.opts.SessionID != "" {
		st = daemon.Scoped(st, t.opts.SessionID)
	}

	out := statusOutput{Scope: string(t.opts.SessionID), Sessions: make([]sessionView, 0, len(st.Sessions))}
	for _, s := range st.Sessions {
		out.Sessions = append(out.Sessions, sessionView{
			ID:      string(s.ID),
			State:   string(s.State),
			Command: s.Command,
			Dir:     s.Dir,
			Parent:  string(s.Parent),
			Self:    s.ID == t.opts.SessionID,
		})
	}
	return nil, out, nil
}

type startInput struct {
	Agent  string `json:"agent" jsonschema:"agent id or command as listed by the fleet, e.g. claude-agent-acp"`
	ID     string `json:"id,omitempty" jsonschema:"session id for the worker; empty picks one"`
	Dir    string `json:"dir,omitempty" jsonschema:"directory to run the worker in; use repo instead to create a worktree"`
	Repo   string `json:"repo,omitempty" jsonschema:"repository whose fresh worktree the worker should run in"`
	Branch string `json:"branch,omitempty" jsonschema:"branch to create for the worktree; empty names it after the session"`
	Base   string `json:"base,omitempty" jsonschema:"commit-ish to branch from; empty means HEAD"`
	Policy string `json:"policy,omitempty" jsonschema:"permission policy: supervised (default), accept-edits, auto or full-access; a worker may not be given more than this session has"`
}

type startOutput struct {
	ID       string   `json:"id"`
	HostPID  int      `json:"host_pid"`
	Dir      string   `json:"dir,omitempty"`
	Included []string `json:"worktree_included,omitempty"`
	Skipped  []string `json:"worktree_skipped,omitempty"`
}

func (t *tools) start(ctx context.Context, _ *mcp.CallToolRequest, in startInput) (*mcp.CallToolResult, startOutput, error) {
	if strings.TrimSpace(in.Agent) == "" {
		return nil, startOutput{}, errors.New("name an agent: the fleet lists this machine's agents")
	}
	if in.Repo != "" && in.Dir != "" {
		return nil, startOutput{}, errors.New("dir and repo name two different places; pass one")
	}
	if err := t.checkPolicy(in.Policy); err != nil {
		return nil, startOutput{}, err
	}

	list, err := t.fleet.Agents(ctx)
	if err != nil {
		return nil, startOutput{}, fmt.Errorf("list the agents this machine has: %w", err)
	}
	entry, err := pickAgent(list, in.Agent)
	if err != nil {
		return nil, startOutput{}, err
	}
	if in.Dir == "" && in.Repo == "" {
		return nil, startOutput{}, errors.New("name where the worker runs: repo for a fresh worktree or dir to run in place")
	}

	id := session.ID(strings.TrimSpace(in.ID))
	if id == "" {
		if id, err = newSessionID(); err != nil {
			return nil, startOutput{}, err
		}
	}

	mode := entry.Mode
	if mode == "" {
		mode = session.ModeACP
	}
	req := daemon.StartRequest{
		ID:      id,
		Mode:    mode,
		Command: entry.Command,
		Args:    entry.Args,
		Dir:     in.Dir,
		Policy:  in.Policy,
		Parent:  t.opts.SessionID,
	}
	if in.Repo != "" {
		branch := in.Branch
		if branch == "" {
			branch = "worker/" + string(id)
		}
		req.Worktree = &daemon.WorktreeRequest{Repo: in.Repo, Branch: branch, Base: in.Base}
	}

	resp, err := t.fleet.StartSession(ctx, req)
	if err != nil {
		return nil, startOutput{}, fmt.Errorf("start the worker: %w", err)
	}
	return nil, startOutput{
		ID:       string(resp.ID),
		HostPID:  resp.HostPID,
		Dir:      resp.Dir,
		Included: resp.Included,
		Skipped:  resp.Skipped,
	}, nil
}

type promptInput struct {
	ID   string `json:"id" jsonschema:"session id of a worker this session started"`
	Text string `json:"text" jsonschema:"the message to send"`
}

type promptOutput struct {
	ID         string `json:"id"`
	StopReason string `json:"stop_reason"`
	Answer     string `json:"answer,omitempty"`
	// AnswerTruncated says the answer was cut to fit one frame.
	AnswerTruncated bool `json:"answer_truncated,omitempty"`
	// Declined is how many tool-permission questions were refused because no
	// person was attached to answer them.
	Declined int `json:"declined,omitempty"`
}

func (t *tools) prompt(ctx context.Context, _ *mcp.CallToolRequest, in promptInput) (*mcp.CallToolResult, promptOutput, error) {
	id := session.ID(strings.TrimSpace(in.ID))
	if id == "" {
		return nil, promptOutput{}, errors.New("name the worker to prompt")
	}
	if in.Text == "" {
		return nil, promptOutput{}, errors.New("the message is empty")
	}
	if err := t.mayAct(ctx, id); err != nil {
		return nil, promptOutput{}, err
	}
	if err := t.readyToPrompt(ctx, id); err != nil {
		return nil, promptOutput{}, err
	}

	res, err := t.fleet.PromptSession(ctx, id, in.Text)
	if err != nil {
		return nil, promptOutput{}, fmt.Errorf("prompt the worker: %w", err)
	}
	return nil, promptOutput{
		ID:              string(id),
		StopReason:      res.StopReason,
		Answer:          res.Answer,
		AnswerTruncated: res.AnswerTruncated,
		Declined:        res.Declined,
	}, nil
}

type stopInput struct {
	ID string `json:"id" jsonschema:"session id of a worker this session started"`
}

type stopOutput struct {
	ID string `json:"id"`
}

func (t *tools) stop(ctx context.Context, _ *mcp.CallToolRequest, in stopInput) (*mcp.CallToolResult, stopOutput, error) {
	id := session.ID(strings.TrimSpace(in.ID))
	if id == "" {
		return nil, stopOutput{}, errors.New("name the worker to stop")
	}
	if err := t.mayAct(ctx, id); err != nil {
		return nil, stopOutput{}, err
	}
	if err := t.fleet.StopSession(ctx, id); err != nil {
		return nil, stopOutput{}, fmt.Errorf("stop the worker: %w", err)
	}
	return nil, stopOutput{ID: string(id)}, nil
}

// mayAct refuses an id outside this session's part of the fleet, and the
// session itself: a running turn cannot be prompted or stopped by its own
// tools.
func (t *tools) mayAct(ctx context.Context, id session.ID) error {
	if t.opts.SessionID == "" {
		return nil
	}
	if id == t.opts.SessionID {
		return fmt.Errorf("session %q cannot act on itself: it is the session these tools serve", id)
	}

	st, err := t.fleet.Status(ctx)
	if err != nil {
		return fmt.Errorf("check this session's workers: %w", err)
	}
	if !daemon.Subtree(st, t.opts.SessionID)[id] {
		return fmt.Errorf("session %q is not this session's worker: the fleet shows only %q and the sessions it started",
			id, t.opts.SessionID)
	}
	return nil
}

// readyToPrompt refuses a worker that is already waiting on a person, and one
// that is still running a turn: a prompt would either answer someone else's
// card or collide with the running turn.
func (t *tools) readyToPrompt(ctx context.Context, id session.ID) error {
	st, err := t.fleet.Status(ctx)
	if err != nil {
		return fmt.Errorf("read the worker's state: %w", err)
	}
	for _, s := range st.Sessions {
		if s.ID != id {
			continue
		}
		switch s.State {
		case session.StateBlocked:
			return fmt.Errorf("session %q is waiting on a permission question; answer it from the window or stop the worker", id)
		case session.StateWorking:
			return fmt.Errorf("session %q is already running a turn; wait for it or stop the worker", id)
		}
		return nil
	}
	return fmt.Errorf("session %q is not in the fleet", id)
}

// policyRank orders the permission policies by how much they allow without
// asking. A missing or unrecognised name ranks lowest: it cannot be shown to
// stay inside the caller's own.
var policyRank = map[string]int{
	"supervised":   0,
	"accept-edits": 1,
	"auto":         2,
	"full-access":  3,
}

// checkPolicy refuses a worker more autonomy than the session that starts it.
// The tools run inside that session's permission gate, so a wider worker would
// be a way around it. A person running the server by hand is not capped.
func (t *tools) checkPolicy(want string) error {
	if t.opts.SessionID == "" || policyRank[want] <= policyRank[t.opts.Policy] {
		return nil
	}
	mine := t.opts.Policy
	if mine == "" {
		mine = "supervised"
	}
	return fmt.Errorf("a worker may not run with policy %q: this session runs with %q", want, mine)
}

// pickAgent resolves a name the caller read from the fleet: an agent id, or the
// command it runs when only one agent runs it.
func pickAgent(list agent.Listing, name string) (agent.Entry, error) {
	want := strings.TrimSpace(name)
	var byCommand []agent.Entry
	for _, e := range list.Agents {
		if e.ID == want {
			return runnable(e, want)
		}
		if e.Command == want {
			byCommand = append(byCommand, e)
		}
	}

	switch len(byCommand) {
	case 1:
		return runnable(byCommand[0], want)
	case 0:
		return agent.Entry{}, fmt.Errorf("unknown agent %q: the fleet lists this machine's agents", want)
	default:
		ids := make([]string, len(byCommand))
		for i, e := range byCommand {
			ids[i] = e.ID
		}
		return agent.Entry{}, fmt.Errorf("agent command %q names %d agents (%s); use the id",
			want, len(byCommand), strings.Join(ids, ", "))
	}
}

// runnable refuses an agent the machine cannot actually start.
func runnable(e agent.Entry, name string) (agent.Entry, error) {
	if !e.Installed {
		return agent.Entry{}, fmt.Errorf("agent %q is not installed on this machine", name)
	}
	if !e.Enabled {
		return agent.Entry{}, fmt.Errorf("agent %q is disabled in settings", name)
	}
	return e, nil
}

// newSessionID names a worker without asking the caller to. The random part
// keeps two workers of the same agent apart, which is what a person starting a
// second one by hand would otherwise have to invent.
func newSessionID() (session.ID, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("choose a session id: %w", err)
	}
	return session.ID("worker-" + hex.EncodeToString(raw[:])), nil
}

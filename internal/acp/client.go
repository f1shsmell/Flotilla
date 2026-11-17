// Package acp drives an Agent Client Protocol agent over stdio.
//
// The GUI never talks to this package directly: a session host owns the agent
// process and the ACP connection, then relays both to the daemon.
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/coder/acp-go-sdk"
)

// defaultMaxReadBytes bounds one file reply when the caller names no limit. A
// reply rides one transport frame, so a larger file is refused before it is
// loaded rather than after it fails to send.
const defaultMaxReadBytes = 1 << 20

// Policy decides what an agent may do without asking. It answers the same
// question for every permission request, so a session's autonomy is one setting
// rather than a set of special cases.
type Policy int

const (
	// Supervised asks about every tool call that needs permission.
	Supervised Policy = iota
	// AcceptEdits allows reads and edits and asks about everything else.
	AcceptEdits
	// Auto allows every tool call the agent does not mark as risky.
	Auto
	// FullAccess allows everything, including paths outside the workspace.
	FullAccess
)

// String renders the policy for logs and the wire.
func (p Policy) String() string {
	switch p {
	case Supervised:
		return "supervised"
	case AcceptEdits:
		return "accept-edits"
	case Auto:
		return "auto"
	case FullAccess:
		return "full-access"
	default:
		return "unknown"
	}
}

// Update is one event from the agent, flattened for the daemon and the GUI. The
// raw payload is kept so a later renderer is not limited to what this build
// already understood.
type Update struct {
	// Kind is "message", "thought", "tool", "plan" or "other".
	Kind string
	// Text is the message or thought text, empty for other kinds.
	Text string
	// ToolCallID, ToolTitle and ToolStatus describe a tool event.
	ToolCallID string
	ToolTitle  string
	ToolStatus string
	// Raw is the SDK's own encoding of the update.
	Raw json.RawMessage
}

// Option is one choice a permission request offers.
type Option struct {
	// ID is the value an answer returns.
	ID string
	// Kind is the ACP kind, such as "allow_once" or "reject_always".
	Kind string
}

// Granted reports whether this option lets the tool call proceed.
func (o Option) Granted() bool { return strings.HasPrefix(o.Kind, "allow") }

// PermissionRequest is one question the agent asked before acting.
type PermissionRequest struct {
	// Title names the tool call in the user's terms.
	Title string
	// Kind is the tool kind, such as "edit" or "read".
	Kind string
	// Options are the choices the agent offered, in its own order.
	Options []Option
}

// PermissionAnswer is the client's reply to a permission request.
type PermissionAnswer struct {
	// OptionID is one of the offered option ids; empty cancels the tool call.
	OptionID string
}

// Asker decides a request the policy leaves to the user. A nil Asker with a
// supervised policy refuses rather than guessing.
type Asker func(ctx context.Context, req PermissionRequest) (PermissionAnswer, error)

// Config is what a client needs to serve one session.
type Config struct {
	// Workspace is the directory the agent may read and write. Every file
	// request is checked against it, which is what keeps an agent inside its
	// worktree.
	Workspace string
	// Policy decides what is allowed without asking.
	Policy Policy
	// Asker answers requests the policy does not decide on its own.
	Asker Asker
	// MaxReadBytes bounds one file reply. Zero uses the default; the host sets
	// it from the transport's frame limit, which is the only place that knows
	// how much one reply can carry.
	MaxReadBytes int
	// OnUpdate receives every session update. It is called from the reader
	// goroutine, so it must not block.
	OnUpdate func(Update)
}

func (c Config) maxReadBytes() int {
	if c.MaxReadBytes > 0 {
		return c.MaxReadBytes
	}
	return defaultMaxReadBytes
}

// Client is the client side of an ACP conversation: it serves the agent's file
// and permission requests and streams its updates out.
type Client struct {
	cfg Config
}

var _ sdk.Client = (*Client)(nil)

// NewClient returns a client that keeps the agent inside cfg.Workspace. A
// caller that names none falls back the same way Open does, so the file
// boundary and the session's cwd are always the same directory.
func NewClient(cfg Config) *Client {
	cfg.Workspace = workspaceOf(cfg.Workspace)
	return &Client{cfg: cfg}
}

// ReadTextFile serves the agent's read requests inside the workspace.
func (c *Client) ReadTextFile(_ context.Context, params sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	path, err := c.resolve(params.Path)
	if err != nil {
		return sdk.ReadTextFileResponse{}, err
	}

	f, err := os.Open(path)
	if err != nil {
		return sdk.ReadTextFileResponse{}, fmt.Errorf("read %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	limit := c.cfg.maxReadBytes()
	content, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return sdk.ReadTextFileResponse{}, fmt.Errorf("read %q: %w", path, err)
	}
	if len(content) > limit {
		return sdk.ReadTextFileResponse{}, fmt.Errorf("read %q: over %d bytes: %w", path, limit, ErrFileTooLarge)
	}

	return sdk.ReadTextFileResponse{Content: sliceLines(string(content), params.Line, params.Limit)}, nil
}

// WriteTextFile serves the agent's write requests inside the workspace.
func (c *Client) WriteTextFile(_ context.Context, params sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	path, err := c.resolve(params.Path)
	if err != nil {
		return sdk.WriteTextFileResponse{}, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sdk.WriteTextFileResponse{}, fmt.Errorf("create the parent directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(params.Content), 0o644); err != nil {
		return sdk.WriteTextFileResponse{}, fmt.Errorf("write %q: %w", path, err)
	}
	return sdk.WriteTextFileResponse{}, nil
}

// RequestPermission applies the policy and asks only when the decision belongs
// to the user.
func (c *Client) RequestPermission(ctx context.Context, params sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	req := PermissionRequest{
		Title: deref(params.ToolCall.Title),
		Kind:  string(deref(params.ToolCall.Kind)),
	}
	for _, opt := range params.Options {
		req.Options = append(req.Options, Option{ID: string(opt.OptionId), Kind: string(opt.Kind)})
	}

	switch c.decide(req) {
	case decisionAllow:
		if grant, ok := firstGrant(req.Options); ok {
			return selected(grant), nil
		}
		return cancelled(), nil
	case decisionDeny:
		return cancelled(), nil
	}

	if c.cfg.Asker == nil {
		// A supervised session with nobody to ask must refuse: guessing yes is
		// the one answer that cannot be taken back.
		return cancelled(), nil
	}

	answer, err := c.cfg.Asker(ctx, req)
	if err != nil {
		return sdk.RequestPermissionResponse{}, fmt.Errorf("ask about %q: %w", req.Title, err)
	}
	if answer.OptionID == "" {
		return cancelled(), nil
	}
	return selected(answer.OptionID), nil
}

// SessionUpdate forwards one agent event to the configured sink.
func (c *Client) SessionUpdate(_ context.Context, params sdk.SessionNotification) error {
	if c.cfg.OnUpdate == nil {
		return nil
	}
	c.cfg.OnUpdate(flatten(params.Update))
	return nil
}

// CreateTerminal refuses terminal requests: this build gives an agent file
// access and nothing more, and saying so beats pretending to support it.
func (c *Client) CreateTerminal(context.Context, sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, ErrUnsupported
}

// KillTerminal refuses terminal requests.
func (c *Client) KillTerminal(context.Context, sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, ErrUnsupported
}

// TerminalOutput refuses terminal requests.
func (c *Client) TerminalOutput(context.Context, sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, ErrUnsupported
}

// ReleaseTerminal refuses terminal requests.
func (c *Client) ReleaseTerminal(context.Context, sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, ErrUnsupported
}

// WaitForTerminalExit refuses terminal requests.
func (c *Client) WaitForTerminalExit(context.Context, sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, ErrUnsupported
}

type decision int

const (
	decisionAsk decision = iota
	decisionAllow
	decisionDeny
)

// decide applies the policy to one request.
func (c *Client) decide(req PermissionRequest) decision {
	switch c.cfg.Policy {
	case FullAccess:
		return decisionAllow
	case Auto:
		return decisionAllow
	case AcceptEdits:
		if req.Kind == kindRead || req.Kind == kindEdit {
			return decisionAllow
		}
		return decisionAsk
	default:
		return decisionAsk
	}
}

// resolve turns an agent's path into one inside the workspace, refusing
// anything that escapes it.
func (c *Client) resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path: %w", ErrOutsideWorkspace)
	}

	root, err := filepath.Abs(c.cfg.Workspace)
	if err != nil {
		return "", fmt.Errorf("resolve the workspace: %w", err)
	}
	root = resolveExistingAncestor(root)

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = resolveExistingAncestor(filepath.Clean(candidate))

	if inside(root, candidate) || c.cfg.Policy == FullAccess {
		return candidate, nil
	}
	return "", fmt.Errorf("%q: %w", path, ErrOutsideWorkspace)
}

// resolveExistingAncestor resolves the deepest ancestor of path that exists and
// re-appends the rest. EvalSymlinks needs its argument to exist, and an agent
// routinely names a file it is about to create; skipping the resolution there
// would compare a short 8.3 name against a long one and report a false escape.
func resolveExistingAncestor(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	dir := filepath.Dir(path)
	if dir == path {
		// Reached a volume root that does not resolve; leave it as it is.
		return path
	}
	return filepath.Join(resolveExistingAncestor(dir), filepath.Base(path))
}

// inside reports whether path is root or below it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func sliceLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}

	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 0 {
		start = min(*line-1, len(lines))
	}
	end := len(lines)
	if limit != nil && *limit > 0 {
		end = min(start+*limit, len(lines))
	}
	return strings.Join(lines[start:end], "\n")
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// flatten converts one SDK update into the shape the daemon forwards.
func flatten(update sdk.SessionUpdate) Update {
	out := Update{Kind: kindOther}

	switch {
	case update.AgentMessageChunk != nil:
		out.Kind = kindMessage
		if update.AgentMessageChunk.Content.Text != nil {
			out.Text = update.AgentMessageChunk.Content.Text.Text
		}
	case update.AgentThoughtChunk != nil:
		out.Kind = kindThought
		if update.AgentThoughtChunk.Content.Text != nil {
			out.Text = update.AgentThoughtChunk.Content.Text.Text
		}
	case update.ToolCall != nil:
		out.Kind = kindTool
		out.ToolCallID = string(update.ToolCall.ToolCallId)
		out.ToolTitle = update.ToolCall.Title
		out.ToolStatus = string(update.ToolCall.Status)
	case update.ToolCallUpdate != nil:
		out.Kind = kindTool
		out.ToolCallID = string(update.ToolCallUpdate.ToolCallId)
		out.ToolTitle = deref(update.ToolCallUpdate.Title)
		out.ToolStatus = string(deref(update.ToolCallUpdate.Status))
	case update.Plan != nil:
		out.Kind = kindPlan
	}

	if raw, err := json.Marshal(update); err == nil {
		out.Raw = raw
	}
	return out
}

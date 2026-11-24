package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

func TestResolveKeepsFilesInsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Config{Workspace: root})

	// Root and candidate are both normalised, so the result may name the
	// workspace by its long form where the input used a short one. What matters
	// is that the file the agent asked for is still the one it gets.
	inner := filepath.Join(root, "src", "main.go")
	got, err := c.resolve(inner)
	if err != nil {
		t.Fatalf("resolve(%q): %v", inner, err)
	}
	if !inside(resolveExistingAncestor(root), got) {
		t.Fatalf("resolve(%q) = %q, outside the workspace", inner, got)
	}
	if filepath.Base(got) != "main.go" {
		t.Fatalf("resolve(%q) = %q, want the same file", inner, got)
	}

	relative, err := c.resolve("src/main.go")
	if err != nil {
		t.Fatalf("resolve a relative path: %v", err)
	}
	if !inside(resolveExistingAncestor(root), relative) {
		t.Fatalf("resolve turned a relative path into %q, outside %q", relative, root)
	}
}

func TestResolveRefusesAPathThatEscapesTheWorkspace(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Config{Workspace: root})

	for _, path := range []string{
		filepath.Join(root, "..", "outside.txt"),
		"../outside.txt",
		filepath.Join(root, "..", "..", "escape.txt"),
	} {
		if _, err := c.resolve(path); !errors.Is(err, ErrOutsideWorkspace) {
			t.Fatalf("resolve(%q) = %v, want ErrOutsideWorkspace", path, err)
		}
	}
}

func TestResolveAllowsOutsidePathsOnlyWithFullAccess(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "shared.txt")

	c := NewClient(Config{Workspace: root, Policy: FullAccess})
	if _, err := c.resolve(outside); err != nil {
		t.Fatalf("full access refused %q: %v", outside, err)
	}
}

// TestResolveAcceptsAFileThatDoesNotExistYet pins the CI failure this build
// shipped once: a workspace reached through a short 8.3 name resolves to its
// long name, so a candidate that skipped resolution looked like an escape.
func TestResolveAcceptsAFileThatDoesNotExistYet(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Config{Workspace: root})

	future := filepath.Join(root, "deep", "nested", "not-created.txt")
	got, err := c.resolve(future)
	if err != nil {
		t.Fatalf("resolve(%q): %v", future, err)
	}
	if filepath.Base(got) != "not-created.txt" {
		t.Fatalf("resolve kept %q as the base name, want the tail the agent asked for", filepath.Base(got))
	}
	if len(got) == 0 || !filepath.IsAbs(got) {
		t.Fatalf("resolve returned %q, want an absolute path", got)
	}
}

// TestResolveNormalisesBothSidesAlike covers the other half: however the
// workspace is written, a path inside it compares equal to it.
func TestResolveNormalisesBothSidesAlike(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Config{Workspace: root})

	got, err := c.resolve(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatalf("resolve inside the workspace: %v", err)
	}
	if !inside(resolveExistingAncestor(root), got) {
		t.Fatalf("resolved %q is not under the resolved workspace %q", got, resolveExistingAncestor(root))
	}
}

func TestReadTextFileSlicesLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	c := NewClient(Config{Workspace: root})
	resp, err := c.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{
		Path:  path,
		Line:  sdk.Ptr[int64](2),
		Limit: sdk.Ptr[int64](2),
	})
	if err != nil {
		t.Fatalf("ReadTextFile: %v", err)
	}
	if resp.Content != "two\nthree" {
		t.Fatalf("content = %q, want the second and third lines", resp.Content)
	}
}

// TestReadTextFileRefusesAFileBeyondOneFrame keeps the client from asking for
// content the transport can never carry back to the agent.
func TestReadTextFileRefusesAFileBeyondOneFrame(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", defaultMaxReadBytes+1)), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	c := NewClient(Config{Workspace: root})
	if _, err := c.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{Path: path}); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("ReadTextFile of an oversized file = %v, want ErrFileTooLarge", err)
	}
}

// TestReadTextFileHonoursTheCallersLimit pins the knob the host sets from the
// transport's frame limit, so the number lives where the wire is known.
func TestReadTextFileHonoursTheCallersLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	c := NewClient(Config{Workspace: root, MaxReadBytes: 8})
	if _, err := c.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{Path: path}); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("ReadTextFile past a 8-byte limit = %v, want ErrFileTooLarge", err)
	}

	c = NewClient(Config{Workspace: root, MaxReadBytes: len("0123456789abcdef")})
	resp, err := c.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{Path: path})
	if err != nil {
		t.Fatalf("ReadTextFile at the limit: %v", err)
	}
	if resp.Content != "0123456789abcdef" {
		t.Fatalf("content = %q, want the whole file", resp.Content)
	}
}

func TestWriteTextFileCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "deep", "nested", "file.txt")

	c := NewClient(Config{Workspace: root})
	if _, err := c.WriteTextFile(context.Background(), sdk.WriteTextFileRequest{
		Path:    path,
		Content: "hello",
	}); err != nil {
		t.Fatalf("WriteTextFile: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(written) != "hello" {
		t.Fatalf("wrote %q, want hello", written)
	}
}

// permissionParams builds the request the agent sends before a tool call.
func permissionParams(kind sdk.ToolKind) sdk.RequestPermissionRequest {
	return sdk.RequestPermissionRequest{
		SessionId: "sess",
		ToolCall: sdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Title:      sdk.PatchSet("do the thing"),
			Kind:       sdk.PatchSet(kind),
		},
		Options: []sdk.PermissionOption{
			{Kind: sdk.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
			{Kind: sdk.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"},
		},
	}
}

func TestSupervisedPolicyAsks(t *testing.T) {
	asked := 0
	c := NewClient(Config{
		Workspace: t.TempDir(),
		Policy:    Supervised,
		Asker: func(_ context.Context, req PermissionRequest) (PermissionAnswer, error) {
			asked++
			if req.Kind != string(sdk.ToolKindEdit) {
				t.Errorf("asker saw kind %q, want edit", req.Kind)
			}
			if len(req.Options) != 2 {
				t.Errorf("asker saw %d options, want 2", len(req.Options))
			}
			return PermissionAnswer{OptionID: "allow"}, nil
		},
	})

	resp, err := c.RequestPermission(context.Background(), permissionParams(sdk.ToolKindEdit))
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if asked != 1 {
		t.Fatalf("the asker was called %d times, want 1", asked)
	}
	if resp.Outcome.Selected == nil || string(resp.Outcome.Selected.OptionId) != "allow" {
		t.Fatalf("outcome = %+v, want the allow option", resp.Outcome)
	}
}

func TestSupervisedPolicyWithoutAnAskerRefuses(t *testing.T) {
	c := NewClient(Config{Workspace: t.TempDir(), Policy: Supervised})

	resp, err := c.RequestPermission(context.Background(), permissionParams(sdk.ToolKindEdit))
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("outcome = %+v, want a cancellation: guessing yes cannot be taken back", resp.Outcome)
	}
}

func TestAcceptEditsPolicyAllowsEditsAndAsksForTheRest(t *testing.T) {
	asked := 0
	c := NewClient(Config{
		Workspace: t.TempDir(),
		Policy:    AcceptEdits,
		Asker: func(context.Context, PermissionRequest) (PermissionAnswer, error) {
			asked++
			return PermissionAnswer{OptionID: "reject"}, nil
		},
	})

	edit, err := c.RequestPermission(context.Background(), permissionParams(sdk.ToolKindEdit))
	if err != nil {
		t.Fatalf("RequestPermission(edit): %v", err)
	}
	if edit.Outcome.Selected == nil || string(edit.Outcome.Selected.OptionId) != "allow" {
		t.Fatalf("an edit was not allowed under accept-edits: %+v", edit.Outcome)
	}
	if asked != 0 {
		t.Fatalf("the asker was called for an edit, which accept-edits decides")
	}

	if _, err := c.RequestPermission(context.Background(), permissionParams(sdk.ToolKindExecute)); err != nil {
		t.Fatalf("RequestPermission(execute): %v", err)
	}
	if asked != 1 {
		t.Fatalf("the asker was called %d times for an execute call, want 1", asked)
	}
}

func TestAutoPolicyDecidesWithoutAsking(t *testing.T) {
	c := NewClient(Config{
		Workspace: t.TempDir(),
		Policy:    Auto,
		Asker: func(context.Context, PermissionRequest) (PermissionAnswer, error) {
			t.Error("the auto policy asked the user")
			return PermissionAnswer{}, nil
		},
	})

	resp, err := c.RequestPermission(context.Background(), permissionParams(sdk.ToolKindExecute))
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if resp.Outcome.Selected == nil {
		t.Fatalf("auto did not allow the call: %+v", resp.Outcome)
	}
}

func TestCancelledAnswerBecomesACancellation(t *testing.T) {
	c := NewClient(Config{
		Workspace: t.TempDir(),
		Policy:    Supervised,
		Asker: func(context.Context, PermissionRequest) (PermissionAnswer, error) {
			return PermissionAnswer{}, nil
		},
	})

	resp, err := c.RequestPermission(context.Background(), permissionParams(sdk.ToolKindEdit))
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("outcome = %+v, want a cancellation", resp.Outcome)
	}
}

func TestTerminalRequestsAreRefused(t *testing.T) {
	c := NewClient(Config{Workspace: t.TempDir()})
	ctx := context.Background()

	if _, err := c.CreateTerminal(ctx, sdk.CreateTerminalRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CreateTerminal = %v, want ErrUnsupported", err)
	}
	if _, err := c.TerminalOutput(ctx, sdk.TerminalOutputRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("TerminalOutput = %v, want ErrUnsupported", err)
	}
}

// TestAPlanUpdateCarriesItsEntries pins the shape a plan has: its content lives
// in the entries, never in a text field, so a client that only carried Text
// showed an empty line and a recording built from it dropped the plan.
func TestAPlanUpdateCarriesItsEntries(t *testing.T) {
	var got []Update
	c := NewClient(Config{
		Workspace: t.TempDir(),
		OnUpdate:  func(u Update) { got = append(got, u) },
	})
	ctx := context.Background()

	plan := sdk.UpdatePlan(
		sdk.PlanEntry{Content: "read the relay", Status: sdk.PlanEntryStatusPending, Priority: sdk.PlanEntryPriorityHigh},
		sdk.PlanEntry{Content: "fix the fan-out", Status: sdk.PlanEntryStatusPending, Priority: sdk.PlanEntryPriorityMedium},
	)
	if err := c.SessionUpdate(ctx, sdk.SessionNotification{SessionId: "s", Update: plan}); err != nil {
		t.Fatalf("SessionUpdate: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("received %d updates, want the one plan", len(got))
	}
	if got[0].Kind != UpdatePlan {
		t.Fatalf("plan update = %+v, want kind plan", got[0])
	}
	want := "read the relay\nfix the fan-out"
	if got[0].Text != want {
		t.Fatalf("plan text = %q, want %q", got[0].Text, want)
	}
}

// TestAnEmptyPlanCarriesNoText keeps the store's rule: an agent that has not
// filled in a plan yet said nothing, and a blank line would claim otherwise.
func TestAnEmptyPlanCarriesNoText(t *testing.T) {
	var got []Update
	c := NewClient(Config{
		Workspace: t.TempDir(),
		OnUpdate:  func(u Update) { got = append(got, u) },
	})

	if err := c.SessionUpdate(context.Background(), sdk.SessionNotification{
		SessionId: "s",
		Update:    sdk.UpdatePlan(),
	}); err != nil {
		t.Fatalf("SessionUpdate: %v", err)
	}
	if len(got) != 1 || got[0].Text != "" {
		t.Fatalf("an empty plan = %+v, want kind plan and no text", got)
	}
}

func TestSessionUpdateIsFlattened(t *testing.T) {
	var got []Update
	c := NewClient(Config{
		Workspace: t.TempDir(),
		OnUpdate:  func(u Update) { got = append(got, u) },
	})
	ctx := context.Background()

	notifications := []sdk.SessionNotification{
		{SessionId: "s", Update: sdk.UpdateAgentMessageText("hello")},
		{SessionId: "s", Update: sdk.UpdateAgentThoughtText("hmm")},
		{SessionId: "s", Update: sdk.StartToolCall("call-9", "reading", sdk.WithStartStatus(sdk.ToolCallStatusPending))},
	}
	for _, n := range notifications {
		if err := c.SessionUpdate(ctx, n); err != nil {
			t.Fatalf("SessionUpdate: %v", err)
		}
	}

	if len(got) != 3 {
		t.Fatalf("received %d updates, want 3", len(got))
	}
	if got[0].Kind != UpdateMessage || got[0].Text != "hello" {
		t.Fatalf("message update = %+v, want the text hello", got[0])
	}
	if got[1].Kind != UpdateThought || got[1].Text != "hmm" {
		t.Fatalf("thought update = %+v, want the text hmm", got[1])
	}
	if got[2].Kind != UpdateTool || got[2].ToolCallID != "call-9" {
		t.Fatalf("tool update = %+v, want call-9", got[2])
	}
	for i, u := range got {
		if len(u.Raw) == 0 || !json.Valid(u.Raw) {
			t.Fatalf("update %d kept no usable raw payload", i)
		}
	}
}

// A child session sends ordinary updates under its own sessionId. The id is the
// only attribution this protocol version carries, so an update that reaches the
// host without it cannot be placed in the right child's history.
func TestSessionUpdateCarriesItsSessionId(t *testing.T) {
	var got []Update
	c := NewClient(Config{
		Workspace: t.TempDir(),
		OnUpdate:  func(u Update) { got = append(got, u) },
	})
	ctx := context.Background()

	notifications := []sdk.SessionNotification{
		{SessionId: "sess_parent", Update: sdk.UpdateAgentMessageText("from the parent")},
		{SessionId: "sess_child_1", Update: sdk.UpdateAgentMessageText("from the child")},
	}
	for _, n := range notifications {
		if err := c.SessionUpdate(ctx, n); err != nil {
			t.Fatalf("SessionUpdate: %v", err)
		}
	}

	if len(got) != 2 {
		t.Fatalf("received %d updates, want 2", len(got))
	}
	if got[0].SessionID != "sess_parent" {
		t.Fatalf("parent update session = %q, want sess_parent", got[0].SessionID)
	}
	if got[1].SessionID != "sess_child_1" {
		t.Fatalf("child update session = %q, want sess_child_1", got[1].SessionID)
	}
}

// TestASubagentUpdateIsNotReadAsASessionInfoUpdate is the guard the SDK asked
// for. A payload whose sessionUpdate no decoder of this build knows must not
// land in a variant that happens to share a member name: an agent announcing a
// child session would otherwise be recorded as renaming the parent, with no
// error anywhere.
func TestASubagentUpdateIsNotReadAsASessionInfoUpdate(t *testing.T) {
	payload := []byte(`{"sessionUpdate":"subagent_update","sessionId":"sess_child_1","title":"Test investigator","description":"Investigates."}`)

	var u sdk.SessionUpdate
	if err := json.Unmarshal(payload, &u); err != nil {
		t.Fatalf("decode a subagent update: %v", err)
	}
	if u.SessionInfoUpdate != nil {
		t.Fatalf("a subagent update was read as a session info update: %+v", u.SessionInfoUpdate)
	}
	if u.SubagentUpdate == nil {
		t.Fatalf("a subagent update was not read as one: %+v", u)
	}
	if string(u.SubagentUpdate.SessionId) != "sess_child_1" {
		t.Fatalf("subagent update session = %q, want sess_child_1", u.SubagentUpdate.SessionId)
	}
}

// TestFlattenReadsASubagentRegistration pins the projection of the unstable
// subagent update into the shape the host forwards: the child's id, the title
// and description it announced, and the work state that follows in later
// patches of the same child.
func TestFlattenReadsASubagentRegistration(t *testing.T) {
	payload := []byte(`{"sessionUpdate":"subagent_update","sessionId":"sess_child_1","title":"Test investigator","description":"Investigates.","state":{"state":"running"}}`)

	var u sdk.SessionUpdate
	if err := json.Unmarshal(payload, &u); err != nil {
		t.Fatalf("decode a subagent update: %v", err)
	}

	got := flatten(u, "sess_parent")
	if got.Kind != kindSubagent {
		t.Fatalf("kind = %q, want %q", got.Kind, kindSubagent)
	}
	if got.SessionID != "sess_parent" {
		t.Fatalf("session = %q, want the announcing session", got.SessionID)
	}
	if got.SubagentID != "sess_child_1" {
		t.Fatalf("child = %q, want sess_child_1", got.SubagentID)
	}
	if got.SubagentTitle != "Test investigator" || got.SubagentDescription != "Investigates." {
		t.Fatalf("registration reads %+v", got)
	}
	if got.SubagentState != "running" {
		t.Fatalf("state = %q, want running", got.SubagentState)
	}
}

// TestFlattenReadsAnIdlePatch keeps the state machine honest: a later update
// that only says the child went idle carries no title, and the flattening must
// not invent one.
func TestFlattenReadsAnIdlePatch(t *testing.T) {
	// The shape is what codex-acp 2.12 sends for a child that ended; the stop
	// reason is part of the idle state and is not a fifth state.
	payload := []byte(`{"sessionUpdate":"subagent_update","sessionId":"sess_child_1","state":{"state":"idle","stopReason":"end_turn"}}`)

	var u sdk.SessionUpdate
	if err := json.Unmarshal(payload, &u); err != nil {
		t.Fatalf("decode a subagent update: %v", err)
	}

	got := flatten(u, "sess_parent")
	if got.SubagentState != "idle" || got.SubagentTitle != "" {
		t.Fatalf("state patch reads %+v", got)
	}
}

func TestPolicyStrings(t *testing.T) {
	want := map[Policy]string{
		Supervised:   "supervised",
		AcceptEdits:  "accept-edits",
		Auto:         "auto",
		FullAccess:   "full-access",
		Policy(9999): "unknown",
	}
	for policy, expected := range want {
		if got := policy.String(); got != expected {
			t.Fatalf("Policy(%d).String() = %q, want %q", policy, got, expected)
		}
	}
}

func TestOptionGranted(t *testing.T) {
	if !(Option{Kind: string(sdk.PermissionOptionKindAllowOnce)}).Granted() {
		t.Fatal("an allow option did not count as a grant")
	}
	if (Option{Kind: string(sdk.PermissionOptionKindRejectOnce)}).Granted() {
		t.Fatal("a reject option counted as a grant")
	}
	if !strings.HasPrefix(string(sdk.PermissionOptionKindAllowAlways), "allow") {
		t.Fatal("the SDK's allow-always kind no longer starts with allow: Granted needs revisiting")
	}
}

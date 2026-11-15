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

	inner := filepath.Join(root, "src", "main.go")
	got, err := c.resolve(inner)
	if err != nil {
		t.Fatalf("resolve(%q): %v", inner, err)
	}
	if got != inner {
		t.Fatalf("resolve(%q) = %q, want it unchanged", inner, got)
	}

	relative, err := c.resolve("src/main.go")
	if err != nil {
		t.Fatalf("resolve a relative path: %v", err)
	}
	if !inside(root, relative) {
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

func TestReadTextFileSlicesLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	c := NewClient(Config{Workspace: root})
	resp, err := c.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{
		Path:  path,
		Line:  sdk.Ptr(2),
		Limit: sdk.Ptr(2),
	})
	if err != nil {
		t.Fatalf("ReadTextFile: %v", err)
	}
	if resp.Content != "two\nthree" {
		t.Fatalf("content = %q, want the second and third lines", resp.Content)
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
			Title:      sdk.Ptr("do the thing"),
			Kind:       sdk.Ptr(kind),
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

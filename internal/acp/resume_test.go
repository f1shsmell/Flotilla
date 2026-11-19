package acp

import (
	"context"
	"errors"
	"os"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

// TestOpenResumesASavedSession pins the reopen path: a launch naming a saved
// agent session id resumes that conversation instead of creating a new one,
// which is what lets a host survive the crash of its agent process.
func TestOpenResumesASavedSession(t *testing.T) {
	binary := fakeAgentBinary(t)
	workspace := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	first, err := Open(ctx, Launch{Command: binary, Dir: workspace}, Config{Policy: Auto})
	if err != nil {
		t.Fatalf("open the first session: %v", err)
	}
	saved := first.ID()
	if err := first.Close(); err != nil {
		t.Fatalf("close the first session: %v", err)
	}

	resumed, err := Open(ctx, Launch{Command: binary, Dir: workspace, ResumeID: string(saved)}, Config{Policy: Auto})
	if err != nil {
		t.Fatalf("resume the saved session: %v", err)
	}
	t.Cleanup(func() { _ = resumed.Close() })

	if resumed.ID() != saved {
		t.Fatalf("resumed session %q, want the saved %q", resumed.ID(), saved)
	}

	resp, err := resumed.Prompt(ctx, sdk.TextBlock("after the crash"))
	if err != nil {
		t.Fatalf("prompt the resumed session: %v", err)
	}
	if resp.StopReason != sdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
	}

	// The fake marks a resumed surface with a "resumed" option: the proof
	// that the conversation was reopened rather than created again.
	var marked bool
	for _, option := range resumed.ConfigOptions() {
		if option.Boolean != nil && option.Boolean.Id == "resumed" && option.Boolean.CurrentValue {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("the resumed surface carries no resume marker: %+v", resumed.ConfigOptions())
	}
	if resumed.Modes() == nil {
		t.Fatal("the resume answer's modes never reached the surface")
	}
}

// TestOpenResumeNeedsTheCapability pins the boundary: an agent that never
// advertised session/resume cannot be asked to reopen a saved conversation.
func TestOpenResumeNeedsTheCapability(t *testing.T) {
	binary := fakeAgentBinary(t)
	workspace := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	_, err := Open(ctx, Launch{
		Command:  binary,
		Dir:      workspace,
		Env:      append(os.Environ(), "FLT_FAKE_NO_RESUME=1"),
		ResumeID: "fake-saved",
	}, Config{Policy: Auto})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("resume without the capability = %v, want ErrUnsupported", err)
	}
}

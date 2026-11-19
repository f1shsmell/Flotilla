package probe_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
)

// handshakeTimeout bounds the one test that spawns a real process. It is not a
// condition: a probe that needs the clock to pass is a broken probe.
const handshakeTimeout = 30 * time.Second

// fakeAgentBinary builds the stand-in ACP agent. The name is deliberately not
// the shipped one: Resolve checks the binary this build carries beside itself
// first, and a test must prove the path it was given, not a coincidence of
// naming.
func fakeAgentBinary(t *testing.T) string {
	t.Helper()

	root := repoRoot(t)
	target := filepath.Join(t.TempDir(), "fake-agent-for-probe")
	if filepath.Separator == '\\' {
		target += ".exe"
	}

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, "./cmd/flt-fake-acp-agent")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the fake agent: %v: %s", err, output)
	}
	return target
}

// repoRoot walks up from the package directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// TestHandshakeReadsWhatTheAgentAnnounced is the reason the mode exists: a
// maintainer learns the modes and configuration options an agent offers without
// spending a token. The fake announces both, so reading nothing here can only
// mean the probe is broken.
func TestHandshakeReadsWhatTheAgentAnnounced(t *testing.T) {
	binary := fakeAgentBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	res, err := probe.Handshake(ctx, probe.Agent{Name: "fake", Command: binary}, t.TempDir())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	if res.SessionID == "" {
		t.Fatal("the handshake reports no session id, so the session was never opened")
	}

	// The zero-token promise, made checkable: a prompt that reached the agent
	// comes back as updates, and every ACP agent answers a turn with some.
	if res.Updates != 0 {
		t.Fatalf("the agent streamed %d updates during the handshake, so a turn ran", res.Updates)
	}

	if !res.Resume {
		t.Fatal("the fake advertises session/resume; the handshake did not report it")
	}

	if len(res.Modes) != 2 {
		t.Fatalf("read %d modes, want the 2 the fake announces: %+v", len(res.Modes), res.Modes)
	}
	ids := modeIDs(res)
	if ids != "plan,code" {
		t.Fatalf("mode ids = %q, want plan,code", ids)
	}
	if current := currentMode(res); current != "code" {
		t.Fatalf("current mode = %q, want code", current)
	}

	if len(res.Options) != 2 {
		t.Fatalf("read %d options, want the 2 the fake announces: %+v", len(res.Options), res.Options)
	}
	models := findOption(res, "model")
	if models == nil {
		t.Fatalf("no model option among %+v", res.Options)
	}
	if models.Current != "fake-small" {
		t.Fatalf("model current value = %q, want fake-small", models.Current)
	}
	if len(models.Choices) != 2 {
		t.Fatalf("model offers %d choices, want 2", len(models.Choices))
	}

	thoughts := findOption(res, "thought_level")
	if thoughts == nil {
		t.Fatalf("no thought_level option among %+v", res.Options)
	}
	if thoughts.Current != "low" {
		t.Fatalf("thought_level current value = %q, want low", thoughts.Current)
	}
}

// TestHandshakeReportNamesTheIds pins what a support-matrix row records: the
// ids a user can switch between, not just a count.
func TestHandshakeReportNamesTheIds(t *testing.T) {
	res := probe.HandshakeResult{
		Command:   "fake",
		SessionID: "sess-1",
		Modes: []probe.Mode{
			{ID: "plan", Name: "Plan"},
			{ID: "code", Name: "Code", Current: true},
		},
		Options: []probe.ConfigOption{
			{
				ID:      "model",
				Name:    "Model",
				Type:    "select",
				Current: "small",
				Choices: []probe.Choice{{Value: "small", Name: "Small"}},
			},
		},
	}

	report := res.Report()
	for _, want := range []string{"fake", "modes=2", "options=1"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report %q does not mention %q", report, want)
		}
	}

	lines := strings.Join(res.Lines(), "\n")
	for _, want := range []string{"Code (current)", "model", "small"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("lines %q do not mention %q", lines, want)
		}
	}
}

// TestHandshakeSaysNoneWhenNothingWasAnnounced pins the honest empty case: an
// agent that offers no modes must read as announcing nothing, not as a failure
// to report.
func TestHandshakeSaysNoneWhenNothingWasAnnounced(t *testing.T) {
	res := probe.HandshakeResult{Command: "bare"}

	lines := res.Lines()
	if len(lines) != 1 {
		t.Fatalf("an empty handshake rendered %d lines, want only the report: %q", len(lines), lines)
	}
	for _, want := range []string{"modes=0", "options=0"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("report %q does not say %q", lines[0], want)
		}
	}
}

func modeIDs(res probe.HandshakeResult) string {
	ids := make([]string, 0, len(res.Modes))
	for _, mode := range res.Modes {
		ids = append(ids, mode.ID)
	}
	return strings.Join(ids, ",")
}

func currentMode(res probe.HandshakeResult) string {
	for _, mode := range res.Modes {
		if mode.Current {
			return mode.ID
		}
	}
	return ""
}

func findOption(res probe.HandshakeResult, id string) *probe.ConfigOption {
	for i := range res.Options {
		if res.Options[i].ID == id {
			return &res.Options[i]
		}
	}
	return nil
}

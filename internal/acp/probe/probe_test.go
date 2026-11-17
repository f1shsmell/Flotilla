package probe_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/acp/probe"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestKnownProjectsTheBuiltinPresets keeps the two lists from drifting: the
// picker offers what agent.Builtins() holds, and `flt probe` accepts exactly
// the ACP ones of those. A new builtin must reach both without a second edit.
func TestKnownProjectsTheBuiltinPresets(t *testing.T) {
	presets := agent.Builtins()
	want := make(map[string]agent.Preset, len(presets))
	for _, preset := range presets {
		if preset.Mode == session.ModeACP {
			want[preset.ID] = preset
		}
	}

	known := probe.Known()
	if len(known) != len(want) {
		t.Fatalf("the probe knows %d agents, the builtins hold %d ACP presets", len(known), len(want))
	}

	for _, one := range known {
		preset, ok := want[one.Name]
		if !ok {
			t.Fatalf("the probe knows %q, which is not a builtin ACP preset", one.Name)
		}
		if one.Command != preset.Command {
			t.Fatalf("%q probes %q, the preset runs %q", one.Name, one.Command, preset.Command)
		}
		if strings.Join(one.Args, " ") != strings.Join(preset.Args, " ") {
			t.Fatalf("%q probes with %q, the preset runs with %q", one.Name, one.Args, preset.Args)
		}
		if one.NeedsCredential != preset.Needs {
			t.Fatalf("%q says it needs %q, the preset says %q", one.Name, one.NeedsCredential, preset.Needs)
		}
	}
}

// TestKnownAgentsAreWellFormed pins the probe's catalogue: every entry names a
// command and says what login it needs, so a failed probe can explain itself.
func TestKnownAgentsAreWellFormed(t *testing.T) {
	known := probe.Known()
	if len(known) == 0 {
		t.Fatal("the probe knows no agents")
	}

	for _, agent := range known {
		if agent.Name == "" {
			t.Fatalf("agent %+v has no name", agent)
		}
		if agent.Command == "" {
			t.Fatalf("agent %q has no command", agent.Name)
		}
		if agent.NeedsCredential == "" {
			t.Fatalf("agent %q does not say what login it needs", agent.Name)
		}
	}

	seen := make(map[string]bool, len(known))
	for _, agent := range known {
		if seen[agent.Name] {
			t.Fatalf("agent %q is listed twice, so a name would resolve to two commands", agent.Name)
		}
		seen[agent.Name] = true
	}
}

// TestLookupFindsAKnownNameAndRejectsTheRest covers the one path a user hits when
// they typo an agent name.
func TestLookupFindsAKnownNameAndRejectsTheRest(t *testing.T) {
	first := probe.Known()[0]

	found, err := probe.Lookup(first.Name)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", first.Name, err)
	}
	if found.Command != first.Command {
		t.Fatalf("Lookup(%q) = %+v, want %+v", first.Name, found, first)
	}

	if _, err := probe.Lookup("no-such-agent"); err == nil {
		t.Fatal("Lookup accepted an unknown name")
	}
}

// TestAvailableReportsAMissingBinary is the honest failure a maintainer sees
// before any token is spent.
func TestAvailableReportsAMissingBinary(t *testing.T) {
	missing := probe.Agent{
		Name:            "absent",
		Command:         "flotilla-no-such-agent-binary",
		NeedsCredential: "nothing: the binary does not exist",
	}
	if err := missing.Available(); err == nil {
		t.Fatal("Available reported a missing binary as present")
	}

	// A command the user names has no login hint, so the message must not
	// carry an empty pair of parentheses.
	bare := probe.Agent{Name: "absent", Command: "flotilla-no-such-agent-binary"}
	err := bare.Available()
	if err == nil {
		t.Fatal("Available reported a missing binary as present")
	}
	if strings.Contains(err.Error(), "()") {
		t.Fatalf("error %q carries an empty hint", err)
	}

	present := probe.Agent{Name: "go", Command: "go"}
	if _, err := exec.LookPath("go"); err == nil {
		if err := present.Available(); err != nil {
			t.Fatalf("Available on a binary that exists: %v", err)
		}
	}
}

// TestReportNamesWhatWasObserved keeps the report machine-readable enough to
// paste into a PR.
func TestReportNamesWhatWasObserved(t *testing.T) {
	res := probe.Result{
		Command:    "gemini",
		StopReason: "end_turn",
		Resume:     true,
		Messages:   2,
		Thoughts:   1,
		Tools:      3,
		Elapsed:    1500 * time.Millisecond,
		Reply:      "flotilla-ok",
	}

	report := res.Report()
	for _, want := range []string{"gemini", "end_turn", "resume=true", "messages=2", "flotilla-ok"} {
		if !contains(report, want) {
			t.Fatalf("report %q does not mention %q", report, want)
		}
	}
}

// TestRunReportsAMissingCredentialWithoutSpending checks the probe refuses
// before it starts a process that cannot work.
func TestRunReportsAMissingCredentialWithoutSpending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := probe.Run(ctx, probe.Agent{
		Name:            "absent",
		Command:         "flotilla-no-such-agent-binary",
		NeedsCredential: "nothing: the binary does not exist",
	}, t.TempDir(), "hello")
	if err == nil {
		t.Fatal("Run accepted a binary that does not exist")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Run waited for its deadline instead of failing on the missing binary")
	}
}

// TestPolicyZeroIsSupervised pins the default: a caller that names no policy
// gets the cautious one.
func TestPolicyZeroIsSupervised(t *testing.T) {
	var p acp.Policy
	if p.String() != "supervised" {
		t.Fatalf("the zero policy is %q, want supervised", p.String())
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

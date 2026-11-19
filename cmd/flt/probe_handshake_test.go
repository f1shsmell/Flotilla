package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
)

// buildProbeAgent builds the stand-in ACP agent and puts it on PATH under a name
// with no spaces, so `--command` can name it the way a user types one. A path
// with a space would be split by probeTarget, and the test would then measure the
// splitter rather than the handshake.
func buildProbeAgent(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	target := filepath.Join(dir, "flt-probe-fixture-agent")
	if filepath.Separator == '\\' {
		target += ".exe"
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, "./cmd/flt-fake-acp-agent")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the fake agent: %v: %s", err, output)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return "flt-probe-fixture-agent"
}

// TestProbeHandshakeRefusesAPrompt pins the loud failure: --handshake sends no
// prompt, so --prompt beside it is a request this mode cannot honour. Dropping
// it silently would let a maintainer believe a turn ran.
func TestProbeHandshakeRefusesAPrompt(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"probe", "--handshake", "--prompt", "hello", "--command", "flt-no-such-agent-binary"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--prompt") {
		t.Fatalf("probe --handshake --prompt = %v, want the pair refused", err)
	}
}

// TestProbeHandshakeReportsWhatTheAgentAnnounced is the user-facing half of the
// zero-token probe: with a real agent process, the text report names the modes
// and options it announced, and sends no prompt.
func TestProbeHandshakeReportsWhatTheAgentAnnounced(t *testing.T) {
	name := buildProbeAgent(t)

	var out bytes.Buffer
	if err := run([]string{"probe", "--handshake", "--command", name, "--dir", t.TempDir()}, &out); err != nil {
		t.Fatalf("probe --handshake: %v", err)
	}

	report := out.String()
	for _, want := range []string{"modes=2", "options=3", "mode code = Code (current)", "option model = Model"} {
		if !strings.Contains(report, want) {
			t.Fatalf("handshake report does not mention %q:\n%s", want, report)
		}
	}
	// A turn that ran would have printed its message; this mode must not.
	if strings.Contains(report, "working on:") {
		t.Fatalf("the handshake ran a turn:\n%s", report)
	}
}

// TestProbeHandshakeJSONCarriesTheWholeAnswer pins --json on this mode: a script
// that records a support-matrix row needs the ids and choices, not a sentence.
func TestProbeHandshakeJSONCarriesTheWholeAnswer(t *testing.T) {
	name := buildProbeAgent(t)

	var out bytes.Buffer
	if err := run([]string{"--json", "probe", "--handshake", "--command", name, "--dir", t.TempDir()}, &out); err != nil {
		t.Fatalf("probe --handshake --json: %v", err)
	}

	var res probe.HandshakeResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("the JSON output does not decode as a handshake result: %v\n%s", err, out.String())
	}

	if res.SessionID == "" {
		t.Fatal("the JSON report names no session")
	}
	if res.Updates != 0 {
		t.Fatalf("the agent streamed %d updates, so a turn ran", res.Updates)
	}
	if len(res.Modes) != 2 || len(res.Options) != 3 {
		t.Fatalf("JSON reports %d modes and %d options, want 2 and 3", len(res.Modes), len(res.Options))
	}
}

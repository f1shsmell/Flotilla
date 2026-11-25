package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/registry"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/version"
)

func TestRunPrintsTheBuildVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out bytes.Buffer

		if err := run(args, &out); err != nil {
			t.Fatalf("run(%q): %v", args, err)
		}
		if got, want := strings.TrimSpace(out.String()), version.String(); got != want {
			t.Fatalf("run(%q) printed %q, want %q", args, got, want)
		}
	}
}

// TestChannelFollowsTheSessionEnvironment pins the default: a command run
// inside a session must reach the daemon that serves it, not the default one.
func TestChannelFollowsTheSessionEnvironment(t *testing.T) {
	t.Setenv("FLT_CHANNEL", "")
	if got := defaultChannel(); got != version.Channel {
		t.Fatalf("default channel = %q, want %q", got, version.Channel)
	}

	t.Setenv("FLT_CHANNEL", "preview")
	if got := defaultChannel(); got != "preview" {
		t.Fatalf("channel inside a session = %q, want preview", got)
	}
}

// TestMCPRejectsWhatItDoesNotServe keeps a mistyped subcommand from opening a
// protocol stream on stdout that will never be answered.
func TestMCPRejectsWhatItDoesNotServe(t *testing.T) {
	for _, args := range [][]string{{"mcp"}, {"mcp", "start"}, {"mcp", "serve", "extra"}} {
		var out bytes.Buffer
		err := run(args, &out)
		if err == nil {
			t.Fatalf("run(%q) succeeded", args)
		}
		if !strings.Contains(err.Error(), "mcp serve") {
			t.Fatalf("run(%q) error = %q, want it to name mcp serve", args, err)
		}
	}
}

func TestRunRejectsAnUnknownCommand(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"frobnicate"}, &out)
	if err == nil {
		t.Fatal("run accepted an unknown command")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("error %q does not name the rejected command", err)
	}
}

// TestProbeNamesWhatItKnows keeps a bare probe from spending anything: it says
// what can be named instead.
func TestProbeNamesWhatItKnows(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"probe"}, &out)
	if err == nil {
		t.Fatal("a bare probe should name the agents it knows")
	}
	for _, name := range probe.Names() {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name %q", err, name)
		}
	}
}

func TestProbeRejectsAnUnknownAgent(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"probe", "no-such-agent"}, &out)
	if err == nil || !strings.Contains(err.Error(), "no-such-agent") {
		t.Fatalf("probe of an unknown agent = %v, want it named", err)
	}
}

// TestProbeStopsBeforeSpending pins the order: a command that is not on PATH
// fails the availability check, so no turn and no token is spent.
func TestProbeStopsBeforeSpending(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"probe", "--command", "flt-no-such-agent-binary"}, &out)
	if err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("probe of a missing binary = %v, want a PATH failure", err)
	}
}

// TestProbePassesArgumentsAfterTheSeparator pins the `--` form: with --command
// the trailing arguments belong to the agent, not to a name lookup.
func TestProbePassesArgumentsAfterTheSeparator(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"probe", "--command", "flt-no-such-agent-binary", "--", "--acp"}, &out)
	if err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("probe --command with an argument = %v, want a PATH failure", err)
	}
}

// TestProbeRefusesFlagsAfterTheName pins the loud failure: the flag package
// stops at the first non-flag, so `probe claude --command x` would otherwise
// silently probe claude and ignore the command.
func TestProbeRefusesFlagsAfterTheName(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"probe", "claude", "--command", "something"}, &out)
	if err == nil || !strings.Contains(err.Error(), "flags go before") {
		t.Fatalf("probe with trailing flags = %v, want them refused", err)
	}
}

// TestStatusRefusesTrailingArguments covers the same hole on a command that
// takes none: `flt status --json` used to print the human form and say nothing.
func TestStatusRefusesTrailingArguments(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"status", "--json"}, &out)
	if err == nil || !strings.Contains(err.Error(), "no arguments") {
		t.Fatalf("status with a trailing flag = %v, want it refused", err)
	}
}

// TestHelpExitsCleanly pins -h as a normal outcome: a help request is not a
// parse failure, and exiting non-zero makes `flt -h` look broken in scripts.
func TestHelpExitsCleanly(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"probe", "-h"}} {
		var out bytes.Buffer

		if err := run(args, &out); err != nil {
			t.Fatalf("run(%q) = %v, want a clean exit", args, err)
		}
		if out.Len() == 0 {
			t.Fatalf("run(%q) printed nothing", args)
		}
	}
}

// TestAgentLinesShowEverySwitch pins the human form of `flt agents`: a disabled,
// half-installed or missing agent has to read as such, because the words are the
// only signal a terminal gets.
func TestAgentLinesShowEverySwitch(t *testing.T) {
	lines := agentLines(agent.Listing{Agents: []agent.Entry{
		{
			Preset:    agent.Preset{ID: "reasonix", Command: "reasonix", Mode: session.ModeACP},
			Installed: true,
			Enabled:   true,
			Chat:      true,
			Tier:      agent.TierAvailable,
		},
		{
			Preset:  agent.Preset{ID: "claude", Command: "claude-agent-acp"},
			Enabled: false,
			Tier:    agent.TierInstalledNotACP,
		},
		{
			// An older daemon sends no tier, and its install flag still has to
			// read as an answer rather than as missing.
			Preset:    agent.Preset{ID: "gemini", Command: "gemini"},
			Installed: true,
			Enabled:   false,
		},
		{
			// The same older daemon with nothing installed still reads as missing.
			Preset: agent.Preset{ID: "opencode", Command: "opencode"},
		},
	}})

	if len(lines) != 4 {
		t.Fatalf("agentLines returned %d lines, want one per agent", len(lines))
	}

	// An empty mode is the daemon's "acp" default, and it is spelled out rather
	// than left as a blank column.
	if got, want := lines[0], "reasonix\tacp\tavailable\tenabled,chat\treasonix"; got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	if got, want := lines[1], "claude\tacp\tinstalled-but-not-acp\t\tclaude-agent-acp"; got != want {
		t.Fatalf("second line = %q, want %q", got, want)
	}
	if got, want := lines[2], "gemini\tacp\tinstalled\t\tgemini"; got != want {
		t.Fatalf("third line = %q, want %q", got, want)
	}
	if got, want := lines[3], "opencode\tacp\tmissing\t\topencode"; got != want {
		t.Fatalf("fourth line = %q, want %q", got, want)
	}
}

// TestRegistryListPrintsTheCachedDirectory pins the terminal view of the
// directory the daemon refreshes, and that a missing cache names the command
// that fills it instead of reading as an empty registry.
func TestRegistryListPrintsTheCachedDirectory(t *testing.T) {
	t.Setenv("FLT_HOME", t.TempDir())
	t.Setenv("FLT_CHANNEL", "")

	var out bytes.Buffer
	err := run([]string{"registry", "list"}, &out)
	if err == nil || !strings.Contains(err.Error(), "flt registry refresh") {
		t.Fatalf("list without a cache = %v, want the refresh hint", err)
	}

	paths, err := home.Resolve(version.Channel)
	if err != nil {
		t.Fatalf("resolve state paths: %v", err)
	}
	path := registry.CachePath(paths.Cache)
	cached := registry.Registry{Version: "1.0.0", Agents: []registry.Agent{{
		ID: "npx-agent", Name: "NPX Agent", Version: "1.3.0",
		Distribution: adapters.Install{Npx: &adapters.Package{Package: "agent@1.3.0"}},
	}}}
	if err := registry.Save(path, cached); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out.Reset()
	if err := run([]string{"registry", "list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := "npx-agent\t1.3.0\tnpx agent@1.3.0\tNPX Agent\ncached just now\n"; out.String() != want {
		t.Fatalf("list printed %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := run([]string{"--json", "registry", "list"}, &out); err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var decoded struct {
		registry.Registry
		CachedAt time.Time `json:"cached_at"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode the json listing: %v", err)
	}
	if len(decoded.Agents) != 1 || decoded.Agents[0].ID != "npx-agent" || decoded.CachedAt.IsZero() {
		t.Fatalf("json listing = %+v", decoded)
	}

	old := time.Now().Add(-2 * registry.TTL)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age the cache: %v", err)
	}
	out.Reset()
	if err := run([]string{"registry", "list"}, &out); err != nil {
		t.Fatalf("list a stale cache: %v", err)
	}
	if want := "cached 2h ago (stale; run `flt registry refresh`)"; !strings.Contains(out.String(), want) {
		t.Fatalf("stale listing = %q, want %q", out.String(), want)
	}
}

// TestRegistryListCannotRewriteTheTerminal keeps fetched text from acting on
// the terminal: a control character is printed as a question mark.
func TestRegistryListCannotRewriteTheTerminal(t *testing.T) {
	t.Setenv("FLT_HOME", t.TempDir())
	t.Setenv("FLT_CHANNEL", "")

	paths, err := home.Resolve(version.Channel)
	if err != nil {
		t.Fatalf("resolve state paths: %v", err)
	}
	cached := registry.Registry{Version: "1.0.0", Agents: []registry.Agent{{
		ID: "agent", Name: "x\u001b[2K\ry", Version: "1.0.0",
		Distribution: adapters.Install{Npx: &adapters.Package{Package: "agent@1.0.0"}},
	}}}
	if err := registry.Save(registry.CachePath(paths.Cache), cached); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{"registry", "list"}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.ContainsAny(out.String(), "\x1b\r") {
		t.Fatalf("listing printed control characters: %q", out.String())
	}
	if !strings.Contains(out.String(), "x?[2K?y") {
		t.Fatalf("listing = %q, want the control characters replaced", out.String())
	}
}

// TestRegistryRejectsWhatItDoesNotServe keeps a mistyped subcommand from
// reaching the network or the cache.
func TestRegistryRejectsWhatItDoesNotServe(t *testing.T) {
	for _, args := range [][]string{
		{"registry"},
		{"registry", "frobnicate"},
		{"registry", "list", "extra"},
		{"registry", "refresh", "extra"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil {
			t.Fatalf("run(%q) = nil, want an error", args)
		}
	}
}

// TestAgentsRefusesTrailingArguments closes the same hole as status: a stray
// flag must not be silently ignored.
func TestAgentsRefusesTrailingArguments(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"agents", "--json"}, &out)
	if err == nil || !strings.Contains(err.Error(), "no arguments") {
		t.Fatalf("agents with a trailing flag = %v, want it refused", err)
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
	"github.com/f1shsmell/flotilla/internal/agent"
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

// TestAgentLinesShowEverySwitch pins the human form of `flt agents`: a disabled
// or missing agent has to read as such, because the words are the only signal a
// terminal gets.
func TestAgentLinesShowEverySwitch(t *testing.T) {
	lines := agentLines(agent.Listing{Agents: []agent.Entry{
		{
			Preset:    agent.Preset{ID: "reasonix", Command: "reasonix", Mode: session.ModeACP},
			Installed: true,
			Enabled:   true,
			Chat:      true,
		},
		{
			Preset:  agent.Preset{ID: "gemini", Command: "gemini"},
			Enabled: false,
		},
	}})

	if len(lines) != 2 {
		t.Fatalf("agentLines returned %d lines, want one per agent", len(lines))
	}

	// An empty mode is the daemon's "acp" default, and it is spelled out rather
	// than left as a blank column.
	if got, want := lines[0], "reasonix\tacp\tinstalled\tenabled,chat\treasonix"; got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	if got, want := lines[1], "gemini\tacp\tmissing\t\tgemini"; got != want {
		t.Fatalf("second line = %q, want %q", got, want)
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

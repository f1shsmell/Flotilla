package agent

import (
	"testing"
)

// TestAnAdapterIsFoundByAnAbsoluteOrWrappedCommand pins the lookup the daemon
// does at start: the new-session form accepts a free-text command, so a user who
// pastes an absolute path or a wrapper would otherwise lose the agent's blocked
// detection without any error to say so.
func TestAnAdapterIsFoundByAnAbsoluteOrWrappedCommand(t *testing.T) {
	c := newCatalog(t)
	writeAdapter(t, c.dir, "claude.json", `{
	  "id": "claude",
	  "command": "claude-agent-acp",
	  "blocked": ["do you trust"]
	}`)
	c.load()

	for _, command := range []string{
		"claude-agent-acp",
		`C:\tools\claude-agent-acp.exe`,
		"/usr/local/bin/claude-agent-acp",
		"claude-agent-acp.cmd",
	} {
		adapter, ok := c.AdapterForCommand(command)
		if !ok {
			t.Errorf("%q did not find the agent's adapter", command)
			continue
		}
		if len(adapter.Blocked) != 1 {
			t.Errorf("%q found an adapter with %d patterns", command, len(adapter.Blocked))
		}
	}
}

// TestAnAgentWithoutAPatternIsNotAnAdapter pins the difference between "declared
// nothing" and "declared no patterns": only the second is a definition worth
// handing to a host, and a preset with no operational knowledge at all must not
// be reported as one.
func TestAnAgentWithoutAPatternIsNotAnAdapter(t *testing.T) {
	c := newCatalog(t)

	if _, ok := c.AdapterForCommand("gemini"); ok {
		t.Fatal("a builtin with no operational knowledge reported an adapter")
	}
	if _, ok := c.AdapterForCommand("no-such-command"); ok {
		t.Fatal("an unknown command reported an adapter")
	}
	if _, ok := c.AdapterForCommand("  "); ok {
		t.Fatal("an empty command reported an adapter")
	}
}

// TestCommandBaseStripsTheDirectoryAndShellExtension pins the normalisation on
// its own, so a failure names the input rather than the catalogue.
func TestCommandBaseStripsTheDirectoryAndShellExtension(t *testing.T) {
	for input, want := range map[string]string{
		"claude-agent-acp":              "claude-agent-acp",
		`C:\tools\claude-agent-acp.exe`: "claude-agent-acp",
		"/usr/local/bin/gemini":         "gemini",
		"gemini.cmd":                    "gemini",
		"wrapper.ps1":                   "wrapper",
		"a.b.c.exe":                     "a.b.c",
	} {
		if got := commandBase(input); got != want {
			t.Errorf("commandBase(%q) = %q, want %q", input, got, want)
		}
	}
}

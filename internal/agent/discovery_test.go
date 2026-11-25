package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// homeWith returns a home directory holding the named directories, which is how
// a test says what an agent's earlier install left behind.
func homeWith(t *testing.T, dirs ...string) string {
	t.Helper()

	home := t.TempDir()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatalf("create %s in the home: %v", dir, err)
		}
	}
	return home
}

// gradedCatalog builds a catalog whose PATH and home are the test's, so nothing
// this machine has installed can change the answer.
func gradedCatalog(t *testing.T, home string, installed ...string) *Catalog {
	t.Helper()

	c := newCatalog(t, installed...)
	c.home = func() (string, error) { return home, nil }
	return c
}

// TestTheTierSaysHowAnAgentWasFound pins the discovery list's answers on a
// machine that has one signal each: the ACP entry point, only the vendor CLI, or
// only the config directory an install left behind.
func TestTheTierSaysHowAnAgentWasFound(t *testing.T) {
	home := homeWith(t, ".claude", ".gemini")

	cases := []struct {
		name    string
		catalog *Catalog
		id      string
		want    Tier
	}{
		{"the bridge resolves", gradedCatalog(t, home, "claude-agent-acp"), "claude", TierAvailable},
		{"only the vendor CLI resolves", gradedCatalog(t, home, "claude"), "claude", TierInstalledNotACP},
		{"only the config directory exists", gradedCatalog(t, home), "claude", TierSuspected},
		{"a preset that starts its own CLI", gradedCatalog(t, home), "gemini", TierSuspected},
		{"nothing at all", gradedCatalog(t, t.TempDir()), "claude", TierMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := entryFor(t, c.catalog.List(), c.id).Tier; got != c.want {
				t.Fatalf("%s reads %q, want %q", c.id, got, c.want)
			}
		})
	}
}

// TestAReplacedBuiltinKeepsHowItIsFound keeps an adapter's command change from
// silently dropping the knowledge of which vendor CLI it belongs to: overriding
// where an agent starts must not turn a found agent into a missing one.
func TestAReplacedBuiltinKeepsHowItIsFound(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "claude.json", `{"id":"claude","command":"claude-agent-acp-fork"}`)

	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	c.home = func() (string, error) { return homeWith(t, ".claude"), nil }

	entry := entryFor(t, c.List(), "claude")
	if entry.Command != "claude-agent-acp-fork" {
		t.Fatalf("the command override did not land: %q", entry.Command)
	}
	if want := (Discovery{Command: "claude", Config: ".claude"}); entry.Discovery != want {
		t.Fatalf("discovery = %+v, want the builtin's %+v", entry.Discovery, want)
	}
	// The user-visible invariant the test is named for: the inherited config
	// directory still grades the overridden agent as found.
	if got := entry.Tier; got != TierSuspected {
		t.Fatalf("tier = %q, want %q", got, TierSuspected)
	}
}

// TestAnAdapterKeepsTheDiscoveryHalfItDoesNotRestate pins the field-by-field
// merge: an adapter that restates one half of a builtin's discovery must keep
// the other.
func TestAnAdapterKeepsTheDiscoveryHalfItDoesNotRestate(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "claude.json",
		`{"id":"claude","command":"claude-agent-acp-fork","discovery":{"config":".claude-work"}}`)

	c := New(dir)
	c.selfDir = t.TempDir()
	c.home = func() (string, error) { return homeWith(t, ".claude-work"), nil }
	c.lookPath = func(command string) (string, error) {
		if command == "claude" {
			return `C:\bin\claude.exe`, nil
		}
		return "", os.ErrNotExist
	}

	entry := entryFor(t, c.List(), "claude")
	if want := (Discovery{Command: "claude", Config: ".claude-work"}); entry.Discovery != want {
		t.Fatalf("discovery = %+v, want %+v", entry.Discovery, want)
	}
	if got := entry.Tier; got != TierInstalledNotACP {
		t.Fatalf("tier = %q, want %q", got, TierInstalledNotACP)
	}
}

// TestAnAdapterDiscoveryIsReadAsData pins the adapter file's promise for the new
// field: padding is trimmed, and a config that is not under the user's home is
// refused loudly instead of grading an agent as silently missing.
func TestAnAdapterDiscoveryIsReadAsData(t *testing.T) {
	t.Run("padding is trimmed", func(t *testing.T) {
		dir := t.TempDir()
		writeAdapter(t, dir, "x.json",
			`{"id":"x","command":"x","discovery":{"command":" claude ","config":" .claude "}}`)

		c := New(dir)
		c.selfDir = t.TempDir()
		c.home = func() (string, error) { return homeWith(t, ".claude"), nil }
		c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

		want := Discovery{Command: "claude", Config: ".claude"}
		if got := entryFor(t, c.List(), "x").Discovery; got != want {
			t.Fatalf("discovery = %+v, want %+v", got, want)
		}
	})

	t.Run("a config outside the home is refused", func(t *testing.T) {
		for _, config := range []string{
			"../elsewhere", `..\..\x`, ".", "...", ".claude.", "~/.codex", `C:\Users\x\.claude`,
		} {
			dir := t.TempDir()
			writeAdapter(t, dir, "y.json",
				`{"id":"y","command":"y","discovery":{"config":"`+strings.ReplaceAll(config, `\`, `\\`)+`"}}`)

			c := New(dir)
			c.selfDir = t.TempDir()
			c.home = func() (string, error) { return t.TempDir(), nil }

			problems := strings.Join(c.List().Problems, "\n")
			if !strings.Contains(problems, "discovery.config") {
				t.Fatalf("config %q = problems %q, want a discovery.config refusal", config, problems)
			}
		}
	})

	t.Run("a command with a path separator is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeAdapter(t, dir, "z.json", `{"id":"z","command":"z","discovery":{"command":"tools/claude"}}`)

		problems := strings.Join(New(dir).List().Problems, "\n")
		if !strings.Contains(problems, "discovery.command") {
			t.Fatalf("problems = %q, want a discovery.command refusal", problems)
		}
	})
}

// TestAnAdapterCanSayHowItIsFound keeps the tier reachable for an agent this
// build does not ship: the adapter file names the vendor CLI whose install
// counts.
func TestAnAdapterCanSayHowItIsFound(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "codex.json",
		`{"id":"codex","command":"codex-acp","discovery":{"command":"codex"}}`)

	c := New(dir)
	c.selfDir = t.TempDir()
	c.home = func() (string, error) { return t.TempDir(), nil }
	c.lookPath = func(command string) (string, error) {
		if command == "codex" {
			return `C:\bin\codex.exe`, nil
		}
		return "", os.ErrNotExist
	}

	entry := entryFor(t, c.List(), "codex")
	if entry.Installed {
		t.Fatalf("codex-acp resolved in this test, so the bridge is not the signal under test")
	}
	if got := entry.Tier; got != TierInstalledNotACP {
		t.Fatalf("codex reads %q, want %q", got, TierInstalledNotACP)
	}
}

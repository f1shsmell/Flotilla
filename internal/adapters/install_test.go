package adapters

import (
	"errors"
	"strings"
	"testing"
)

// TestAnInstallRecipeMustBePinnable pins the security choice: a package runner
// that is not pinned to an exact version fetches whatever the publisher shipped
// that day, so the recipe is refused rather than recorded.
func TestAnInstallRecipeMustBePinnable(t *testing.T) {
	pinned := []Install{
		{Npx: &Package{Package: "agoragentic-mcp@1.3.0"}},
		{Npx: &Package{Package: "@zed-industries/codex-acp@0.16.0"}},
		{Uvx: &Package{Package: "fast-agent-acp==0.10.66"}},
		{Uvx: &Package{Package: "minion-code@0.1.44"}},
		{Binary: map[string]Binary{
			"windows-x86_64": {
				Archive: "https://example.com/x.zip", Cmd: "./x.exe",
				Env: map[string]string{"ACP": "on"},
			},
		}},
	}
	for _, install := range pinned {
		if err := install.Validate(); err != nil {
			t.Fatalf("%+v was refused: %v", install, err)
		}
	}

	unpinned := []Install{
		{Npx: &Package{Package: "agoragentic-mcp"}},
		{Npx: &Package{Package: "agoragentic-mcp@latest"}},
		{Npx: &Package{Package: "@scope/pkg"}},
		{Uvx: &Package{Package: "fast-agent-acp"}},
		{Uvx: &Package{Package: "fast-agent-acp>=0.10"}},
		{Npx: &Package{Package: "agent@1.3.0-0 || 2.0.0"}},
		{Npx: &Package{Package: "agent@1.3.0-a - 2.0.0"}},
		{Uvx: &Package{Package: "minion-code@0.1 || 0.2"}},
	}
	for _, install := range unpinned {
		err := install.Validate()
		if err == nil || !strings.Contains(err.Error(), "exact version") {
			t.Fatalf("%+v = %v, want an exact-version refusal", install, err)
		}
	}
}

// TestAnInstallRecipeRefusesWhatIsNotAPackage keeps the name before the pin a
// package: a leading '-' or a URL is an option or a redirect dressed as a
// recipe, and the runner would act on it.
func TestAnInstallRecipeRefusesWhatIsNotAPackage(t *testing.T) {
	cases := []struct {
		install Install
		want    string
	}{
		{Install{Npx: &Package{Package: "--registry=http://evil.example@1.2.3"}}, "start with '-'"},
		{Install{Npx: &Package{Package: "git+https://host/repo@1.2.3"}}, "cannot contain"},
		{Install{Npx: &Package{Package: "../../x@1.0.0"}}, "cannot start with '.'"},
		{Install{Npx: &Package{Package: "a/b@1.0.0"}}, "scoped package name"},
	}
	for _, c := range cases {
		err := c.install.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v = %v, want it to name %q", c.install, err, c.want)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v = %v, want ErrInvalid", c.install, err)
		}
	}
}

// TestNormalizeTrimsTheStoredRecipe pins validate-what-you-store: a padded
// value is the one npm would reject, so the stored recipe is the trimmed one.
func TestNormalizeTrimsTheStoredRecipe(t *testing.T) {
	install := Install{
		Npx: &Package{Package: " agent@1.0.0 "},
		Binary: map[string]Binary{
			"linux-x86_64": {Archive: " https://example.com/x.tgz ", Cmd: " ./x "},
		},
	}

	install.Normalize()

	if install.Npx.Package != "agent@1.0.0" {
		t.Fatalf("npx package = %q, want it trimmed", install.Npx.Package)
	}
	if binary := install.Binary["linux-x86_64"]; binary.Archive != "https://example.com/x.tgz" || binary.Cmd != "./x" {
		t.Fatalf("binary = %+v, want it trimmed", binary)
	}
}

// TestAnInstallRecipeNamesWhereItComesFrom refuses recipes a user could not
// trust: an archive is https, and a hash, when given, is a sha256.
func TestAnInstallRecipeNamesWhereItComesFrom(t *testing.T) {
	cases := []struct {
		name    string
		install Install
		want    string
	}{
		{"http archive", Install{Binary: map[string]Binary{
			"linux-x86_64": {Archive: "http://example.com/x.tar.gz", Cmd: "./x"},
		}}, "https"},
		{"no command", Install{Binary: map[string]Binary{
			"linux-x86_64": {Archive: "https://example.com/x.tar.gz"},
		}}, "command"},
		{"bad hash", Install{Binary: map[string]Binary{
			"linux-x86_64": {Archive: "https://example.com/x.tar.gz", Cmd: "./x", SHA256: "abc"},
		}}, "sha256"},
		{"empty package", Install{Npx: &Package{}}, "package"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.install.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want it to name %q", err, c.want)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestARecipesPinsAreWhatTheCheckLooksUp pins the split the drift check uses:
// the runner, the bare package name and the exact version, in the order the
// recipe names them. A binary recipe has no package registry to ask.
func TestARecipesPinsAreWhatTheCheckLooksUp(t *testing.T) {
	install := Install{
		Npx: &Package{Package: "@scope/agent@1.2.3"},
		Uvx: &Package{Package: "kimi-code@0.3.1"},
	}
	pins := install.Pins()
	if len(pins) != 2 {
		t.Fatalf("Pins = %+v, want npx and uvx", pins)
	}
	if pins[0] != (Pin{Runner: RunnerNPM, Package: "@scope/agent", Version: "1.2.3"}) {
		t.Fatalf("npx pin = %+v", pins[0])
	}
	if pins[1] != (Pin{Runner: RunnerPyPI, Package: "kimi-code", Version: "0.3.1"}) {
		t.Fatalf("uvx pin = %+v", pins[1])
	}

	equal := Install{Uvx: &Package{Package: "fast-agent-acp==0.10.66"}}.Pins()
	if len(equal) != 1 || equal[0].Version != "0.10.66" || equal[0].Package != "fast-agent-acp" {
		t.Fatalf("a '==' pin = %+v", equal)
	}

	padded := Install{Npx: &Package{Package: " agent@1.0.0 "}}.Pins()
	if len(padded) != 1 || padded[0].Package != "agent" || padded[0].Version != "1.0.0" {
		t.Fatalf("a padded pin = %+v", padded)
	}

	binary := Install{Binary: map[string]Binary{
		"linux-x86_64": {Archive: "https://example.com/x.tgz", Cmd: "./x"},
	}}
	if got := binary.Pins(); len(got) != 0 {
		t.Fatalf("a binary recipe pinned %+v", got)
	}
	unpinned := Install{Npx: &Package{Package: "agent@latest"}}
	if got := unpinned.Pins(); len(got) != 0 {
		t.Fatalf("an unpinned recipe pinned %+v", got)
	}
}

// TestAnInstallSummaryNamesTheRunner keeps the one line a terminal prints
// honest: a reader sees which runner a package goes through.
func TestAnInstallSummaryNamesTheRunner(t *testing.T) {
	cases := []struct {
		install Install
		want    string
	}{
		{Install{}, ""},
		{Install{Npx: &Package{Package: "x@1.0.0"}}, "npx x@1.0.0"},
		{Install{Uvx: &Package{Package: "y==2.0.0"}}, "uvx y==2.0.0"},
		{Install{Binary: map[string]Binary{"linux-x86_64": {}, "windows-x86_64": {}}}, "binary (2 platforms)"},
		{
			Install{Npx: &Package{Package: "x@1.0.0"}, Binary: map[string]Binary{"linux-x86_64": {}}},
			"npx x@1.0.0 + binary (1 platform)",
		},
	}
	for _, c := range cases {
		if got := c.install.Summary(); got != c.want {
			t.Fatalf("Summary(%+v) = %q, want %q", c.install, got, c.want)
		}
	}
}

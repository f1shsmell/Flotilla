package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/registry"
	"github.com/f1shsmell/flotilla/internal/version"
)

// installRecipe is one published entry's binary distribution for this machine.
func installRecipe(archive, sum string) adapters.Install {
	platform := agent.Platform(runtime.GOOS, runtime.GOARCH)
	return adapters.Install{Binary: map[string]adapters.Binary{
		platform: {Archive: archive, Cmd: "./bin.exe", SHA256: sum},
	}}
}

// seedInstall writes one adapter with a package recipe and one published entry
// with a binary recipe for this machine.
func seedInstall(t *testing.T, binary *registry.Agent) home.Paths {
	t.Helper()
	t.Setenv("FLT_HOME", t.TempDir())
	t.Setenv("FLT_CHANNEL", "")

	paths, err := home.Resolve(version.Channel)
	if err != nil {
		t.Fatalf("resolve state paths: %v", err)
	}
	dir := filepath.Join(paths.Config, "adapters")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the adapter directory: %v", err)
	}
	adapter := `{"id":"local-agent","name":"Local","command":"local-agent","install":{"npx":{"package":"local-agent@1.0.0","args":["--acp"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "local.json"), []byte(adapter), 0o600); err != nil {
		t.Fatalf("write the adapter: %v", err)
	}

	if binary != nil {
		cached := registry.Registry{Version: "1.0.0", Agents: []registry.Agent{*binary}}
		if err := registry.Save(registry.CachePath(paths.Cache), cached); err != nil {
			t.Fatalf("seed the directory cache: %v", err)
		}
	}
	return paths
}

// TestInstallAsksBeforeItWrites is the confirmation: a no, an empty line, and a
// closed stdin all leave the config directory exactly as it was.
func TestInstallAsksBeforeItWrites(t *testing.T) {
	paths := seedInstall(t, nil)

	for _, answer := range []string{"n\n", "\n", ""} {
		var out bytes.Buffer
		err := installCommand([]string{"local-agent"}, version.Channel, false, strings.NewReader(answer), &out)
		if !errors.Is(err, errNoConsent) {
			t.Fatalf("install with answer %q = %v, want errNoConsent", answer, err)
		}
		if !strings.Contains(out.String(), "install? [y/N]") {
			t.Fatalf("the question was not asked:\n%s", out.String())
		}
		if _, err := os.Stat(filepath.Join(paths.Config, "adapters", "local-agent.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused install wrote an adapter")
		}
	}
}

// TestInstallPrintsThePlanThenWritesTheAdapter is the package path: the plan
// names the pin, and a yes writes the adapter the catalog reads next.
func TestInstallPrintsThePlanThenWritesTheAdapter(t *testing.T) {
	paths := seedInstall(t, nil)

	var out bytes.Buffer
	if err := installCommand([]string{"local-agent"}, version.Channel, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatalf("install: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"install Local (local-agent)",
		"recipe  npx local-agent@1.0.0",
		"npx -y local-agent@1.0.0 --acp",
		"the runner fetches the pinned package at first launch",
		"installed local-agent Local as npx -y local-agent@1.0.0 --acp",
		"the daemon reads it on its next start",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the output does not contain %q:\n%s", want, text)
		}
	}

	stored := filepath.Join(paths.Config, "adapters", "local-agent.json")
	raw, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("read the adapter: %v", err)
	}
	if !strings.Contains(string(raw), `"command": "npx"`) {
		t.Fatalf("the adapter is %s", raw)
	}
}

// TestYesSkipsTheQuestion keeps a script's path quiet, and --json with --yes is
// the shape a caller parses.
func TestYesSkipsTheQuestion(t *testing.T) {
	seedInstall(t, nil)

	var out bytes.Buffer
	if err := installCommand([]string{"--yes", "local-agent"}, version.Channel, true, nil, &out); err != nil {
		t.Fatalf("install --yes --json: %v", err)
	}
	var installed struct {
		agent.Installed
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(out.Bytes(), &installed); err != nil {
		t.Fatalf("decode the install (%s): %v", out.String(), err)
	}
	// Nothing was checked for a package: its bytes arrive at first launch.
	if installed.ID != "local-agent" || installed.Kind != "package" || installed.Verified {
		t.Fatalf("installed = %+v", installed)
	}
}

// TestJSONWithoutYesIsRefused keeps a machine format from carrying a prompt: a
// caller parsing the answer would get the question in the middle of it.
func TestJSONWithoutYesIsRefused(t *testing.T) {
	seedInstall(t, nil)

	err := installCommand([]string{"local-agent"}, version.Channel, true, strings.NewReader("y\n"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("install --json without --yes = %v", err)
	}
}

// TestAnUnhashedBinaryIsRefusedBeforeTheQuestion is the pin policy at the
// command boundary: a recipe the publisher gave no hash for cannot be installed
// without --allow-unverified, and a yes alone does not count.
func TestAnUnhashedBinaryIsRefusedBeforeTheQuestion(t *testing.T) {
	seedInstall(t, &registry.Agent{
		ID: "bin-agent", Name: "Bin Agent", Version: "1.0.0",
		Distribution: installRecipe("https://example.com/bin.zip", ""),
	})

	var out bytes.Buffer
	err := installCommand([]string{"bin-agent"}, version.Channel, false, strings.NewReader("y\n"), &out)
	if !errors.Is(err, agent.ErrUnverified) {
		t.Fatalf("install of an unhashed binary = %v, want ErrUnverified", err)
	}
	text := out.String()
	if !strings.Contains(text, "sha256  none") || !strings.Contains(text, "nothing could verify it") {
		t.Fatalf("the plan did not say the archive is unverified:\n%s", text)
	}
	if strings.Contains(text, "install? [y/N]") {
		t.Fatalf("the question was asked for an archive that cannot be verified:\n%s", text)
	}
}

// TestAnUnknownAgentNamesTheDirectoryWhenItIsMissing keeps the answer honest: an
// id the catalog does not have, with no cache to look it up in, is told so.
func TestAnUnknownAgentNamesTheDirectoryWhenItIsMissing(t *testing.T) {
	seedInstall(t, nil)

	err := installCommand([]string{"no-such-agent"}, version.Channel, false, nil, &bytes.Buffer{})
	if !errors.Is(err, agent.ErrNoAgent) {
		t.Fatalf("install of an unknown agent = %v, want ErrNoAgent", err)
	}
	if !strings.Contains(err.Error(), "registry refresh") {
		t.Fatalf("the error does not say how to fill the directory: %v", err)
	}
}

// TestASecondInstallNeedsForce keeps an install from replacing one the user may
// have written by hand: the adapter file is the user's, and only --force says
// the install may be the thing that overwrites it.
func TestASecondInstallNeedsForce(t *testing.T) {
	seedInstall(t, nil)

	write := func(args []string) error {
		return installCommand(args, version.Channel, false, strings.NewReader("y\n"), &bytes.Buffer{})
	}
	if err := write([]string{"local-agent"}); err != nil {
		t.Fatalf("first install: %v", err)
	}

	err := write([]string{"local-agent"})
	if !errors.Is(err, ErrInstalled) {
		t.Fatalf("second install = %v, want ErrInstalled", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the refusal does not say how to replace it: %v", err)
	}
	var out bytes.Buffer
	if err := installCommand([]string{"--force", "local-agent"}, version.Channel, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatalf("install --force: %v", err)
	}
	if !strings.Contains(out.String(), "its blocked, paste and resume settings are not kept") {
		t.Fatalf("the replacement did not say what it discards:\n%s", out.String())
	}
}

func TestInstallRefusesNoIdAndTwoIds(t *testing.T) {
	seedInstall(t, nil)

	for _, args := range [][]string{nil, {"one", "two"}} {
		if err := installCommand(args, version.Channel, false, nil, &bytes.Buffer{}); err == nil {
			t.Fatalf("install with %q was accepted", args)
		}
	}
}

// TestABuiltinIdStillTakesItsPublishedRecipe: the presets this build ships
// carry no recipe, so the directory is the only place an id it also lists can
// be planned from.
func TestABuiltinIdStillTakesItsPublishedRecipe(t *testing.T) {
	seedInstall(t, &registry.Agent{
		ID: "opencode", Name: "opencode", Version: "1.0.0",
		Distribution: adapters.Install{Npx: &adapters.Package{Package: "opencode-ai@1.2.3", Args: []string{"acp"}}},
	})

	var out bytes.Buffer
	err := installCommand([]string{"opencode"}, version.Channel, false, nil, &out)
	if !errors.Is(err, errNoConsent) {
		t.Fatalf("install opencode = %v, want errNoConsent", err)
	}
	for _, want := range []string{
		"install opencode (opencode)",
		"recipe  npx opencode-ai@1.2.3",
		"npx -y opencode-ai@1.2.3 acp",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the plan does not contain %q:\n%s", want, out.String())
		}
	}
}

// TestABinaryInstallFromTheCacheIsPlanned is the command path's published half:
// an entry that is only in the directory's cache is found, and its archive is
// printed as the plan's substance.
func TestABinaryInstallFromTheCacheIsPlanned(t *testing.T) {
	seedInstall(t, &registry.Agent{
		ID: "bin-agent", Name: "Bin Agent", Version: "1.0.0",
		Distribution: installRecipe("https://example.com/bin-agent.zip", strings.Repeat("a", 64)),
	})

	var out bytes.Buffer
	err := installCommand([]string{"bin-agent"}, version.Channel, false, nil, &out)
	if !errors.Is(err, errNoConsent) {
		t.Fatalf("install with no answer = %v, want errNoConsent", err)
	}
	text := out.String()
	for _, want := range []string{
		"install Bin Agent (bin-agent)",
		"recipe  binary (1 platform)",
		"archive https://example.com/bin-agent.zip",
		"sha256  " + strings.Repeat("a", 64),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the plan does not contain %q:\n%s", want, text)
		}
	}
}

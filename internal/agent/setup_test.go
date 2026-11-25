package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

func runtimeOS() string { return runtime.GOOS }

func runtimeArch() string { return runtime.GOARCH }

// seedInstallHome writes one catalog with a package preset and one binary
// preset, the two shapes an install has to carry.
func seedInstallHome(t *testing.T) []Preset {
	t.Helper()

	dir := t.TempDir()
	writeAdapter(t, dir, "pkg.json",
		`{"id":"pkg","name":"Package Agent","command":"pkg","install":{"npx":{"package":"pkg-agent@1.2.3","args":["--acp"]}}}`)
	writeAdapter(t, dir, "bin.json",
		`{"id":"bin","name":"Binary Agent","command":"bin","install":{"binary":{"`+Platform("windows", "amd64")+`":{"archive":"https://example.com/bin.zip","cmd":"./bin.exe"}}}}`)
	writeAdapter(t, dir, "none.json", `{"id":"none","name":"Nothing","command":"none"}`)

	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if problems := c.List().Problems; len(problems) > 0 {
		t.Fatalf("the seeded adapters have problems: %v", problems)
	}
	return presetsOf(c.List().Agents)
}

func presetsOf(entries []Entry) []Preset {
	out := make([]Preset, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Preset)
	}
	return out
}

// TestThePlanSaysWhatAnInstallWouldRun is the confirmation's substance: the
// full command, the pin, and the URL a binary would fetch, before anything is.
func TestThePlanSaysWhatAnInstallWouldRun(t *testing.T) {
	plan, err := PlanFor(seedInstallHome(t), "pkg")
	if err != nil {
		t.Fatalf("plan for pkg: %v", err)
	}
	if plan.Kind != "package" || plan.Command != "npx" || plan.Recipe != "npx pkg-agent@1.2.3" {
		t.Fatalf("plan = %+v", plan)
	}
	if got := strings.Join(plan.Args, " "); got != "-y pkg-agent@1.2.3 --acp" {
		t.Fatalf("args = %q, want the runner to fetch the pinned package", got)
	}

	plan, err = PlanFor(seedInstallHome(t), "bin")
	if err != nil {
		t.Fatalf("plan for bin: %v", err)
	}
	if plan.Kind != "binary" || plan.Archive != "https://example.com/bin.zip" || plan.Platform == "" {
		t.Fatalf("plan = %+v", plan)
	}
}

// TestAPlanRefusesWhatItCannotInstall keeps a recipe with nothing to run, and a
// binary with no archive for this platform, from becoming an install that
// writes an adapter pointing nowhere.
func TestAPlanRefusesWhatItCannotInstall(t *testing.T) {
	list := seedInstallHome(t)

	if _, err := PlanFor(list, "none"); !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("plan for a recipe with nothing to run = %v, want ErrNoRecipe", err)
	}

	other := "linux-s390x"
	if Platform(runtimeOS(), runtimeArch()) == other {
		t.Skip("this machine is the platform the test calls absent")
	}
	dir := t.TempDir()
	writeAdapter(t, dir, "elsewhere.json",
		`{"id":"elsewhere","command":"elsewhere","install":{"binary":{"`+other+`":{"archive":"https://example.com/x.zip","cmd":"./x"}}}}`)
	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if _, err := PlanFor(presetsOf(c.List().Agents), "elsewhere"); !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("plan for another platform = %v, want ErrNoRecipe", err)
	}
}

// TestAnInstallWritesTheAdapterThatStartsIt is the package path end to end: the
// adapter lands, and the catalog the daemon builds next reads it as a preset
// whose command is the pinned runner.
func TestAnInstallWritesTheAdapterThatStartsIt(t *testing.T) {
	configDir := t.TempDir()
	plan, err := PlanFor(seedInstallHome(t), "pkg")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	installed, err := NewInstaller(configDir, nil).Install(context.Background(), plan, Options{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if installed.Adapter != filepath.Join(configDir, "adapters", "pkg.json") {
		t.Fatalf("adapter = %q", installed.Adapter)
	}

	c := New(configDir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	entry := entryFor(t, c.List(), "pkg")
	if entry.Command != "npx" || len(entry.Args) < 2 || entry.Args[0] != "-y" {
		t.Fatalf("the catalog reads the install as %+v", entry.Preset)
	}
}

// TestABinaryInstallLandsAndTakesItsCommandFromTheArchive follows the whole
// binary path: the archive is verified, unpacked into the install directory,
// and the adapter names the command inside it.
func TestABinaryInstallLandsAndTakesItsCommandFromTheArchive(t *testing.T) {
	configDir := t.TempDir()
	payload := zipProgram(t, "bin.exe", "binary")
	server := serveArchive(t, http.StatusOK, payload)

	preset := Preset{ID: "bin", Name: "Binary", Install: adapters.Install{Binary: map[string]adapters.Binary{
		Platform(runtimeOS(), runtimeArch()): {
			Archive: server.URL + "/bin.zip", Cmd: "./bin.exe", SHA256: sum(payload),
		},
	}}}
	plan, err := planForPreset(preset)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	installed, err := NewInstaller(configDir, server.Client()).Install(context.Background(), plan, Options{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	want := filepath.Join(InstallDir(configDir), "bin", "bin.exe")
	if installed.Command != want || !installed.Verified {
		t.Fatalf("installed = %+v, want %q", installed, want)
	}
	if body, err := os.ReadFile(want); err != nil || string(body) != "binary" {
		t.Fatalf("the command holds %q (%v)", body, err)
	}
	if _, err := os.Stat(filepath.Join(InstallDir(configDir), "bin", "..", "bin-"+plan.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a staging directory survived: %v", err)
	}

	var stored adapterFile
	raw, err := os.ReadFile(installed.Adapter)
	if err != nil {
		t.Fatalf("read the adapter: %v", err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode the adapter: %v", err)
	}
	if stored.Command != want {
		t.Fatalf("the adapter names %q, want %q", stored.Command, want)
	}

	// The command is an absolute path outside PATH, so the catalog has to
	// resolve it as one: an install the settings page calls missing is no install.
	c := New(configDir)
	c.selfDir = t.TempDir()
	entry := entryFor(t, c.List(), "bin")
	if !entry.Installed || entry.Path != want {
		t.Fatalf("the catalog reads the install as %+v", entry)
	}
}

// TestAHashedArchiveBeatsAPackageRunner keeps the verified path from being
// shadowed: an agent that publishes both is installed from the archive whose
// bytes this build can check, not by a runner that fetches at launch.
func TestAHashedArchiveBeatsAPackageRunner(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "both.json",
		`{"id":"both","name":"Both","command":"both","install":{`+
			`"npx":{"package":"both@1.0.0"},`+
			`"binary":{"`+Platform(runtimeOS(), runtimeArch())+`":{"archive":"https://example.com/both.zip","cmd":"./both.exe","sha256":"`+strings.Repeat("a", 64)+`"}}}}`)
	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	plan, err := PlanFor(presetsOf(c.List().Agents), "both")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.IsBinary() || plan.SHA256 == "" {
		t.Fatalf("plan = %+v, want the hashed archive", plan)
	}
}

// TestAnUnhashedArchiveYieldsToTheRunner is the other direction: an archive
// nothing can check does not take the place of a package the user can install
// without a second --allow-unverified decision.
func TestAnUnhashedArchiveYieldsToTheRunner(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "both.json",
		`{"id":"both","name":"Both","command":"both","install":{`+
			`"npx":{"package":"both@1.0.0"},`+
			`"binary":{"`+Platform(runtimeOS(), runtimeArch())+`":{"archive":"https://example.com/both.zip","cmd":"./both.exe"}}}}`)
	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	plan, err := PlanFor(presetsOf(c.List().Agents), "both")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.IsPackage() || plan.Command != "npx" {
		t.Fatalf("plan = %+v, want the package runner", plan)
	}
}

// TestAReplacementKeepsTheOldDirectoryUntilTheNewOneLands covers the swap both
// ways: a move that fails after the old directory was set aside puts it back,
// and a completed one replaces it without leaving a copy behind.
func TestAReplacementKeepsTheOldDirectoryUntilTheNewOneLands(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed the previous install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.exe"), []byte("previous"), 0o700); err != nil {
		t.Fatalf("seed the previous command: %v", err)
	}

	if err := replaceDir(dir, filepath.Join(root, "bin-lost")); err == nil {
		t.Fatal("a replacement from a missing directory reported success")
	}
	if body, err := os.ReadFile(filepath.Join(dir, "bin.exe")); err != nil || string(body) != "previous" {
		t.Fatalf("the previous install is %q (%v): a failed replace destroyed it", body, err)
	}

	next := filepath.Join(root, "bin-next")
	if err := os.MkdirAll(next, 0o700); err != nil {
		t.Fatalf("seed the new install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(next, "bin.exe"), []byte("binary"), 0o700); err != nil {
		t.Fatalf("seed the new command: %v", err)
	}
	if err := replaceDir(dir, next); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "bin.exe")); err != nil || string(body) != "binary" {
		t.Fatalf("the install holds %q (%v)", body, err)
	}
	if _, err := os.Stat(next + ".previous"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the set-aside copy survived a completed replace: %v", err)
	}
}

// TestAFailedInstallLeavesNoAdapter: a hash that does not match must not leave
// a preset that launches something the publisher did not sign.
func TestAFailedInstallLeavesNoAdapter(t *testing.T) {
	configDir := t.TempDir()
	payload := zipBytes(t, map[string]string{"bin.exe": "binary"})
	server := serveArchive(t, http.StatusOK, payload)

	preset := Preset{ID: "bin", Install: adapters.Install{Binary: map[string]adapters.Binary{
		Platform(runtimeOS(), runtimeArch()): {
			Archive: server.URL + "/bin.zip", Cmd: "./bin.exe", SHA256: strings.Repeat("0", 64),
		},
	}}}
	plan, err := planForPreset(preset)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	_, err = NewInstaller(configDir, server.Client()).Install(context.Background(), plan, Options{})
	if !errors.Is(err, ErrHash) {
		t.Fatalf("install with a bad hash = %v, want ErrHash", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "adapters", "bin.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed install wrote an adapter")
	}
	if entries, _ := os.ReadDir(InstallDir(configDir)); len(entries) != 0 {
		t.Fatalf("a failed install left %v", entries)
	}
}

// TestAnUnhashedBinaryNeedsTheUsersWord: without consent the install refuses
// before it fetches, and with it the archive installs and says it was unverified.
func TestAnUnhashedBinaryNeedsTheUsersWord(t *testing.T) {
	payload := zipBytes(t, map[string]string{"bin.exe": "binary"})
	fetched := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched++
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	preset := Preset{ID: "bin", Install: adapters.Install{Binary: map[string]adapters.Binary{
		Platform(runtimeOS(), runtimeArch()): {Archive: server.URL + "/bin.zip", Cmd: "./bin.exe"},
	}}}
	plan, err := planForPreset(preset)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	configDir := t.TempDir()
	if _, err := NewInstaller(configDir, server.Client()).Install(context.Background(), plan, Options{}); !errors.Is(err, ErrUnverified) {
		t.Fatalf("install without consent = %v, want ErrUnverified", err)
	}
	if fetched != 0 {
		t.Fatal("the archive was fetched before the user agreed")
	}

	installed, err := NewInstaller(configDir, server.Client()).Install(context.Background(), plan, Options{AllowUnverified: true})
	if err != nil {
		t.Fatalf("install with consent: %v", err)
	}
	if installed.Verified {
		t.Fatal("an unhashed install reported itself as verified")
	}
}

// TestAnInstallCannotEscapeTheConfigDirectory keeps a published id from naming
// a file outside the adapter directory: the id becomes a path segment.
func TestAnInstallCannotEscapeTheConfigDirectory(t *testing.T) {
	for _, id := range []string{"../../evil", "a/b", "C:evil", ""} {
		if adapterName.MatchString(id) {
			t.Fatalf("id %q was accepted as a file name", id)
		}
	}
	preset := Preset{ID: "../evil", Install: adapters.Install{Npx: &adapters.Package{Package: "x@1.0.0"}}}
	if _, err := planForPreset(preset); err == nil {
		t.Fatal("a plan was made for an id that is not a file name")
	}
}

// TestPlanForRefusesAnIdItCannotName keeps the plan's boundary: an empty id, an
// id nothing defines, and an id that is not a file name all refuse before
// anything is fetched or written.
func TestPlanForRefusesAnIdItCannotName(t *testing.T) {
	list := seedInstallHome(t)

	for _, id := range []string{"", "  ", "no-such-agent"} {
		if _, err := PlanFor(list, id); !errors.Is(err, ErrNoAgent) {
			t.Fatalf("PlanFor(%q) = %v, want ErrNoAgent", id, err)
		}
	}
}

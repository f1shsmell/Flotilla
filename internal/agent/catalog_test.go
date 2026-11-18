package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/session"
)

// exeSuffix is the extension this platform puts on a binary. A bundled agent is
// written with it, and the catalogue's lookup has to find it without the preset
// naming the extension.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// newCatalog builds a catalog with a stubbed PATH, so tests never depend on
// what this machine happens to have installed. The binary directory is an empty
// temp dir for the same reason: nothing found next to the test binary leaks in.
func newCatalog(t *testing.T, installed ...string) *Catalog {
	t.Helper()

	c := New(t.TempDir())
	c.selfDir = t.TempDir()
	c.lookPath = func(command string) (string, error) {
		for _, name := range installed {
			if name == command {
				return "/usr/local/bin/" + command, nil
			}
		}
		return "", os.ErrNotExist
	}
	return c
}

func writeAdapter(t *testing.T, dir, name, body string) {
	t.Helper()

	path := filepath.Join(dir, "adapters")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create the adapter directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write adapter %q: %v", name, err)
	}
}

func entryFor(t *testing.T, list Listing, id string) Entry {
	t.Helper()

	for _, entry := range list.Agents {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("the listing has no %q: %+v", id, list.Agents)
	return Entry{}
}

func TestListProjectsEveryBuiltin(t *testing.T) {
	c := newCatalog(t)

	list := c.List()
	if len(list.Agents) != len(Builtins()) {
		t.Fatalf("List returned %d agents, want the %d builtins", len(list.Agents), len(Builtins()))
	}
	for _, preset := range Builtins() {
		entry := entryFor(t, list, preset.ID)
		if entry.Source != BuiltinSource {
			t.Fatalf("%s came from %q, want %q", preset.ID, entry.Source, BuiltinSource)
		}
		if entry.Name == "" || entry.Command != preset.Command {
			t.Fatalf("%s was projected as %+v", preset.ID, entry)
		}
	}
}

// TestTheReplayPresetShipsWithTheBuild pins the point of the preset: a demo, a
// GIF recording or a smoke run can start a session with nothing prepared and no
// token spent, so the catalogue has to offer it like any other agent.
func TestTheReplayPresetShipsWithTheBuild(t *testing.T) {
	entry := entryFor(t, newCatalog(t).List(), "replay")

	if entry.Source != BuiltinSource {
		t.Fatalf("the replay preset came from %q, want %q", entry.Source, BuiltinSource)
	}
	if entry.Mode != session.ModeACP || !entry.Chat || entry.CLI {
		t.Fatalf("the replay preset = %+v, want an acp agent offered to chat", entry)
	}
	// It needs nothing from the user: that is what makes it usable for a demo on
	// a machine with no agent installed at all.
	if entry.Needs == "" || !strings.Contains(entry.Needs, "nothing") {
		t.Fatalf("the replay preset needs %q, want it to say it needs nothing", entry.Needs)
	}
}

// TestABundledAgentIsFoundBesideTheExecutable pins why the replay preset is
// usable at all: its binary ships next to the daemon rather than on PATH, so a
// lookup that only asked PATH would report the agent missing on every install.
func TestABundledAgentIsFoundBesideTheExecutable(t *testing.T) {
	c := newCatalog(t)
	dir := t.TempDir()
	c.selfDir = dir

	// A release on Windows carries the .exe suffix; the lookup has to see it
	// without the preset naming it.
	path := filepath.Join(dir, "flt-replay-agent"+exeSuffix())
	if err := os.WriteFile(path, []byte("not really a binary"), 0o600); err != nil {
		t.Fatalf("write the bundled agent: %v", err)
	}

	entry := entryFor(t, c.List(), "replay")
	if !entry.Installed {
		t.Fatal("a bundled agent was reported as missing")
	}
	if entry.Path != path {
		t.Fatalf("the bundled agent resolved to %q, want %q", entry.Path, path)
	}
}

// TestABundledAgentWinsOverOneOnPath pins the precedence: a user who also
// installed the agent on PATH gets the one this build was tested with, not a
// different version that happens to be first in PATH.
func TestABundledAgentWinsOverOneOnPath(t *testing.T) {
	c := newCatalog(t, "flt-replay-agent")
	dir := t.TempDir()
	c.selfDir = dir

	bundled := filepath.Join(dir, "flt-replay-agent"+exeSuffix())
	if err := os.WriteFile(bundled, []byte("bundled"), 0o600); err != nil {
		t.Fatalf("write the bundled agent: %v", err)
	}

	entry := entryFor(t, c.List(), "replay")
	if entry.Path != bundled {
		t.Fatalf("the replay agent resolved to %q, want the bundled %q", entry.Path, bundled)
	}
}

// TestAMissingBundledAgentStaysMissing pins the cautious outcome: a build whose
// binary was not copied over must not claim the agent is installed, because the
// picker would then start a session that cannot spawn.
func TestAMissingBundledAgentStaysMissing(t *testing.T) {
	c := newCatalog(t)
	c.selfDir = t.TempDir()

	entry := entryFor(t, c.List(), "replay")
	if entry.Installed || entry.Path != "" {
		t.Fatalf("the replay preset = %+v, want it missing", entry)
	}
}

// TestAnEmptySelfDirIsNotAGuess pins the fallback: a process that cannot name
// its own directory looks on PATH only, rather than treating the working
// directory as the place its agents live.
func TestAnEmptySelfDirIsNotAGuess(t *testing.T) {
	c := newCatalog(t)
	c.selfDir = ""

	if entry := entryFor(t, c.List(), "replay"); entry.Installed {
		t.Fatalf("the replay preset = %+v, want it missing with no binary directory", entry)
	}
}

// TestResolveTurnsABundledNameIntoItsPath pins the step that makes a bundled
// preset usable: the picker sends a command name, and the host spawns it with
// exec, which does not look beside the daemon. Resolve is what closes the gap,
// so a name this build ships becomes an absolute path before the spawn.
func TestResolveTurnsABundledNameIntoItsPath(t *testing.T) {
	c := newCatalog(t)
	dir := t.TempDir()
	c.selfDir = dir

	path := filepath.Join(dir, "flt-replay-agent"+exeSuffix())
	if err := os.WriteFile(path, []byte("bundled"), 0o600); err != nil {
		t.Fatalf("write the bundled agent: %v", err)
	}

	if got := c.Resolve("flt-replay-agent"); got != path {
		t.Fatalf("Resolve = %q, want the bundled %q", got, path)
	}
}

// TestResolveLeavesAnUnknownCommandAlone pins the fallback: a command this build
// does not ship is handed to exec unchanged, so PATH and an absolute path the
// user typed keep working exactly as before.
func TestResolveLeavesAnUnknownCommandAlone(t *testing.T) {
	c := newCatalog(t)

	if got := c.Resolve("claude-agent-acp"); got != "claude-agent-acp" {
		t.Fatalf("Resolve = %q, want the name unchanged", got)
	}
	if got := c.Resolve("/opt/bin/claude-agent-acp"); got != "/opt/bin/claude-agent-acp" {
		t.Fatalf("Resolve = %q, want the absolute path unchanged", got)
	}
}

// TestResolvePrefersTheBundledBinaryOverPath pins the same precedence the
// listing shows: the preset is offered pointing at the bundled copy, so the
// spawn has to use that copy rather than a different version on PATH.
func TestResolvePrefersTheBundledBinaryOverPath(t *testing.T) {
	c := newCatalog(t, "flt-replay-agent")
	dir := t.TempDir()
	c.selfDir = dir

	bundled := filepath.Join(dir, "flt-replay-agent"+exeSuffix())
	if err := os.WriteFile(bundled, []byte("bundled"), 0o600); err != nil {
		t.Fatalf("write the bundled agent: %v", err)
	}

	if got := c.Resolve("flt-replay-agent"); got != bundled {
		t.Fatalf("Resolve = %q, want the bundled %q", got, bundled)
	}
}

// TestABundledLookupTakesOnlyABareName pins the guard: a command that names a
// directory is the caller's to run, and joining it under the binary directory
// would quietly rewrite a path the user typed.
func TestABundledLookupTakesOnlyABareName(t *testing.T) {
	c := newCatalog(t)
	dir := t.TempDir()
	c.selfDir = dir

	// A file that exists, reached through a relative path, must not be found.
	if err := os.WriteFile(filepath.Join(dir, "agent"+exeSuffix()), []byte("x"), 0o600); err != nil {
		t.Fatalf("write the file: %v", err)
	}

	for _, command := range []string{
		"../agent",
		"./agent",
		filepath.Join(dir, "agent"),
		"sub/agent",
	} {
		if got := c.Resolve(command); got != command {
			t.Errorf("Resolve(%q) = %q, want the path left alone", command, got)
		}
	}
}

func TestListReportsWhatIsInstalled(t *testing.T) {
	c := newCatalog(t, "reasonix")

	list := c.List()

	installed := entryFor(t, list, "reasonix")
	if !installed.Installed || installed.Path == "" {
		t.Fatalf("reasonix is on PATH but reported as %+v", installed)
	}

	missing := entryFor(t, list, "gemini")
	if missing.Installed || missing.Path != "" {
		t.Fatalf("gemini is not on PATH but reported as %+v", missing)
	}
}

// A preset's mode decides the purpose: an acp agent is offered to chat sessions
// and a pty one to terminal sessions. The switch for that purpose is the user's,
// persists, and is the only one that can be read.
func TestSwitchDefaultsFollowTheMode(t *testing.T) {
	c := newCatalog(t)

	acp := entryFor(t, c.List(), "claude")
	if !acp.Enabled || !acp.Chat || acp.CLI {
		t.Fatalf("an untouched acp preset = %+v, want enabled, chat, not cli", acp)
	}

	if _, err := c.Set("claude", Change{Chat: boolPtr(false)}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	again := entryFor(t, c.List(), "claude")
	if again.Chat {
		t.Fatalf("after the switch = %+v, want chat off", again)
	}

	// The mode, not the store, owns the purpose: a switch file that names the
	// other one is read as off rather than as a switch nobody can clear.
	if _, err := c.Set("claude", Change{CLI: boolPtr(true)}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if entryFor(t, c.List(), "claude").CLI {
		t.Fatal("an acp preset reads as a terminal agent")
	}
}

func TestDisablingKeepsTheAgentListed(t *testing.T) {
	c := newCatalog(t)

	if _, err := c.Set("gemini", Change{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entry := entryFor(t, c.List(), "gemini")
	if entry.Enabled {
		t.Fatalf("gemini was disabled but reads as %+v", entry)
	}

	if _, err := c.Set("gemini", Change{Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("Set back: %v", err)
	}
	if !entryFor(t, c.List(), "gemini").Enabled {
		t.Fatal("gemini stays disabled after being turned back on")
	}
}

func TestSwitchesSurviveANewCatalog(t *testing.T) {
	dir := t.TempDir()
	c := New(dir)
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	if _, err := c.Set("reasonix", Change{Enabled: boolPtr(false), Chat: boolPtr(false)}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A daemon restart rebuilds the catalog from disk.
	reloaded := New(dir)
	reloaded.lookPath = c.lookPath

	entry := entryFor(t, reloaded.List(), "reasonix")
	if entry.Enabled || entry.Chat {
		t.Fatalf("after a restart = %+v, want the stored switches", entry)
	}
}

func TestSetRefusesAnUnknownAgent(t *testing.T) {
	c := newCatalog(t)

	_, err := c.Set("no-such-agent", Change{Enabled: boolPtr(true)})
	if err == nil || !strings.Contains(err.Error(), "no-such-agent") {
		t.Fatalf("Set of an unknown agent = %v, want it named", err)
	}
}

func TestAdapterAddsAPreset(t *testing.T) {
	c := newCatalog(t)
	writeAdapter(t, c.dir, "my-agent.json", `{
	  "id": "my-agent",
	  "name": "My Agent",
	  "command": "my-agent",
	  "args": ["acp"],
	  "needs": "a token"
	}`)
	c.load()

	entry := entryFor(t, c.List(), "my-agent")
	if entry.Source != "my-agent.json" || entry.Name != "My Agent" {
		t.Fatalf("adapter entry = %+v", entry)
	}
	if entry.Mode != session.ModeACP {
		t.Fatalf("an adapter without a mode = %q, want %q", entry.Mode, session.ModeACP)
	}
}

func TestAdapterReplacesABuiltinInPlace(t *testing.T) {
	c := newCatalog(t)
	writeAdapter(t, c.dir, "gemini.json", `{"id": "gemini", "command": "gemini-next", "args": ["acp"]}`)
	c.load()

	list := c.List()
	entry := entryFor(t, list, "gemini")
	if entry.Command != "gemini-next" || entry.Source != "gemini.json" {
		t.Fatalf("overridden gemini = %+v, want the adapter's command", entry)
	}
	if len(list.Agents) != len(Builtins()) {
		t.Fatalf("an override changed the count: %d agents", len(list.Agents))
	}
}

// An explicit empty args list is a decision, not an omission: a wrapper that
// must not receive the builtin's arguments has to be able to say so.
func TestAdapterCanDropInheritedArgs(t *testing.T) {
	c := newCatalog(t)
	writeAdapter(t, c.dir, "gemini.json", `{"id": "gemini", "command": "gemini-wrapper", "args": []}`)
	c.load()

	entry := entryFor(t, c.List(), "gemini")
	if entry.Command != "gemini-wrapper" {
		t.Fatalf("overridden gemini runs %q, want the adapter's command", entry.Command)
	}
	if len(entry.Args) != 0 {
		t.Fatalf("gemini kept %q, want an explicitly empty argument list", entry.Args)
	}
	// The fields the adapter did not mention still come from the builtin.
	if entry.Needs == "" || entry.Mode != session.ModeACP {
		t.Fatalf("the override lost inherited fields: %+v", entry)
	}
}

// A broken adapter is reported and skipped. One bad file must not hide the
// agents the user can still start.
func TestABadAdapterIsReportedAndSkipped(t *testing.T) {
	c := newCatalog(t)
	writeAdapter(t, c.dir, "broken.json", `{"command": ""}`)
	c.load()
	writeAdapter(t, c.dir, "not-json.json", `{`)
	c.load()

	list := c.List()
	if len(list.Agents) != len(Builtins()) {
		t.Fatalf("a bad adapter changed the listing: %+v", list.Agents)
	}
	if len(list.Problems) != 2 {
		t.Fatalf("Problems = %q, want one per bad file", list.Problems)
	}
	for _, problem := range list.Problems {
		if !strings.Contains(problem, ".json") {
			t.Fatalf("problem %q does not name its file", problem)
		}
	}
}

func TestAdapterTakesItsIDFromTheFileName(t *testing.T) {
	c := newCatalog(t)
	writeAdapter(t, c.dir, "local-agent.json", `{"command": "local-agent"}`)
	c.load()

	entry := entryFor(t, c.List(), "local-agent")
	if entry.Name != "local-agent" {
		t.Fatalf("a nameless adapter = %+v, want the id as its name", entry)
	}
}

func TestAnInvalidSwitchFileDoesNotHideEveryAgent(t *testing.T) {
	c := newCatalog(t)
	if err := os.WriteFile(filepath.Join(c.dir, "agents.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write the switch file: %v", err)
	}

	list := c.List()
	if len(list.Agents) != len(Builtins()) {
		t.Fatalf("an unreadable switch file changed the listing: %+v", list.Agents)
	}
	if len(list.Problems) != 1 {
		t.Fatalf("Problems = %q, want the unreadable switch file", list.Problems)
	}
	// The defaults stand, because a bad file says nothing about the user's
	// intent.
	if !entryFor(t, list, "reasonix").Enabled {
		t.Fatal("an unreadable switch file disabled an agent")
	}
}

// A corrupt switch file must not lock the switches forever: the page offers no
// other way to clear it, so a write replaces the file instead of failing.
func TestSetReplacesAnUnreadableSwitchFile(t *testing.T) {
	c := newCatalog(t)
	if err := os.WriteFile(filepath.Join(c.dir, "agents.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write the switch file: %v", err)
	}

	entry, err := c.Set("gemini", Change{Enabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("Set over a corrupt switch file: %v", err)
	}
	if entry.Enabled {
		t.Fatalf("the write reported %+v, want gemini disabled", entry)
	}

	// The replacement file is readable again, and the problem the reader
	// reported is gone with it.
	list := c.List()
	if len(list.Problems) != 0 {
		t.Fatalf("Problems = %q, want the rewritten file", list.Problems)
	}
	if entryFor(t, list, "gemini").Enabled {
		t.Fatal("gemini reads as enabled after being disabled")
	}

	// The corrupt bytes are kept beside the file: the rewrite starts from empty,
	// and switches it could not decode are still the user's to recover.
	backup, err := os.ReadFile(filepath.Join(c.dir, "agents.json.corrupt"))
	if err != nil {
		t.Fatalf("the corrupt switch file was not kept: %v", err)
	}
	if string(backup) != "{" {
		t.Fatalf("the backup holds %q, want the bytes that were there", backup)
	}
}

// TestTheGUITypeMatchesTheWireShape is the parity gate for the types the
// renderer mirrors by hand. Go sends JSON tags; the TS interfaces read the same
// names, and a rename on either side fails here instead of rendering undefined.
func TestTheGUITypeMatchesTheWireShape(t *testing.T) {
	for _, tc := range []struct {
		goValue any
		tsName  string
	}{
		{Entry{}, "AgentEntry"},
		{Listing{}, "AgentListing"},
		{Change{}, "AgentChange"},
	} {
		got := goJSONFields(t, tc.goValue)
		want := tsInterfaceFields(t, tc.tsName)

		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: Go sends %v, gui/src/store.ts reads %v", tc.tsName, got, want)
		}
	}
}

// goJSONFields lists the JSON names a value marshals with, including the fields
// of an embedded struct, sorted so declaration order on one side is not a gate.
func goJSONFields(t *testing.T, value any) []string {
	t.Helper()

	var out []string
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous {
				inner := field.Type
				if inner.Kind() == reflect.Pointer {
					inner = inner.Elem()
				}
				walk(inner)
				continue
			}
			tag := field.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			out = append(out, strings.Split(tag, ",")[0])
		}
	}
	walk(reflect.TypeOf(value))

	sort.Strings(out)
	return out
}

// tsInterfaceFields reads one interface out of the renderer's store.ts. The
// interface bodies hold one field per line, which is what the project's style
// keeps them to; a body that stops doing so fails loudly rather than silently.
func tsInterfaceFields(t *testing.T, name string) []string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate this test file")
	}
	store := filepath.Join(filepath.Dir(file), "..", "..", "gui", "src", "store.ts")

	payload, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read %s: %v", store, err)
	}

	body, ok := between(string(payload), "export interface "+name+" {", "\n}")
	if !ok {
		t.Fatalf("%s has no interface %s", store, name)
	}

	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") {
			continue
		}
		field, _, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("%s.%s has a line that is not a field: %q", name, store, line)
		}
		out = append(out, strings.TrimSuffix(strings.TrimSpace(field), "?"))
	}

	sort.Strings(out)
	return out
}

// between returns the text after start up to the first end.
func between(text, start, end string) (string, bool) {
	_, rest, ok := strings.Cut(text, start)
	if !ok {
		return "", false
	}
	body, _, ok := strings.Cut(rest, end)
	return body, ok
}

func boolPtr(v bool) *bool { return &v }

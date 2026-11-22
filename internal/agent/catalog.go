// Package agent is the catalog of agents this build can start: the presets it
// ships, the adapter files a user added, and the switches that decide which of
// them the GUI offers. It holds no handles: a preset is only the command and
// mode a session start would use, so the catalog never spawns anything.
package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// ErrNoAgent reports an agent id the catalog does not hold.
var ErrNoAgent = ipc.Class(errors.New("agent: no such agent"), ipc.CodeNoAgent)

// ErrBadPreset reports an adapter file that cannot define an agent.
var ErrBadPreset = ipc.Class(errors.New("agent: invalid preset"), ipc.CodeBadRequest)

// ErrBadSwitches reports a switch file that cannot be decoded. It is exported
// because it is recoverable: a write replaces the file rather than failing on
// it forever, and the error is only what a reader reports until then.
var ErrBadSwitches = errors.New("agent: invalid switch file")

// BuiltinSource is the Source an entry has when this build ships its preset.
const BuiltinSource = "builtin"

// Preset is one agent definition: what to run, how, and what it needs beyond
// its binary.
type Preset struct {
	// ID names the preset in a session request and in a switch file.
	ID string `json:"id"`
	// Name is the display name; empty means ID.
	Name string `json:"name,omitempty"`
	// Command and Args launch the agent.
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// Mode is how the agent is driven. Empty means acp.
	Mode session.Mode `json:"mode,omitempty"`
	// Needs names what the agent requires besides its binary, such as a login.
	Needs string `json:"needs,omitempty"`
	// Description is one line the picker can show.
	Description string `json:"description,omitempty"`
	// Adapter is the operational knowledge a terminal session needs: how the
	// agent looks when it is waiting, how it reads a paste, and what it takes to
	// start or resume without a dialog. It is data because a new agent should be
	// a new file, not a new branch in the host.
	//
	// It never crosses the wire. Approval-UI recognition is heuristic by nature,
	// and FLOTILLA.md keeps it at the PTY adapter boundary: a renderer that
	// received the patterns could start classifying lines itself.
	Adapter adapters.Adapter `json:"-"`

	// source is where the preset came from: a builtin, or an adapter file name.
	source string
}

// Entry is one agent as the GUI sees it: the preset, whether this machine has
// it, and the user's switches.
type Entry struct {
	Preset
	// Source is BuiltinSource or the adapter file that defined the preset.
	Source string `json:"source"`
	// Installed is true when Command resolves on this machine's PATH.
	Installed bool `json:"installed"`
	// Path is what the command resolved to, when it did.
	Path string `json:"path,omitempty"`
	// Enabled, Chat and CLI are the user's switches; a disabled agent stays listed.
	Enabled bool `json:"enabled"`
	Chat    bool `json:"chat"`
	CLI     bool `json:"cli"`
}

// Change is one switch update, and the stored form keyed by preset id in
// agents.json: a nil field keeps the value the entry already has.
type Change struct {
	Enabled *bool `json:"enabled,omitempty"`
	Chat    *bool `json:"chat,omitempty"`
	CLI     *bool `json:"cli,omitempty"`
}

// Listing is what the daemon reports: the agents in display order, plus the
// adapter files it could not read.
type Listing struct {
	Agents   []Entry  `json:"agents"`
	Problems []string `json:"problems,omitempty"`
}

// Catalog is the presets this build knows and the switches on them.
type Catalog struct {
	dir      string
	mu       sync.Mutex
	presets  []Preset
	problems []string
	lookPath func(string) (string, error)
	// selfDir is where this build's own agent binaries sit: the directory of the
	// executable, which is where a release puts them beside the daemon. A preset
	// for an agent this build ships is found there when PATH does not have it,
	// because a bundled binary is not installed by the user.
	selfDir string
}

// New returns the catalog rooted at dir: adapters live in dir/adapters and the
// user's switches in dir/agents.json. Nothing is created until a switch is set.
func New(dir string) *Catalog {
	c := &Catalog{dir: dir, lookPath: exec.LookPath, selfDir: executableDir()}
	c.load()
	return c
}

// executableDir is the directory of this process's binary, or empty when it
// cannot be known. An empty value makes every bundled lookup fail, which is the
// cautious outcome: an agent this build did not ship stays missing.
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// bundledCommands lists the agent binaries a release carries beside the daemon.
// It is the whole of what "this build ships" means, and it is deliberately a
// list rather than every preset: the other presets name a vendor CLI the user
// installed, and looking beside the executable for one of those would let a
// stray file shadow the copy on PATH.
//
// gui/scripts/bundle-binaries.mjs copies these into the app's resources, so a
// name added here that the packaging does not carry is an agent the picker
// offers and cannot start. TestEveryShippedBinaryIsAPresetThePackagingCarries
// holds the lists together.
var bundledCommands = []string{"flt-replay-agent"}

// Bundled resolves one of this build's own agents as it ships: a bare command
// name is looked for beside the executable, with the platform's extension
// appended as well. It is exported because `flt probe` offers the same names and
// has to find them the same way — a probe that only asked PATH would offer an
// agent and then fail on every install.
//
// Only an agent this build actually travels with is looked up, and only by a
// bare name. Anything else is the user's own install: PATH is the one place it
// lives, so a file that happens to share its name cannot shadow it. And a
// command that names a directory, absolute or relative, is the caller's to run:
// joining it under the binary directory would quietly rewrite a path the user
// typed.
func Bundled(command string) (string, bool) {
	if !shipped(command) {
		return "", false
	}
	return besideDir(executableDir(), command)
}

// bundled finds one of this build's own agents next to the executable, using the
// directory this catalog was built with. The rule lives in Bundled so `flt
// probe` resolves the same names the same way; this method only supplies the
// directory, which tests replace.
func (c *Catalog) bundled(command string) (string, bool) {
	if !shipped(command) {
		return "", false
	}
	return besideDir(c.selfDir, command)
}

// besideDir looks for command inside dir, with the platform's extension appended
// as well. An empty dir, an empty name, or a name that is not bare finds
// nothing: nothing is guessed about where a caller's own files live.
func besideDir(dir, command string) (string, bool) {
	if dir == "" || command == "" || command != filepath.Base(command) {
		return "", false
	}
	candidate := filepath.Join(dir, command)
	for _, path := range []string{candidate + ".exe", candidate} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

// shipped reports whether command names an agent binary that travels with this
// build. It is the gate that keeps the lookup honest: beside-the-executable is
// the right place only for a binary the build itself carries, because every
// other agent is installed by the user and PATH is where it is found.
//
// The comparison is case-insensitive for the same reason AdapterForCommand's is:
// a user on Windows may type the capitalisation the catalogue does not.
func shipped(command string) bool {
	if command == "" {
		return false
	}
	for _, bundled := range bundledCommands {
		if strings.EqualFold(commandBase(bundled), commandBase(command)) {
			return true
		}
	}
	return false
}

// List projects every preset with this machine's and the user's answers. The
// commands are resolved on every call: an install can land while the window is
// open. The lock covers the snapshot only, so a slow PATH cannot hold a write.
func (c *Catalog) List() Listing {
	c.mu.Lock()
	presets := append([]Preset(nil), c.presets...)
	problems := append([]string(nil), c.problems...)
	c.mu.Unlock()

	state, err := readSwitches(c.switchPath())
	if err != nil {
		problems = append(problems, err.Error())
		state = nil
	}

	list := Listing{Agents: make([]Entry, 0, len(presets)), Problems: problems}
	for _, preset := range presets {
		list.Agents = append(list.Agents, c.project(preset, state[preset.ID]))
	}
	return list
}

// Set applies one switch change and returns the updated entry. The write is
// atomic: a crash must not leave a switch file that hides every agent. A file
// that cannot be decoded is replaced rather than refused, because this page is
// the only place the user can repair it.
func (c *Catalog) Set(id string, change Change) (Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	preset, ok := c.find(id)
	if !ok {
		return Entry{}, fmt.Errorf("%q: %w", id, ErrNoAgent)
	}

	state, err := readSwitches(c.switchPath())
	switch {
	case errors.Is(err, ErrBadSwitches):
		// The user's other switches are worth keeping a copy of: the rewrite
		// below starts from empty, and a corrupt byte should not be the end of
		// them without a way back.
		_ = backupSwitches(c.switchPath())
		state = nil
	case err != nil:
		return Entry{}, err
	}
	if state == nil {
		state = make(map[string]Change)
	}

	set := state[id]
	if change.Enabled != nil {
		set.Enabled = change.Enabled
	}
	if change.Chat != nil {
		set.Chat = change.Chat
	}
	if change.CLI != nil {
		set.CLI = change.CLI
	}
	state[id] = set

	if err := writeSwitches(c.switchPath(), state); err != nil {
		return Entry{}, err
	}
	return c.project(preset, set), nil
}

// Builtins lists the presets this build ships, in the order the picker shows
// them. The list is a convenience, not a capability claim: what an agent can do
// is measured by `flt probe` and recorded in the support matrix, which lives
// outside this tree.
func Builtins() []Preset {
	return []Preset{
		{
			ID:          "replay",
			Name:        "Replay",
			Command:     "flt-replay-agent",
			Mode:        session.ModeACP,
			Needs:       "nothing: it answers from a recorded scenario",
			Description: "A recorded turn, replayed. No model, no login, no spend.",
		},
		{
			ID:          "reasonix",
			Name:        "Reasonix Studio",
			Command:     "reasonix",
			Args:        []string{"acp"},
			Mode:        session.ModeACP,
			Needs:       "a logged-in Reasonix Studio",
			Description: "Reasonix Studio over ACP, including session resume.",
		},
		{
			ID:          "opencode",
			Name:        "opencode",
			Command:     "opencode",
			Args:        []string{"acp"},
			Mode:        session.ModeACP,
			Needs:       "an opencode provider that accepts this machine's token",
			Description: "opencode over ACP.",
		},
		{
			ID:      "claude",
			Name:    "Claude Code",
			Command: "claude-agent-acp",
			Mode:    session.ModeACP,
			Needs:   "a logged-in Claude Code CLI",
			Description: "Claude Code through the claude-agent-acp bridge. The bridge's sdk wants " +
				"its own native binary; CLAUDE_CODE_EXECUTABLE points it at the working CLI, " +
				"resolved on PATH at spawn.",
			Adapter: adapters.Adapter{
				Env: map[string]string{"CLAUDE_CODE_EXECUTABLE": "claude"},
			},
		},
		{
			ID:          "gemini",
			Name:        "Gemini CLI",
			Command:     "gemini",
			Args:        []string{"--acp"},
			Mode:        session.ModeACP,
			Needs:       "a logged-in Gemini CLI",
			Description: "Gemini CLI in ACP mode.",
		},
	}
}

// load rebuilds the presets from disk. It runs once, at construction: an adapter
// edited while the daemon runs lands on the next start.
func (c *Catalog) load() {
	c.presets = Builtins()
	for i := range c.presets {
		c.presets[i].source = BuiltinSource
	}

	adapters, problems := loadAdapters(filepath.Join(c.dir, "adapters"))
	c.presets = merge(c.presets, adapters)
	c.problems = problems
}

func (c *Catalog) find(id string) (Preset, bool) {
	for _, preset := range c.presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return Preset{}, false
}

// AdapterForCommand reports the knowledge declared for the agent behind a
// command. A start request names a command, not a catalogue id — the form lets
// the user type one — so this is how the daemon finds the definition that
// belongs to it. The first preset whose command matches wins, which is stable
// because the catalogue order is.
//
// A caller may name the command by an absolute path, or with a Windows
// extension: the form accepts a free-text command, and a definition that stops
// matching because the user pasted `C:\tools\claude.cmd` would silently lose its
// blocked detection. The program name is therefore compared too, case-insensitively
// because a Windows user may type the capitalisation the catalogue does not.
//
// An exact command match wins over a program-name match: two presets can share a
// binary name (a wrapper and the tool it wraps), and the one the caller named
// outright is the one they meant.
func (c *Catalog) AdapterForCommand(command string) (adapters.Adapter, bool) {
	want := strings.TrimSpace(command)
	if want == "" {
		return adapters.Adapter{}, false
	}
	base := commandBase(want)

	c.mu.Lock()
	presets := append([]Preset(nil), c.presets...)
	c.mu.Unlock()

	for _, preset := range presets {
		if !preset.Adapter.Empty() && preset.Command == want {
			return preset.Adapter, true
		}
	}
	for _, preset := range presets {
		if !preset.Adapter.Empty() && strings.EqualFold(commandBase(preset.Command), base) {
			return preset.Adapter, true
		}
	}
	return adapters.Adapter{}, false
}

// Resolve turns a command into what a spawn should execute. A command this
// build actually ships is looked for beside the executable first, because a
// bundled agent is not installed by the user and exec does not search there.
// Anything else is handed back unchanged: a vendor CLI is the user's install, so
// PATH and an absolute path they typed keep working exactly as they did, and a
// file that happens to share its name cannot shadow it.
func (c *Catalog) Resolve(command string) string {
	if path, ok := c.bundled(command); ok {
		return path
	}
	return command
}

// commandBase is the program name a command names, without its directory or the
// Windows extension a shell would add.
func commandBase(command string) string {
	base := command
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	return base
}

// project answers one preset for the GUI. A preset's mode is its recipe, so it
// decides the purpose: an acp preset can serve chat sessions and never terminal
// ones, whatever a switch file holds. Reading the stored value only for the
// purpose the mode can serve is what keeps the settings page from showing a
// switch that is on, disabled and impossible to clear.
func (c *Catalog) project(preset Preset, set Change) Entry {
	acp := preset.Mode == session.ModeACP
	entry := Entry{
		Preset:  preset,
		Source:  preset.source,
		Enabled: true,
		Chat:    acp,
		CLI:     !acp,
	}
	if set.Enabled != nil {
		entry.Enabled = *set.Enabled
	}
	if acp && set.Chat != nil {
		entry.Chat = *set.Chat
	}
	if !acp && set.CLI != nil {
		entry.CLI = *set.CLI
	}

	// A command that does not resolve is not an error: the entry says the agent
	// is not installed, which is what the settings page shows and what the picker
	// uses to keep an unavailable agent out of the way.
	//
	// A bundled agent is looked for beside the executable first, then on PATH: a
	// release ships its own agents there, and a user who also installed one on
	// PATH should still get the one the build was tested with. A vendor CLI is
	// not looked for there, so the entry points at the copy the user installed.
	if path, ok := c.bundled(preset.Command); ok {
		entry.Installed = true
		entry.Path = path
	} else if path, err := c.lookPath(preset.Command); err == nil {
		entry.Installed = true
		entry.Path = path
	}
	return entry
}

func (c *Catalog) switchPath() string {
	return filepath.Join(c.dir, "agents.json")
}

// loadAdapters reads every *.json in dir. ReadDir sorts by name, so one file
// overriding another is deterministic. A file that cannot define an agent is
// reported and skipped: one bad adapter must not empty the picker.
func loadAdapters(dir string) ([]Preset, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read the adapter directory %q: %v", dir, err)}
	}

	var (
		out      []Preset
		problems []string
	)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		preset, err := readAdapter(path)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, preset)
	}
	return out, problems
}

func readAdapter(path string) (Preset, error) {
	name := filepath.Base(path)

	payload, err := os.ReadFile(path)
	if err != nil {
		return Preset{}, fmt.Errorf("read adapter %q: %w", name, err)
	}

	preset, err := decodeAdapter(payload)
	if err != nil {
		return Preset{}, fmt.Errorf("decode adapter %q: %w", name, err)
	}

	// The file name is the id an adapter does not name itself by, so one file
	// stays one agent even when its body forgot the id.
	if strings.TrimSpace(preset.ID) == "" {
		preset.ID = strings.TrimSuffix(name, filepath.Ext(name))
	}
	if err := normalize(&preset); err != nil {
		return Preset{}, fmt.Errorf("adapter %q: %w", name, err)
	}
	preset.source = name
	return preset, nil
}

// adapterFile is the on-disk shape of an adapter: one flat object holding the
// launch recipe and the operational knowledge. It is a separate type because
// Preset hides Adapter from JSON — the wire must not carry heuristics — while the
// file a user edits writes both in one place.
type adapterFile struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Command     string       `json:"command"`
	Args        []string     `json:"args"`
	Mode        session.Mode `json:"mode"`
	Needs       string       `json:"needs"`
	Description string       `json:"description"`

	Blocked       []string       `json:"blocked"`
	Paste         adapters.Paste `json:"paste"`
	TrustArgs     []string       `json:"trust_args"`
	ResumeArgs    []string       `json:"resume_args"`
	ResumeCommand string         `json:"resume_command"`
}

// decodeAdapter reads one adapter file strictly. A field this build does not
// know is an error rather than a silent no-op: a typo in "blocked" would leave
// the agent looking busy while it waits for an answer, and nothing else would say
// so.
func decodeAdapter(payload []byte) (Preset, error) {
	var file adapterFile

	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return Preset{}, fmt.Errorf("decode the adapter: %w", err)
	}

	return Preset{
		ID:          file.ID,
		Name:        file.Name,
		Command:     file.Command,
		Args:        file.Args,
		Mode:        file.Mode,
		Needs:       file.Needs,
		Description: file.Description,
		Adapter: adapters.Adapter{
			Blocked:       file.Blocked,
			Paste:         file.Paste,
			TrustArgs:     file.TrustArgs,
			ResumeArgs:    file.ResumeArgs,
			ResumeCommand: file.ResumeCommand,
		},
	}, nil
}

// normalize refuses what a session start could not use. Fields an adapter leaves
// out stay empty on purpose: an adapter that overrides a builtin keeps that
// preset's name, mode and login note unless it states its own.
func normalize(p *Preset) error {
	p.ID = strings.TrimSpace(p.ID)
	p.Command = strings.TrimSpace(p.Command)

	if p.ID == "" {
		return fmt.Errorf("empty id: %w", ErrBadPreset)
	}
	if p.Command == "" {
		return fmt.Errorf("%s: empty command: %w", p.ID, ErrBadPreset)
	}
	if p.Mode != "" && !p.Mode.Valid() {
		return fmt.Errorf("%s: mode %q is neither %s nor %s: %w", p.ID, p.Mode, session.ModeACP, session.ModePTY, ErrBadPreset)
	}
	if err := p.Adapter.Validate(); err != nil {
		return fmt.Errorf("%s: %w: %w", p.ID, ErrBadPreset, err)
	}
	return nil
}

// merge adds the adapters to the builtins. An adapter that names a builtin id
// replaces it in place, which is how a user corrects a command without losing
// the picker's order. A field the adapter leaves out keeps the builtin's value,
// so overriding a command does not silently reset the name, mode or login note.
func merge(builtin, overrides []Preset) []Preset {
	out := append([]Preset(nil), builtin...)
	for _, adapter := range overrides {
		replaced := false
		for i := range out {
			if out[i].ID == adapter.ID {
				out[i] = inherit(out[i], adapter)
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, defaults(adapter))
		}
	}
	return out
}

// inherit fills an overriding adapter's blanks from the preset it replaces. A
// field is blank when JSON left it nil; an explicit `"args": []` is not blank, so
// a wrapper can drop the inherited arguments on purpose.
func inherit(base, adapter Preset) Preset {
	if adapter.Name == "" {
		adapter.Name = base.Name
	}
	if adapter.Args == nil {
		adapter.Args = base.Args
	}
	if adapter.Mode == "" {
		adapter.Mode = base.Mode
	}
	if adapter.Needs == "" {
		adapter.Needs = base.Needs
	}
	if adapter.Description == "" {
		adapter.Description = base.Description
	}
	// The operational knowledge inherits field by field too: correcting a
	// command must not silently drop the patterns that recognise a blocked
	// agent, or the session would look working while it waits for an answer.
	adapter.Adapter = adapters.Inherit(base.Adapter, adapter.Adapter)
	return adapter
}

// defaults fills a new preset's blanks with what a session start needs.
func defaults(adapter Preset) Preset {
	if adapter.Name == "" {
		adapter.Name = adapter.ID
	}
	if adapter.Mode == "" {
		adapter.Mode = session.ModeACP
	}
	return adapter
}

func readSwitches(path string) (map[string]Change, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent switches: %w", err)
	}

	var state map[string]Change
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrBadSwitches, err)
	}
	return state, nil
}

// backupSwitches keeps a copy of a file that cannot be decoded, so the rewrite
// that follows does not destroy switches the user may still want. The copy
// holds the most recent corruption: a second one replaces the first. A backup
// that cannot be written is ignored: the rewrite is the repair, the copy is a
// courtesy.
func backupSwitches(path string) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read the switch file to back it up: %w", err)
	}
	if err := os.WriteFile(path+".corrupt", payload, 0o600); err != nil {
		return fmt.Errorf("back up the switch file: %w", err)
	}
	return nil
}

func writeSwitches(path string, state map[string]Change) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the agent config directory: %w", err)
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent switches: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".agents-*.json")
	if err != nil {
		return fmt.Errorf("create a temporary switch file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write agent switches: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close agent switches: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("store agent switches: %w", err)
	}
	return nil
}

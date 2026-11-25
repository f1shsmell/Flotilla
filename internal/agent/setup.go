package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// ErrNoRecipe reports a plan this build cannot carry out.
var ErrNoRecipe = errors.New("agent: nothing in the recipe this build can install")

// The two ways a recipe installs: a package runner fetches the pin at launch,
// or an archive is downloaded and checked now. They are unexported because no
// client reads a plan, and an exported constant would enter the wire contract.
const (
	kindPackage = "package"
	kindBinary  = "binary"
)

// adapterName accepts an id as a file name: the ids this build ships and the
// registry publishes are lowercase words with dots and dashes, and an id that
// is not is refused rather than rewritten, so the file a user reads is named
// after the agent they asked for.
var adapterName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// InstallDir is where installed agents live under a state root's config
// directory: one directory per agent id, holding what its archive unpacked.
func InstallDir(configDir string) string {
	return filepath.Join(configDir, "agents")
}

// installedCommand reports an absolute command that names a startable file. An
// installed archive names its command this way, so it is never on PATH: the
// file is the whole answer, and a directory named as one is not.
func installedCommand(command string) (string, bool) {
	if !filepath.IsAbs(command) {
		return "", false
	}
	info, err := os.Stat(command)
	if err != nil || info.IsDir() {
		return "", false
	}
	return command, true
}

// Plan is one agent's install as a command reports it, so the user can see what
// would be fetched and run before anything is.
type Plan struct {
	// ID and Name identify the agent; the adapter file is named after ID.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Recipe is the one line a terminal shows.
	Recipe string `json:"recipe"`
	// Kind is kindPackage or kindBinary: a package recipe is written as an
	// adapter and fetched by its runner at launch, a binary is downloaded now.
	Kind string `json:"kind"`
	// Command is the command the adapter will launch.
	Command string `json:"command"`
	// Args, Env and Platform restate the recipe for a binary; Platform is empty
	// for a package.
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Platform string            `json:"platform,omitempty"`
	// Archive and SHA256 are what a binary install fetches and checks; SHA256 is
	// empty when the publisher provides none, which the user has to accept.
	Archive string `json:"archive,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	// ArchiveCmd is the command inside the archive, as the recipe spells it.
	// It is not shown: a plan shows what it downloads, not its internals.
	ArchiveCmd string `json:"-"`
}

// IsPackage reports whether the plan writes an adapter for a package runner.
func (p Plan) IsPackage() bool { return p.Kind == kindPackage }

// IsBinary reports whether the plan downloads and unpacks an archive.
func (p Plan) IsBinary() bool { return p.Kind == kindBinary }

// Installed is one finished install: where the command landed and the adapter
// file that makes it a preset.
type Installed struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Adapter  string `json:"adapter"`
	Verified bool   `json:"verified"`
}

// Installer runs one plan. It writes exactly two kinds of things: an agent
// directory under config/agents, and one adapter file under config/adapters.
// Every attempt downloads into a fresh staging directory, so a failed hash
// leaves nothing a later attempt or a launch could mistake for a program.
//
// Install replaces an install directory and an adapter file that already
// exist. The caller decides whether the user consented to that: a command
// refuses without --force, and the engine is not the place that remembers it.
type Installer struct {
	configDir string
	client    *http.Client
}

// NewInstaller returns an installer writing under configDir.
func NewInstaller(configDir string, client *http.Client) *Installer {
	if client == nil {
		client = http.DefaultClient
	}
	return &Installer{configDir: configDir, client: client}
}

// PlanFor decides what installing one id would do. presets is the catalog
// merged with the published directory's cached entries, so one id is one
// install however the agent reached this build. Nothing is fetched: a plan is
// what the user confirms.
func PlanFor(presets []Preset, id string) (Plan, error) {
	want := strings.ToLower(strings.TrimSpace(id))
	if want == "" {
		return Plan{}, fmt.Errorf("%q: %w", id, ErrNoAgent)
	}
	for _, preset := range presets {
		if strings.ToLower(preset.ID) == want {
			return planForPreset(preset)
		}
	}
	return Plan{}, fmt.Errorf("%q: %w", id, ErrNoAgent)
}

// planForPreset decides what installing one preset does on this machine.
func planForPreset(preset Preset) (Plan, error) {
	plan := Plan{ID: preset.ID, Name: preset.Name, Recipe: preset.Install.Summary()}
	if plan.Name == "" {
		plan.Name = plan.ID
	}

	platform := Platform(runtime.GOOS, runtime.GOARCH)
	binary, hasBinary := BinariesFor(preset.Install.Binary, runtime.GOOS, runtime.GOARCH)
	asBinary := func() {
		plan.Kind, plan.Platform = kindBinary, platform
		plan.Archive, plan.SHA256 = binary.Archive, binary.SHA256
		plan.ArchiveCmd, plan.Args, plan.Env = binary.Cmd, binary.Args, binary.Env
		plan.Command = binary.Cmd
	}

	switch {
	// A hashed archive is the one recipe whose bytes this build can check before
	// anything runs, so it wins over a runner that fetches unverified at launch.
	case hasBinary && binary.SHA256 != "":
		asBinary()
	case preset.Install.Npx != nil:
		plan.Kind, plan.Command = kindPackage, "npx"
		plan.Args, plan.Env = preset.Install.Npx.Args, preset.Install.Npx.Env
		plan.Args = append([]string{"-y", preset.Install.Npx.Package}, plan.Args...)
	case preset.Install.Uvx != nil:
		plan.Kind, plan.Command = kindPackage, "uvx"
		plan.Args, plan.Env = preset.Install.Uvx.Args, preset.Install.Uvx.Env
		plan.Args = append([]string{preset.Install.Uvx.Package}, plan.Args...)
	case hasBinary:
		asBinary()
	case len(preset.Install.Binary) > 0:
		return Plan{}, fmt.Errorf("%s: the recipe has no archive for %s: %w: %w", preset.ID, platform, ErrNoRecipe, ErrNoArchive)
	default:
		return Plan{}, fmt.Errorf("%s: %w", preset.ID, ErrNoRecipe)
	}
	if !adapterName.MatchString(plan.ID) {
		return Plan{}, fmt.Errorf("%q is not an id this build can name an adapter file after: %w", plan.ID, ErrBadPreset)
	}
	return plan, nil
}

// Install runs one plan. A failure before the adapter is written leaves the
// previous install, if any, in place: the staging directory is removed, the
// adapter is only written after the archive has been verified and moved.
func (in *Installer) Install(ctx context.Context, plan Plan, options Options) (Installed, error) {
	if plan.Kind == kindPackage {
		return in.installPackage(plan)
	}
	return in.installBinary(ctx, plan, options)
}

// installPackage writes the adapter that launches a package runner. Nothing is
// fetched ahead of time: npx and uvx fetch at first launch, which is why the
// recipe is pinned and why the user confirmed it.
func (in *Installer) installPackage(plan Plan) (Installed, error) {
	installed := Installed{ID: plan.ID, Command: plan.Command}
	installed.Adapter = filepath.Join(in.configDir, "adapters", plan.ID+".json")

	file := adapterFile{ID: plan.ID, Name: plan.Name, Command: plan.Command, Args: plan.Args, Env: plan.Env}
	if err := storeAdapter(installed.Adapter, file); err != nil {
		return Installed{}, err
	}
	return installed, nil
}

// installBinary fetches, verifies and unpacks one platform's archive, then
// writes the adapter that names the command it landed.
func (in *Installer) installBinary(ctx context.Context, plan Plan, options Options) (Installed, error) {
	dir := filepath.Join(InstallDir(in.configDir), plan.ID)
	stage := filepath.Dir(dir)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return Installed{}, fmt.Errorf("create the staging directory: %w", err)
	}

	staging, err := os.MkdirTemp(stage, plan.ID+"-")
	if err != nil {
		return Installed{}, fmt.Errorf("create a staging directory: %w", err)
	}
	// A staging directory is either renamed into place or removed whole.
	defer func() { _ = os.RemoveAll(staging) }()

	recipe := adapters.Binary{
		Archive: plan.Archive,
		Cmd:     plan.ArchiveCmd,
		Args:    plan.Args,
		Env:     plan.Env,
		SHA256:  plan.SHA256,
	}
	// The staging directory is the archive's own root, and it becomes the
	// install directory: no reader ever sees a half-extracted agent, and a
	// failed install leaves the previous one whole.
	command, err := InstallArchive(ctx, in.client, recipe, staging, options)
	if err != nil {
		return Installed{}, err
	}

	if err := replaceDir(dir, staging); err != nil {
		return Installed{}, err
	}
	rel, err := filepath.Rel(staging, command)
	if err != nil {
		return Installed{}, fmt.Errorf("locate %s inside the install: %w", command, err)
	}
	final := filepath.Join(dir, rel)

	adapter := filepath.Join(in.configDir, "adapters", plan.ID+".json")
	file := adapterFile{ID: plan.ID, Name: plan.Name, Command: final, Args: plan.Args, Env: plan.Env}
	if err := storeAdapter(adapter, file); err != nil {
		return Installed{}, err
	}
	return Installed{ID: plan.ID, Command: final, Adapter: adapter, Verified: plan.SHA256 != ""}, nil
}

// replaceDir puts next at dir, keeping what dir held until next has landed: a
// rename that fails after the old directory was moved aside puts it back.
func replaceDir(dir, next string) error {
	previous := next + ".previous"
	replaced := false
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, previous); err != nil {
			return fmt.Errorf("move the previous install aside: %w", err)
		}
		replaced = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if err := os.Rename(next, dir); err != nil {
		if replaced {
			if restore := os.Rename(previous, dir); restore != nil {
				return fmt.Errorf("move the install into place: %w; the previous install stays at %s: %w", err, previous, restore)
			}
		}
		return fmt.Errorf("move the install into place: %w", err)
	}
	if replaced {
		// The install is complete; a leftover copy is a better outcome than a
		// reported failure on a working install.
		_ = os.RemoveAll(previous)
	}
	return nil
}

// storeAdapter stores the adapter file that makes a finished install startable.
// It is written atomically: a catalog load sees the previous file or the new
// one, never half of either.
func storeAdapter(path string, file adapterFile) error {
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the adapter: %w", err)
	}
	payload = append(payload, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the adapter directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".adapter-*.json")
	if err != nil {
		return fmt.Errorf("create a temporary adapter file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write the adapter: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write the adapter: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store the adapter: %w", err)
	}
	return nil
}

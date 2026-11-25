package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/registry"
)

// installTimeout bounds one install. A binary archive is downloaded and
// unpacked in one request, so the bound covers the whole download, not one
// call.
const installTimeout = 10 * time.Minute

// errNoConsent reports an install the user did not confirm: nothing was
// fetched and nothing was written.
var errNoConsent = errors.New("no confirmation, nothing was installed")

// installCommand is `flt install`: it prints the plan, asks, and only then
// runs it. --yes is for a caller that has already decided; a binary the
// publisher gave no hash for needs --allow-unverified on top of that.
func installCommand(args []string, channel string, asJSON bool, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("flt install", flag.ContinueOnError)
	fs.SetOutput(out)
	yes := fs.Bool("yes", false, "install without asking; the plan is still printed")
	force := fs.Bool("force", false, "replace an install of the same agent that already exists")
	allowUnverified := fs.Bool("allow-unverified", false, "accept a binary archive whose publisher provides no sha256")
	fs.Usage = func() {
		_, _ = fmt.Fprint(out, "usage: flt [--json] install <agent> [--yes] [--force] [--allow-unverified]\n\n"+
			"Prints what the install would fetch and run, asks for confirmation, then\n"+
			"writes an adapter under config/adapters and, for a binary recipe, unpacks\n"+
			"the verified archive under config/agents.\n\n"+
			"  --yes               skip the question; for a caller that already decided\n"+
			"  --force             replace an install of the same agent that already exists\n"+
			"  --allow-unverified  accept an archive the publisher gave no sha256 for\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("install takes exactly one agent id, got %q: flags go before the id", fs.Args())
	}

	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	presets, problems, cacheErr := installable(paths.Config, paths.Cache)
	plan, err := agent.PlanFor(presets, fs.Arg(0))
	if err != nil {
		if cacheErr != nil && errors.Is(err, agent.ErrNoAgent) {
			return fmt.Errorf("%w (the published directory could not be read: %w; run `flt registry refresh`)", err, cacheErr)
		}
		return fmt.Errorf("plan the install: %w", err)
	}

	if asJSON && !*yes {
		// A machine format has nowhere to ask a question, and a prompt printed
		// into it would corrupt the answer a caller is parsing.
		return errors.New("install --json needs --yes: machine output cannot answer a prompt")
	}
	if err := refuseExisting(plan, paths.Config, *force); err != nil {
		return err
	}
	if !asJSON {
		for _, problem := range problems {
			if err := print(out, "! "+problem); err != nil {
				return err
			}
		}
		for _, line := range planLines(plan, paths.Config) {
			if err := print(out, line); err != nil {
				return err
			}
		}
	}

	if plan.IsBinary() && plan.SHA256 == "" && !*allowUnverified {
		return fmt.Errorf("%s: the publisher provides no sha256, and this build will not fetch an unverified archive: %w",
			plan.ID, agent.ErrUnverified)
	}
	if !*yes {
		agreed, err := ask(stdin, out)
		if err != nil {
			return err
		}
		if !agreed {
			return errNoConsent
		}
	}

	return runInstall(plan, *allowUnverified, asJSON, paths.Config, out)
}

// ErrInstalled reports an install that would replace one already on disk.
var ErrInstalled = errors.New("agent: already installed")

// refuseExisting keeps a second install of the same agent from overwriting the
// first without being asked: the adapter may be one the user wrote by hand, and
// an install must not be the thing that discovers that by destroying it.
func refuseExisting(plan agent.Plan, configDir string, force bool) error {
	if force {
		return nil
	}
	adapter := filepath.Join(configDir, "adapters", plan.ID+".json")
	dir := filepath.Join(agent.InstallDir(configDir), plan.ID)
	for _, path := range []string{adapter, dir} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists: pass --force to replace it: %w", path, ErrInstalled)
		}
	}
	return nil
}

// ask puts the one question an install asks. A closed stdin reads as an empty
// answer, which is a no: an install runs code the user did not write, so the
// default is the answer that writes nothing.
func ask(stdin io.Reader, out io.Writer) (bool, error) {
	if stdin == nil {
		return false, nil
	}
	if _, err := fmt.Fprint(out, "install? [y/N] "); err != nil {
		return false, fmt.Errorf("ask the question: %w", err)
	}
	reader := bufio.NewReader(stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read the answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// runInstall carries out a confirmed plan and prints what it landed: the
// adapter the catalog reads next, and the command a session would start.
func runInstall(plan agent.Plan, allowUnverified, asJSON bool, configDir string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()

	installed, err := agent.NewInstaller(configDir, nil).Install(ctx, plan, agent.Options{AllowUnverified: allowUnverified})
	if err != nil {
		return fmt.Errorf("install %s: %w", plan.ID, err)
	}

	if asJSON {
		report := struct {
			agent.Installed
			Kind string `json:"kind"`
		}{Installed: installed, Kind: plan.Kind}
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the install: %w", err)
		}
		return print(out, string(payload))
	}

	lines := []string{fmt.Sprintf("installed %s %s as %s %s",
		plan.ID, plan.Name, plan.Command, strings.Join(plan.Args, " "))}
	if plan.IsBinary() {
		lines = []string{fmt.Sprintf("installed %s %s at %s", plan.ID, plan.Name, installed.Command)}
		if installed.Verified {
			lines = append(lines, "  the archive matches the sha256 the recipe carries")
		} else {
			lines = append(lines, "  no sha256 was published for this archive: its bytes were not verified")
		}
	} else {
		lines = append(lines,
			"  the runner fetches the pinned package at the agent's first launch, not now",
			"  no sha256 covers the package: its registry is trusted to serve the pin",
		)
	}
	lines = append(lines,
		fmt.Sprintf("  adapter %s", installed.Adapter),
		"  the daemon reads it on its next start: restart it, or relaunch the app")
	for _, line := range lines {
		if err := print(out, line); err != nil {
			return err
		}
	}
	return nil
}

// planLines render one plan as the terminal shows it before asking. For a
// binary the archive and hash lines are the confirmation's substance: they say
// what would be downloaded and what would check it.
func planLines(plan agent.Plan, configDir string) []string {
	lines := []string{
		fmt.Sprintf("install %s (%s)", plan.Name, plan.ID),
		fmt.Sprintf("  recipe  %s", plan.Recipe),
	}
	switch {
	case plan.IsPackage():
		lines = append(lines,
			fmt.Sprintf("  command %s %s", plan.Command, strings.Join(plan.Args, " ")),
			"  the runner fetches the pinned package at first launch")
	default:
		lines = append(lines,
			fmt.Sprintf("  archive %s", plan.Archive),
			fmt.Sprintf("  platform %s", plan.Platform),
			fmt.Sprintf("  target  %s", filepath.Join(agent.InstallDir(configDir), plan.ID)))
		if plan.SHA256 == "" {
			lines = append(lines, "  sha256  none: the publisher provides none, so nothing could verify it")
		} else {
			lines = append(lines, fmt.Sprintf("  sha256  %s", plan.SHA256))
		}
	}
	adapter := filepath.Join(configDir, "adapters", plan.ID+".json")
	lines = append(lines, fmt.Sprintf("  adapter %s", adapter))
	if _, err := os.Stat(adapter); err == nil {
		lines = append(lines, "  replaces the adapter there: its blocked, paste and resume settings are not kept")
	}
	return lines
}

// installable is the set a plan can choose from: the presets this build ships,
// the adapter files the user added, and the published directory's cached
// entries mapped into the same preset shape. A missing cache narrows what can
// be installed and is reported only when the id was not found without it.
func installable(configDir, cacheDir string) (presets []agent.Preset, problems []string, cacheErr error) {
	listing := agent.New(configDir).List()
	presets = make([]agent.Preset, 0, len(listing.Agents))
	for _, entry := range listing.Agents {
		presets = append(presets, entry.Preset)
	}
	if cacheDir == "" {
		return presets, listing.Problems, nil
	}

	cached, err := registry.Load(registry.CachePath(cacheDir))
	if err != nil {
		return presets, listing.Problems, fmt.Errorf("read the cached agent directory: %w", err)
	}
	index := make(map[string]int, len(presets))
	for i, preset := range presets {
		index[strings.ToLower(preset.ID)] = i
	}
	for _, published := range cached.Agents {
		key := strings.ToLower(published.ID)
		if i, ok := index[key]; ok {
			// A preset this build ships carries no recipe of its own, so the
			// directory's is the only way to install an agent it also lists.
			if presets[i].Install.Empty() {
				presets[i].Install = published.Distribution
			}
			continue
		}
		presets = append(presets, agent.Preset{
			ID: published.ID, Name: published.Name, Install: published.Distribution,
		})
	}
	return presets, listing.Problems, nil
}

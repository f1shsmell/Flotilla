package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/drift"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/registry"
)

// driftTimeout bounds one drift report. Every pin is one registry request, and
// the default client's 20s bound with four in flight needs four minutes for the
// published directory; five leaves room for the catalog's own pins.
const driftTimeout = 5 * time.Minute

// errDrift is what --fail-on-drift returns when a pin is behind its registry.
// It is a report's exit code, not a finding about this machine's state.
var errDrift = errors.New("the catalog has drifted from the package registries")

// driftReport is the --json shape: every pin with its answer, plus what could
// not be checked and why.
type driftReport struct {
	Results  []drift.Result `json:"results"`
	Skipped  []string       `json:"skipped,omitempty"`
	Problems []string       `json:"problems,omitempty"`
}

// driftCommand answers every pin the catalog and the cached directory name with
// the latest version its registry publishes. Report-only: a pin is printed, not
// rewritten, and a registry that cannot answer is a failed line, not drift.
func driftCommand(args []string, channel string, asJSON, failOnDrift bool, checker drift.Checker, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("registry drift takes no arguments, got %q: flags go before the command", args)
	}

	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}
	pins, skipped, problems := collectPins(paths)

	ctx, cancel := context.WithTimeout(context.Background(), driftTimeout)
	defer cancel()
	results := checker.Check(ctx, pins)

	if asJSON {
		payload, err := json.MarshalIndent(driftReport{Results: results, Skipped: skipped, Problems: problems}, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the drift report: %w", err)
		}
		if err := print(out, string(payload)); err != nil {
			return err
		}
	} else {
		for _, line := range driftLines(results, skipped, problems) {
			if err := print(out, line); err != nil {
				return err
			}
		}
	}

	if failOnDrift && hasDrift(results) {
		return errDrift
	}
	return nil
}

// collectPins gathers the pins the catalog and the cached directory name. A
// recipe with no package pin is named as skipped, and a missing cache is a
// problem that does not stop the catalog from being checked.
func collectPins(paths home.Paths) (pins []drift.Pin, skipped, problems []string) {
	listing := agent.New(paths.Config).List()
	problems = append(problems, listing.Problems...)

	add := func(source string, install adapters.Install) {
		if install.Empty() {
			return
		}
		found := install.Pins()
		if len(found) == 0 {
			skipped = append(skipped, source)
			return
		}
		for _, pin := range found {
			pins = append(pins, drift.Pin{
				Source: source, Runner: pin.Runner, Package: pin.Package, Version: pin.Version,
			})
		}
	}
	for _, entry := range listing.Agents {
		add("catalog:"+entry.ID, entry.Install)
	}

	cached, err := registry.Load(registry.CachePath(paths.Cache))
	if err != nil {
		problems = append(problems, fmt.Sprintf("read the agent registry cache (run `flt registry refresh`): %v", err))
		return pins, skipped, problems
	}
	for _, published := range cached.Agents {
		add("registry:"+published.ID, published.Distribution)
	}
	return pins, skipped, problems
}

// driftLines renders what a terminal reads: a count first, then each pin that
// is behind, then what could not be checked. A match is counted, not printed.
func driftLines(results []drift.Result, skipped, problems []string) []string {
	var ok, drifted, failed int
	for _, result := range results {
		switch result.Status {
		case drift.StatusOK:
			ok++
		case drift.StatusDrift:
			drifted++
		case drift.StatusFailed:
			failed++
		}
	}

	noun := "pins"
	if len(results) == 1 {
		noun = "pin"
	}
	lines := []string{fmt.Sprintf("checked %d %s: %d ok, %d drift, %d failed",
		len(results), noun, ok, drifted, failed)}
	for _, result := range results {
		switch result.Status {
		case drift.StatusDrift:
			lines = append(lines, terminalText(fmt.Sprintf("- %s: %s (latest %s)",
				result.Source, pinText(result.Pin), result.Latest)))
		case drift.StatusFailed:
			lines = append(lines, terminalText(fmt.Sprintf("! %s: %s: %s",
				result.Source, pinText(result.Pin), result.Error)))
		}
	}
	for _, source := range skipped {
		lines = append(lines, terminalText(fmt.Sprintf("- %s: no package pin to check", source)))
	}
	for _, problem := range problems {
		lines = append(lines, terminalText("! "+problem))
	}
	return lines
}

// pinText renders a pin the way its recipe spells it: npm joins with '@', PyPI
// with '=='.
func pinText(pin drift.Pin) string {
	separator := "@"
	if pin.Runner == adapters.RunnerPyPI {
		separator = "=="
	}
	return fmt.Sprintf("%s %s%s%s", pin.Runner, pin.Package, separator, pin.Version)
}

// hasDrift reports whether any pin is behind its registry.
func hasDrift(results []drift.Result) bool {
	for _, result := range results {
		if result.Status == drift.StatusDrift {
			return true
		}
	}
	return false
}

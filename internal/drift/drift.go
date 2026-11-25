// Package drift reports whether the exact versions the agent recipes pin are
// still the ones their package registries publish. It is report-only: a pin is
// never rewritten, and a registry that cannot answer is a failure to show, not
// a drift to guess.
package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

const (
	// DefaultNPM is the npm registry a pin is looked up in.
	DefaultNPM = "https://registry.npmjs.org"
	// DefaultPyPI is the Python package index a pin is looked up in.
	DefaultPyPI = "https://pypi.org"
	// answerLimit caps one registry answer. Metadata larger than this is not an
	// answer this check needs, and reading it would only cost memory.
	answerLimit = 8 << 20
	// lookupsAtOnce bounds concurrent requests, so a catalog's worth of pins
	// does not open one connection per pin at once.
	lookupsAtOnce = 4
)

// DefaultClient is the HTTP client the zero Checker uses.
var DefaultClient = &http.Client{Timeout: 20 * time.Second}

// ErrLookup reports a registry answer the check could not use. A lookup failure
// is never drift: the latest version is unknown, not different.
var ErrLookup = errors.New("drift: lookup failed")

// Status is what one lookup found.
type Status string

const (
	// StatusOK means the pinned version is the latest published.
	StatusOK Status = "ok"
	// StatusDrift means a different version is the latest.
	StatusDrift Status = "drift"
	// StatusFailed means the lookup could not answer, for the reason in Error.
	StatusFailed Status = "failed"
)

// Pin is one package a recipe pins, and where the recipe came from.
type Pin struct {
	Source  string          `json:"source"`
	Runner  adapters.Runner `json:"runner"`
	Package string          `json:"package"`
	Version string          `json:"version"`
}

// Result is one pin and the answer for it.
type Result struct {
	Pin
	Latest string `json:"latest,omitempty"`
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Checker looks pins up in the package registries. The zero value asks the
// public registries; a test points NPM and PyPI at its own server.
type Checker struct {
	Client *http.Client
	// NPM and PyPI are registry base URLs; empty means the default registry.
	NPM  string
	PyPI string
}

// Check answers every pin in the caller's order. A package two recipes pin is
// looked up once, and a lookup that fails is reported against each pin.
func (c Checker) Check(ctx context.Context, pins []Pin) []Result {
	client := c.Client
	if client == nil {
		client = DefaultClient
	}
	npm := strings.TrimSuffix(c.NPM, "/")
	if npm == "" {
		npm = DefaultNPM
	}
	pypi := strings.TrimSuffix(c.PyPI, "/")
	if pypi == "" {
		pypi = DefaultPyPI
	}

	type key struct {
		runner adapters.Runner
		pkg    string
	}
	unique := make([]key, 0, len(pins))
	seen := make(map[key]struct{}, len(pins))
	for _, pin := range pins {
		k := key{pin.Runner, pin.Package}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		unique = append(unique, k)
	}

	answers := make(map[key]answer, len(unique))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, lookupsAtOnce)
	for _, k := range unique {
		wg.Add(1)
		go func(k key) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			latest, err := latestOf(ctx, client, npm, pypi, k.runner, k.pkg)

			mu.Lock()
			answers[k] = answer{latest: latest, err: err}
			mu.Unlock()
		}(k)
	}
	wg.Wait()

	results := make([]Result, 0, len(pins))
	for _, pin := range pins {
		results = append(results, resultFor(pin, answers[key{pin.Runner, pin.Package}]))
	}
	return results
}

// answer is one lookup's outcome.
type answer struct {
	latest string
	err    error
}

// resultFor turns one pin and its lookup into a result. Versions compare as
// strings: a prerelease pin is reported as drift against the stable latest,
// which is the point of a report-only check.
func resultFor(pin Pin, a answer) Result {
	result := Result{Pin: pin}
	switch {
	case a.err != nil:
		result.Status = StatusFailed
		result.Error = a.err.Error()
	case a.latest == pin.Version:
		result.Status = StatusOK
	default:
		result.Status = StatusDrift
		result.Latest = a.latest
	}
	return result
}

// latestOf asks one registry for the package's latest version.
func latestOf(ctx context.Context, client *http.Client, npm, pypi string, runner adapters.Runner, pkg string) (string, error) {
	switch runner {
	case adapters.RunnerNPM:
		endpoint := npm + "/" + url.PathEscape(pkg) + "/latest"
		var answer struct {
			Version string `json:"version"`
		}
		if err := getJSON(ctx, client, endpoint, &answer); err != nil {
			return "", err
		}
		return versionOf(endpoint, answer.Version)
	case adapters.RunnerPyPI:
		endpoint := pypi + "/pypi/" + url.PathEscape(pkg) + "/json"
		var answer struct {
			Info struct {
				Version string `json:"version"`
			} `json:"info"`
		}
		if err := getJSON(ctx, client, endpoint, &answer); err != nil {
			return "", err
		}
		return versionOf(endpoint, answer.Info.Version)
	default:
		return "", fmt.Errorf("%w: unknown package runner %q", ErrLookup, runner)
	}
}

// versionOf reads the one field the answer exists for.
func versionOf(endpoint, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("%w: %s names no version", ErrLookup, endpoint)
	}
	return version, nil
}

// getJSON reads one registry answer. A non-200 status is a lookup failure: a
// 404 means the registry does not know the package, which is information, not
// drift.
func getJSON(ctx context.Context, client *http.Client, endpoint string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLookup, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "flt-drift")

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLookup, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered %s", ErrLookup, endpoint, response.Status)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, answerLimit+1))
	if err != nil {
		return fmt.Errorf("%w: read %s: %w", ErrLookup, endpoint, err)
	}
	if len(payload) > answerLimit {
		return fmt.Errorf("%w: %s answers more than %d bytes", ErrLookup, endpoint, answerLimit)
	}
	if err := json.Unmarshal(payload, into); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrLookup, endpoint, err)
	}
	return nil
}

// Package registry caches the official ACP agent registry: the published
// directory of agents that speak ACP and how each one is installed. Nothing
// here scans a machine: a fetch lands in one file under the state root and a
// reader sees how old it is, so an offline build keeps working from the last
// copy it had.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// URL is the published registry of the latest release.
const URL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

// TTL is how old a cached copy may be before a refresh replaces it. The
// publisher moves at most hourly, which is what the daemon's loop follows.
const TTL = time.Hour

// maxBytes bounds one answer: the registry is small, and a reader that keeps
// reading a hostile or broken stream would hold the daemon in a fetch forever.
const maxBytes = 4 << 20

// DefaultClient bounds one fetch. A registry that hangs must not hang the loop
// that refreshes it.
var DefaultClient = &http.Client{Timeout: 20 * time.Second}

// ErrFetch reports that no answer arrived: the request, the connection, the
// status or the size failed before there was anything to read.
var ErrFetch = errors.New("registry: fetch failed")

// ErrInvalid reports an answer or a cache that is not a registry this build can
// describe.
var ErrInvalid = errors.New("registry: invalid registry")

// Registry is one answer from the publisher.
type Registry struct {
	Version string  `json:"version"`
	Agents  []Agent `json:"agents"`
}

// Agent is one entry. Distribution has the adapter file's install shape, so
// registry data maps in unchanged rather than growing a second catalog format.
type Agent struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Description  string           `json:"description,omitempty"`
	Repository   string           `json:"repository,omitempty"`
	License      string           `json:"license,omitempty"`
	Distribution adapters.Install `json:"distribution"`
}

// CachePath is where the one cached answer lives under a state root's cache
// directory.
func CachePath(cacheDir string) string {
	return filepath.Join(cacheDir, "agents-registry.json")
}

// Fetch reads the registry from url. The answer is external data that may grow
// fields this build does not know, so decoding is lenient; an entry the build
// cannot describe — no id, no version, no pinned install — is left out instead
// of trusted.
func Fetch(ctx context.Context, client *http.Client, url string) (Registry, error) {
	if client == nil {
		client = DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Registry{}, fmt.Errorf("registry: build the request: %w: %w", ErrFetch, err)
	}
	req.Header.Set("User-Agent", "flt-registry")

	resp, err := client.Do(req)
	if err != nil {
		return Registry{}, fmt.Errorf("registry: fetch %s: %w: %w", url, ErrFetch, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Registry{}, fmt.Errorf("registry: fetch %s: %s: %w", url, resp.Status, ErrFetch)
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return Registry{}, fmt.Errorf("registry: read the answer: %w: %w", ErrFetch, err)
	}
	if len(payload) > maxBytes {
		return Registry{}, fmt.Errorf("registry: the answer is larger than %d bytes: %w", maxBytes, ErrFetch)
	}

	var answer Registry
	if err := json.Unmarshal(payload, &answer); err != nil {
		return Registry{}, fmt.Errorf("registry: decode the answer: %w: %w", ErrInvalid, err)
	}
	return answer.validated()
}

// validated keeps the entries this build can describe and refuses an answer
// that is not a registry at all.
func (r Registry) validated() (Registry, error) {
	r.Version = strings.TrimSpace(r.Version)
	if r.Version == "" {
		return Registry{}, fmt.Errorf("registry: the answer names no version: %w", ErrInvalid)
	}

	kept := make([]Agent, 0, len(r.Agents))
	for _, agent := range r.Agents {
		agent.ID = strings.TrimSpace(agent.ID)
		agent.Name = strings.TrimSpace(agent.Name)
		agent.Version = strings.TrimSpace(agent.Version)
		agent.Distribution.Normalize()
		if agent.ID == "" || agent.Name == "" || agent.Version == "" {
			continue
		}
		if agent.Distribution.Empty() || agent.Distribution.Validate() != nil {
			continue
		}
		kept = append(kept, agent)
	}
	if len(kept) == 0 {
		return Registry{}, fmt.Errorf("registry: the answer names no agents this build can install: %w", ErrInvalid)
	}
	r.Agents = kept
	return r, nil
}

// Save writes the cache atomically: a reader either sees the old answer or the
// new one, never half of either.
func Save(path string, r Registry) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("registry: encode the cache: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("registry: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "registry-*.tmp")
	if err != nil {
		return fmt.Errorf("registry: create a temporary cache file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("registry: write the cache: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("registry: write the cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("registry: write the cache: %w", err)
	}
	if err := replace(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("registry: replace the cache: %w", err)
	}
	return nil
}

// replace moves the finished temp file into place. Windows refuses the rename
// while a reader holds the destination open, so a concurrent list can cost a
// few retries rather than the whole refresh.
func replace(from, to string) error {
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("registry: rename the cache: %w", err)
	}
	for attempt := 0; attempt < 20; attempt++ {
		time.Sleep(10 * time.Millisecond)
		if err = os.Rename(from, to); err == nil {
			return nil
		}
	}
	return fmt.Errorf("registry: rename the cache: %w", err)
}

// Load reads the cached answer.
func Load(path string) (Registry, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, fmt.Errorf("registry: read the cache: %w", err)
	}

	var cached Registry
	if err := json.Unmarshal(payload, &cached); err != nil {
		return Registry{}, fmt.Errorf("registry: decode the cache: %w: %w", ErrInvalid, err)
	}
	return cached.validated()
}

// Fresh reports whether the cache is young enough to keep. A missing file is
// never fresh, and neither is one from the future.
func Fresh(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	age := now.Sub(info.ModTime())
	return age >= 0 && age < TTL
}

// Usable reports whether the cache is young enough and decodes: a file that
// survived a crash with a recent mtime but no content is not an answer.
func Usable(path string, now time.Time) bool {
	if !Fresh(path, now) {
		return false
	}
	_, err := Load(path)
	return err == nil
}

// Refresh fetches the registry and replaces the cache with it. A failed save
// leaves the previous cache in place, which the next reader still serves.
func Refresh(ctx context.Context, client *http.Client, path, url string) (Registry, error) {
	answer, err := Fetch(ctx, client, url)
	if err != nil {
		return Registry{}, err
	}
	if err := Save(path, answer); err != nil {
		return Registry{}, err
	}
	return answer, nil
}

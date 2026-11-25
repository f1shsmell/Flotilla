package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/registry"
)

const registryFixture = `{"version":"1.0.0","agents":[
 {"id":"a","name":"A","version":"1.0.0","distribution":{"npx":{"package":"a@1.0.0"}}}]}`

func registryDaemon(t *testing.T, cacheDir, url string, client *http.Client) *Daemon {
	t.Helper()

	d := New(Config{
		Channel: "test", Token: "shared", StateDir: t.TempDir(),
		CacheDir: cacheDir, RegistryURL: url, RegistryClient: client,
		RegistryRefresh: registry.TTL,
		Log:             discardLogger(),
	}, nil)
	t.Cleanup(d.Detach)
	return d
}

// TestTheDaemonFetchesAStaleRegistry pins the startup question: a missing or
// aged cache is not usable, and one refresh makes it so.
func TestTheDaemonFetchesAStaleRegistry(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(registryFixture))
	}))
	t.Cleanup(server.Close)

	cacheDir := t.TempDir()
	d := registryDaemon(t, cacheDir, server.URL, server.Client())
	if d.registryUsable() {
		t.Fatal("a missing cache is usable")
	}

	d.refreshRegistry(context.Background())

	cached, err := registry.Load(registry.CachePath(cacheDir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cached.Agents) != 1 || cached.Agents[0].ID != "a" {
		t.Fatalf("cache = %+v, want the fetched agent", cached)
	}
	if !d.registryUsable() {
		t.Fatal("the fetched cache is not usable")
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times, want once", hits.Load())
	}
}

// TestAnUnreadableCacheCountsAsStale is the healing path: a recent file that
// does not decode is not an answer, so the loop replaces it.
func TestAnUnreadableCacheCountsAsStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(registryFixture))
	}))
	t.Cleanup(server.Close)

	cacheDir := t.TempDir()
	path := registry.CachePath(cacheDir)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("create the cache directory: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed an empty cache: %v", err)
	}

	d := registryDaemon(t, cacheDir, server.URL, server.Client())
	if d.registryUsable() {
		t.Fatal("an empty cache is usable")
	}

	d.refreshRegistry(context.Background())

	if _, err := registry.Load(path); err != nil {
		t.Fatalf("the refresh did not heal the cache: %v", err)
	}
}

// TestAFailedRefreshKeepsTheLastRegistry is the offline promise: a cache that
// cannot be replaced still serves the directory the machine last had.
func TestAFailedRefreshKeepsTheLastRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	cacheDir := t.TempDir()
	path := registry.CachePath(cacheDir)
	last := registry.Registry{Version: "0.9.0", Agents: []registry.Agent{{
		ID: "old", Name: "Old", Version: "0.9.0",
		Distribution: adapters.Install{Npx: &adapters.Package{Package: "old@0.9.0"}},
	}}}
	if err := registry.Save(path, last); err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := time.Now().Add(-2 * registry.TTL)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age the cache: %v", err)
	}

	d := registryDaemon(t, cacheDir, server.URL, server.Client())
	d.refreshRegistry(context.Background())

	cached, err := registry.Load(path)
	if err != nil {
		t.Fatalf("the failed refresh removed the cache: %v", err)
	}
	if cached.Version != "0.9.0" || cached.Agents[0].ID != "old" {
		t.Fatalf("cache = %+v, want the last answer", cached)
	}
}

// TestAShutdownIsNotARefreshFailure keeps a clean exit from logging the
// cancellation as a failure: the daemon cancelled the fetch itself.
func TestAShutdownIsNotARefreshFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := registryDaemon(t, t.TempDir(), server.URL, server.Client())
	var logged strings.Builder
	d.cfg.Log = slog.New(slog.NewTextHandler(&logged, nil))

	d.refreshRegistry(ctx)

	if logged.Len() != 0 {
		t.Fatalf("a cancelled refresh logged %q, want nothing", logged.String())
	}
}

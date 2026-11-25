package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// fixture is the shape the official registry serves: a version, agents whose
// distribution is the adapter file's install shape, and fields this build does
// not know yet.
const fixture = `{
 "version": "1.0.0",
 "agents": [
  {"id": "npx-agent", "name": "NPX Agent", "version": "1.3.0",
   "description": "one", "repository": "https://example.com/r", "license": "MIT",
   "distribution": {"npx": {"package": "agent@1.3.0", "args": ["--acp"]}}},
  {"id": "binary-agent", "name": "Binary Agent", "version": "0.13.0",
   "distribution": {"binary": {"windows-x86_64": {
     "archive": "https://example.com/a.zip", "cmd": "./a.exe",
     "env": {"ACP": "on"},
     "sha256": "ca3315e3e54ea85c7e5db09d19a1908cd2ea149c8e83a728cefb814c7a490edf"}}}},
  {"id": "uvx-agent", "name": "UVX Agent", "version": "0.1.44",
   "distribution": {"uvx": {"package": "minion-code@0.1.44"}}},
  {"id": "no-version", "name": "Nameless Version"},
  {"id": "unpinned", "name": "Unpinned", "version": "1.0.0",
   "distribution": {"npx": {"package": "agent"}}}
 ],
 "extensions": {"future": true}
}`

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestFetchKeepsWhatItCanName pins the lenient edge: the registry is external
// data that may grow fields, but an entry this build cannot describe is left
// out instead of trusted.
func TestFetchKeepsWhatItCanName(t *testing.T) {
	server := serve(t, http.StatusOK, fixture)

	got, err := Fetch(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Version != "1.0.0" {
		t.Fatalf("version = %q, want 1.0.0", got.Version)
	}
	if len(got.Agents) != 3 {
		t.Fatalf("kept %d agents, want the three the build can name", len(got.Agents))
	}
	if got.Agents[0].ID != "npx-agent" || got.Agents[0].Distribution.Npx == nil ||
		got.Agents[0].Distribution.Npx.Package != "agent@1.3.0" {
		t.Fatalf("npx agent = %+v", got.Agents[0])
	}
	binary := got.Agents[1].Distribution.Binary["windows-x86_64"]
	if binary.Cmd != "./a.exe" || binary.Env["ACP"] != "on" {
		t.Fatalf("binary agent = %+v, want its command and env", got.Agents[1])
	}
	if uvx := got.Agents[2].Distribution.Uvx; uvx == nil || uvx.Package != "minion-code@0.1.44" {
		t.Fatalf("uvx agent = %+v, want the @-pinned recipe", got.Agents[2])
	}
}

// TestFetchRefusesAnAnswerThatIsNotARegistry keeps a captive portal or an error
// page from becoming a cached catalog.
func TestFetchRefusesAnAnswerThatIsNotARegistry(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
		class  error
	}{
		{"an error status", http.StatusInternalServerError, "boom", "500", ErrFetch},
		{"empty object", http.StatusOK, "{}", "version", ErrInvalid},
		{"no usable agents", http.StatusOK, `{"version":"1.0.0","agents":[]}`, "agents", ErrInvalid},
		{"not json", http.StatusOK, "<html>login</html>", "decode", ErrInvalid},
		{"larger than the cap", http.StatusOK, `{"version":"1.0.0","agents":["` + strings.Repeat("x", maxBytes) + `"]}`, "larger", ErrFetch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := serve(t, c.status, c.body)

			_, err := Fetch(context.Background(), server.Client(), server.URL)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Fetch = %v, want it to mention %q", err, c.want)
			}
			if !errors.Is(err, c.class) {
				t.Fatalf("Fetch = %v, want %v", err, c.class)
			}
		})
	}
}

// TestTheCacheRoundTrips pins what the daemon writes and the CLI reads: one
// file, atomic enough that a reader never sees half of it, and freshness by
// age.
func TestTheCacheRoundTrips(t *testing.T) {
	path := CachePath(t.TempDir())
	want := Registry{Version: "1.0.0", Agents: []Agent{{
		ID: "npx-agent", Name: "NPX Agent", Version: "1.3.0",
		Distribution: adapters.Install{Npx: &adapters.Package{Package: "agent@1.3.0"}},
	}}}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != want.Version || len(got.Agents) != 1 || got.Agents[0].Distribution.Npx.Package != "agent@1.3.0" {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}

	now := time.Now()
	if !Fresh(path, now) {
		t.Fatal("a just-written cache is not fresh")
	}
	if !Usable(path, now) {
		t.Fatal("the cache just written is not usable")
	}
	old := now.Add(-2 * TTL)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age the cache: %v", err)
	}
	if Fresh(path, now) {
		t.Fatal("a two-TTL-old cache is fresh")
	}
	if Fresh(filepath.Join(t.TempDir(), "missing.json"), now) {
		t.Fatal("a missing cache is fresh")
	}

	// A fresh mtime does not make an empty file an answer: Usable is the
	// question a reader about to serve the cache asks.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("truncate the cache: %v", err)
	}
	if Usable(path, now) {
		t.Fatal("an empty cache counts as usable")
	}
}

// TestRefreshFetchesAndWrites is the one call the daemon loop and flt registry
// refresh share.
func TestRefreshFetchesAndWrites(t *testing.T) {
	server := serve(t, http.StatusOK, fixture)
	path := CachePath(t.TempDir())

	got, err := Refresh(context.Background(), server.Client(), path, server.URL)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(got.Agents) != 3 {
		t.Fatalf("Refresh kept %d agents", len(got.Agents))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Refresh did not write the cache: %v", err)
	}
}

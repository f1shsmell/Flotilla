package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/drift"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/registry"
	"github.com/f1shsmell/flotilla/internal/version"
)

// seedDriftHome writes one adapter pin and one directory cache: a uvx entry to
// check and a binary entry the check has to skip.
func seedDriftHome(t *testing.T) {
	t.Helper()
	t.Setenv("FLT_HOME", t.TempDir())
	t.Setenv("FLT_CHANNEL", "")

	paths, err := home.Resolve(version.Channel)
	if err != nil {
		t.Fatalf("resolve state paths: %v", err)
	}

	dir := filepath.Join(paths.Config, "adapters")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the adapter directory: %v", err)
	}
	adapter := `{"id":"local-agent","name":"Local","command":"local-agent","install":{"npx":{"package":"local-agent@1.0.0"}}}`
	if err := os.WriteFile(filepath.Join(dir, "local.json"), []byte(adapter), 0o600); err != nil {
		t.Fatalf("write the adapter: %v", err)
	}

	cached := registry.Registry{Version: "1.0.0", Agents: []registry.Agent{
		{
			ID: "uvx-agent", Name: "UVX Agent", Version: "0.3.1",
			Distribution: adapters.Install{Uvx: &adapters.Package{Package: "kimi-code@0.3.1"}},
		},
		{
			ID: "bin-agent", Name: "Bin Agent", Version: "0.1.0",
			Distribution: adapters.Install{Binary: map[string]adapters.Binary{
				"windows-x86_64": {Archive: "https://example.com/x.zip", Cmd: "./x.exe"},
			}},
		},
	}}
	if err := registry.Save(registry.CachePath(paths.Cache), cached); err != nil {
		t.Fatalf("seed the directory cache: %v", err)
	}
}

// driftAnswers serves the two registry shapes for the seeded pins.
func driftAnswers(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/local-agent/latest"):
			_, _ = fmt.Fprint(w, `{"version":"1.0.1"}`)
		case strings.HasPrefix(r.URL.Path, "/pypi/kimi-code/json"):
			_, _ = fmt.Fprint(w, `{"info":{"version":"0.3.1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDriftReportsThePinsAndCanFailTheBuild(t *testing.T) {
	seedDriftHome(t)
	server := driftAnswers(t)
	checker := drift.Checker{NPM: server.URL, PyPI: server.URL, Client: server.Client()}

	var out bytes.Buffer
	if err := driftCommand(nil, version.Channel, false, false, checker, &out); err != nil {
		t.Fatalf("drift without --fail-on-drift: %v", err)
	}
	want := strings.Join([]string{
		"checked 2 pins: 1 ok, 1 drift, 0 failed",
		"- catalog:local-agent: npm local-agent@1.0.0 (latest 1.0.1)",
		"- registry:bin-agent: no package pin to check",
	}, "\n") + "\n"
	if got := out.String(); got != want {
		t.Fatalf("drift output =\n%s\nwant\n%s", got, want)
	}

	out.Reset()
	err := driftCommand(nil, version.Channel, false, true, checker, &out)
	if !errors.Is(err, errDrift) {
		t.Fatalf("drift with --fail-on-drift = %v, want errDrift", err)
	}
	if !strings.Contains(out.String(), "1 drift") {
		t.Fatalf("a failing report printed no drift line:\n%s", out.String())
	}
}

func TestDriftJSONCarriesEveryPin(t *testing.T) {
	seedDriftHome(t)
	server := driftAnswers(t)
	checker := drift.Checker{NPM: server.URL, PyPI: server.URL, Client: server.Client()}

	var out bytes.Buffer
	if err := driftCommand(nil, version.Channel, true, false, checker, &out); err != nil {
		t.Fatalf("drift --json: %v", err)
	}

	var report driftReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode the drift report: %v", err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("the report has %d results, want 2", len(report.Results))
	}
	if report.Results[0].Status != drift.StatusDrift || report.Results[0].Latest != "1.0.1" {
		t.Fatalf("the adapter pin = %+v, want drift to 1.0.1", report.Results[0])
	}
	if len(report.Skipped) != 1 || report.Skipped[0] != "registry:bin-agent" {
		t.Fatalf("skipped = %v, want the binary entry", report.Skipped)
	}
}

// TestDriftStillChecksTheCatalogWithoutACache keeps the directory optional: a
// cache that was never written is reported, and the catalog is still checked.
func TestDriftStillChecksTheCatalogWithoutACache(t *testing.T) {
	seedDriftHome(t)
	paths, err := home.Resolve(version.Channel)
	if err != nil {
		t.Fatalf("resolve state paths: %v", err)
	}
	if err := os.Remove(registry.CachePath(paths.Cache)); err != nil {
		t.Fatalf("remove the directory cache: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"version":"1.0.0"}`)
	}))
	t.Cleanup(server.Close)
	checker := drift.Checker{NPM: server.URL, Client: server.Client()}

	var out bytes.Buffer
	if err := driftCommand(nil, version.Channel, false, false, checker, &out); err != nil {
		t.Fatalf("drift without a cache: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "checked 1 pin: 1 ok, 0 drift, 0 failed") {
		t.Fatalf("the catalog pin was not checked:\n%s", text)
	}
	if !strings.Contains(text, "read the agent registry cache") {
		t.Fatalf("the missing cache was not reported:\n%s", text)
	}
}

func TestRegistryDriftRefusesArguments(t *testing.T) {
	err := driftCommand([]string{"x"}, version.Channel, false, false, drift.Checker{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "registry drift takes no arguments") {
		t.Fatalf("drift with an argument = %v", err)
	}
}

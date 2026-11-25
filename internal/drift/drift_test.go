package drift

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// registryAnswers serves the two shapes the real registries answer with, keyed
// by package name. A package the test did not name is a 404.
func registryAnswers(t *testing.T, npm, pypi map[string]string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/pypi/"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/pypi/"), "/json")
			version, ok := pypi[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprintf(w, `{"info":{"version":%q}}`, version)
		case strings.HasSuffix(r.URL.Path, "/latest"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/latest")
			version, ok := npm[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprintf(w, `{"version":%q}`, version)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCheckNamesDriftAndFailure(t *testing.T) {
	server := registryAnswers(t,
		map[string]string{"same": "2.0.0", "old": "1.0.1"},
		map[string]string{"py-same": "3.0.0"},
	)
	checker := Checker{NPM: server.URL, PyPI: server.URL, Client: server.Client()}

	pins := []Pin{
		{Source: "catalog:a", Runner: adapters.RunnerNPM, Package: "same", Version: "2.0.0"},
		{Source: "registry:b", Runner: adapters.RunnerNPM, Package: "old", Version: "1.0.0"},
		{Source: "registry:c", Runner: adapters.RunnerPyPI, Package: "py-same", Version: "3.0.0"},
		{Source: "registry:d", Runner: adapters.RunnerPyPI, Package: "missing", Version: "1.0.0"},
	}

	results := checker.Check(context.Background(), pins)
	if len(results) != len(pins) {
		t.Fatalf("Check returned %d results, want %d", len(results), len(pins))
	}
	if results[0].Status != StatusOK || results[0].Latest != "" {
		t.Fatalf("same = %+v, want ok without a latest", results[0])
	}
	if results[1].Status != StatusDrift || results[1].Latest != "1.0.1" {
		t.Fatalf("old = %+v, want drift to 1.0.1", results[1])
	}
	if results[2].Status != StatusOK {
		t.Fatalf("py-same = %+v, want ok", results[2])
	}
	if results[3].Status != StatusFailed || !strings.Contains(results[3].Error, "404") {
		t.Fatalf("missing = %+v, want a failed lookup naming 404", results[3])
	}
}

// TestAPackageIsLookedUpOnce keeps two recipes that pin the same package from
// turning one report into two registry requests.
func TestAPackageIsLookedUpOnce(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	}))
	t.Cleanup(server.Close)

	checker := Checker{NPM: server.URL, Client: server.Client()}
	results := checker.Check(context.Background(), []Pin{
		{Source: "catalog:a", Runner: adapters.RunnerNPM, Package: "same", Version: "1.0.0"},
		{Source: "registry:b", Runner: adapters.RunnerNPM, Package: "same", Version: "1.0.0"},
	})

	if hits.Load() != 1 {
		t.Fatalf("looked the same package up %d times, want once", hits.Load())
	}
	if len(results) != 2 || results[0].Source != "catalog:a" || results[1].Source != "registry:b" {
		t.Fatalf("Check returned %+v, want one result per pin", results)
	}
}

// TestAScopedNameStaysOnePathSegment holds the npm URL shape: the slash inside
// a scoped name is part of the name, not of the path.
func TestAScopedNameStaysOnePathSegment(t *testing.T) {
	var path atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.EscapedPath())
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	}))
	t.Cleanup(server.Close)

	checker := Checker{NPM: server.URL, Client: server.Client()}
	checker.Check(context.Background(), []Pin{{
		Runner: adapters.RunnerNPM, Package: "@scope/agent", Version: "1.0.0",
	}})

	got, _ := path.Load().(string)
	if !strings.Contains(strings.ToLower(got), "%2f") || !strings.HasSuffix(got, "/latest") {
		t.Fatalf("the request path = %q, want the scoped name escaped in one segment", got)
	}
}

func TestAnAnswerWithoutAVersionIsAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	checker := Checker{NPM: server.URL, Client: server.Client()}
	results := checker.Check(context.Background(), []Pin{{
		Runner: adapters.RunnerNPM, Package: "x", Version: "1.0.0",
	}})

	if len(results) != 1 || results[0].Status != StatusFailed {
		t.Fatalf("an answer without a version = %+v, want failed", results)
	}
	if !strings.Contains(results[0].Error, "version") {
		t.Fatalf("error %q does not name the missing field", results[0].Error)
	}
}

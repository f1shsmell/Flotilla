package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/registry"
)

// fetchAgents asks the daemon what it can offer.
func fetchAgents(t *testing.T, ctx context.Context, client interface {
	Call(context.Context, string, any) (json.RawMessage, error)
},
) agent.Listing {
	t.Helper()

	raw, err := client.Call(ctx, MethodAgents, struct{}{})
	if err != nil {
		t.Fatalf("ask for agents: %v", err)
	}

	var list agent.Listing
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode the agent listing: %v", err)
	}
	return list
}

func agentEntry(t *testing.T, list agent.Listing, id string) agent.Entry {
	t.Helper()

	for _, entry := range list.Agents {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("the listing has no %q: %+v", id, list.Agents)
	return agent.Entry{}
}

// TestDaemonListsTheAgentsItShips is the picker's source of truth: the builtins
// are reachable over the control plane, not only inside the GUI.
func TestDaemonListsTheAgentsItShips(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemonWith(t, Config{Channel: "test", Token: token, StateDir: dir, ConfigDir: t.TempDir()})

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	list := fetchAgents(t, ctx, client)
	if len(list.Agents) != len(agent.Builtins()) {
		t.Fatalf("the daemon listed %d agents, want the %d builtins", len(list.Agents), len(agent.Builtins()))
	}
	for _, preset := range agent.Builtins() {
		entry := agentEntry(t, list, preset.ID)
		if entry.Command != preset.Command || entry.Source != agent.BuiltinSource {
			t.Fatalf("%s was listed as %+v", preset.ID, entry)
		}
	}
}

// TestTheListingCarriesThePublishedDirectory is the picker's second source: an
// agent the catalog does not know appears with the recipe that would install
// it, and one the catalog already knows is not listed twice.
func TestTheListingCarriesThePublishedDirectory(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	const token = "shared"

	published := registry.Registry{Version: "1.0.0", Agents: []registry.Agent{
		{
			ID: "kimi", Name: "Kimi Code", Version: "1.2.3",
			Description: "A Kimi agent", Repository: "https://example.com/kimi",
			Distribution: adapters.Install{Uvx: &adapters.Package{Package: "kimi-code@1.2.3"}},
		},
		{
			ID: "gemini", Name: "Gemini from the directory", Version: "9.9.9",
			Distribution: adapters.Install{Npx: &adapters.Package{Package: "gemini@9.9.9"}},
		},
		{
			ID: "GEMINI", Name: "Gemini in another case", Version: "9.9.8",
			Distribution: adapters.Install{Npx: &adapters.Package{Package: "gemini@9.9.8"}},
		},
	}}
	if err := registry.Save(registry.CachePath(cacheDir), published); err != nil {
		t.Fatalf("seed the directory cache: %v", err)
	}

	runDaemonWith(t, Config{
		Channel: "test", Token: token, StateDir: dir, ConfigDir: t.TempDir(), CacheDir: cacheDir,
	})

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	list := fetchAgents(t, ctx, client)
	var kimi *agent.RegistryEntry
	for i := range list.Registry {
		switch strings.ToLower(list.Registry[i].ID) {
		case "kimi":
			kimi = &list.Registry[i]
		case "gemini":
			t.Fatalf("the listing repeats the builtin gemini: %+v", list.Registry)
		}
	}
	if kimi == nil {
		t.Fatalf("the listing has no kimi from the directory: %+v", list.Registry)
	}
	if kimi.Name != "Kimi Code" || kimi.Version != "1.2.3" || kimi.Recipe != "uvx kimi-code@1.2.3" {
		t.Fatalf("kimi was listed as %+v", *kimi)
	}
}

// TestAMissingDirectoryCacheLeavesTheCatalogAlone keeps the directory optional:
// a daemon whose cache has not been written still serves the agents it ships.
func TestAMissingDirectoryCacheLeavesTheCatalogAlone(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemonWith(t, Config{
		Channel: "test", Token: token, StateDir: dir, ConfigDir: t.TempDir(), CacheDir: t.TempDir(),
	})

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	list := fetchAgents(t, ctx, client)
	if len(list.Registry) != 0 {
		t.Fatalf("a missing cache listed %+v", list.Registry)
	}
	if len(list.Agents) != len(agent.Builtins()) {
		t.Fatalf("the catalog shrank to %d agents", len(list.Agents))
	}
}

// TestDaemonAppliesAnAgentSwitch pins the write path: a switch set over the
// control plane is what a reader sees next.
func TestDaemonAppliesAnAgentSwitch(t *testing.T) {
	dir := t.TempDir()
	configDir := t.TempDir()
	const token = "shared"

	runDaemonWith(t, Config{Channel: "test", Token: token, StateDir: dir, ConfigDir: configDir})

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	disabled := false
	raw, err := client.Call(ctx, MethodAgentSet, SetAgentRequest{
		ID:     "gemini",
		Change: agent.Change{Enabled: &disabled},
	})
	if err != nil {
		t.Fatalf("set the switch: %v", err)
	}

	var applied agent.Entry
	if err := json.Unmarshal(raw, &applied); err != nil {
		t.Fatalf("decode the entry: %v", err)
	}
	if applied.ID != "gemini" || applied.Enabled {
		t.Fatalf("the daemon answered %+v, want gemini disabled", applied)
	}

	if next := agentEntry(t, fetchAgents(t, ctx, client), "gemini"); next.Enabled {
		t.Fatalf("the listing still reports gemini as %+v", next)
	}
}

// TestDaemonRefusesAnAgentSwitchItCannotName is the boundary: a request without
// an id must fail rather than touch an arbitrary agent.
func TestDaemonRefusesAnAgentSwitchItCannotName(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemonWith(t, Config{Channel: "test", Token: token, StateDir: dir, ConfigDir: t.TempDir()})

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	enabled := true
	for _, req := range []SetAgentRequest{
		{Change: agent.Change{Enabled: &enabled}},
		{ID: "no-such-agent", Change: agent.Change{Enabled: &enabled}},
	} {
		_, err := client.Call(ctx, MethodAgentSet, req)
		if err == nil {
			t.Fatalf("the daemon accepted %+v", req)
		}
		if !strings.Contains(err.Error(), MethodAgentSet) {
			t.Fatalf("error %q does not name the method", err)
		}
	}
}

// TestTheWriteRequestMatchesTheGUI mirrors the read-path parity gate for the
// write path: the renderer builds {id, change} by hand, so a rename on either
// side has to fail here rather than send a switch nothing stores.
func TestTheWriteRequestMatchesTheGUI(t *testing.T) {
	got := jsonFieldNames(t, SetAgentRequest{})
	want := tsFields(t, "SetAgentRequest")

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Go decodes %v, gui/src/store.ts sends %v", got, want)
	}
}

func jsonFieldNames(t *testing.T, value any) []string {
	t.Helper()

	typ := reflect.TypeOf(value)
	out := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

// tsFields reads one interface out of the renderer's store.ts, one field per
// line, which is what the project's style keeps them to.
func tsFields(t *testing.T, name string) []string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate this test file")
	}
	store := filepath.Join(filepath.Dir(file), "..", "..", "gui", "src", "store.ts")

	payload, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read %s: %v", store, err)
	}

	_, rest, ok := strings.Cut(string(payload), "export interface "+name+" {")
	if !ok {
		t.Fatalf("%s has no interface %s", store, name)
	}
	body, _, ok := strings.Cut(rest, "\n}")
	if !ok {
		t.Fatalf("interface %s in %s has no closing brace", name, store)
	}

	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		field, _, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("%s.%s has a line that is not a field: %q", name, store, line)
		}
		out = append(out, strings.TrimSuffix(strings.TrimSpace(field), "?"))
	}
	sort.Strings(out)
	return out
}

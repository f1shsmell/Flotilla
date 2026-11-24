package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/session"
)

// capturedNewSession is what the fake agent recorded when the session opened.
type capturedNewSession struct {
	Request struct {
		McpServers []struct {
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"mcpServers"`
	} `json:"request"`
	Env map[string]string `json:"env"`
}

// TestACPSessionGetsTheFleetTools pins the whole injection: the agent process
// carries the session's scope in its environment, and the session it opens
// lists the fleet tool server pinned to the same session and state root.
func TestACPSessionGetsTheFleetTools(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "new-session.json")
	home := filepath.Join(dir, "home")
	t.Setenv("FLT_HOME", home)

	h, err := Start(context.Background(), Config{
		ID:         "sess-scope",
		Mode:       session.ModeACP,
		Token:      "shared",
		StateDir:   dir,
		Command:    buildFakeAgent(t),
		Dir:        dir,
		Policy:     acp.Auto,
		Channel:    "test",
		MCPCommand: `C:\tools\flt.exe`,
		Env:        append(os.Environ(), "FLT_FAKE_CAPTURE="+capture),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the ACP host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	payload, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("the fake agent recorded nothing: %v", err)
	}
	var got capturedNewSession
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode the capture: %v", err)
	}

	if got.Env["FLT_SESSION_ID"] != "sess-scope" || got.Env["FLT_CHANNEL"] != "test" || got.Env["FLT_POLICY"] != "auto" {
		t.Errorf("the agent's environment = %v, want the session's scope", got.Env)
	}

	if len(got.Request.McpServers) != 1 {
		t.Fatalf("agent session lists %d tool servers, want one: %+v",
			len(got.Request.McpServers), got.Request.McpServers)
	}
	// The stdio variant carries no type discriminator: name, command, args and
	// env are what identify it on the wire.
	server := got.Request.McpServers[0]
	if server.Name != "flotilla" || server.Command != `C:\tools\flt.exe` {
		t.Errorf("tool server = %+v", server)
	}
	if len(server.Args) != 2 || server.Args[0] != "mcp" || server.Args[1] != "serve" {
		t.Errorf("tool server args = %q, want `mcp serve`", server.Args)
	}

	env := make(map[string]string, len(server.Env))
	for _, e := range server.Env {
		env[e.Name] = e.Value
	}
	want := map[string]string{
		"FLT_SESSION_ID": "sess-scope",
		"FLT_CHANNEL":    "test",
		"FLT_POLICY":     "auto",
		"FLT_HOME":       home,
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("tool server env %s = %q, want %q (all: %v)", name, env[name], value, env)
		}
	}
}

// TestNoMCPCommandRegistersNoServer keeps a build that ships nothing beside the
// host from handing an agent a command that does not exist.
func TestNoMCPCommandRegistersNoServer(t *testing.T) {
	h := &Host{cfg: Config{ID: "sess-scope", Channel: "test"}}
	if servers := h.mcpServers(); servers != nil {
		t.Errorf("a host with no flt binary registered %+v", servers)
	}
}

// TestWithEnvReplacesTheScopeKeys keeps an inherited scope from shadowing the
// session's own: duplicate keys make the child's environment depend on which
// side the platform reads last.
func TestWithEnvReplacesTheScopeKeys(t *testing.T) {
	env := withEnv(
		[]string{"PATH=C:\\windows", "FLT_SESSION_ID=someone-else", "KEEP=1"},
		[]string{"FLT_SESSION_ID=sess-scope", "FLT_CHANNEL=test"},
	)

	seen := make(map[string]string, len(env))
	count := make(map[string]int, len(env))
	for _, entry := range env {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				seen[entry[:i]] = entry[i+1:]
				count[entry[:i]]++
				break
			}
		}
	}

	if seen["FLT_SESSION_ID"] != "sess-scope" || count["FLT_SESSION_ID"] != 1 {
		t.Errorf("FLT_SESSION_ID = %q (seen %d times), want only the session's own",
			seen["FLT_SESSION_ID"], count["FLT_SESSION_ID"])
	}
	if seen["FLT_CHANNEL"] != "test" {
		t.Errorf("FLT_CHANNEL = %q, want test", seen["FLT_CHANNEL"])
	}
	if seen["PATH"] != `C:\windows` || seen["KEEP"] != "1" {
		t.Errorf("withEnv dropped the inherited environment: %v", env)
	}
}

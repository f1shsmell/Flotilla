package host

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

func TestAgentEnvResolvesBareExecutableNames(t *testing.T) {
	saved := lookPath
	lookPath = func(name string) (string, error) {
		if name == "claude" {
			return `C:\resolved\claude.exe`, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = saved })

	environ := []string{"PATH=C:\\windows", "KEEP=1"}
	env := agentEnv(environ, map[string]string{"CLAUDE_CODE_EXECUTABLE": "claude"})

	var found string
	for _, entry := range env {
		if strings.HasPrefix(entry, "CLAUDE_CODE_EXECUTABLE=") {
			found = entry
		}
	}
	if found != "CLAUDE_CODE_EXECUTABLE=C:\\resolved\\claude.exe" {
		t.Errorf("the bare name did not resolve to the looked-up path: %q", found)
	}
	for _, keep := range environ {
		kept := false
		for _, entry := range env {
			if entry == keep {
				kept = true
			}
		}
		if !kept {
			t.Errorf("the host environment lost %q", keep)
		}
	}
}

func TestAgentEnvKeepsValuesThatAreNotBareNames(t *testing.T) {
	saved := lookPath
	lookPath = func(_ string) (string, error) {
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = saved })

	env := agentEnv(nil, map[string]string{
		"ABSOLUTE": `C:\tools\agent.exe`,
		"PLAIN":    "just-a-value",
		"MISSED":   "not-on-path",
	})
	for _, want := range []string{
		"ABSOLUTE=C:\\tools\\agent.exe",
		"PLAIN=just-a-value",
		"MISSED=not-on-path",
	} {
		found := false
		for _, entry := range env {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Errorf("env lost %q (got %d entries)", want, len(env))
		}
	}
}

func TestAgentEnvNilWithNoDeclarationsStaysInherit(t *testing.T) {
	if env := agentEnv(nil, nil); env != nil {
		t.Errorf("an empty declaration turned an inheriting environment into %d entries", len(env))
	}
	if env := agentEnv(nil, adapters.Adapter{}.Env); env != nil {
		t.Errorf("an empty map turned an inheriting environment into %d entries", len(env))
	}
}

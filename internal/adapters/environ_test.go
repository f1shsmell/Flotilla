package adapters

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEnvironResolvesBareExecutableNames pins the rule every spawn shares: a
// declared value that names an executable on PATH becomes its absolute path,
// and the base environment is kept.
func TestEnvironResolvesBareExecutableNames(t *testing.T) {
	saved := lookPath
	lookPath = func(name string) (string, error) {
		if name == "claude" {
			return `C:\resolved\claude.exe`, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = saved })

	environ := []string{"PATH=C:\\windows", "KEEP=1"}
	env := Environ(environ, map[string]string{"CLAUDE_CODE_EXECUTABLE": "claude"})

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
			t.Errorf("the base environment lost %q", keep)
		}
	}
}

// TestEnvironKeepsValuesThatAreNotBareNames pins the cases a resolver must not
// touch: an absolute path, a value with no PATH entry behind it, and a plain
// value that happens to be a bare word.
func TestEnvironKeepsValuesThatAreNotBareNames(t *testing.T) {
	saved := lookPath
	lookPath = func(_ string) (string, error) {
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = saved })

	env := Environ(nil, map[string]string{
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

// TestEnvironNilBaseWithNoDeclarationsStaysInherit pins the inheritance signal:
// nil means "read this process's own environment at spawn", so an empty
// declaration must not turn it into a snapshot taken here.
func TestEnvironNilBaseWithNoDeclarationsStaysInherit(t *testing.T) {
	if env := Environ(nil, nil); env != nil {
		t.Errorf("an empty declaration turned an inheriting environment into %d entries", len(env))
	}
	if env := Environ(nil, Adapter{}.Env); env != nil {
		t.Errorf("an empty map turned an inheriting environment into %d entries", len(env))
	}
}

package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirHonoursFLT_HOME(t *testing.T) {
	override := t.TempDir()
	t.Setenv(EnvHome, override)

	got, err := Dir("stable")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != override {
		t.Fatalf("Dir() = %q, want %q", got, override)
	}
}

func TestDirSkipsTheRealHomeWhenOverridden(t *testing.T) {
	override := t.TempDir()
	t.Setenv(EnvHome, override)
	t.Setenv("LOCALAPPDATA", filepath.Join(override, "unused"))

	got, err := Dir("preview")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != override {
		t.Fatalf("Dir() = %q, want the override %q for every channel", got, override)
	}
}

func TestResolveKeepsEveryPathUnderTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "flt-dev")
	t.Setenv(EnvHome, root)

	p, err := Resolve("stable")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Resolve did not create the root: %v", err)
	}

	for _, dir := range []string{p.Config, p.Logs, p.Sessions, p.Cache, p.Token} {
		if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || filepath.IsAbs(rel) {
			t.Fatalf("%q escapes the state root %q", dir, root)
		}
	}
}

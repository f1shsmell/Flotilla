package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/version"
)

// TestVersionGoesToStdout pins the stream, not just the text. A caller that
// captures the binary's output — the release workflow checking that the daemon
// and the installer name the same version — reads stdout. A version printed to
// stderr is invisible to it, and the check fails on a build that is correct.
func TestVersionGoesToStdout(t *testing.T) {
	var out bytes.Buffer

	if err := run([]string{"--version"}, &out); err != nil {
		t.Fatalf("run(--version): %v", err)
	}

	got := strings.TrimSpace(out.String())
	if got != version.String() {
		t.Fatalf("--version printed %q, want %q", got, version.String())
	}
}

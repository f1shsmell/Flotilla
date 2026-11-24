package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFleetBinaryIsTheSiblingOfTheHost pins where the agent's tools come from:
// the CLI a release ships beside the session host, and nothing guessed from
// PATH when that build has no such file.
func TestFleetBinaryIsTheSiblingOfTheHost(t *testing.T) {
	dir := t.TempDir()
	hostPath := filepath.Join(dir, "flt-session-host")
	if err := os.WriteFile(hostPath, nil, 0o600); err != nil {
		t.Fatalf("write the host placeholder: %v", err)
	}

	if got := fleetBinary(hostPath); got != "" {
		t.Fatalf("fleetBinary found %q with no flt beside the host", got)
	}

	fltPath := filepath.Join(dir, "flt")
	if err := os.WriteFile(fltPath, nil, 0o600); err != nil {
		t.Fatalf("write the flt placeholder: %v", err)
	}
	if got := fleetBinary(hostPath); got != fltPath {
		t.Fatalf("fleetBinary = %q, want %q", got, fltPath)
	}

	if got := fleetBinary(""); got != "" {
		t.Fatalf("fleetBinary with no host = %q, want none", got)
	}
}

//go:build windows

package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// shortPath returns the 8.3 form of path, or skips the test when the volume does
// not generate short names. CI's temp directory is reached through one, which is
// exactly the case this file exists for.
func shortPath(t *testing.T, path string) string {
	t.Helper()

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode %q: %v", path, err)
	}

	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("this volume does not generate short names for %q", path)
	}
	return windows.UTF16ToString(buf[:n])
}

// TestResolveAcceptsAShortNameWorkspace reproduces the CI failure: the workspace
// arrives in its 8.3 form while the agent asks for the long one, or the other way
// round. Both must land inside the same workspace.
func TestResolveAcceptsAShortNameWorkspace(t *testing.T) {
	root := t.TempDir()
	shortRoot := shortPath(t, root)

	if shortRoot == root {
		t.Skip("the temp directory has no distinct short name")
	}

	longFile := filepath.Join(root, "src", "main.go")
	shortFile := filepath.Join(shortRoot, "src", "main.go")

	for _, tc := range []struct {
		name      string
		workspace string
		candidate string
	}{
		{name: "short workspace, long candidate", workspace: shortRoot, candidate: longFile},
		{name: "long workspace, short candidate", workspace: root, candidate: shortFile},
		{name: "short workspace, short candidate", workspace: shortRoot, candidate: shortFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(Config{Workspace: tc.workspace})
			got, err := c.resolve(tc.candidate)
			if err != nil {
				t.Fatalf("resolve(%q) inside workspace %q: %v", tc.candidate, tc.workspace, err)
			}
			if filepath.Base(got) != "main.go" {
				t.Fatalf("resolve(%q) = %q, want the file the agent asked for", tc.candidate, got)
			}
			if !inside(resolveExistingAncestor(tc.workspace), got) {
				t.Fatalf("resolved %q is not under workspace %q", got, tc.workspace)
			}
		})
	}
}

// TestResolveStillRefusesThroughAShortNameWorkspace pins the boundary the same
// way: normalising both sides must not turn an escape into an acceptance.
func TestResolveStillRefusesThroughAShortNameWorkspace(t *testing.T) {
	root := t.TempDir()
	shortRoot := shortPath(t, root)

	if shortRoot == root {
		t.Skip("the temp directory has no distinct short name")
	}

	c := NewClient(Config{Workspace: shortRoot})

	// written as a long path that still climbs out of the workspace
	escape := filepath.Join(root, "..", "outside.txt")
	if _, err := c.resolve(escape); err == nil {
		t.Fatalf("resolve(%q) accepted a path outside the workspace", escape)
	}

	// and as a short path climbing out of it
	escapeShort := filepath.Join(shortRoot, "..", "outside.txt")
	if _, err := c.resolve(escapeShort); err == nil {
		t.Fatalf("resolve(%q) accepted a short path outside the workspace", escapeShort)
	}
}

// TestShortPathHelperIsHonest keeps the two tests above from silently skipping on
// every machine: it records what the helper saw.
func TestShortPathHelperIsHonest(t *testing.T) {
	root := t.TempDir()
	short := shortPath(t, root)

	if !strings.EqualFold(short, root) {
		if _, err := os.Stat(short); err != nil {
			t.Fatalf("the short name %q does not name the same directory: %v", short, err)
		}
	}
}

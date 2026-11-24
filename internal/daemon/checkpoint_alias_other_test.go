//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// aliasSpelling returns a second spelling of dir: a symlink to it, which is
// how one directory is reached under two names on this platform.
func aliasSpelling(t *testing.T, dir string) string {
	t.Helper()

	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("this machine has no symlink for %q: %v", dir, err)
	}
	return link
}

//go:build windows

package daemon

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// aliasSpelling returns a second spelling of dir: a junction to it, which is
// how one checkout is reached under two names without any privilege.
func aliasSpelling(t *testing.T, dir string) string {
	t.Helper()

	link := filepath.Join(t.TempDir(), "alias")
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, dir).CombinedOutput(); err != nil {
		t.Skipf("this machine cannot create a junction to %q: %v (%s)", dir, err, out)
	}
	return link
}

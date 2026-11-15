//go:build !windows

package ipc

import (
	"fmt"
	"os"
)

// restrictToOwner narrows the file to its owner. POSIX mode bits carry the
// boundary here; there is no DACL to rewrite.
func restrictToOwner(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict %q to its owner: %w", path, err)
	}
	return nil
}

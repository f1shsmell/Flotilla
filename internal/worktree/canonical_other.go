//go:build !windows

package worktree

import "path/filepath"

// canonical resolves every link along path.
func canonical(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

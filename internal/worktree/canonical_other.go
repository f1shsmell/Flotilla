//go:build !windows

package worktree

import "path/filepath"

// Canonical resolves every link along path to the one spelling the filesystem
// keeps.
func Canonical(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// processAlive reports whether pid still names a running process. Signal 0 is
// the portable existence check; a zombie still answers it, which only means the
// dial that follows decides.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

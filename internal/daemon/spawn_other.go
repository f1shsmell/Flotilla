//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

// setDetached starts the host in its own process group so a signal to the
// daemon's group never reaches a session host.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

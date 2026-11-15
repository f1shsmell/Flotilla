//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// setDetached starts the host in its own process group so an interrupt sent to
// the daemon's console never reaches a session host.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

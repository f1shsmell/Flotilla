//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW is the SDK's 0x08000000; syscall does not export it.
const createNoWindow = 0x08000000

// setDetached starts the host in its own process group so an interrupt sent to
// the daemon's console never reaches a session host, and without a console of
// its own: the GUI spawns the daemon detached with its stdio ignored, so the
// daemon has no console to inherit and a plain spawn would hand the host a
// fresh one — a visible black window hosted by the default terminal.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
}

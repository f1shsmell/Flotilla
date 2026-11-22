//go:build windows

package acp

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW is the SDK's 0x08000000; syscall does not export it.
const createNoWindow = 0x08000000

// detachAgent keeps the agent out of the host's process group and off the
// screen: the host runs without a console (the daemon spawns it that way), so
// a plain spawn would hand the agent a fresh visible one behind the user's
// default terminal.
func detachAgent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
}

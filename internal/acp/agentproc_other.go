//go:build !windows

package acp

import (
	"os/exec"
	"syscall"
)

// detachAgent keeps the agent out of the host's process group, so an interrupt
// sent to the host's terminal never reaches the agent.
func detachAgent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

//go:build windows

package acp

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestDetachAgentHidesTheAgentConsole(t *testing.T) {
	var cmd exec.Cmd
	detachAgent(&cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("detachAgent set no SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&0x08000000 == 0 {
		t.Error("the agent spawn does not hide its console: it would appear beside the user's windows")
	}
	if flags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Error("the agent spawn is not in its own process group")
	}
}

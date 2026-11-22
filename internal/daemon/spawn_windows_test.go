//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestSetDetachedHidesTheHostConsole(t *testing.T) {
	var cmd exec.Cmd
	setDetached(&cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("setDetached set no SysProcAttr")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&0x08000000 == 0 {
		t.Error("the host spawn does not hide its console: the daemon is console-less, so the host would get a fresh visible one")
	}
	if flags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Error("the host spawn is not in its own process group")
	}
}

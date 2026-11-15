//go:build windows

package daemon

import "golang.org/x/sys/windows"

// stillActive is what GetExitCodeProcess reports while the process runs.
const stillActive = 259

// processAlive reports whether pid still names a running process. A record's
// pid is only trustworthy as a negative signal: a recycled pid reads as alive,
// and the dial that follows is what decides.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

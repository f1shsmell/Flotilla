//go:build windows

package worktree

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// canonical resolves every link along path, including the junctions Windows
// uses where a symlink would need a privilege. EvalSymlinks stops at a
// junction, and a junction is exactly the escape a containment check has to
// catch.
func canonical(path string) (string, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("encode %q: %w", path, err)
	}

	handle, err := windows.CreateFile(
		pointer,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], size, 0)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", path, err)
		}
		if n < size {
			return trimVolumePrefix(windows.UTF16ToString(buffer[:n])), nil
		}
		size = n + 1
	}
}

// trimVolumePrefix turns the \\?\ form the API returns into a plain path.
func trimVolumePrefix(path string) string {
	const (
		local = `\\?\`
		unc   = `\\?\UNC\`
	)
	if strings.HasPrefix(path, unc) {
		return `\\` + strings.TrimPrefix(path, unc)
	}
	return strings.TrimPrefix(path, local)
}

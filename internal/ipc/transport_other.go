//go:build !windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func platformAddr(a Address) string {
	return "unix:" + a.Channel + "/" + a.Name
}

// socketProbeTimeout bounds the check for a live owner of an existing socket.
// A listening socket accepts at once; a stale one is refused at once. Neither
// needs a long wait.
const socketProbeTimeout = 500 * time.Millisecond

func listen(addr Address, dir string) (Listener, error) {
	path := addr.SocketPath(dir)
	if path == "" {
		return nil, fmt.Errorf("listen on %s: no socket directory: %w", addr, ErrProtocol)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	// An existing path is only removed when nothing is serving it. Unlinking a
	// live socket would leave the running daemon holding a name no client can
	// reach: its sessions keep working and no window can attach to them.
	if err := clearStaleSocket(path); err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("restrict socket %q: %w", path, err)
	}

	// Take the unlink away from the standard library. Its Close removes the
	// path by name, which would delete a successor's socket as readily as this
	// one's: the package has no way to know the name changed hands. The owner
	// below checks that before removing anything.
	unix, ok := l.(*net.UnixListener)
	if !ok {
		_ = l.Close()
		return nil, fmt.Errorf("listen on %s: the listener is not a unix socket", addr)
	}
	unix.SetUnlinkOnClose(false)

	// Remember which file this listener created. Close removes the path only
	// while it still names this socket: a successor that replaced the name owns
	// it, and deleting it would take that daemon's endpoint away.
	owned, err := os.Stat(path)
	if err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("stat the socket %q: %w", path, err)
	}

	return &unixListener{Listener: unix, addr: addr, path: path, owned: owned}, nil
}

// clearStaleSocket removes path when nothing answers on it, and reports ErrLocked
// when a live process does.
func clearStaleSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat the socket %q: %w", path, err)
	}

	if socketIsServed(path) {
		return ErrLocked
	}

	// A dial that failed for a reason other than "nobody is listening" proves
	// nothing about the owner, so it is not a licence to delete the name.
	ctx, cancel := context.WithTimeout(context.Background(), socketProbeTimeout)
	defer cancel()

	var d net.Dialer
	if _, err := d.DialContext(ctx, "unix", path); err != nil && !isStaleSocketError(err) {
		return fmt.Errorf("probe the existing socket %q: %w", path, err)
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %q: %w", path, err)
	}
	return nil
}

// isStaleSocketError reports a dial failure that means "the file is there and
// nobody is listening". A timeout or a permission error proves nothing about the
// owner, so it is not a licence to delete the name.
func isStaleSocketError(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

func dial(ctx context.Context, addr Address, dir string) (net.Conn, error) {
	path := addr.SocketPath(dir)
	if path == "" {
		return nil, fmt.Errorf("dial %s: no socket directory: %w", addr, ErrProtocol)
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return conn, nil
}

type unixListener struct {
	net.Listener
	addr  Address
	path  string
	owned os.FileInfo
}

func (l *unixListener) Address() Address { return l.addr }

// Close stops accepting and removes the socket file, but only the one this
// listener created: a successor that bound the same name owns it now, and
// deleting it would leave that daemon unreachable.
//
// SameFile is the first check and not the only one: it compares inode and device,
// and the file system may hand a successor the inode this listener just
// released. A live listener still answers, so the probe is what makes the answer
// certain rather than probable.
func (l *unixListener) Close() error {
	err := l.Listener.Close()

	current, statErr := os.Stat(l.path)
	if statErr != nil || l.owned == nil || !os.SameFile(l.owned, current) {
		return err
	}
	if socketIsServed(l.path) {
		// Someone else is accepting on this name now; the file is theirs.
		return err
	}
	_ = os.Remove(l.path)
	return err
}

// socketIsServed reports whether a peer can connect to path. It is used only
// where a stale file is about to be removed, so a dial that succeeds means the
// name has an owner and must stay.
func socketIsServed(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), socketProbeTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

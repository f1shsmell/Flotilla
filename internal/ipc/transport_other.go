//go:build !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func platformAddr(a Address) string {
	return "unix:" + a.Channel + "/" + a.Name
}

func listen(addr Address, dir string) (Listener, error) {
	path := addr.SocketPath(dir)
	if path == "" {
		return nil, fmt.Errorf("listen on %s: no socket directory: %w", addr, ErrProtocol)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	// A crashed daemon leaves its socket behind; the directory is user-private,
	// so removing it cannot hit another user's endpoint.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %q: %w", path, err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("restrict socket %q: %w", path, err)
	}

	return &unixListener{Listener: l, addr: addr, path: path}, nil
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
	addr Address
	path string
}

func (l *unixListener) Address() Address { return l.addr }

func (l *unixListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}

//go:build !windows

package ipc

import (
	"errors"
	"net"
	"os"
	"testing"
)

// TestListenRefusesAnEndpointSomeoneIsServing pins the Ownership rule the Unix
// transport inherited from a Windows-only guarantee: a path another process is
// accepting on belongs to it. Unlinking it would leave that daemon holding a name
// no client can reach, with its sessions still running behind it.
func TestListenRefusesAnEndpointSomeoneIsServing(t *testing.T) {
	addr := testAddress()
	dir := t.TempDir()

	first, err := Listen(addr, dir)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	acceptc := asyncAccept(first)

	second, err := Listen(addr, dir)
	if err == nil {
		_ = second.Close()
		t.Fatal("a second listener took over an endpoint that was being served")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Listen = %v, want ErrLocked", err)
	}

	// The first listener still owns the path: the refused second one must not
	// have removed it.
	dialed, err := DialConn(t.Context(), addr, dir)
	if err != nil {
		t.Fatalf("DialConn after the refused second listener: %v", err)
	}
	t.Cleanup(func() { _ = dialed.Close() })
	finishAccept(t, acceptc)
}

// TestListenReplacesASocketNobodyServes pins the crash path: a daemon that died
// without cleaning up leaves a file with no listener behind it, and the next
// daemon has to be able to take that name.
func TestListenReplacesASocketNobodyServes(t *testing.T) {
	addr := testAddress()
	dir := t.TempDir()

	dead, err := Listen(addr, dir)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	path := addr.SocketPath(dir)

	// Drop the listener without removing the path, which is what a killed
	// process leaves behind.
	if err := dead.(*unixListener).Listener.Close(); err != nil {
		t.Fatalf("close the dead listener: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the stale socket is gone before the test can use it: %v", err)
	}

	revived, err := Listen(addr, dir)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	t.Cleanup(func() { _ = revived.Close() })

	acceptc := asyncAccept(revived)
	dialed, err := DialConn(t.Context(), addr, dir)
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}
	t.Cleanup(func() { _ = dialed.Close() })
	finishAccept(t, acceptc)
}

// TestClosingAListenerLeavesASuccessorsSocketAlone pins the other half of
// ownership: a listener that is shutting down removes the path only while that
// path is still its own file. A successor that replaced the name keeps it.
func TestClosingAListenerLeavesASuccessorsSocketAlone(t *testing.T) {
	addr := testAddress()
	dir := t.TempDir()

	first, err := Listen(addr, dir)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	path := addr.SocketPath(dir)

	// Stand in for a successor: a fresh socket file at the same path, created
	// after the first listener was bound.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the first socket: %v", err)
	}
	succ, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind the successor socket: %v", err)
	}
	t.Cleanup(func() { _ = succ.Close() })

	if err := first.Close(); err != nil {
		t.Fatalf("close the first listener: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("closing the first listener removed its successor's socket: %v", err)
	}
}

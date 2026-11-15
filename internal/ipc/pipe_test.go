package ipc

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

var pipeSeq atomic.Int64

// acceptTimeout bounds the wait for a peer to connect. On Windows a named pipe
// listener only creates its next instance after a connection arrives, so a dial
// can race a pending accept.
const acceptTimeout = 10 * time.Second

// testAddress returns an endpoint nobody else on this machine will use, so
// parallel test binaries and repeated runs do not collide.
func testAddress() Address {
	return Address{
		Channel: fmt.Sprintf("test-%d-%d", os.Getpid(), pipeSeq.Add(1)),
		Name:    "conn",
	}
}

// listenTest opens a listener on a fresh address and returns it with its
// directory, so a call can dial the same endpoint.
func listenTest(t *testing.T) (Listener, Address, string) {
	t.Helper()

	addr := testAddress()
	dir := t.TempDir()

	l, err := Listen(addr, dir)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	return l, addr, dir
}

type acceptResult struct {
	conn net.Conn
	err  error
}

// asyncAccept starts one Accept in the background. It must be started before
// the dial: Accept blocks until a peer arrives, and on Windows the pending
// instance has to exist before a dialler can find the pipe.
func asyncAccept(l Listener) <-chan acceptResult {
	c := make(chan acceptResult, 1)
	go func() {
		conn, err := l.Accept()
		c <- acceptResult{conn, err}
	}()
	return c
}

// finishAccept waits for an accepted connection and registers its cleanup.
func finishAccept(t *testing.T, acceptc <-chan acceptResult) net.Conn {
	t.Helper()

	select {
	case res := <-acceptc:
		if res.err != nil {
			t.Fatalf("Accept: %v", res.err)
		}
		t.Cleanup(func() { _ = res.conn.Close() })
		return res.conn
	case <-time.After(acceptTimeout):
		t.Fatal("Accept never returned")
		return nil
	}
}

// netPipe returns the two halves of a real transport connection: the accepted
// side and the dialled side. Tests exercise the same pipe production uses.
func netPipe(t *testing.T) (accepted, connected net.Conn) {
	t.Helper()

	l, addr, dir := listenTest(t)
	acceptc := asyncAccept(l)

	dialed, err := DialConn(t.Context(), addr, dir)
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}
	t.Cleanup(func() { _ = dialed.Close() })

	return finishAccept(t, acceptc), dialed
}

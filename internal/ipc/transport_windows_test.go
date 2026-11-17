//go:build windows

package ipc

import (
	"errors"
	"testing"
)

// TestSecondListenerOnOneEndpointIsRefused pins the single-daemon rule on
// Windows: a pipe name that already exists belongs to a live daemon, and the
// second one must be told so rather than silently sharing the channel.
func TestSecondListenerOnOneEndpointIsRefused(t *testing.T) {
	addr := testAddress()
	dir := t.TempDir()

	first, err := Listen(addr, dir)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	second, err := Listen(addr, dir)
	if err == nil {
		_ = second.Close()
		t.Fatal("a second listener on the same endpoint was accepted")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Listen = %v, want ErrLocked", err)
	}

	// The first listener is untouched: refusing the second must not disturb
	// the daemon that owns the endpoint.
	acceptc := asyncAccept(first)
	dialed, err := DialConn(t.Context(), addr, dir)
	if err != nil {
		t.Fatalf("DialConn after the refused second listener: %v", err)
	}
	t.Cleanup(func() { _ = dialed.Close() })
	finishAccept(t, acceptc)
}

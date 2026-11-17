package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// relayPair returns the two halves of one authenticated connection, so a test
// can drive the relay without a daemon or a host in the way.
func relayPair(t *testing.T, onNotify func(method string, params json.RawMessage)) (client, server *ipc.Conn) {
	t.Helper()

	addr := ipc.Address{Channel: "relay-test", Name: "pair"}
	dir := t.TempDir()

	l, err := ipc.Listen(addr, dir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	served := make(chan error, 1)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			served <- err
			return
		}
		server, err = ipc.Serve(nc, ipc.Config{Token: "shared", Role: "daemon"})
		served <- err
	}()

	client, err = ipc.Dial(ctx, addr, dir, ipc.Config{
		Token: "shared",
		Role:  "gui",
		Handler: func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
			onNotify(method, params)
			return struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(client.Close)

	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(server.Close)

	return client, server
}

// TestAFreshBindingHoldsEveryFrameUntilItsRelease pins the ordering a window
// depends on: the attach reply carries the history a renderer folds first, and
// anything the session produces while that reply is being prepared waits behind
// it. A frame that overtook the reply would put live work above the lines that
// came before it.
func TestAFreshBindingHoldsEveryFrameUntilItsRelease(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)
	_, server := relayPair(t, func(method string, _ json.RawMessage) {
		mu.Lock()
		received = append(received, method)
		mu.Unlock()
	})

	r := newRelay(nil)
	r.bind(server, "sess-order")
	t.Cleanup(func() { r.forget(server) })

	send := func() error {
		return server.Notify(session.MethodState, session.StateUpdate{ID: "sess-order", State: session.StateWorking})
	}

	// A frame produced while the attach reply is still being built.
	if err := r.deliver(server, send); err != nil {
		t.Fatalf("deliver while holding: %v", err)
	}

	// The frame is queued, not dropped: a hold that silently discarded it would
	// pass the "nothing arrived" check below for the wrong reason.
	if held := r.queues[server].heldCount(); held != 1 {
		t.Fatalf("the held queue carries %d frames, want 1", held)
	}

	// Waiting is the assertion: "did not arrive" cannot be observed any other
	// way, and a frame that overtook the reply would be here by now.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	if len(received) != 0 {
		early := append([]string(nil), received...)
		mu.Unlock()
		t.Fatalf("%v reached the client before the attach reply", early)
	}
	mu.Unlock()

	// The reply is out; everything held goes now, in arrival order.
	r.release(server)

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(received)
		mu.Unlock()
		if got == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 || received[0] != session.MethodState {
		t.Fatalf("after the release the client had %v, want one %q", received, session.MethodState)
	}
}

// TestHeldFramesKeepTheirArrivalOrder pins what a queue is for: release sends
// what arrived, in the order it arrived, not the newest frame alone.
func TestHeldFramesKeepTheirArrivalOrder(t *testing.T) {
	var (
		mu     sync.Mutex
		states []session.State
	)
	_, server := relayPair(t, func(method string, params json.RawMessage) {
		if method != session.MethodState {
			return
		}
		var update session.StateUpdate
		if err := json.Unmarshal(params, &update); err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		states = append(states, update.State)
	})

	r := newRelay(nil)
	r.bind(server, "sess-order")
	t.Cleanup(func() { r.forget(server) })

	want := []session.State{session.StateWorking, session.StateBlocked, session.StateDone}
	for _, state := range want {
		if err := r.deliver(server, func() error {
			return server.Notify(session.MethodState, session.StateUpdate{ID: "sess-order", State: state})
		}); err != nil {
			t.Fatalf("deliver %q while holding: %v", state, err)
		}
	}

	r.release(server)

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(states)
		mu.Unlock()
		if got == len(want) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(states) != len(want) {
		t.Fatalf("three held frames arrived as %v, want %v", states, want)
	}
	// The order is the assertion: a LIFO release would pass a length check and
	// fail here, which is the defect this test exists for.
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("held frames arrived as %v, want %v in that order", states, want)
		}
	}
}

// TestTheGateOpensForEveryFrameAfterTheRelease pins the live half: a queue that
// never released would show a window a frozen session.
func TestTheGateOpensForEveryFrameAfterTheRelease(t *testing.T) {
	var (
		mu   sync.Mutex
		seen int
	)
	_, server := relayPair(t, func(method string, _ json.RawMessage) {
		if method != session.MethodState {
			return
		}
		mu.Lock()
		seen++
		mu.Unlock()
	})

	r := newRelay(nil)
	r.bind(server, "sess-order")
	t.Cleanup(func() { r.forget(server) })
	r.release(server)

	for i := 0; i < 3; i++ {
		if err := r.deliver(server, func() error {
			return server.Notify(session.MethodState, session.StateUpdate{ID: "sess-order", State: session.StateWorking})
		}); err != nil {
			t.Fatalf("deliver after release: %v", err)
		}
	}

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := seen
		mu.Unlock()
		if got == 3 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("a released client received %d of 3 live frames", seen)
}

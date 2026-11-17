package ipc

import (
	"context"
	"sync"
	"testing"
)

// TestOnReplyOutsideACallRunsAtOnce pins the documented default: a caller that
// has no reply to wait for must not have its work queued forever.
func TestOnReplyOutsideACallRunsAtOnce(t *testing.T) {
	var ran bool

	OnReply(context.Background(), func() { ran = true })

	if !ran {
		t.Fatal("OnReply outside a call did not run its work")
	}
}

// TestOnReplyAfterTheReplyStillRuns pins the failure a handler cannot see: a
// callback registered once the reply has gone has nothing left to order against,
// and dropping it would silently lose the work it carried. The alternative —
// appending to a slice that was already drained — never runs at all.
func TestOnReplyAfterTheReplyStillRuns(t *testing.T) {
	gate := &replyGate{}

	// The reply is out.
	gate.close()

	var ran bool
	contextWithGate := context.WithValue(context.Background(), replyKey{}, gate)
	OnReply(contextWithGate, func() { ran = true })

	if !ran {
		t.Fatal("OnReply after the reply silently dropped its work")
	}
}

// TestOnReplyRunsQueuedWorkInOrder pins the ordering the gate exists for: a
// handler queues what must follow its reply, and the gate runs it in the order
// it was registered.
func TestOnReplyRunsQueuedWorkInOrder(t *testing.T) {
	var (
		mu     sync.Mutex
		order  []int
		gate   = &replyGate{}
		ctxKey = context.WithValue(context.Background(), replyKey{}, gate)
	)

	for i := 1; i <= 3; i++ {
		OnReply(ctxKey, func() {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		})
	}

	mu.Lock()
	if len(order) != 0 {
		mu.Unlock()
		t.Fatalf("%v ran before the reply", order)
	}
	mu.Unlock()

	gate.close()

	mu.Lock()
	defer mu.Unlock()
	want := []int{1, 2, 3}
	if len(order) != len(want) {
		t.Fatalf("queued work ran as %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("queued work ran as %v, want %v in that order", order, want)
		}
	}
}

// TestTheGateClosesOnce pins the idempotence the dispatcher relies on: close is
// called from a defer and must be safe on every return path.
func TestTheGateClosesOnce(t *testing.T) {
	gate := &replyGate{}
	var ran int

	OnReply(context.WithValue(context.Background(), replyKey{}, gate), func() { ran++ })
	gate.close()
	gate.close()

	if ran != 1 {
		t.Fatalf("the gate ran its work %d times", ran)
	}
}

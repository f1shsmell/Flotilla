package outbox_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite" // the same driver the store uses, so the file format matches

	"github.com/f1shsmell/flotilla/internal/outbox"
	"github.com/f1shsmell/flotilla/internal/store"
)

// open returns a store over a fresh file, closed with the test.
func open(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// queue writes one task change and the effect it owes, which is how the
// orchestrator produces every effect this worker will ever see.
func queue(t *testing.T, s *store.Store, effect store.Effect) {
	t.Helper()

	task := store.Task{ID: "task-1", Prompt: "do it", Dialect: "claude", State: store.TaskDone}
	if _, err := s.AppendTaskWithEffect(context.Background(), store.EventTaskFinished, task, &effect); err != nil {
		t.Fatalf("AppendTaskWithEffect: %v", err)
	}
}

// recorder is a handler that remembers every payload it was given.
type recorder struct {
	mu       sync.Mutex
	payloads []json.RawMessage
	err      error
}

func (r *recorder) handle(_ context.Context, payload json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.payloads = append(r.payloads, payload)
	return r.err
}

func (r *recorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.payloads)
}

func TestDrainRunsTheQueuedEffect(t *testing.T) {
	s := open(t)
	payload := json.RawMessage(`{"task_id":"task-1","run":1}`)
	queue(t, s, store.Effect{Name: "task.output", Payload: payload})

	// The event sequence is read before the drain: it is what the receipt is
	// keyed by, and after the drain the queue no longer holds the row.
	queued, err := s.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("pending = %+v, want the one queued effect", queued)
	}

	rec := &recorder{}
	w, err := outbox.New(outbox.Config{Store: s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Register("task.output", rec.handle)

	settled, err := w.Drain(t.Context())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want 1", settled)
	}
	if rec.calls() != 1 {
		t.Fatalf("handler called %d times, want once", rec.calls())
	}

	// The queue is empty afterwards, which is what "settled" has to mean: a
	// receipt without an empty queue would be a retry waiting to happen.
	pending, err := s.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want the effect settled", pending)
	}

	applied, err := s.Applied(t.Context(), queued[0].EventSeq, "task.output")
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if !applied {
		t.Fatal("no receipt recorded, want one for the applied effect")
	}
}

// TestARestartDoesNotRepeatAnAppliedEffect is the reason receipts exist: the
// work was done, the process died before the outbox row settled, and the next
// start must not do it again.
func TestARestartDoesNotRepeatAnAppliedEffect(t *testing.T) {
	s := open(t)
	queue(t, s, store.Effect{Name: "task.output", Payload: json.RawMessage(`{}`)})

	pending, err := s.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want the one queued effect", pending)
	}

	// The receipt lands, but the outbox row does not: this is the crash window.
	if err := s.Complete(context.Background(), pending[0], "written before the crash"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Put the row back the way a crash would have left it.
	resurrect(t, s, pending[0])

	rec := &recorder{}
	w, err := outbox.New(outbox.Config{Store: s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Register("task.output", rec.handle)

	if _, err := w.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if rec.calls() != 0 {
		t.Fatalf("handler called %d times, want none: the receipt says the work was done", rec.calls())
	}

	pending, err = s.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want the row settled from its receipt", pending)
	}
}

// resurrect clears the settled state of one outbox row, which is what a crash
// between the write and the receipt leaves behind.
func resurrect(t *testing.T, s *store.Store, p store.PendingEffect) {
	t.Helper()

	// The store has no API for this and should not: it is a state only a crash
	// produces. Reaching for the database directly keeps the production path
	// free of a method that exists for tests. WAL lets this second connection
	// write while the store holds its own.
	db, err := sql.Open("sqlite", s.Path()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open the database directly: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(t.Context(),
		`UPDATE outbox SET state = 'pending' WHERE id = ?`, p.ID); err != nil {
		t.Fatalf("resurrect the outbox row: %v", err)
	}
}

func TestAFailingEffectIsRetriedThenAbandoned(t *testing.T) {
	s := open(t)
	queue(t, s, store.Effect{Name: "task.output", Payload: json.RawMessage(`{}`)})

	cause := errors.New("the disk said no")
	rec := &recorder{err: cause}

	w, err := outbox.New(outbox.Config{Store: s, MaxAttempts: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Register("task.output", rec.handle)

	// Three drains: each one makes an attempt and records it.
	for i := 0; i < 3; i++ {
		if _, err := w.Drain(t.Context()); err != nil {
			t.Fatalf("Drain %d: %v", i, err)
		}
	}

	if rec.calls() != 3 {
		t.Fatalf("handler called %d times, want exactly the 3 attempts", rec.calls())
	}

	// Past the cap the effect is abandoned rather than left pending forever: it
	// is a bug, and a queue that keeps retrying it looks busy doing nothing.
	pending, err := s.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want the failing effect abandoned", pending)
	}

	applied, err := s.Applied(t.Context(), 1, "task.output")
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if applied {
		t.Fatal("a receipt was recorded for work that never succeeded")
	}
}

func TestAnEffectNoHandlerClaimsIsAbandoned(t *testing.T) {
	s := open(t)
	queue(t, s, store.Effect{Name: "task.output", Payload: json.RawMessage(`{}`)})

	w, err := outbox.New(outbox.Config{Store: s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	settled, err := w.Drain(t.Context())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want the unknown effect settled by abandoning it", settled)
	}

	pending, err := s.Pending(t.Context(), 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want nothing left: retrying cannot teach the worker a handler", pending)
	}
}

func TestDrainIsEmptyWhenNothingIsOwed(t *testing.T) {
	s := open(t)

	w, err := outbox.New(outbox.Config{Store: s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	settled, err := w.Drain(t.Context())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if settled != 0 {
		t.Fatalf("settled = %d, want 0 on an empty queue", settled)
	}
}

func TestNewRefusesNoStore(t *testing.T) {
	if _, err := outbox.New(outbox.Config{}); err == nil {
		t.Fatal("New returned nil error, want a refusal without a store")
	}
}

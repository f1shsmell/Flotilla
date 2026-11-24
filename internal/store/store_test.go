package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/f1shsmell/flotilla/internal/store"
)

// open returns a store over a fresh file. The close is not cosmetic: a test
// that leaves the file open on Windows keeps the directory from being removed.
func open(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// dbPath names a database file inside the test's own directory. A reopen test
// needs the path, not the store, so it is returned on its own.
func dbPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.db")
}

// TestOneCommitWritesTheEventItsProjectionAndItsEffect is the contract this
// package exists for: a fact, the view of it, and the work it owes land
// together, so no reader can see one without the others.
func TestOneCommitWritesTheEventItsProjectionAndItsEffect(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	seq, err := s.Append(ctx, store.Commit{
		Event:  store.Event{Kind: "session.started", SessionID: "sess-1"},
		Effect: &store.Effect{Name: "session.persist", Payload: json.RawMessage(`{"id":"sess-1"}`)},
		Apply: func(ctx context.Context, tx *sql.Tx, seq uint64) error {
			// The projection is written through the commit's own transaction;
			// reaching for s.db here would be the bug this API prevents.
			_, err := tx.ExecContext(ctx,
				`INSERT INTO seen (client, session_id, at) VALUES (?, ?, ?)`,
				"window-a", "sess-1", seq)
			return err
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq == 0 {
		t.Fatal("Append returned no sequence")
	}

	events, err := s.Events(ctx, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "session.started" {
		t.Fatalf("events = %+v, want the one that was committed", events)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Name != "session.persist" {
		t.Fatalf("pending = %+v, want the queued effect", pending)
	}
}

// TestAFailedProjectionRollsTheEventBack pins the other half: an event whose
// projection cannot be written must not be recorded either. Half a commit is
// a state the next start cannot explain.
func TestAFailedProjectionRollsTheEventBack(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	boom := errors.New("the projection refused")
	_, err := s.Append(ctx, store.Commit{
		Event: store.Event{Kind: "session.started", SessionID: "sess-1"},
		Apply: func(context.Context, *sql.Tx, uint64) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Append = %v, want the projection's error", err)
	}

	events, err := s.Events(ctx, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none: the projection failed", events)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none: the event was rolled back", pending)
	}
}

// TestAnEffectRunsOnceAndIsReceipted is the outbox's whole point: a worker that
// dies after doing the work must not do it again on the next pass.
func TestAnEffectRunsOnceAndIsReceipted(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.Append(ctx, store.Commit{
		Event:  store.Event{Kind: "turn.finished", SessionID: "sess-1"},
		Effect: &store.Effect{Name: "checkpoint.capture"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want one", pending)
	}

	if err := s.Complete(ctx, pending[0], "ref captured"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	applied, err := s.Applied(ctx, pending[0].EventSeq, pending[0].Name)
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if !applied {
		t.Fatal("the receipt was not recorded")
	}

	// A second pass over the queue finds nothing: the row is settled.
	again, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("pending = %+v, want none after completing", again)
	}
}

// TestAFailedAttemptStaysQueued pins the retry discipline: a transient failure
// leaves the work owed, with the attempt and its reason recorded.
func TestAFailedAttemptStaysQueued(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.Append(ctx, store.Commit{
		Event:  store.Event{Kind: "turn.finished"},
		Effect: &store.Effect{Name: "checkpoint.capture"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if err := s.Fail(ctx, pending[0], errors.New("the disk was busy")); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	again, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("pending = %+v, want the row to stay queued", again)
	}
	if again[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", again[0].Attempts)
	}

	applied, err := s.Applied(ctx, again[0].EventSeq, again[0].Name)
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if applied {
		t.Fatal("a failed attempt must not leave a receipt")
	}
}

// TestAbandonTakesTheRowOutOfTheQueue is the permanent-failure exit: the work
// will not be done, and the queue must stop offering it.
func TestAbandonTakesTheRowOutOfTheQueue(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.Append(ctx, store.Commit{
		Event:  store.Event{Kind: "turn.finished"},
		Effect: &store.Effect{Name: "checkpoint.capture"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	pending, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if err := s.Abandon(ctx, pending[0], errors.New("no worktree")); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	again, err := s.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("pending = %+v, want none after abandoning", again)
	}
}

// TestTheDatabaseOutlivesTheProcess is the restart case: a second open reads
// what the first one committed, and does not re-create it.
func TestTheDatabaseOutlivesTheProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	ctx := context.Background()

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := first.Append(ctx, store.Commit{
		Event:  store.Event{Kind: "session.started", SessionID: "sess-1"},
		Effect: &store.Effect{Name: "session.persist"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	events, err := second.Events(ctx, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want the one the first store committed", events)
	}

	// The queue survives too: a restart owes the work it had not finished.
	pending, err := second.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want the unfinished effect", pending)
	}
}

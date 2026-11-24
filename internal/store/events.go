package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Event is one fact the daemon accepted. The log is append-only: a correction
// is a later event, never a rewrite, because a reader that saw the old row must
// be able to explain what it saw.
type Event struct {
	Seq uint64 `json:"seq"`
	// Kind names the fact, e.g. "session.started".
	Kind string `json:"kind"`
	// SessionID scopes the fact; empty for a fact about the daemon itself.
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	At        time.Time       `json:"at"`
}

// Effect is work owed as a consequence of an event. It is written in the same
// transaction as the event: an accepted fact with no queued effect is a fact
// the daemon will never act on.
type Effect struct {
	// Name identifies the work, e.g. "session.persist". It is also the receipt
	// key, so one effect name runs at most once per event.
	Name    string
	Payload json.RawMessage
}

// Commit is one atomic change: the event, the projection rows it changes, and
// the effects it owes. Everything in it lands or nothing does.
type Commit struct {
	Event  Event
	Effect *Effect
	// Apply writes the projection rows. It runs inside the same transaction, so
	// a projection that cannot be written rolls the event back too.
	Apply func(ctx context.Context, tx *sql.Tx, seq uint64) error
}

// ErrAlreadyApplied reports an effect whose receipt exists: the work was done,
// so doing it again would duplicate a side effect.
var ErrAlreadyApplied = errors.New("store: effect already applied")

// Append records one commit. The event gets the next sequence, the projection
// is written, and the effect is queued — in one transaction.
func (s *Store) Append(ctx context.Context, c Commit) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin a commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	at := c.Event.At
	if at.IsZero() {
		at = time.Now()
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO events (kind, session_id, payload, at) VALUES (?, ?, ?, ?)`,
		c.Event.Kind, c.Event.SessionID, []byte(c.Event.Payload), at.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("record %s: %w", c.Event.Kind, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read the sequence of %s: %w", c.Event.Kind, err)
	}

	if c.Apply != nil {
		if err := c.Apply(ctx, tx, uint64(seq)); err != nil {
			return 0, err
		}
	}

	if c.Effect != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO outbox (event_seq, effect, payload, at) VALUES (?, ?, ?, ?)`,
			seq, c.Effect.Name, []byte(c.Effect.Payload), at.UnixNano()); err != nil {
			return 0, fmt.Errorf("queue %s for %s: %w", c.Effect.Name, c.Event.Kind, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit %s: %w", c.Event.Kind, err)
	}
	return uint64(seq), nil
}

// PendingEffect is one queued effect waiting to run.
type PendingEffect struct {
	ID       int64
	EventSeq uint64
	Name     string
	Payload  json.RawMessage
	Attempts int
}

// Pending returns the effects still owed, oldest first. It is what a worker
// loop reads after a restart: the queue is the durable record of what the
// daemon had not finished.
func (s *Store) Pending(ctx context.Context, limit int) ([]PendingEffect, error) {
	if limit <= 0 {
		limit = 64
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, event_seq, effect, payload, attempts
		   FROM outbox WHERE state = 'pending' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read the outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []PendingEffect
	for rows.Next() {
		var p PendingEffect
		var payload []byte
		if err := rows.Scan(&p.ID, &p.EventSeq, &p.Name, &payload, &p.Attempts); err != nil {
			return nil, fmt.Errorf("read an outbox row: %w", err)
		}
		p.Payload = payload
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the outbox: %w", err)
	}
	return out, nil
}

// Applied reports whether the effect named name was already applied for the
// event. A worker checks this before doing anything with a side effect.
func (s *Store) Applied(ctx context.Context, eventSeq uint64, name string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM receipts WHERE event_seq = ? AND effect = ?`,
		eventSeq, name).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the receipt for %s: %w", name, err)
	}
	return true, nil
}

// Complete records that the effect at id was applied: the receipt and the
// outbox row settle in one transaction, so a crash between them cannot leave
// the queue saying "still owed" about work that is done.
func (s *Store) Complete(ctx context.Context, p PendingEffect, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin completing %s: %w", p.Name, err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixNano()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO receipts (event_seq, effect, at, detail) VALUES (?, ?, ?, ?)`,
		p.EventSeq, p.Name, now, detail); err != nil {
		return fmt.Errorf("record the receipt for %s: %w", p.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE outbox SET state = 'done' WHERE id = ?`, p.ID); err != nil {
		return fmt.Errorf("settle %s: %w", p.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completing %s: %w", p.Name, err)
	}
	return nil
}

// Fail records a failed attempt. The row stays pending so the next pass retries
// it; a caller that knows the failure is permanent uses Abandon instead.
func (s *Store) Fail(ctx context.Context, p PendingEffect, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	message := ""
	if cause != nil {
		message = cause.Error()
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?`,
		message, p.ID); err != nil {
		return fmt.Errorf("record a failed attempt at %s: %w", p.Name, err)
	}
	return nil
}

// Abandon takes a row out of the queue without a receipt: the work will not be
// done, and the caller has decided that is final.
func (s *Store) Abandon(ctx context.Context, p PendingEffect, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	message := ""
	if cause != nil {
		message = cause.Error()
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET state = 'abandoned', last_error = ? WHERE id = ?`,
		message, p.ID); err != nil {
		return fmt.Errorf("abandon %s: %w", p.Name, err)
	}
	return nil
}

// Events reads the log, oldest first. afterSeq lets a caller resume from where
// it stopped; zero reads from the beginning.
func (s *Store) Events(ctx context.Context, afterSeq uint64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 256
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, kind, session_id, payload, at
		   FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("read the event log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var e Event
		var payload []byte
		var at int64
		if err := rows.Scan(&e.Seq, &e.Kind, &e.SessionID, &payload, &at); err != nil {
			return nil, fmt.Errorf("read an event: %w", err)
		}
		e.Payload = payload
		e.At = time.Unix(0, at)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the event log: %w", err)
	}
	return out, nil
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNoTask reports a task id this store does not hold.
var ErrNoTask = errors.New("store: no such task")

// ErrTaskExists reports a task id the store already holds. It is the receipt an
// idempotent enqueue reads: the work was accepted once, and a retry must not
// queue it a second time.
var ErrTaskExists = errors.New("store: task already recorded")

// TaskState is where one unattended job stands. It is a closed set: a row whose
// state this build does not recognise is reported as-is rather than mapped onto
// the nearest one, because guessing here would either re-run finished work or
// claim work that never ran.
type TaskState string

const (
	// TaskQueued means the task has not started and will.
	TaskQueued TaskState = "queued"
	// TaskRunning means a run is in flight right now.
	TaskRunning TaskState = "running"
	// TaskWaiting means the task stopped to ask a person something. Nothing is
	// running and nothing is being spent while it waits: that is the whole point
	// of naming the state instead of holding a process open.
	TaskWaiting TaskState = "waiting"
	// TaskDone means the last run finished and its work stands.
	TaskDone TaskState = "done"
	// TaskFailed means the last run ended without the work happening. Whether it
	// may run again is the orchestrator's decision, not this state's.
	TaskFailed TaskState = "failed"
	// TaskCancelled means a person stopped it. It is distinct from failed: one
	// is an accident, the other an instruction.
	TaskCancelled TaskState = "cancelled"
)

// Valid reports whether s is one of the states this build understands.
func (s TaskState) Valid() bool {
	switch s {
	case TaskQueued, TaskRunning, TaskWaiting, TaskDone, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// Task is one unattended job, as stored.
type Task struct {
	ID string `json:"id"`
	// Prompt is the instruction the agent receives.
	Prompt string `json:"prompt"`
	// Dialect names the headless dialect that runs it, e.g. "claude".
	Dialect string `json:"dialect"`
	// Dir is the working directory. Empty inherits the daemon's.
	Dir string `json:"dir,omitempty"`
	// Access is the dialect's own word for what the agent may do.
	Access string `json:"access,omitempty"`
	// State is where the task stands.
	State TaskState `json:"state"`
	// Parent names the task that created this one, empty for a top-level job.
	// It is what lets a family of work be judged together.
	Parent string `json:"parent,omitempty"`
	// BudgetUSD bounds what this task may spend across all its runs. Zero means
	// unbounded, which is a decision the caller made rather than a default that
	// crept in.
	BudgetUSD float64 `json:"budget_usd,omitempty"`
	// SpentUSD is what its runs have reported so far. It only moves when a run
	// reports a cost: an unpriced run is not a free one, so this staying put is
	// the honest record of "unknown".
	SpentUSD float64 `json:"spent_usd,omitempty"`
	// Runs counts how many times the task was started. The crash cap counts it,
	// and a retry does not reset it.
	Runs int `json:"runs"`
	// ResumeID is the conversation a later run continues, when the dialect
	// reported one. Empty starts a new conversation.
	ResumeID string `json:"resume_id,omitempty"`
	// LastError is the last run's reason for failing, for a person to read.
	LastError string    `json:"last_error,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// Task event kinds. They are the facts beside the row, so a reader can explain a
// task's state without inferring it from the fields it happens to hold.
const (
	// EventTaskQueued records a new task.
	EventTaskQueued = "task.queued"
	// EventTaskStarted records one run beginning.
	EventTaskStarted = "task.started"
	// EventTaskWaiting records a run stopping to ask a person something.
	EventTaskWaiting = "task.waiting"
	// EventTaskFinished records a run ending, successfully or not.
	EventTaskFinished = "task.finished"
	// EventTaskCancelled records a person stopping the task.
	EventTaskCancelled = "task.cancelled"
)

// AppendTask records one task change as an event and writes the row in the same
// transaction. A row without its event would be a state nobody can explain, and
// an event without its row would be a fact the queue does not act on.
func (s *Store) AppendTask(ctx context.Context, kind string, t Task) (uint64, error) {
	return s.AppendTaskWithEffect(ctx, kind, t, nil)
}

// AppendTaskWithEffect records the same change and, in the same transaction,
// queues the work it owes. The two belong together for the reason the whole
// package exists: a task marked finished whose output was never stored is a
// state no later reader can repair, because nothing knows what was lost.
//
// A nil effect means the change owes nothing, which is the common case.
func (s *Store) AppendTaskWithEffect(ctx context.Context, kind string, t Task, effect *Effect) (uint64, error) {
	if err := validateTask(t); err != nil {
		return 0, err
	}

	payload, err := marshalTask(t)
	if err != nil {
		return 0, err
	}

	return s.Append(ctx, Commit{
		Event:  Event{Kind: kind, Payload: payload},
		Effect: effect,
		Apply: func(ctx context.Context, tx *sql.Tx, _ uint64) error {
			return writeTask(ctx, tx, t)
		},
	})
}

// validateTask is the write path's own gate, shared by every task writer so a
// second entry point cannot store a row the queue cannot read.
func validateTask(t Task) error {
	if !t.State.Valid() {
		return fmt.Errorf("task %q: state %q is not one this build knows", t.ID, t.State)
	}
	if t.ID == "" {
		return fmt.Errorf("task: empty id")
	}
	return nil
}

// AppendTaskOnce records a task only when its id is new, and reports whether it
// did. It is the store half of an idempotent enqueue: a caller that derives the
// id from what it is asking for — a schedule slot, a webhook request — can send
// the same request again and get the row it already made rather than a second
// run. A conflicting attempt writes no event either, because nothing happened.
func (s *Store) AppendTaskOnce(ctx context.Context, kind string, t Task) (Task, bool, error) {
	if err := validateTask(t); err != nil {
		return Task{}, false, err
	}
	payload, err := marshalTask(t)
	if err != nil {
		return Task{}, false, err
	}

	// The row is checked under the store lock first: a conflicting commit would
	// roll its event back, but burning a sequence number on a retry is noise in
	// a log a person reads.
	s.mu.Lock()
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ?`, t.ID).Scan(&exists)
	s.mu.Unlock()
	switch {
	case err == nil:
		existing, readErr := s.Task(ctx, t.ID)
		if readErr != nil {
			return Task{}, false, readErr
		}
		return existing, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Task{}, false, fmt.Errorf("read task %q: %w", t.ID, err)
	}

	if _, err := s.Append(ctx, Commit{
		Event: Event{Kind: kind, Payload: payload},
		Apply: func(ctx context.Context, tx *sql.Tx, _ uint64) error {
			inserted, err := insertTaskOnce(ctx, tx, t)
			if err != nil {
				return err
			}
			if !inserted {
				return ErrTaskExists
			}
			return nil
		},
	}); err != nil {
		// A writer that slipped in between the check and the commit is the
		// same retry; the row it made is the answer.
		if errors.Is(err, ErrTaskExists) {
			existing, readErr := s.Task(ctx, t.ID)
			if readErr != nil {
				return Task{}, false, readErr
			}
			return existing, false, nil
		}
		return Task{}, false, fmt.Errorf("record task %q: %w", t.ID, err)
	}
	return t, true, nil
}

// writeTask upserts one row. It runs inside a commit's transaction, so it takes
// the tx rather than the store: reaching for s.db here would be the bug the
// commit API exists to prevent.
func writeTask(ctx context.Context, tx *sql.Tx, t Task) error {
	created := t.Created
	if created.IsZero() {
		created = time.Now()
	}
	updated := t.Updated
	if updated.IsZero() {
		updated = time.Now()
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tasks (id, prompt, dialect, dir, access, state, parent,
		                    budget_usd, spent_usd, runs, resume_id, last_error,
		                    created, updated)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   prompt     = excluded.prompt,
		   dialect    = excluded.dialect,
		   dir        = excluded.dir,
		   access     = excluded.access,
		   state      = excluded.state,
		   parent     = excluded.parent,
		   budget_usd = excluded.budget_usd,
		   spent_usd  = excluded.spent_usd,
		   runs       = excluded.runs,
		   resume_id  = excluded.resume_id,
		   last_error = excluded.last_error,
		   updated    = excluded.updated`,
		t.ID, t.Prompt, t.Dialect, t.Dir, t.Access, string(t.State), t.Parent,
		t.BudgetUSD, t.SpentUSD, t.Runs, t.ResumeID, t.LastError,
		created.UnixNano(), updated.UnixNano()); err != nil {
		return fmt.Errorf("write task %q: %w", t.ID, err)
	}
	return nil
}

// insertTaskOnce writes one row only if its id is free. It runs inside a
// commit's transaction; a conflict reports false rather than updating, which is
// what tells the caller its request was a retry.
func insertTaskOnce(ctx context.Context, tx *sql.Tx, t Task) (bool, error) {
	created := t.Created
	if created.IsZero() {
		created = time.Now()
	}
	updated := t.Updated
	if updated.IsZero() {
		updated = time.Now()
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO tasks (id, prompt, dialect, dir, access, state, parent,
		                    budget_usd, spent_usd, runs, resume_id, last_error,
		                    created, updated)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO NOTHING`,
		t.ID, t.Prompt, t.Dialect, t.Dir, t.Access, string(t.State), t.Parent,
		t.BudgetUSD, t.SpentUSD, t.Runs, t.ResumeID, t.LastError,
		created.UnixNano(), updated.UnixNano())
	if err != nil {
		return false, fmt.Errorf("write task %q: %w", t.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read whether task %q was new: %w", t.ID, err)
	}
	return affected > 0, nil
}

// Task reads one task.
func (s *Store) Task(ctx context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx,
		`SELECT id, prompt, dialect, dir, access, state, parent,
		        budget_usd, spent_usd, runs, resume_id, last_error,
		        created, updated
		   FROM tasks WHERE id = ?`, id)

	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %q: %w", id, ErrNoTask)
	}
	if err != nil {
		return Task{}, fmt.Errorf("read task %q: %w", id, err)
	}
	return t, nil
}

// AllTasks reads every task this daemon knows, newest first. It is what a status
// report needs: a list of the queue rather than one state of it, because a
// person reading the fleet wants to see what is waiting behind what.
//
// The bound exists because a queue can hold more rows than a window can draw. A
// caller that asked for everything gets the newest, which is what a reader is
// looking for.
func (s *Store) AllTasks(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = maxStatusTasks
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, prompt, dialect, dir, access, state, parent,
		        budget_usd, spent_usd, runs, resume_id, last_error,
		        created, updated
		   FROM tasks ORDER BY created DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read the tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("read a task: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the tasks: %w", err)
	}
	return out, nil
}

// maxStatusTasks bounds what a status report lists. A queue longer than this is
// a queue nobody is reading, and the bound keeps one runaway producer from
// making every window's poll expensive.
const maxStatusTasks = 200

// TasksInState reads the tasks in one state, oldest first. It is what a worker
// reads after a restart: the rows are the durable record of what was owed.
func (s *Store) TasksInState(ctx context.Context, state TaskState) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, prompt, dialect, dir, access, state, parent,
		        budget_usd, spent_usd, runs, resume_id, last_error,
		        created, updated
		   FROM tasks WHERE state = ? ORDER BY created, id`, string(state))
	if err != nil {
		return nil, fmt.Errorf("read the %s tasks: %w", state, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("read a %s task: %w", state, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the %s tasks: %w", state, err)
	}
	return out, nil
}

// marshalTask encodes a task for the event payload. A payload that cannot be
// encoded is a programming error in this package, so it is returned rather than
// logged and dropped: an event whose payload is missing is a fact nobody can
// explain later.
func marshalTask(t Task) (json.RawMessage, error) {
	encoded, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("encode task %q: %w", t.ID, err)
	}
	return encoded, nil
}

// scanTask reads one row. It takes the interface both QueryRow and Rows satisfy
// so the two readers above cannot drift.
func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var state string
	var created, updated int64
	if err := row.Scan(&t.ID, &t.Prompt, &t.Dialect, &t.Dir, &t.Access, &state,
		&t.Parent, &t.BudgetUSD, &t.SpentUSD, &t.Runs, &t.ResumeID, &t.LastError,
		&created, &updated); err != nil {
		return Task{}, fmt.Errorf("read a task row: %w", err)
	}
	t.State = TaskState(state)
	t.Created = timeFromUnix(created)
	t.Updated = timeFromUnix(updated)
	return t, nil
}

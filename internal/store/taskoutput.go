package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNoTaskOutput reports a run whose output this store does not hold. It is a
// normal answer, not a failure: a run that printed nothing still happened.
var ErrNoTaskOutput = errors.New("store: no output for this run")

// TaskOutput is what one run of one task produced, as the agent reported it.
//
// It is stored rather than kept in memory because the record of what an
// unattended run said is the only thing a person can audit afterwards: the
// process is gone, no window was attached, and the task row holds a verdict but
// not the reasoning behind it.
type TaskOutput struct {
	TaskID string          `json:"task_id"`
	Run    int             `json:"run"`
	Body   json.RawMessage `json:"body"`
	At     time.Time       `json:"at"`
}

// OutputEffect is the name of the effect that stores a run's output. It is
// exported so the orchestrator queues it and the worker registers it without a
// shared string literal that nothing checks.
const OutputEffect = "task.output"

// TaskOutputPayload is what the output effect carries: which run of which task,
// and the run's own record as JSON.
//
// The body stays JSON rather than a typed field on purpose. This package stores
// what an execution layer produced without depending on that layer's types, so a
// change to what a run reports does not reach into the schema — and a worker
// that cannot decode a body it does not recognise still stores it.
type TaskOutputPayload struct {
	TaskID string          `json:"task_id"`
	Run    int             `json:"run"`
	Body   json.RawMessage `json:"body"`
}

// WriteTaskOutput stores one run's output, replacing any earlier copy of the
// same run. Replacing is what makes the effect idempotent: a worker that died
// after writing but before its receipt runs the write again, and the second copy
// has to be the same row rather than a second one.
func (s *Store) WriteTaskOutput(ctx context.Context, out TaskOutput) error {
	if out.TaskID == "" {
		return errors.New("write task output: empty task id")
	}
	if out.Run < 0 {
		return fmt.Errorf("write task output for %q: run %d is negative", out.TaskID, out.Run)
	}

	at := out.At
	if at.IsZero() {
		at = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO task_output (task_id, run, body, at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (task_id, run) DO UPDATE SET
		   body = excluded.body,
		   at   = excluded.at`,
		out.TaskID, out.Run, []byte(out.Body), at.UnixNano()); err != nil {
		return fmt.Errorf("write the output of task %q run %d: %w", out.TaskID, out.Run, err)
	}
	return nil
}

// TaskOutputs reads every run of one task, oldest run first.
func (s *Store) TaskOutputs(ctx context.Context, taskID string) ([]TaskOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, run, body, at FROM task_output
		  WHERE task_id = ? ORDER BY run`, taskID)
	if err != nil {
		return nil, fmt.Errorf("read the output of task %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []TaskOutput
	for rows.Next() {
		var one TaskOutput
		var body []byte
		var at int64
		if err := rows.Scan(&one.TaskID, &one.Run, &body, &at); err != nil {
			return nil, fmt.Errorf("read a run of task %q: %w", taskID, err)
		}
		one.Body = body
		one.At = timeFromUnix(at)
		out = append(out, one)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the output of task %q: %w", taskID, err)
	}
	return out, nil
}

// LastTaskOutput reads the newest run of one task that has output.
func (s *Store) LastTaskOutput(ctx context.Context, taskID string) (TaskOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx,
		`SELECT task_id, run, body, at FROM task_output
		  WHERE task_id = ? ORDER BY run DESC LIMIT 1`, taskID)

	var one TaskOutput
	var body []byte
	var at int64
	err := row.Scan(&one.TaskID, &one.Run, &body, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TaskOutput{}, fmt.Errorf("task %q: %w", taskID, ErrNoTaskOutput)
	case err != nil:
		return TaskOutput{}, fmt.Errorf("read the last output of task %q: %w", taskID, err)
	}
	one.Body = body
	one.At = timeFromUnix(at)
	return one, nil
}

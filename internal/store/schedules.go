package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/f1shsmell/flotilla/internal/schedule"
)

// ErrNoSchedule reports a schedule id this store does not hold.
var ErrNoSchedule = errors.New("store: no such schedule")

// AppendSchedule records one schedule change as an event and writes the row in
// the same transaction, for the reason AppendTask does: the row is what the
// clock acts on, and the event is how a person explains afterwards why it did.
func (s *Store) AppendSchedule(ctx context.Context, kind string, sch schedule.Schedule) (uint64, error) {
	if err := sch.Validate(); err != nil {
		return 0, fmt.Errorf("store schedule %q: %w", sch.ID, err)
	}
	payload, err := marshalSchedule(sch)
	if err != nil {
		return 0, err
	}

	return s.Append(ctx, Commit{
		Event: Event{Kind: kind, Payload: payload},
		Apply: func(ctx context.Context, tx *sql.Tx, _ uint64) error {
			return writeSchedule(ctx, tx, sch)
		},
	})
}

// RemoveSchedule records a deleted schedule and drops its row in the same
// transaction. The row goes because nothing should fire it again; the event
// stays, because a person reading the log still needs to see it existed.
func (s *Store) RemoveSchedule(ctx context.Context, id string, at time.Time) (uint64, error) {
	if id == "" {
		return 0, fmt.Errorf("remove a schedule: empty id")
	}
	payload, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return 0, fmt.Errorf("encode the removal of schedule %q: %w", id, err)
	}
	return s.Append(ctx, Commit{
		Event: Event{Kind: schedule.EventScheduleRemoved, Payload: payload, At: at},
		Apply: func(ctx context.Context, tx *sql.Tx, _ uint64) error {
			res, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
			if err != nil {
				return fmt.Errorf("remove schedule %q: %w", id, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("read whether schedule %q was there: %w", id, err)
			}
			if affected == 0 {
				return fmt.Errorf("schedule %q: %w", id, ErrNoSchedule)
			}
			return nil
		},
	})
}

// writeSchedule upserts one row inside a commit's transaction.
func writeSchedule(ctx context.Context, tx *sql.Tx, sch schedule.Schedule) error {
	created := sch.Created
	if created.IsZero() {
		created = time.Now()
	}
	updated := sch.Updated
	if updated.IsZero() {
		updated = time.Now()
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schedules (id, name, prompt, cadence_type, every_ms, cron_expression,
		                        timezone, target_type, agent_id, dialect, cwd, access, isolation,
		                        status, missed_run, next_at, last_at, last_task, last_error,
		                        skipped, created, updated)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   name            = excluded.name,
		   prompt          = excluded.prompt,
		   cadence_type    = excluded.cadence_type,
		   every_ms        = excluded.every_ms,
		   cron_expression = excluded.cron_expression,
		   timezone        = excluded.timezone,
		   target_type     = excluded.target_type,
		   agent_id        = excluded.agent_id,
		   dialect         = excluded.dialect,
		   cwd             = excluded.cwd,
		   access          = excluded.access,
		   isolation       = excluded.isolation,
		   status          = excluded.status,
		   missed_run      = excluded.missed_run,
		   next_at         = excluded.next_at,
		   last_at         = excluded.last_at,
		   last_task       = excluded.last_task,
		   last_error      = excluded.last_error,
		   skipped         = excluded.skipped,
		   updated         = excluded.updated`,
		sch.ID, sch.Name, sch.Prompt, sch.Cadence.Type, sch.Cadence.EveryMS, sch.Cadence.Expression,
		sch.Cadence.Timezone, sch.Target.Type, sch.Target.AgentID, sch.Target.Config.Dialect,
		sch.Target.Config.Cwd, sch.Target.Config.Access, sch.Target.Config.Isolation,
		string(sch.Status), string(sch.MissedRun), unixOrZero(sch.NextAt), unixOrZero(sch.LastAt),
		sch.LastTask, sch.LastError, sch.Skipped, created.UnixNano(), updated.UnixNano()); err != nil {
		return fmt.Errorf("write schedule %q: %w", sch.ID, err)
	}
	return nil
}

// Schedule reads one schedule.
func (s *Store) Schedule(ctx context.Context, id string) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id)
	sch, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return schedule.Schedule{}, fmt.Errorf("schedule %q: %w", id, ErrNoSchedule)
	}
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("read schedule %q: %w", id, err)
	}
	return sch, nil
}

// AllSchedules reads every schedule, oldest first. The clock walks this list
// on every tick, so the order is by creation: two schedules due in the same
// minute fire in the order a person made them.
func (s *Store) AllSchedules(ctx context.Context) ([]schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules ORDER BY created, id`)
	if err != nil {
		return nil, fmt.Errorf("read the schedules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []schedule.Schedule
	for rows.Next() {
		sch, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("read a schedule: %w", err)
		}
		out = append(out, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the schedules: %w", err)
	}
	return out, nil
}

const scheduleColumns = `id, name, prompt, cadence_type, every_ms, cron_expression,
	timezone, target_type, agent_id, dialect, cwd, access, isolation,
	status, missed_run, next_at, last_at, last_task, last_error,
	skipped, created, updated`

// scanSchedule reads one row; it takes the interface both QueryRow and Rows
// satisfy so the readers above cannot drift.
func scanSchedule(row interface{ Scan(...any) error }) (schedule.Schedule, error) {
	var (
		sch              schedule.Schedule
		cadenceType      string
		status, missed   string
		nextAt, lastAt   int64
		created, updated int64
	)
	if err := row.Scan(&sch.ID, &sch.Name, &sch.Prompt, &cadenceType, &sch.Cadence.EveryMS,
		&sch.Cadence.Expression, &sch.Cadence.Timezone, &sch.Target.Type, &sch.Target.AgentID,
		&sch.Target.Config.Dialect, &sch.Target.Config.Cwd, &sch.Target.Config.Access,
		&sch.Target.Config.Isolation, &status, &missed, &nextAt, &lastAt, &sch.LastTask,
		&sch.LastError, &sch.Skipped, &created, &updated); err != nil {
		return schedule.Schedule{}, fmt.Errorf("read a schedule row: %w", err)
	}
	sch.Cadence.Type = cadenceType
	sch.Status = schedule.Status(status)
	sch.MissedRun = schedule.MissedRun(missed)
	sch.NextAt = timeFromUnix(nextAt)
	sch.LastAt = timeFromUnix(lastAt)
	sch.Created = timeFromUnix(created)
	sch.Updated = timeFromUnix(updated)
	return sch, nil
}

// marshalSchedule encodes a schedule for an event payload.
func marshalSchedule(sch schedule.Schedule) ([]byte, error) {
	encoded, err := json.Marshal(sch)
	if err != nil {
		return nil, fmt.Errorf("encode schedule %q: %w", sch.ID, err)
	}
	return encoded, nil
}

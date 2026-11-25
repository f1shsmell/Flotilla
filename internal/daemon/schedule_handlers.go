package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/schedule"
	"github.com/f1shsmell/flotilla/internal/store"
)

// errNoClock reports a call the daemon cannot answer because it has no queue:
// a schedule is a task that fires later, and a daemon that cannot record a task
// must not promise one.
var errNoClock = ipc.Class(
	errors.New("the task queue is unavailable, so schedules cannot be served"), ipc.CodeMethodFailed)

// ScheduleListResponse is what a client reads back.
type ScheduleListResponse struct {
	Schedules []schedule.Schedule `json:"schedules"`
}

// ScheduleSetRequest creates or edits one schedule. An empty ID makes a new one:
// the daemon names it, the way a queue task is named.
type ScheduleSetRequest struct {
	ID        string             `json:"id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Prompt    string             `json:"prompt"`
	Cadence   schedule.Cadence   `json:"cadence"`
	Target    schedule.Target    `json:"target"`
	Status    schedule.Status    `json:"status,omitempty"`
	MissedRun schedule.MissedRun `json:"missed_run,omitempty"`
}

// ScheduleRemoveRequest deletes one schedule.
type ScheduleRemoveRequest struct {
	ID string `json:"id"`
}

// ScheduleTriggerRequest runs one schedule now. RequestID is the caller's
// idempotency key: the same id twice runs the job once, and the second answer
// is the first request's task.
type ScheduleTriggerRequest struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
}

// ScheduleTriggerResponse is what a trigger answers: the task it accepted, and
// whether this call made it. Created false means the request id was a retry.
type ScheduleTriggerResponse struct {
	Task    TaskEntry `json:"task"`
	Created bool      `json:"created"`
}

// listSchedules reports every schedule this daemon holds, oldest first.
func (d *Daemon) listSchedules() (any, error) {
	if d.clock == nil {
		return nil, fmt.Errorf("%s: %w", MethodScheduleList, errNoClock)
	}
	schedules, err := d.clock.store.AllSchedules(context.Background())
	if err != nil {
		return nil, fmt.Errorf("read the schedules: %w", err)
	}
	return ScheduleListResponse{Schedules: schedules}, nil
}

// setSchedule creates or edits one schedule. An edit keeps the stored
// schedule's creation time and any fields the request left empty, so a client
// that changes one field does not have to send the rest back correctly.
func (d *Daemon) setSchedule(params json.RawMessage) (any, error) {
	if d.clock == nil {
		return nil, fmt.Errorf("%s: %w", MethodScheduleSet, errNoClock)
	}
	var req ScheduleSetRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", MethodScheduleSet, err)
	}

	sch := schedule.Schedule{
		ID:        req.ID,
		Name:      req.Name,
		Prompt:    req.Prompt,
		Cadence:   req.Cadence,
		Target:    req.Target,
		Status:    req.Status,
		MissedRun: req.MissedRun,
	}
	if sch.ID == "" {
		id, err := newScheduleID()
		if err != nil {
			return nil, err
		}
		sch.ID = id
	}

	applied, err := d.clock.Apply(context.Background(), sch)
	if err != nil {
		// The scheduler codes what it knows was the request's fault; a store
		// failure is the daemon's, and is reported as such.
		return nil, fmt.Errorf("%s: %w", MethodScheduleSet, ipc.Class(err, ipc.CodeMethodFailed))
	}
	return applied, nil
}

// removeSchedule deletes one schedule.
func (d *Daemon) removeSchedule(params json.RawMessage) (any, error) {
	if d.clock == nil {
		return nil, fmt.Errorf("%s: %w", MethodScheduleRemove, errNoClock)
	}
	var req ScheduleRemoveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", MethodScheduleRemove, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: %w", MethodScheduleRemove,
			ipc.Class(errors.New("empty schedule id"), ipc.CodeBadRequest))
	}
	if err := d.clock.Remove(context.Background(), req.ID); err != nil {
		code := ipc.CodeMethodFailed
		if errors.Is(err, store.ErrNoSchedule) {
			code = ipc.CodeBadRequest
		}
		return nil, fmt.Errorf("%s: %w", MethodScheduleRemove, ipc.Class(err, code))
	}
	return struct{}{}, nil
}

// triggerSchedule runs one schedule now, idempotently per request id.
func (d *Daemon) triggerSchedule(ctx context.Context, params json.RawMessage) (any, error) {
	if d.clock == nil {
		return nil, fmt.Errorf("%s: %w", MethodScheduleTrigger, errNoClock)
	}
	var req ScheduleTriggerRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", MethodScheduleTrigger, err)
	}

	task, created, err := d.clock.Trigger(ctx, req.ID, req.RequestID)
	if err != nil {
		code := ipc.CodeMethodFailed
		if errors.Is(err, store.ErrNoSchedule) {
			code = ipc.CodeBadRequest
		}
		return nil, fmt.Errorf("%s: %w", MethodScheduleTrigger, ipc.Class(err, code))
	}
	return ScheduleTriggerResponse{Task: taskEntry(task), Created: created}, nil
}

// taskEntry renders one task for a client.
func taskEntry(t store.Task) TaskEntry {
	return TaskEntry{
		ID:        t.ID,
		Dialect:   t.Dialect,
		State:     string(t.State),
		Prompt:    t.Prompt,
		Dir:       t.Dir,
		Runs:      t.Runs,
		SpentUSD:  t.SpentUSD,
		BudgetUSD: t.BudgetUSD,
		LastError: t.LastError,
	}
}

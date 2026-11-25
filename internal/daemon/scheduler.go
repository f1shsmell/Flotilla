package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/f1shsmell/flotilla/internal/headless"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/schedule"
	"github.com/f1shsmell/flotilla/internal/store"
	"github.com/f1shsmell/flotilla/internal/worktree"
)

// scheduler is the daemon's clock. It keeps no timer of its own beyond the
// tick: every decision about which schedule fires is one store read, so a
// daemon that died comes back and sees the same next_at rows rather than a
// memory of what it was about to do.
//
// It lives in the daemon, not the GUI: a window may be closed when the clock
// reaches a slot, and the slot still has to fire.
type scheduler struct {
	store *store.Store
	tasks orchestrator
	log   *slog.Logger
	now   func() time.Time
	// tick is how often the clock looks, and how late a slot may start and
	// still count as on time. Zero means defaultTick.
	tick time.Duration
	// worktrees creates an isolated checkout for one fire whose target says
	// so. It is a variable so a test can answer without running git.
	worktrees func(ctx context.Context, repo, taskID string) (string, error)
	// dialects answers the headless dialect a target's agent runs under. Nil
	// uses headless.DialectFor; it is a variable so a test names its own.
	dialects func(name string) (headless.Dialect, bool)
	// agentDialect answers the unattended dialect a catalog agent runs under.
	agentDialect func(id string) (string, bool)
	// mu serialises the clock against schedule edits and manual triggers. A
	// fire writes the whole row back, so an edit that landed between the read
	// and the write would be silently undone.
	mu sync.Mutex
}

// orchestrator is the slice of orch.Orchestrator the scheduler uses. It is an
// interface here because these tests are about when a fire is accepted, not
// about how a task runs.
type orchestrator interface {
	EnqueueOnce(ctx context.Context, t store.Task) (store.Task, bool, error)
}

// defaultTick is how often the clock looks. A minute matches the finest cron
// field: a slot can be up to one tick late, and anything later than that is a
// slot the daemon was not serving for.
const defaultTick = time.Minute

// maxMissedCount bounds how many missed slots one pass counts. A schedule left
// alone for a year must not make a boot walk a million slots to say "many".
const maxMissedCount = 1000

// newScheduler builds a clock over the store. tasks must be the orchestrator
// that runs what a fire enqueues; nil disables firing, which a daemon whose
// queue failed to build uses.
func newScheduler(st *store.Store, tasks orchestrator, log *slog.Logger, now func() time.Time) *scheduler {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &scheduler{
		store: st, tasks: tasks, log: log, now: now, tick: defaultTick,
		worktrees: createScheduleWorktree, dialects: headless.DialectFor,
	}
}

// withTick sets the clock's resolution, ignoring a non-positive value so a
// caller that names none gets the default rather than a spinning ticker.
func (s *scheduler) withTick(tick time.Duration) *scheduler {
	if tick > 0 {
		s.tick = tick
	}
	return s
}

// discard is an io.Writer that drops everything, for a default logger.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Run serves the clock until ctx ends. It ticks rather than sleeping to the
// next due instant: a schedule edited between ticks must not wait for a timer
// that was set before the edit, and a tick is one store read.
func (s *scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Step(ctx); err != nil {
				// A failed pass is not a failed loop: the next tick reads the
				// rows again, and one bad row must not stop the rest.
				s.log.Warn("the schedule pass failed", "err", err.Error())
			}
		}
	}
}

// Step fires every schedule whose slot has come. It is one pass, so a test
// drives it by hand and never waits on a timer.
func (s *scheduler) Step(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.store.AllSchedules(ctx)
	if err != nil {
		return fmt.Errorf("read the schedules: %w", err)
	}
	now := s.now()
	for _, sch := range schedules {
		if err := s.fireDue(ctx, sch, now); err != nil {
			s.log.Warn("cannot fire a schedule", "schedule", sch.ID, "err", err.Error())
		}
	}
	return nil
}

// Apply creates or edits a schedule. A new schedule's first slot is computed
// from now, so creating one never fires it for a slot that passed before it
// existed. An edit keeps the stored slot and the clock's bookkeeping unless it
// changes the cadence: a new prompt must not postpone the job, and a stale
// client must not undo the overlap guard.
func (s *scheduler) Apply(ctx context.Context, sch schedule.Schedule) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx, sch)
}

// applyLocked is Apply without the lock, for the callers that already hold it.
func (s *scheduler) applyLocked(ctx context.Context, sch schedule.Schedule) (schedule.Schedule, error) {
	now := s.now()

	existing, err := s.store.Schedule(ctx, sch.ID)
	isNew := errors.Is(err, store.ErrNoSchedule)
	if err != nil && !isNew {
		return schedule.Schedule{}, fmt.Errorf("read schedule %q: %w", sch.ID, err)
	}
	if !isNew {
		// The clock owns when the schedule last ran, what it enqueued and how
		// many slots it skipped; an edit that carried those back would let a
		// stale client undo the overlap guard. The merge happens under the
		// clock's lock so an edit cannot overwrite a fire that landed meanwhile.
		sch.Created = existing.Created
		sch.LastAt = existing.LastAt
		sch.LastTask = existing.LastTask
		sch.LastError = existing.LastError
		sch.Skipped = existing.Skipped
		if sch.Status == "" {
			sch.Status = existing.Status
		}
		if sch.MissedRun == "" {
			sch.MissedRun = existing.MissedRun
		}
		// Editing the cadence makes the stored next_at a fact about the old
		// one, so it goes with it; every other edit keeps the slot, because
		// changing a prompt must not postpone the job.
		if sch.Cadence != existing.Cadence {
			sch.NextAt = time.Time{}
		} else if sch.NextAt.IsZero() {
			sch.NextAt = existing.NextAt
		}
	}

	if sch.Created.IsZero() {
		sch.Created = now
	}
	if sch.MissedRun == "" {
		sch.MissedRun = schedule.MissedSkip
	}
	if sch.Status == "" {
		sch.Status = schedule.StatusActive
	}
	if sch.Status == schedule.StatusPaused {
		sch.NextAt = time.Time{}
	} else if sch.NextAt.IsZero() {
		next, err := sch.Cadence.Next(now)
		if err != nil {
			return schedule.Schedule{}, fmt.Errorf("schedule %q: %w", sch.ID, ipc.Class(err, ipc.CodeBadRequest))
		}
		sch.NextAt = next
	}
	sch.Updated = now
	if err := sch.Validate(); err != nil {
		return schedule.Schedule{}, fmt.Errorf("schedule %q: %w", sch.ID, ipc.Class(err, ipc.CodeBadRequest))
	}
	// A target the catalog cannot run unattended is refused when it is set,
	// not when its slot comes. An unchanged target passed this when it was set,
	// so pausing a schedule whose agent was since removed still works.
	if isNew || existing.Target != sch.Target {
		if _, err := s.dialectFor(sch); err != nil {
			return schedule.Schedule{}, fmt.Errorf("set schedule %q: %w", sch.ID, ipc.Class(err, ipc.CodeBadRequest))
		}
	}
	if _, err := s.store.AppendSchedule(ctx, schedule.EventScheduleSet, sch); err != nil {
		return schedule.Schedule{}, fmt.Errorf("store schedule %q: %w", sch.ID, err)
	}
	return sch, nil
}

// Pause stops the clock without deleting the schedule. Runs that already
// queued still run: pausing stops future fires, it does not cancel work.
func (s *scheduler) Pause(ctx context.Context, id string) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sch, err := s.store.Schedule(ctx, id)
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("pause schedule %q: %w", id, err)
	}
	if sch.Status == schedule.StatusPaused {
		return sch, nil
	}
	sch.Status = schedule.StatusPaused
	sch.NextAt = time.Time{}
	return s.applyLocked(ctx, sch)
}

// Resume puts a paused schedule back on the clock, computing the next slot from
// now: the slots that passed while it was paused are gone, the same answer a
// missed slot gets.
func (s *scheduler) Resume(ctx context.Context, id string) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sch, err := s.store.Schedule(ctx, id)
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("resume schedule %q: %w", id, err)
	}
	sch.Status = schedule.StatusActive
	return s.applyLocked(ctx, sch)
}

// Remove deletes a schedule. An id the store does not hold is an error, not a
// silent success: a caller that mistyped an id must hear about it.
func (s *scheduler) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.store.RemoveSchedule(ctx, id, s.now()); err != nil {
		return fmt.Errorf("remove schedule %q: %w", id, err)
	}
	return nil
}

// fireDue moves one schedule along when its slot has come. A slot no older than
// one tick fires; an older one was missed, and the schedule's own policy says
// whether it fires once as a catch-up or not at all.
func (s *scheduler) fireDue(ctx context.Context, sch schedule.Schedule, now time.Time) error {
	if sch.Status != schedule.StatusActive || sch.NextAt.IsZero() || now.Before(sch.NextAt) {
		return nil
	}

	slot := sch.NextAt
	if now.Sub(slot) < s.tick {
		return s.fire(ctx, sch, slot, now, 0)
	}

	// The daemon was not serving for this slot. Count every slot up to now:
	// skip policy lets them all go, run_once fires the latest as one catch-up.
	count, latest := s.dueSlots(sch, now)
	if sch.MissedRun == schedule.MissedRunOnce {
		if err := s.fire(ctx, sch, latest, now, count-1); err != nil {
			return err
		}
		return nil
	}

	sch.Skipped += count
	sch.LastError = fmt.Sprintf("%d slot(s) passed while the daemon was not serving", count)
	return s.skip(ctx, sch, now, schedule.EventScheduleMissed)
}

// dueSlots counts the slots at or before now, and returns the latest of them.
// It is bounded: a schedule left alone for years reports the cap, not a walk.
func (s *scheduler) dueSlots(sch schedule.Schedule, now time.Time) (count int, latest time.Time) {
	slot := sch.NextAt
	for count < maxMissedCount && !slot.After(now) {
		latest = slot
		count++
		next, err := sch.Cadence.Next(slot)
		if err != nil {
			break
		}
		slot = next
	}
	if latest.IsZero() {
		latest = sch.NextAt
	}
	return count, latest
}

// fire accepts one slot: it refuses when the previous run is still in flight,
// enqueues the task when it does not, and always moves the clock past the slot.
func (s *scheduler) fire(ctx context.Context, sch schedule.Schedule, slot time.Time, now time.Time, skipped int) error {
	busy, err := s.busy(ctx, sch.LastTask)
	if err != nil {
		return err
	}
	if busy {
		sch.Skipped += skipped + 1
		sch.LastError = "the previous run is still in flight, so this slot was skipped"
		return s.skip(ctx, sch, now, schedule.EventScheduleSkipped)
	}

	task, created, err := s.enqueue(ctx, schedule.Fire{Schedule: sch, Slot: slot})
	if err != nil {
		sch.Skipped += skipped + 1
		sch.LastError = err.Error()
		return s.skip(ctx, sch, now, schedule.EventScheduleSkipped)
	}
	if !created {
		// This slot's task already exists: a previous pass enqueued it and
		// died before moving the clock. Accepting it again would be the second
		// run the idempotent id exists to prevent.
		sch.Skipped += skipped + 1
		sch.LastError = "this slot's run was already accepted"
		sch.LastTask = task.ID
		return s.skip(ctx, sch, now, schedule.EventScheduleSkipped)
	}

	next, err := sch.Cadence.Next(now)
	if err != nil {
		return fmt.Errorf("schedule %q: %w", sch.ID, err)
	}
	sch.Skipped += skipped
	sch.NextAt = next
	sch.LastAt = now
	sch.LastTask = task.ID
	sch.LastError = ""
	sch.Updated = now
	if _, err := s.store.AppendSchedule(ctx, schedule.EventScheduleFired, sch); err != nil {
		return fmt.Errorf("record the fire of schedule %q: %w", sch.ID, err)
	}
	s.log.Info("schedule fired", "schedule", sch.ID, "task", task.ID, "slot", slot)
	return nil
}

// skip moves the clock past a slot without enqueueing anything, and records
// why. The event kind is the caller's: a missed slot and an overlap are both
// skips, but the log says which.
func (s *scheduler) skip(ctx context.Context, sch schedule.Schedule, now time.Time, kind string) error {
	next, err := sch.Cadence.Next(now)
	if err != nil {
		return fmt.Errorf("schedule %q: %w", sch.ID, err)
	}
	sch.NextAt = next
	sch.Updated = now
	if _, err := s.store.AppendSchedule(ctx, kind, sch); err != nil {
		return fmt.Errorf("record the skip of schedule %q: %w", sch.ID, err)
	}
	s.log.Warn("schedule slot skipped", "schedule", sch.ID, "reason", sch.LastError)
	return nil
}

// busy reports whether the task a previous fire enqueued still holds the
// schedule. A queued, running or waiting task does: a second run of the same
// work would race the first. A settled task, or one the store no longer holds,
// does not.
func (s *scheduler) busy(ctx context.Context, taskID string) (bool, error) {
	if taskID == "" {
		return false, nil
	}
	t, err := s.store.Task(ctx, taskID)
	if errors.Is(err, store.ErrNoTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the previous run of the schedule: %w", err)
	}
	switch t.State {
	case store.TaskQueued, store.TaskRunning, store.TaskWaiting:
		return true, nil
	}
	return false, nil
}

// enqueue accepts one fire. The task id is derived from the schedule and the
// slot, so a fire that reaches the queue twice is one task: that is the receipt
// discipline the orchestrator promises, applied to the clock's own entry.
func (s *scheduler) enqueue(ctx context.Context, fire schedule.Fire) (store.Task, bool, error) {
	if s.tasks == nil {
		return store.Task{}, false, fmt.Errorf("schedule %q: the task queue is not available", fire.Schedule.ID)
	}

	// The receipt is checked before the fire is prepared: a retry must not make
	// a second worktree, and must not fail because the first one still exists.
	id := schedule.TaskID(fire)
	existing, err := s.store.Task(ctx, id)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, store.ErrNoTask):
		return store.Task{}, false, fmt.Errorf("read the task of schedule %q: %w", fire.Schedule.ID, err)
	}

	task, err := s.taskFor(ctx, fire)
	if err != nil {
		return store.Task{}, false, err
	}
	got, created, err := s.tasks.EnqueueOnce(ctx, task)
	if err != nil {
		return store.Task{}, false, fmt.Errorf("schedule %q: enqueue: %w", fire.Schedule.ID, err)
	}
	return got, created, nil
}

// dialectFor picks the headless dialect one fire runs under. An explicit
// dialect wins; an agent target asks the catalog which dialect that agent runs
// under unattended. A build that cannot answer refuses the target here, before
// a slot makes it a promise.
func (s *scheduler) dialectFor(sch schedule.Schedule) (string, error) {
	cfg := sch.Target.Config
	if cfg.Dialect != "" {
		if _, ok := s.dialects(cfg.Dialect); !ok {
			return "", fmt.Errorf("schedule %q: no headless dialect named %q", sch.ID, cfg.Dialect)
		}
		return cfg.Dialect, nil
	}
	if s.agentDialect == nil {
		return "", fmt.Errorf("schedule %q: agent targets need the catalog", sch.ID)
	}
	name, ok := s.agentDialect(sch.Target.AgentID)
	if !ok {
		return "", fmt.Errorf("schedule %q: agent %q runs no headless dialect", sch.ID, sch.Target.AgentID)
	}
	if _, ok := s.dialects(name); !ok {
		return "", fmt.Errorf("schedule %q: agent %q names dialect %q, which this build lacks",
			sch.ID, sch.Target.AgentID, name)
	}
	return name, nil
}

// taskFor renders one fire as the task it becomes: who runs it, where, and with
// which prompt.
func (s *scheduler) taskFor(ctx context.Context, fire schedule.Fire) (store.Task, error) {
	sch := fire.Schedule
	cfg := sch.Target.Config
	id := schedule.TaskID(fire)

	dialect, err := s.dialectFor(sch)
	if err != nil {
		return store.Task{}, err
	}

	dir := cfg.Cwd
	if sch.Target.Isolation() == "worktree" {
		created, err := s.worktrees(ctx, cfg.Cwd, id)
		if err != nil {
			return store.Task{}, fmt.Errorf("schedule %q: %w", sch.ID, err)
		}
		dir = created
	}

	return store.Task{
		ID:      id,
		Prompt:  sch.Prompt,
		Dialect: dialect,
		Dir:     dir,
		Access:  cfg.Access,
	}, nil
}

// Trigger accepts a fire asked for by a person or a webhook. requestID is the
// caller's idempotency key and is required: a trigger without one cannot be
// retried safely, and the whole point of this entry is that it can.
//
// The trigger does not move the clock: firing by hand is a run now, not a vote
// to skip the next slot. It does become the schedule's last run, so a clock
// slot that comes while it is still working is treated as an overlap.
func (s *scheduler) Trigger(ctx context.Context, id, requestID string) (store.Task, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == "" {
		return store.Task{}, false, fmt.Errorf("trigger: %w", ipc.Class(errors.New("empty schedule id"), ipc.CodeBadRequest))
	}
	if requestID == "" {
		return store.Task{}, false, fmt.Errorf("trigger of schedule %q: %w", id,
			ipc.Class(errors.New("a request id is required"), ipc.CodeBadRequest))
	}
	sch, err := s.store.Schedule(ctx, id)
	if err != nil {
		return store.Task{}, false, fmt.Errorf("trigger schedule %q: %w", id, err)
	}

	now := s.now()
	task, created, err := s.enqueue(ctx, schedule.Fire{Schedule: sch, Slot: now, RequestID: requestID})
	if err != nil {
		return store.Task{}, false, fmt.Errorf("trigger schedule %q: %w", id, err)
	}
	if !created {
		// The request id names a task that already exists: this is the retry,
		// and the existing row is its receipt.
		return task, false, nil
	}

	sch.LastAt = now
	sch.LastTask = task.ID
	sch.LastError = ""
	sch.Updated = now
	if _, err := s.store.AppendSchedule(ctx, schedule.EventScheduleFired, sch); err != nil {
		return task, true, fmt.Errorf("record the trigger of schedule %q: %w", id, err)
	}
	return task, true, nil
}

// createScheduleWorktree makes the isolated checkout one fire runs in. The
// branch is derived from the task id, so a worktree left behind by a crash is
// recognisable as belonging to that fire — and a retried trigger, which names
// the same task, never needs a second branch.
func createScheduleWorktree(ctx context.Context, repo, taskID string) (string, error) {
	branch := "flt/" + strings.TrimPrefix(taskID, "task-")
	created, err := worktree.Create(ctx, worktree.Spec{Repo: repo, Branch: branch})
	if err != nil {
		return "", fmt.Errorf("create a worktree for the fire: %w", err)
	}
	return created.Dir, nil
}

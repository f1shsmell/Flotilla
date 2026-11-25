package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/headless"
	"github.com/f1shsmell/flotilla/internal/schedule"
	"github.com/f1shsmell/flotilla/internal/store"
)

// agentSchedule is a schedule whose target is a catalog agent: no dialect in
// the config, because the preset is what says how the agent runs unattended.
func agentSchedule(t *testing.T, id string) schedule.Schedule {
	t.Helper()
	sch := hourlySchedule(t, id)
	sch.Target = schedule.Target{
		Type:    "agent",
		AgentID: "claude",
		Config:  schedule.TargetConfig{Cwd: t.TempDir()},
	}
	return sch
}

// TestAnAgentTargetTakesItsDialectFromTheCatalog pins the agent target: a
// schedule that names a catalog agent runs under that agent's unattended
// dialect, so a person does not have to know the dialect's own name.
func TestAnAgentTargetTakesItsDialectFromTheCatalog(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	applied, err := clock.Apply(ctx, agentSchedule(t, "sch-agent"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	clock.now = func() time.Time { return applied.NextAt.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Dialect != "claude" {
		t.Fatalf("tasks = %+v, want one under the catalog agent's dialect", tasks)
	}
}

// TestAnAgentWithoutAnUnattendedDialectIsRefusedWhenSet pins the refusal
// point: a target that could never run is refused at set time, not discovered
// at its first slot.
func TestAnAgentWithoutAnUnattendedDialectIsRefusedWhenSet(t *testing.T) {
	_, _, clock := clockDaemon(t, &fixedExecutor{})
	clock.agentDialect = func(string) (string, bool) { return "", false }

	if _, err := clock.Apply(t.Context(), agentSchedule(t, "sch-1")); err == nil {
		t.Fatal("Apply accepted an agent with no unattended dialect")
	}
}

// TestPausingWorksAfterTheAgentLeavesTheCatalog pins the exit: a schedule whose
// agent was uninstalled must still be pausable, because its target passed the
// check when it was set and pause does not change it.
func TestPausingWorksAfterTheAgentLeavesTheCatalog(t *testing.T) {
	_, _, clock := clockDaemon(t, &fixedExecutor{})
	ctx := t.Context()

	sch := agentSchedule(t, "sch-1")
	if _, err := clock.Apply(ctx, sch); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	clock.agentDialect = func(string) (string, bool) { return "", false }
	changed := sch
	changed.Target.AgentID = "notepad"
	if _, err := clock.Apply(ctx, changed); err == nil {
		t.Fatal("Apply accepted an edit to an agent that cannot run")
	}

	paused, err := clock.Pause(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Pause after the agent left: %v", err)
	}
	if paused.Status != schedule.StatusPaused {
		t.Fatalf("status = %q, want paused", paused.Status)
	}
}

// clockDaemon builds a daemon whose queue and clock are driven by hand. The
// store is the real one: these tests are about what the clock writes, not about
// a mock of it.
func clockDaemon(t *testing.T, exec *fixedExecutor) (*Daemon, *store.Store, *scheduler) {
	t.Helper()
	d, state := newTaskDaemon(t, exec)
	// The wall clock is pinned so a test states its own instants rather than
	// waiting for one. Every test that wants "later" moves the pin.
	d.clock.now = func() time.Time { return testNow }
	return d, state, d.clock
}

// testNow is the instant every clock test starts from: a Thursday at noon UTC.
var testNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// everyHour is a cadence whose slots a test can compute by hand.
func everyHour() schedule.Cadence {
	return schedule.Cadence{Type: "every", EveryMS: int64(time.Hour / time.Millisecond)}
}

func hourlySchedule(t *testing.T, id string) schedule.Schedule {
	t.Helper()
	return schedule.Schedule{
		ID:        id,
		Prompt:    "check the builds",
		Cadence:   everyHour(),
		Target:    schedule.Target{Type: "new-agent", Config: schedule.TargetConfig{Dialect: "claude", Cwd: t.TempDir()}},
		Status:    schedule.StatusActive,
		MissedRun: schedule.MissedSkip,
	}
}

func TestApplyStartsTheClockFromNowRatherThanAPastSlot(t *testing.T) {
	_, state, clock := clockDaemon(t, &fixedExecutor{})

	sch, err := clock.Apply(t.Context(), hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := testNow.Add(time.Hour); !sch.NextAt.Equal(want) {
		t.Fatalf("next = %s, want %s", sch.NextAt, want)
	}

	stored, err := state.Schedule(t.Context(), "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if !stored.NextAt.Equal(sch.NextAt) {
		t.Fatalf("stored next = %s, want %s", stored.NextAt, sch.NextAt)
	}
	if stored.Created.IsZero() {
		t.Fatal("created is zero: the row does not say when the schedule was made")
	}
}

func TestASlotThatHasNotComeDoesNotFire(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	if _, err := clock.Apply(ctx, hourlySchedule(t, "sch-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(exec.seen) != 0 {
		t.Fatalf("the executor ran %d times before the slot came", len(exec.seen))
	}
	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks = %+v, want none", tasks)
	}
}

func TestADueSlotEnqueuesOneTaskAndMovesTheClockForward(t *testing.T) {
	exec := &fixedExecutor{res: headless.Result{Output: "nothing to report", StopReason: "end_turn"}}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The clock reaches the slot.
	due := sch.NextAt
	clock.now = func() time.Time { return due.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 {
		fired, _ := state.Schedule(ctx, "sch-1")
		t.Fatalf("tasks = %+v, want the fire's one task (last error: %q)", tasks, fired.LastError)
	}
	task := tasks[0]
	if task.ID != schedule.TaskID(schedule.Fire{Schedule: sch, Slot: due}) {
		t.Errorf("task id = %q, want the id derived from the slot", task.ID)
	}
	if task.Prompt != sch.Prompt || task.Dialect != "claude" || task.Dir != sch.Target.Config.Cwd {
		t.Errorf("task = %+v, want the schedule's prompt, dialect and directory", task)
	}

	fired, err := state.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if !fired.LastAt.Equal(clock.now()) {
		t.Errorf("last = %s, want the fire's instant", fired.LastAt)
	}
	if !fired.NextAt.Equal(clock.now().Add(time.Hour)) {
		t.Errorf("next = %s, want an hour past the fire", fired.NextAt)
	}
	if fired.LastTask != task.ID {
		t.Errorf("last task = %q, want %q: the row names what the fire enqueued", fired.LastTask, task.ID)
	}
}

func TestTwoPassesOverTheSameSlotEnqueueOneTask(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	clock.now = func() time.Time { return sch.NextAt.Add(time.Second) }

	// Two passes at the same instant: the second sees the moved next_at and
	// does nothing. This is the anti-overlap rule at its smallest.
	for range 2 {
		if err := clock.Step(ctx); err != nil {
			t.Fatalf("Step: %v", err)
		}
	}

	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d tasks, want one: one slot fires once", len(tasks))
	}
}

func TestAMissedSlotIsSkippedAndCountedNotReplayed(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The daemon was down while four slots came and went.
	clock.now = func() time.Time { return sch.NextAt.Add(3*time.Hour + 30*time.Minute) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("%d tasks, want none: the skip policy does not replay missed slots", len(tasks))
	}

	fired, err := state.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if fired.Skipped != 4 {
		t.Errorf("skipped = %d, want the four slots that passed silently", fired.Skipped)
	}
	if fired.LastError == "" {
		t.Error("last error is empty: a person cannot see why the job did not run")
	}
	if !fired.NextAt.After(clock.now()) {
		t.Errorf("next = %s, want a future slot after %s", fired.NextAt, clock.now())
	}
}

func TestRunOnceCatchesUpASingleMissedSlot(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	sch := hourlySchedule(t, "sch-1")
	sch.MissedRun = schedule.MissedRunOnce
	sch, err := clock.Apply(ctx, sch)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	late := sch.NextAt.Add(3*time.Hour + 30*time.Minute)
	clock.now = func() time.Time { return late }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d tasks, want one catch-up", len(tasks))
	}
	// The catch-up is the latest slot that came due, not the oldest: it is the
	// one whose work is still worth doing.
	latest := sch.NextAt.Add(3 * time.Hour)
	if want := schedule.TaskID(schedule.Fire{Schedule: sch, Slot: latest}); tasks[0].ID != want {
		t.Errorf("task id = %q, want the latest missed slot %q", tasks[0].ID, want)
	}

	fired, err := state.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if fired.Skipped != 3 {
		t.Errorf("skipped = %d, want the three slots the catch-up did not cover", fired.Skipped)
	}
	if !fired.NextAt.After(late) {
		t.Errorf("next = %s, want a future slot after %s", fired.NextAt, late)
	}
}

func TestAPausedScheduleNeverFires(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	paused, err := clock.Pause(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !paused.NextAt.IsZero() {
		t.Fatalf("paused next = %s, want zero", paused.NextAt)
	}

	clock.now = func() time.Time { return sch.NextAt.Add(48 * time.Hour) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}
	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("%d tasks, want none while paused", len(tasks))
	}
}

func TestResumeComputesTheNextSlotFromNow(t *testing.T) {
	_, _, clock := clockDaemon(t, &fixedExecutor{})
	ctx := t.Context()

	if _, err := clock.Apply(ctx, hourlySchedule(t, "sch-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := clock.Pause(ctx, "sch-1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	later := testNow.Add(5 * time.Hour)
	clock.now = func() time.Time { return later }
	resumed, err := clock.Resume(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if want := later.Add(time.Hour); !resumed.NextAt.Equal(want) {
		t.Fatalf("next = %s, want %s: the paused hours are gone, not replayed", resumed.NextAt, want)
	}
}

func TestAFireDoesNotWaitForThePreviousRun(t *testing.T) {
	// Anti-overlap: the previous run is still queued when the next slot comes,
	// so the clock skips that slot rather than stacking one run on another.
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	clock.now = func() time.Time { return sch.NextAt.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	fired, err := state.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if fired.LastError != "" {
		t.Fatalf("last error = %q, want empty: enqueueing a task is what a fire means", fired.LastError)
	}
	if fired.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", fired.Skipped)
	}

	// The task is still queued — nothing drained it — so the next slot finds
	// the schedule busy and skips instead of queueing a second copy.
	nextSlot := fired.NextAt
	clock.now = func() time.Time { return nextSlot.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("second Step: %v", err)
	}

	overlapped, err := state.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if overlapped.Skipped != 1 {
		t.Errorf("skipped = %d, want the overlapped slot", overlapped.Skipped)
	}
	if overlapped.LastError == "" {
		t.Error("last error is empty: the overlap must be visible to a person")
	}
	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d tasks, want one: a second run would race the first", len(tasks))
	}
}

func TestTheClockWritesWhatItDidAsEvents(t *testing.T) {
	_, state, clock := clockDaemon(t, &fixedExecutor{})
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	clock.now = func() time.Time { return sch.NextAt.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	events, err := state.Events(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[schedule.EventScheduleSet] != 1 {
		t.Errorf("schedule.set = %d, want the creation", kinds[schedule.EventScheduleSet])
	}
	if kinds[schedule.EventScheduleFired] != 1 {
		t.Errorf("schedule.fired = %d, want the fire", kinds[schedule.EventScheduleFired])
	}
	if kinds[store.EventTaskQueued] != 1 {
		t.Errorf("task.queued = %d, want the task the fire enqueued", kinds[store.EventTaskQueued])
	}
}

func TestACadenceEditedLaterUsesTheNewSlot(t *testing.T) {
	_, _, clock := clockDaemon(t, &fixedExecutor{})
	ctx := t.Context()

	sch, err := clock.Apply(ctx, hourlySchedule(t, "sch-1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The person changes the cadence before the first slot; the next_at that
	// belongs to the old cadence is discarded rather than kept.
	edited := sch
	edited.Cadence = schedule.Cadence{Type: "every", EveryMS: int64(15 * time.Minute / time.Millisecond)}
	edited.NextAt = time.Time{}
	applied, err := clock.Apply(ctx, edited)
	if err != nil {
		t.Fatalf("Apply edit: %v", err)
	}
	if want := testNow.Add(15 * time.Minute); !applied.NextAt.Equal(want) {
		t.Fatalf("next = %s, want %s", applied.NextAt, want)
	}
}

func TestRemovingAnUnknownScheduleIsAnError(t *testing.T) {
	_, _, clock := clockDaemon(t, &fixedExecutor{})
	if err := clock.Remove(t.Context(), "sch-missing"); err == nil {
		t.Fatal("Remove accepted an id the store does not hold")
	}
}

func TestAFireWithNoQueueIsRecordedAsSkipped(t *testing.T) {
	d, _ := newTaskDaemon(t, &fixedExecutor{})
	ctx := t.Context()
	clock := newScheduler(d.state, nil, discardLogger(), func() time.Time { return testNow })

	sch := hourlySchedule(t, "sch-1")
	sch.NextAt = testNow
	if _, err := d.state.AppendSchedule(ctx, schedule.EventScheduleSet, sch); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := clock.fireDue(ctx, sch, testNow); err != nil {
		t.Fatalf("fireDue: %v", err)
	}

	stored, err := d.state.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if stored.LastError == "" {
		t.Error("last error is empty: a fire the queue could not take must say so")
	}
}

// TestAWorktreeTargetRunsInItsOwnCheckout pins the isolation rule: a schedule
// whose target says worktree hands the queue a directory the worktree layer
// made, and never the repository itself.
func TestAWorktreeTargetRunsInItsOwnCheckout(t *testing.T) {
	exec := &fixedExecutor{}
	_, state, clock := clockDaemon(t, exec)
	ctx := t.Context()

	repo := t.TempDir()
	isolated := t.TempDir()
	var askedRepo string
	clock.worktrees = func(_ context.Context, r, _ string) (string, error) {
		askedRepo = r
		return isolated, nil
	}

	sch := hourlySchedule(t, "sch-1")
	sch.Target.Config.Cwd = repo
	sch.Target.Config.Isolation = "worktree"
	applied, err := clock.Apply(ctx, sch)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	clock.now = func() time.Time { return applied.NextAt.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	if askedRepo != repo {
		t.Errorf("worktree asked for %q, want the target's repository %q", askedRepo, repo)
	}
	tasks, err := state.AllTasks(ctx, 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Dir != isolated {
		t.Fatalf("tasks = %+v, want one task in the isolated checkout %q", tasks, isolated)
	}
}

func TestAWorktreeFailureIsSkippedNotRetriedIntoABrokenState(t *testing.T) {
	_, _, clock := clockDaemon(t, &fixedExecutor{})
	ctx := t.Context()

	clock.worktrees = func(context.Context, string, string) (string, error) {
		return "", errors.New("the branch already exists")
	}
	sch := hourlySchedule(t, "sch-1")
	sch.Target.Config.Isolation = "worktree"
	applied, err := clock.Apply(ctx, sch)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	clock.now = func() time.Time { return applied.NextAt.Add(time.Second) }
	if err := clock.Step(ctx); err != nil {
		t.Fatalf("Step: %v", err)
	}

	stored, err := clock.store.Schedule(ctx, "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if stored.LastError == "" {
		t.Error("last error is empty: a fire that could not prepare its checkout must say so")
	}
	// The clock still moved on, or every later slot would retry the same
	// broken fire.
	if !stored.NextAt.After(clock.now()) {
		t.Errorf("next = %s, want the clock to move past the failed fire", stored.NextAt)
	}
}

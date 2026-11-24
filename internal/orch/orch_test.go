package orch_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/headless"
	"github.com/f1shsmell/flotilla/internal/orch"
	"github.com/f1shsmell/flotilla/internal/store"
)

// fakeExecutor answers a run from the fields a test set, and counts how many
// times it was asked. It is a struct rather than a function so a test can read
// the call count after the orchestrator has finished: "it was not run again" is
// the assertion several of these tests exist for.
type fakeExecutor struct {
	// results are answered in order; past the end the last one repeats.
	results []headless.Result
	// errs are answered in order; past the end the last one repeats.
	errs []error
	// tasks records what each run was asked to do.
	tasks []headless.Task
}

func (f *fakeExecutor) Run(_ context.Context, _ headless.Dialect, t headless.Task) (headless.Result, error) {
	f.tasks = append(f.tasks, t)

	i := len(f.tasks) - 1
	var res headless.Result
	if len(f.results) > 0 {
		if i < len(f.results) {
			res = f.results[i]
		} else {
			res = f.results[len(f.results)-1]
		}
	}

	var err error
	if len(f.errs) > 0 {
		if i < len(f.errs) {
			err = f.errs[i]
		} else {
			err = f.errs[len(f.errs)-1]
		}
	}
	return res, err
}

// newOrch builds an orchestrator over a fresh database.
func newOrch(t *testing.T, cfg orch.Config) (*orch.Orchestrator, *store.Store) {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cfg.Store = s
	o, err := orch.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return o, s
}

// dialect answers every name with the same harmless definition, so a test that
// is about the orchestrator never depends on a real agent being installed.
func dialect(string) (headless.Dialect, bool) {
	return headless.Dialect{Name: "fake", Command: "fake", Parser: headless.ParseClaudeStreamJSON}, true
}

func TestEnqueueStartsQueued(t *testing.T) {
	o, s := newOrch(t, orch.Config{})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if task.ID == "" {
		t.Fatal("Enqueue returned no id")
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task back: %v", err)
	}
	if stored.State != store.TaskQueued || stored.Runs != 0 || stored.SpentUSD != 0 {
		t.Fatalf("stored = %+v, want a fresh queued task", stored)
	}

	// The row is not the only record: the event beside it is what explains the
	// row, so a task with no event would be a state nobody can account for.
	events, err := s.Events(ctx, 0, 10)
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventTaskQueued {
		t.Fatalf("events = %+v, want one task.queued", events)
	}
}

func TestRefusesATaskWithNoDialectOrPrompt(t *testing.T) {
	o, _ := newOrch(t, orch.Config{})
	ctx := t.Context()

	if _, err := o.Enqueue(ctx, store.Task{Prompt: "do it"}); err == nil {
		t.Fatal("Enqueue accepted a task with no dialect")
	}
	if _, err := o.Enqueue(ctx, store.Task{Dialect: "fake"}); err == nil {
		t.Fatal("Enqueue accepted a task with no prompt")
	}
	if _, err := o.Enqueue(ctx, store.Task{Dialect: "fake", Prompt: "x", BudgetUSD: -1}); err == nil {
		t.Fatal("Enqueue accepted a negative budget")
	}
}

// TestAHeldTaskCostsNothing is the suspend-ask rule: a task that stops to ask a
// person something must not run, and therefore must not spend.
func TestAHeldTaskCostsNothing(t *testing.T) {
	exec := &fakeExecutor{}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	held, err := o.Hold(ctx, task.ID, "which branch?")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if held.State != store.TaskWaiting {
		t.Fatalf("state = %s, want waiting", held.State)
	}

	// A waiting task is not in the queued set, so the worker does not see it at
	// all. That is what makes waiting free rather than merely paused.
	_, more, err := o.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if more {
		t.Fatal("Next ran something, want the waiting task left alone")
	}
	if len(exec.tasks) != 0 {
		t.Fatalf("executor ran %d times, want zero while the task waits on a person", len(exec.tasks))
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.Runs != 0 || stored.SpentUSD != 0 {
		t.Fatalf("stored = %+v, want nothing run and nothing spent", stored)
	}

	// Releasing it puts it back in the queue, and only then does it run.
	if _, err := o.Release(ctx, task.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, more, err := o.Next(ctx); err != nil || !more {
		t.Fatalf("Next after release: more=%v err=%v, want the task run", more, err)
	}
	if len(exec.tasks) != 1 {
		t.Fatalf("executor ran %d times, want once after release", len(exec.tasks))
	}
}

func TestRunSettlesDone(t *testing.T) {
	exec := &fakeExecutor{results: []headless.Result{{
		SessionID:  "conv-1",
		StopReason: "end_turn",
		Output:     "ok",
		Usage:      headless.Usage{InputTokens: 10, OutputTokens: 2, CostUSD: 0.25, HasCost: true},
	}}}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskDone {
		t.Fatalf("state = %s, want done", stored.State)
	}
	if stored.Runs != 1 || stored.SpentUSD != 0.25 {
		t.Fatalf("runs/spent = %d/%.4f, want 1/0.25", stored.Runs, stored.SpentUSD)
	}
	// The conversation id has to survive: it is what a follow-up run continues,
	// and losing it makes every task a fresh context.
	if stored.ResumeID != "conv-1" {
		t.Fatalf("resume id = %q, want the id the run reported", stored.ResumeID)
	}
}

// TestATimeoutIsNeverResent is the retry rule. A run that timed out may have
// done its work, so running it again could do that work twice.
func TestATimeoutIsNeverResent(t *testing.T) {
	exec := &fakeExecutor{errs: []error{headless.ErrTimeout}}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskFailed {
		t.Fatalf("state = %s, want failed: a timed-out run is parked, not retried", stored.State)
	}

	// And the queue does not pick it up again. This is the assertion that
	// matters: a retry would be the blind resend.
	if _, more, err := o.Next(ctx); err != nil || more {
		t.Fatalf("Next: more=%v err=%v, want the parked task left alone", more, err)
	}
	if len(exec.tasks) != 1 {
		t.Fatalf("executor ran %d times, want exactly once", len(exec.tasks))
	}
}

// TestTheHopCapStopsALoop is the circuit breaker: a task that keeps failing
// outright stops on its own instead of spending a provider's quota forever.
func TestTheHopCapStopsALoop(t *testing.T) {
	failed := errors.New("the agent exited 1")
	exec := &fakeExecutor{errs: []error{failed}}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect, MaxRuns: 3})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Each Next runs the task once and requeues it until the cap. Four calls:
	// three runs, and a fourth that finds the queue empty.
	for i := 0; i < 4; i++ {
		if _, _, err := o.Next(ctx); err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
	}

	if len(exec.tasks) != 3 {
		t.Fatalf("executor ran %d times, want exactly the 3 allowed runs", len(exec.tasks))
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskFailed || stored.Runs != 3 {
		t.Fatalf("state/runs = %s/%d, want failed/3", stored.State, stored.Runs)
	}
	// The reason a person reads has to say the cap is why it stopped, not just
	// repeat the last agent error.
	if !strings.Contains(stored.LastError, orch.ErrRunsExhausted.Error()) {
		t.Fatalf("last error = %q, want it to name the run cap", stored.LastError)
	}
}

// TestTheBudgetGateStopsSpending covers the second gate: a task with a budget
// stops once its runs have reported enough cost, and nothing more is attempted.
func TestTheBudgetGateStopsSpending(t *testing.T) {
	exec := &fakeExecutor{results: []headless.Result{{
		Usage: headless.Usage{CostUSD: 1.50, HasCost: true},
	}}}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake", BudgetUSD: 1.00})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// The first task: it runs and overspends its budget in one go.
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}
	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.SpentUSD != 1.50 {
		t.Fatalf("spent = %v, want the run's reported cost", stored.SpentUSD)
	}

	// A second task under the same budget is refused before it runs, so nothing
	// is spent twice to learn the same thing.
	second, err := o.Enqueue(ctx, store.Task{Prompt: "again", Dialect: "fake", BudgetUSD: 1.00})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Burn its budget the way a first run would.
	before := len(exec.tasks)
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(exec.tasks) != before+1 {
		t.Fatalf("second task did not run: %d calls", len(exec.tasks))
	}

	// Requeue it with its budget already spent, which is what a retry after a
	// failed run would look like.
	reloaded, err := s.Task(ctx, second.ID)
	if err != nil {
		t.Fatalf("read the second task: %v", err)
	}
	reloaded.State = store.TaskQueued
	reloaded.SpentUSD = reloaded.BudgetUSD
	if _, err := s.AppendTask(ctx, store.EventTaskQueued, reloaded); err != nil {
		t.Fatalf("requeue: %v", err)
	}

	before = len(exec.tasks)
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(exec.tasks) != before {
		t.Fatalf("executor ran a task whose budget was spent (%d calls, want %d)", len(exec.tasks), before)
	}

	settled, err := s.Task(ctx, second.ID)
	if err != nil {
		t.Fatalf("read the settled task: %v", err)
	}
	if settled.State != store.TaskFailed {
		t.Fatalf("state = %s, want failed: a spent budget settles the task", settled.State)
	}
}

// TestRecoverParksTheRunTheDaemonDiedUnder is the restart rule: a row that says
// running is a run whose outcome is unknown, and running it again could do the
// work twice.
func TestRecoverParksTheRunTheDaemonDiedUnder(t *testing.T) {
	o, s := newOrch(t, orch.Config{})
	ctx := t.Context()

	// A row left behind by a daemon that died mid-run.
	interrupted := store.Task{
		ID:      "task-interrupted",
		Prompt:  "do it",
		Dialect: "fake",
		State:   store.TaskRunning,
		Runs:    1,
	}
	if _, err := s.AppendTask(ctx, store.EventTaskStarted, interrupted); err != nil {
		t.Fatalf("write the interrupted row: %v", err)
	}

	recovered, err := o.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 1 || recovered[0].ID != interrupted.ID {
		t.Fatalf("recovered = %+v, want the interrupted task", recovered)
	}

	stored, err := s.Task(ctx, interrupted.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskFailed {
		t.Fatalf("state = %s, want failed: an unknown outcome is parked, not resumed", stored.State)
	}
	if stored.LastError == "" {
		t.Fatal("last error is empty, want a person to be able to read why it stopped")
	}
	if stored.Runs != 1 {
		t.Fatalf("runs = %d, want 1: recovering is not a run", stored.Runs)
	}
}

// TestAFailedRunIsPriced covers the case that ties the two together: a run that
// failed still cost its money, so the budget must see it.
func TestAFailedRunIsPriced(t *testing.T) {
	exec := &fakeExecutor{errs: []error{headless.ErrTimeout}, results: []headless.Result{{
		Usage: headless.Usage{CostUSD: 0.4, HasCost: true},
	}}}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake", BudgetUSD: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.SpentUSD != 0.4 {
		t.Fatalf("spent = %v, want the failed run's reported cost", stored.SpentUSD)
	}
}

// TestAnUnpricedRunIsNotFree pins the distinction the budget depends on: a
// dialect that reports no cost (codex) must not look like a run that cost zero.
func TestAnUnpricedRunIsNotFree(t *testing.T) {
	exec := &fakeExecutor{results: []headless.Result{{Usage: headless.Usage{HasCost: false}}}}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake", BudgetUSD: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	// Zero spent, one run: the price is unknown, so the budget cannot claim it
	// was used up — and the task still finished.
	if stored.State != store.TaskDone || stored.SpentUSD != 0 {
		t.Fatalf("state/spent = %s/%v, want done/0", stored.State, stored.SpentUSD)
	}
}

func TestCancelStopsATask(t *testing.T) {
	o, s := newOrch(t, orch.Config{})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := o.Cancel(ctx, task.ID, "not needed"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskCancelled {
		t.Fatalf("state = %s, want cancelled", stored.State)
	}

	// Cancelling twice is refused rather than silently accepted: a person who
	// pressed it twice should not be told the second press did anything.
	if _, err := o.Cancel(ctx, task.ID, "again"); !errors.Is(err, orch.ErrWrongState) {
		t.Fatalf("second cancel = %v, want ErrWrongState", err)
	}
}

func TestAnUnknownDialectSettlesInsteadOfLooping(t *testing.T) {
	o, s := newOrch(t, orch.Config{
		Executor: &fakeExecutor{},
		Dialects: func(string) (headless.Dialect, bool) { return headless.Dialect{}, false },
	})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "nowhere"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskFailed {
		t.Fatalf("state = %s, want failed: a dialect this build lacks cannot be fixed by retrying", stored.State)
	}
}

package orch_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/headless"
	"github.com/f1shsmell/flotilla/internal/orch"
	"github.com/f1shsmell/flotilla/internal/store"
)

// fakeExecutor answers a run from the fields a test set, and counts how many
// times it was asked. It is a struct rather than a function so a test can read
// the call count after the orchestrator has finished: "it was not run again" is
// the assertion several of these tests exist for.
//
// The counter is behind a mutex because Run is exercised from its own goroutine,
// and a test that read the slice while the loop appended to it would race.
type fakeExecutor struct {
	mu sync.Mutex
	// results are answered in order; past the end the last one repeats.
	results []headless.Result
	// errs are answered in order; past the end the last one repeats.
	errs []error
	// tasks records what each run was asked to do.
	tasks []headless.Task
}

func (f *fakeExecutor) Run(_ context.Context, _ headless.Dialect, t headless.Task) (headless.Result, error) {
	f.mu.Lock()
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
	f.mu.Unlock()

	return res, err
}

// calls reports how many runs were asked for.
func (f *fakeExecutor) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tasks)
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
	if exec.calls() != 0 {
		t.Fatalf("executor ran %d times, want zero while the task waits on a person", exec.calls())
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
	if exec.calls() != 1 {
		t.Fatalf("executor ran %d times, want once after release", exec.calls())
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
	if exec.calls() != 1 {
		t.Fatalf("executor ran %d times, want exactly once", exec.calls())
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

	if exec.calls() != 3 {
		t.Fatalf("executor ran %d times, want exactly the 3 allowed runs", exec.calls())
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
	before := exec.calls()
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if exec.calls() != before+1 {
		t.Fatalf("second task did not run: %d calls", exec.calls())
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

	before = exec.calls()
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if exec.calls() != before {
		t.Fatalf("executor ran a task whose budget was spent (%d calls, want %d)", exec.calls(), before)
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

// TestAFailedRunStillKeepsWhatItSaid is the regression this branch needed: a run
// that failed after printing something used to have its record thrown away, so
// the one account of what an unattended agent did before it stopped was lost.
// Every terminal path now carries the effect.
func TestAFailedRunStillKeepsWhatItSaid(t *testing.T) {
	exec := &fakeExecutor{
		results: []headless.Result{{
			StopReason: "failed",
			Output:     "I read three files, then the tool call was refused",
			Updates: []headless.UpdateRecord{
				{Seq: 1, Kind: headless.KindMessage, Text: "I read three files, then the tool call was refused"},
			},
		}},
		errs: []error{errors.New("the agent exited 1")},
	}
	o, s := newOrch(t, orch.Config{Executor: exec, Dialects: dialect, MaxRuns: 1})
	ctx := t.Context()

	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := o.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	// The task is settled as failed at the cap, and the run's own record is
	// owed as an effect in the same commit.
	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskFailed {
		t.Fatalf("state = %s, want failed", stored.State)
	}

	pending, err := s.Pending(ctx, 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want the failed run's output queued", pending)
	}
	if pending[0].Name != store.OutputEffect {
		t.Fatalf("effect = %q, want %q", pending[0].Name, store.OutputEffect)
	}

	// And what it said survives the round trip through the payload.
	var payload store.TaskOutputPayload
	if err := json.Unmarshal(pending[0].Payload, &payload); err != nil {
		t.Fatalf("decode the queued payload: %v", err)
	}
	var record struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(payload.Body, &record); err != nil {
		t.Fatalf("decode the run record: %v", err)
	}
	if record.Output == "" {
		t.Fatal("the failed run's output is empty, want what it said before it stopped")
	}
	if record.Error == "" {
		t.Fatal("the run record carries no error, want why it stopped")
	}
}

// TestRunServesTasksArrivingAfterItStarted is the wake rule. A loop that began
// with an empty queue must pick up a task enqueued later: waiting on the queue
// rather than returning is what makes an idle daemon pick work up at once.
//
// The wait is on the orchestrator's own change signal, not on the executor's
// start: the loop is only done with a task once its last commit landed, and a
// test that read the row when the run began would see `running` and be right.
func TestRunServesTasksArrivingAfterItStarted(t *testing.T) {
	exec := &fakeExecutor{results: []headless.Result{{Output: "done"}}}

	// The change hook fires after every commit, so counting them is an exact
	// synchronisation point: enqueue, started, finished.
	var (
		mu      sync.Mutex
		commits int
		settled = make(chan struct{})
	)
	o, s := newOrch(t, orch.Config{
		Executor: exec,
		Dialects: dialect,
		OnChange: func() {
			mu.Lock()
			defer mu.Unlock()
			commits++
			// Three commits is the whole life of one task. Closing once is what
			// makes the wait a signal rather than a poll.
			if commits == 3 {
				close(settled)
			}
		},
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = o.Run(ctx)
	}()

	// Enqueue only after the loop is running. A loop that returned on an empty
	// queue would have died here and never seen this task.
	task, err := o.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "fake"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("the loop never settled the task enqueued after it started")
	}

	stored, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskDone {
		t.Fatalf("state = %s, want done", stored.State)
	}
	if exec.calls() != 1 {
		t.Fatalf("executor ran %d times, want once", exec.calls())
	}

	// Stopping the loop is the daemon's stop, and it must be a clean return
	// rather than an error: nothing failed.
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop when its context ended")
	}
}

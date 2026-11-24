package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/f1shsmell/flotilla/internal/headless"
	"github.com/f1shsmell/flotilla/internal/orch"
	"github.com/f1shsmell/flotilla/internal/store"
)

// fixedExecutor answers every run with the same result and remembers what it
// was asked. It never starts a process: these tests are about the daemon's
// wiring, not the agent's behaviour.
type fixedExecutor struct {
	res  headless.Result
	err  error
	seen []headless.Task
}

func (f *fixedExecutor) Run(_ context.Context, _ headless.Dialect, task headless.Task) (headless.Result, error) {
	f.seen = append(f.seen, task)
	return f.res, f.err
}

// newTaskDaemon builds a daemon over a fresh state directory whose queue is
// driven by exec. Nothing serves: these tests call the queue by hand so the
// assertions never wait on a timer.
func newTaskDaemon(t *testing.T, exec orch.Executor) (*Daemon, *store.Store) {
	t.Helper()

	dir := t.TempDir()
	d := New(Config{
		Channel:      "test",
		Token:        "shared",
		StateDir:     dir,
		Log:          discardLogger(),
		TaskExecutor: exec,
	}, nil)
	t.Cleanup(d.Detach)

	if d.state == nil {
		t.Fatal("the daemon has no state database")
	}
	if d.tasks == nil {
		t.Fatal("the daemon has no task queue")
	}
	if d.effects == nil {
		t.Fatal("the daemon has no effect worker")
	}
	return d, d.state
}

// TestTheQueueWritesARunsOutputThroughTheOutbox is the whole chain in one test:
// a task is enqueued, run, settled, and what the run said lands in the database
// through an effect rather than on the orchestrator's own path.
func TestTheQueueWritesARunsOutputThroughTheOutbox(t *testing.T) {
	exec := &fixedExecutor{res: headless.Result{
		SessionID:  "conv-1",
		StopReason: "end_turn",
		Output:     "the work is done",
		Updates: []headless.UpdateRecord{
			{Seq: 1, Kind: headless.KindMessage, Text: "the work is done"},
		},
		Usage: headless.Usage{CostUSD: 0.5, HasCost: true},
	}}
	d, state := newTaskDaemon(t, exec)
	ctx := t.Context()

	task, err := d.tasks.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "claude"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, more, err := d.tasks.Next(ctx); err != nil || !more {
		t.Fatalf("Next: more=%v err=%v, want the task run", more, err)
	}

	stored, err := state.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if stored.State != store.TaskDone {
		t.Fatalf("state = %s, want done", stored.State)
	}
	if stored.SpentUSD != 0.5 {
		t.Fatalf("spent = %v, want the run's reported cost", stored.SpentUSD)
	}

	// The output is owed, not written: that separation is the point of the
	// outbox, so the queue having finished must leave exactly one pending
	// effect behind.
	pending, err := state.Pending(ctx, 0)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Name != store.OutputEffect {
		t.Fatalf("pending = %+v, want one task.output effect", pending)
	}

	settled, err := d.effects.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled = %d, want the effect applied", settled)
	}

	out, err := state.LastTaskOutput(ctx, task.ID)
	if err != nil {
		t.Fatalf("LastTaskOutput: %v", err)
	}
	var record struct {
		Run    int    `json:"run"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(out.Body, &record); err != nil {
		t.Fatalf("decode the stored record: %v", err)
	}
	if record.Output != "the work is done" || record.Run != 1 {
		t.Fatalf("record = %+v, want the run's own output at run 1", record)
	}
}

// TestASecondDrainDoesNotWriteTheRunTwice is the receipt doing its job: the
// worker may be asked again after a crash, and a second copy of one run's output
// would be a duplicate nobody could reconcile.
func TestASecondDrainDoesNotWriteTheRunTwice(t *testing.T) {
	exec := &fixedExecutor{res: headless.Result{Output: "once"}}
	d, state := newTaskDaemon(t, exec)
	ctx := t.Context()

	task, err := d.tasks.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "claude"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, _, err := d.tasks.Next(ctx); err != nil {
		t.Fatalf("Next: %v", err)
	}

	if _, err := d.effects.Drain(ctx); err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	var first store.TaskOutput
	first, err = state.LastTaskOutput(ctx, task.ID)
	if err != nil {
		t.Fatalf("LastTaskOutput: %v", err)
	}

	settled, err := d.effects.Drain(ctx)
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if settled != 0 {
		t.Fatalf("settled = %d on the second drain, want 0: the receipt says the work is done", settled)
	}

	again, err := state.LastTaskOutput(ctx, task.ID)
	if err != nil {
		t.Fatalf("LastTaskOutput: %v", err)
	}
	if again.Run != first.Run {
		t.Fatalf("run = %d after a second drain, want %d", again.Run, first.Run)
	}

	// One row, not two: the task has exactly one run's worth of output.
	all, err := state.TaskOutputs(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskOutputs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("outputs = %d, want one row after two drains", len(all))
	}
}

// TestADaemonRestartParksTheRunItDiedUnder is the acceptance rule at the daemon
// level: a row left saying running is a run whose outcome nobody knows, and the
// next start must park it rather than run it again.
func TestADaemonRestartParksTheRunItDiedUnder(t *testing.T) {
	dir := t.TempDir()

	// A daemon that died mid-run leaves this row behind. It is written through
	// the store directly because that is all a crash leaves: no process, no
	// queue, no in-memory state.
	state, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	interrupted := store.Task{
		ID:      "task-interrupted",
		Prompt:  "do it",
		Dialect: "claude",
		State:   store.TaskRunning,
		Runs:    1,
	}
	if _, err := state.AppendTask(context.Background(), store.EventTaskStarted, interrupted); err != nil {
		t.Fatalf("write the interrupted row: %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close the state database: %v", err)
	}

	exec := &fixedExecutor{}
	d := New(Config{
		Channel:      "test",
		Token:        "shared",
		StateDir:     dir,
		Log:          discardLogger(),
		TaskExecutor: exec,
	}, nil)
	t.Cleanup(d.Detach)

	// startBackground is what Run calls before it listens: it repairs the queue
	// first, so an enqueue that arrives later is not stuck behind work that will
	// never finish.
	d.startBackground()
	d.stopBackground()

	parked, err := d.state.Task(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatalf("read the task: %v", err)
	}
	if parked.State != store.TaskFailed {
		t.Fatalf("state = %s, want failed: an unknown outcome is parked, not resumed", parked.State)
	}
	if parked.Runs != 1 {
		t.Fatalf("runs = %d, want 1: recovering is not a run", parked.Runs)
	}
	if len(exec.seen) != 0 {
		t.Fatalf("executor ran %d times, want zero: the task was not resumed", len(exec.seen))
	}
}

// TestRecoveredWorkIsNotRunAgain is the same rule from the queue's side: after
// the repair, the parked task is not in the queued set, so the next run of the
// loop finds nothing to do.
func TestRecoveredWorkIsNotRunAgain(t *testing.T) {
	exec := &fixedExecutor{}
	d, state := newTaskDaemon(t, exec)
	ctx := t.Context()

	interrupted := store.Task{
		ID:      "task-interrupted",
		Prompt:  "do it",
		Dialect: "claude",
		State:   store.TaskRunning,
		Runs:    1,
	}
	if _, err := state.AppendTask(ctx, store.EventTaskStarted, interrupted); err != nil {
		t.Fatalf("write the interrupted row: %v", err)
	}

	if _, err := d.tasks.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	_, more, err := d.tasks.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if more {
		t.Fatal("Next ran a task the recovery parked")
	}
	if len(exec.seen) != 0 {
		t.Fatalf("executor ran %d times, want zero", len(exec.seen))
	}
}

// TestStatusListsTheQueue is the projection a person reads: the queue rides
// beside the fleet, with the fields needed to decide what to do next.
func TestStatusListsTheQueue(t *testing.T) {
	exec := &fixedExecutor{err: errors.New("the agent said no")}
	d, _ := newTaskDaemon(t, exec)
	ctx := t.Context()

	task, err := d.tasks.Enqueue(ctx, store.Task{
		Prompt:    "summarise the branch",
		Dialect:   "claude",
		Dir:       `C:\work`,
		BudgetUSD: 2,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Read the queue through the same path a client does, without a live
	// connection: a nil peer is what the daemon's own tests and a raw CLI get.
	st := d.status(nil)
	if len(st.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want the queued one", st.Tasks)
	}

	got := st.Tasks[0]
	if got.ID != task.ID || got.Dialect != "claude" || got.State != string(store.TaskQueued) {
		t.Fatalf("entry = %+v, want the queued task's identity and state", got)
	}
	if got.Prompt != "summarise the branch" || got.Dir != `C:\work` || got.BudgetUSD != 2 {
		t.Fatalf("entry = %+v, want the task's own fields", got)
	}
}

// TestStatusReportsAFailedRunsReason keeps the last error visible: a task that
// stopped for a reason nobody can read is a task a person cannot act on.
//
// The run is driven to the cap so the task settles as failed: a first failure
// only requeues it, and a requeued task's error is a note about the last attempt
// rather than the reason it stopped.
func TestStatusReportsAFailedRunsReason(t *testing.T) {
	exec := &fixedExecutor{err: errors.New("the agent exited 1")}
	d, _ := newTaskDaemon(t, exec)
	ctx := t.Context()

	if _, err := d.tasks.Enqueue(ctx, store.Task{Prompt: "do it", Dialect: "claude"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Three runs is the default cap; the fourth Next finds nothing to do.
	for i := 0; i < orch.DefaultMaxRuns; i++ {
		if _, _, err := d.tasks.Next(ctx); err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
	}

	st := d.status(nil)
	if len(st.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want the one task", st.Tasks)
	}
	got := st.Tasks[0]
	if got.State != string(store.TaskFailed) {
		t.Fatalf("state = %s, want failed once every run is used", got.State)
	}
	if got.Runs != orch.DefaultMaxRuns {
		t.Fatalf("runs = %d, want %d", got.Runs, orch.DefaultMaxRuns)
	}
	if got.LastError == "" {
		t.Fatal("last error is empty, want the reason a person can read")
	}
}

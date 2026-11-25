// Package orch is the orchestrator kernel: the queue of unattended jobs, the
// gate a person can hold one at, the budget that bounds it, and the discipline
// around retrying it.
//
// It is deliberately I/O-free in its decisions. Every state change goes through
// one store commit, so a daemon that dies between accepting a task and running
// it re-reads the same queue on the next start rather than reconstructing what
// it was about to do.
//
// The retry rule is the one thing here that must not be softened. A run that
// timed out, or that the daemon died under, may well have done its work; running
// it again could do that work twice. So such a run is never retried on its own
// (herdr's lesson, and FLOTILLA.md's): it stops and waits for a person to say
// what to do.
package orch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/f1shsmell/flotilla/internal/headless"
	"github.com/f1shsmell/flotilla/internal/store"
)

// ErrNoTask reports a task id the queue does not hold.
var ErrNoTask = store.ErrNoTask

// ErrBudget reports a task that may not run because its budget is spent. It is
// distinct from a failed run: nothing was attempted, so nothing was spent twice.
var ErrBudget = errors.New("orch: the task's budget is spent")

// ErrRunsExhausted reports a task that has used every run it is allowed. It is
// the hop circuit breaker: a task that keeps failing stops on its own instead of
// spending a provider's quota in a loop.
var ErrRunsExhausted = errors.New("orch: the task used every run it is allowed")

// ErrWrongState reports a call that does not fit where the task stands.
var ErrWrongState = errors.New("orch: the task is not in a state this call applies to")

// Executor runs one unattended job. It is an interface so a test can answer
// without starting a process: the orchestration decisions are what this package
// owns, and a real agent is the caller's business.
type Executor interface {
	Run(ctx context.Context, d headless.Dialect, t headless.Task) (headless.Result, error)
}

// Config is what an orchestrator needs to run.
type Config struct {
	// Store is where the queue lives. It is required.
	Store *store.Store
	// Executor runs a task. Nil uses the real headless runner.
	Executor Executor
	// Dialects resolves a task's dialect by name. Nil uses headless.Builtins.
	Dialects func(name string) (headless.Dialect, bool)
	// MaxRuns bounds how many times one task may start. Zero uses DefaultMaxRuns.
	// It is the hop circuit breaker, and it counts retries: a task that keeps
	// failing stops instead of looping.
	MaxRuns int
	// Log is where the orchestrator says what it did. Nil discards.
	Log *slog.Logger
	// Now reads the clock. Nil uses time.Now.
	Now func() time.Time
	// OnChange is called after every committed change, so a caller that runs
	// the effect worker knows to look at the queue it just grew. Nil means
	// nobody is waiting for one.
	//
	// It is called outside the store's transaction and must not block: the
	// daemon's implementation rings a channel and returns.
	OnChange func()
}

// DefaultMaxRuns is how many starts one task gets when the caller named none. It
// is small on purpose: a task that has failed three times is not one more run
// away from working.
const DefaultMaxRuns = 3

func (c Config) withDefaults() Config {
	if c.Executor == nil {
		c.Executor = headless.Runner{}
	}
	if c.Dialects == nil {
		c.Dialects = headless.DialectFor
	}
	if c.MaxRuns <= 0 {
		c.MaxRuns = DefaultMaxRuns
	}
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// discard is an io.Writer that drops everything, for a default logger.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Orchestrator runs tasks from the queue.
type Orchestrator struct {
	cfg Config
	// wake carries one token per request to look at the queue again. A caller
	// that just enqueued work rings it; the loop then reads the rows rather than
	// trusting the caller's account of what it wrote.
	wake chan struct{}
}

// New builds an orchestrator.
func New(cfg Config) (*Orchestrator, error) {
	if cfg.Store == nil {
		return nil, errors.New("orch: no store")
	}
	return &Orchestrator{cfg: cfg.withDefaults(), wake: make(chan struct{}, 1)}, nil
}

// Wake asks a serving loop to look at the queue. It never blocks: a second
// wake-up while one is pending tells the loop nothing it does not already know.
//
// It is called after every change that might make a task runnable. The loop
// re-reads the rows instead of being handed the task, so a wake-up that arrives
// twice cannot run anything twice.
func (o *Orchestrator) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// commit writes one task change and, when there is one, the effect it owes. It
// is the single write path, so the effect worker is told about every change
// without each caller remembering to: a forgotten notification here is a queue
// row nobody drains, which is exactly the failure the outbox exists to prevent.
//
// The notification runs after the transaction commits. A caller that learned
// about an effect before it was durable would race the store and might read a
// row that rolled back.
func (o *Orchestrator) commit(ctx context.Context, kind string, t store.Task, effect *store.Effect) (uint64, error) {
	var (
		seq uint64
		err error
	)
	if effect != nil {
		seq, err = o.cfg.Store.AppendTaskWithEffect(ctx, kind, t, effect)
	} else {
		seq, err = o.cfg.Store.AppendTask(ctx, kind, t)
	}
	if err != nil {
		return 0, fmt.Errorf("record %s for task %q: %w", kind, t.ID, err)
	}

	if o.cfg.OnChange != nil {
		o.cfg.OnChange()
	}
	return seq, nil
}

// Enqueue adds one task to the queue.
func (o *Orchestrator) Enqueue(ctx context.Context, t store.Task) (store.Task, error) {
	if t.Dialect == "" {
		return store.Task{}, errors.New("orch: a task needs a dialect")
	}
	if t.Prompt == "" {
		return store.Task{}, errors.New("orch: a task needs a prompt")
	}
	if t.BudgetUSD < 0 {
		return store.Task{}, fmt.Errorf("orch: budget %v is negative", t.BudgetUSD)
	}

	if t.ID == "" {
		id, err := newID()
		if err != nil {
			return store.Task{}, err
		}
		t.ID = id
	}
	t.State = store.TaskQueued
	t.Runs = 0
	t.SpentUSD = 0
	t.Created = o.cfg.Now()
	t.Updated = t.Created

	if _, err := o.commit(ctx, store.EventTaskQueued, t, nil); err != nil {
		return store.Task{}, fmt.Errorf("enqueue task %q: %w", t.ID, err)
	}
	// A loop that is idle is waiting for this: without it the task would sit
	// queued until something else happened to wake the loop.
	o.Wake()
	return t, nil
}

// EnqueueOnce adds one task only when its id is new, and reports whether it
// did. The id is the caller's idempotency key: a schedule slot or a webhook
// request rendered as a task id, so a trigger that arrives twice is one queue
// row and the second call gets the first call's answer.
func (o *Orchestrator) EnqueueOnce(ctx context.Context, t store.Task) (store.Task, bool, error) {
	if t.Dialect == "" {
		return store.Task{}, false, errors.New("orch: a task needs a dialect")
	}
	if t.Prompt == "" {
		return store.Task{}, false, errors.New("orch: a task needs a prompt")
	}
	if t.BudgetUSD < 0 {
		return store.Task{}, false, fmt.Errorf("orch: budget %v is negative", t.BudgetUSD)
	}
	if t.ID == "" {
		return store.Task{}, false, errors.New("orch: an idempotent enqueue needs the caller's task id")
	}

	t.State = store.TaskQueued
	t.Runs = 0
	t.SpentUSD = 0
	t.Created = o.cfg.Now()
	t.Updated = t.Created

	stored, created, err := o.cfg.Store.AppendTaskOnce(ctx, store.EventTaskQueued, t)
	if err != nil {
		return store.Task{}, false, fmt.Errorf("enqueue task %q: %w", t.ID, err)
	}
	if created {
		// Nothing changed on a retry, so nothing is woken: a duplicate trigger
		// must not cost a queue pass.
		if o.cfg.OnChange != nil {
			o.cfg.OnChange()
		}
		o.Wake()
	}
	return stored, created, nil
}

// Hold moves a queued task to waiting, so it stops to ask a person something
// before it runs. Nothing runs and nothing is spent while it waits: that is what
// the state is for, and it is why a suspended task costs nothing at all.
func (o *Orchestrator) Hold(ctx context.Context, id, reason string) (store.Task, error) {
	t, err := o.cfg.Store.Task(ctx, id)
	if err != nil {
		return store.Task{}, fmt.Errorf("hold task %q: %w", id, err)
	}
	if t.State != store.TaskQueued {
		return store.Task{}, fmt.Errorf("hold task %q (it is %s): %w", id, t.State, ErrWrongState)
	}

	t.State = store.TaskWaiting
	t.Updated = o.cfg.Now()
	if _, err := o.commit(ctx, store.EventTaskWaiting, t, nil); err != nil {
		return store.Task{}, fmt.Errorf("hold task %q: %w", id, err)
	}
	o.cfg.Log.Info("task held for a person", "task", id, "reason", reason)
	return t, nil
}

// Release puts a waiting task back in the queue: a person answered it and the
// work may go on.
func (o *Orchestrator) Release(ctx context.Context, id string) (store.Task, error) {
	t, err := o.cfg.Store.Task(ctx, id)
	if err != nil {
		return store.Task{}, fmt.Errorf("release task %q: %w", id, err)
	}
	if t.State != store.TaskWaiting {
		return store.Task{}, fmt.Errorf("release task %q (it is %s): %w", id, t.State, ErrWrongState)
	}

	t.State = store.TaskQueued
	t.Updated = o.cfg.Now()
	if _, err := o.commit(ctx, store.EventTaskQueued, t, nil); err != nil {
		return store.Task{}, fmt.Errorf("release task %q: %w", id, err)
	}
	// The task is runnable again, so an idle loop has to be told. Without this
	// a released task waits for the next enqueue to be noticed.
	o.Wake()
	return t, nil
}

// Cancel stops a task. It applies to every state but done and cancelled: a
// person's instruction outranks whatever the task was doing.
func (o *Orchestrator) Cancel(ctx context.Context, id, reason string) (store.Task, error) {
	t, err := o.cfg.Store.Task(ctx, id)
	if err != nil {
		return store.Task{}, fmt.Errorf("cancel task %q: %w", id, err)
	}
	switch t.State {
	case store.TaskDone, store.TaskCancelled:
		return store.Task{}, fmt.Errorf("cancel task %q (it is %s): %w", id, t.State, ErrWrongState)
	}

	t.State = store.TaskCancelled
	t.LastError = reason
	t.Updated = o.cfg.Now()
	if _, err := o.commit(ctx, store.EventTaskCancelled, t, nil); err != nil {
		return store.Task{}, fmt.Errorf("cancel task %q: %w", id, err)
	}
	return t, nil
}

// Recover re-reads the queue after a restart. A task that was running when the
// previous daemon died is not resumed: this build cannot tell whether the agent
// got the instruction, and running it again could do the work twice. It is
// parked as failed for a person to look at, which is the same discipline a
// timed-out run gets.
func (o *Orchestrator) Recover(ctx context.Context) ([]store.Task, error) {
	running, err := o.cfg.Store.TasksInState(ctx, store.TaskRunning)
	if err != nil {
		return nil, fmt.Errorf("recover the queue: %w", err)
	}

	recovered := make([]store.Task, 0, len(running))
	for _, t := range running {
		t.State = store.TaskFailed
		t.LastError = "the daemon stopped while this task was running; it may have done its work, " +
			"so it was not run again"
		t.Updated = o.cfg.Now()
		if _, err := o.commit(ctx, store.EventTaskFinished, t, nil); err != nil {
			return recovered, fmt.Errorf("park task %q: %w", t.ID, err)
		}
		o.cfg.Log.Warn("parked a task the daemon died under", "task", t.ID)
		recovered = append(recovered, t)
	}
	return recovered, nil
}

// Next takes the oldest queued task and starts it, or reports that the queue is
// empty. One call is one task: a caller that wants a loop writes one, and a test
// that wants a single step calls this directly, so neither has to wait on a
// timer.
//
// A task this call will not run — no budget left, or every run used — is settled
// with a failure rather than returned, so the queue keeps making progress.
func (o *Orchestrator) Next(ctx context.Context) (store.Task, bool, error) {
	queued, err := o.cfg.Store.TasksInState(ctx, store.TaskQueued)
	if err != nil {
		return store.Task{}, false, fmt.Errorf("read the queue: %w", err)
	}

	for _, t := range queued {
		if refused := o.refusal(t); refused != nil {
			if _, err := o.settle(ctx, t, refused); err != nil {
				return store.Task{}, false, err
			}
			continue
		}

		ran, err := o.run(ctx, t)
		return ran, true, err
	}
	return store.Task{}, false, nil
}

// refusal reports why a task may not run at all, or nil when it may.
func (o *Orchestrator) refusal(t store.Task) error {
	if t.Runs >= o.cfg.MaxRuns {
		return fmt.Errorf("%w (%d of %d runs)", ErrRunsExhausted, t.Runs, o.cfg.MaxRuns)
	}
	// The budget gate. A task with no budget is unbounded, which is what the
	// caller asked for; a task with one stops when it is spent. The comparison
	// is on what the runs reported, so a dialect that prices nothing (codex)
	// never trips it — an unpriced run is not a free one, but it is also not
	// evidence of spending.
	if t.BudgetUSD > 0 && t.SpentUSD >= t.BudgetUSD {
		return fmt.Errorf("%w ($%.4f of $%.4f)", ErrBudget, t.SpentUSD, t.BudgetUSD)
	}
	return nil
}

// run starts one task, waits for it, and settles the result. It returns the task
// as it stands when it finished.
//
// A run's output is not written here. The task change and the output are queued
// together — the row says what happened, the effect says what was said — so a
// daemon that dies between the two does not lose the only record of an
// unattended run. Who drains that queue is the caller's business: this package
// never runs an effect itself.
func (o *Orchestrator) run(ctx context.Context, t store.Task) (store.Task, error) {
	dialect, ok := o.cfg.Dialects(t.Dialect)
	if !ok {
		return o.settle(ctx, t, fmt.Errorf("orch: no headless dialect named %q", t.Dialect))
	}

	// The run is recorded before it starts. A daemon that dies during the run
	// leaves the row saying running, which is what Recover parks: "it may have
	// done the work" is a fact worth knowing, and a row that still said queued
	// would be a lie the next start would act on by running it again.
	t.State = store.TaskRunning
	t.Runs++
	t.Updated = o.cfg.Now()
	if _, err := o.commit(ctx, store.EventTaskStarted, t, nil); err != nil {
		return store.Task{}, fmt.Errorf("start task %q: %w", t.ID, err)
	}
	o.cfg.Log.Info("task started", "task", t.ID, "dialect", t.Dialect, "run", t.Runs)

	res, err := o.cfg.Executor.Run(ctx, dialect, headless.Task{
		Prompt:   t.Prompt,
		Dir:      t.Dir,
		Access:   t.Access,
		ResumeID: t.ResumeID,
	})

	// What the run reported is recorded whether it succeeded or not: a failed
	// run that was priced still cost its money, and a run that died mid-turn
	// still said things worth reading.
	if res.Usage.HasCost {
		t.SpentUSD += res.Usage.CostUSD
	}
	if res.SessionID != "" {
		t.ResumeID = res.SessionID
	}

	effect, effectErr := OutputEffectFor(t.ID, t.Runs, res, err)
	if effectErr != nil {
		return store.Task{}, effectErr
	}

	if err != nil {
		return o.failAfterRun(ctx, t, err, effect)
	}

	t.State = store.TaskDone
	t.LastError = ""
	t.Updated = o.cfg.Now()
	if _, err := o.commit(ctx, store.EventTaskFinished, t, effect); err != nil {
		return store.Task{}, fmt.Errorf("finish task %q: %w", t.ID, err)
	}
	o.cfg.Log.Info("task finished", "task", t.ID, "runs", t.Runs, "spent", t.SpentUSD)
	return t, nil
}

// failAfterRun settles a run that did not succeed, applying the retry rule.
//
// The rule, in one sentence: a run that may have done its work is never run
// again on its own. A timeout is exactly that case — the agent had the
// instruction and this build does not know how far it got — so it is parked. A
// run that plainly failed before it could act is retried, up to the hop cap.
func (o *Orchestrator) failAfterRun(ctx context.Context, t store.Task, cause error, effect *store.Effect) (store.Task, error) {
	t.LastError = cause.Error()
	t.Updated = o.cfg.Now()

	// A timeout or a cancelled context means the instruction was delivered and
	// the outcome is unknown. Retrying would be the blind resend this package
	// exists to prevent.
	ambiguous := errors.Is(cause, headless.ErrTimeout) || errors.Is(cause, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded)
	if ambiguous {
		t.State = store.TaskFailed
		if _, err := o.commit(ctx, store.EventTaskFinished, t, effect); err != nil {
			return store.Task{}, fmt.Errorf("park task %q: %w", t.ID, err)
		}
		o.cfg.Log.Warn("task parked: the run may have done its work", "task", t.ID, "err", cause.Error())
		return t, nil
	}

	// A run that failed outright is retried until the cap, and the cap is the
	// circuit breaker: past it the task stops instead of spending a provider's
	// quota in a loop. The effect travels either way: what the run said before
	// it failed is the only account of it, and discarding that here would throw
	// away exactly the records a person most wants to read.
	if t.Runs < o.cfg.MaxRuns {
		t.State = store.TaskQueued
		if _, err := o.commit(ctx, store.EventTaskQueued, t, effect); err != nil {
			return store.Task{}, fmt.Errorf("requeue task %q: %w", t.ID, err)
		}
		o.cfg.Log.Info("task requeued after a failed run", "task", t.ID, "run", t.Runs)
		return t, nil
	}

	t.State = store.TaskFailed
	t.LastError = fmt.Errorf("%w after %d of %d runs: %w",
		ErrRunsExhausted, t.Runs, o.cfg.MaxRuns, cause).Error()
	if _, err := o.commit(ctx, store.EventTaskFinished, t, effect); err != nil {
		return store.Task{}, fmt.Errorf("fail task %q: %w", t.ID, err)
	}
	o.cfg.Log.Warn("task failed: every run used", "task", t.ID, "err", cause.Error())
	return t, nil
}

// settle ends a task without running it: something about the task itself is
// wrong, and no number of runs would fix it.
func (o *Orchestrator) settle(ctx context.Context, t store.Task, cause error) (store.Task, error) {
	t.State = store.TaskFailed
	t.LastError = cause.Error()
	t.Updated = o.cfg.Now()
	if _, err := o.commit(ctx, store.EventTaskFinished, t, nil); err != nil {
		return store.Task{}, fmt.Errorf("settle task %q: %w", t.ID, err)
	}
	o.cfg.Log.Warn("task settled without running", "task", t.ID, "err", cause.Error())
	return t, nil
}

// Run serves the queue until ctx ends: it takes tasks while there are any and
// otherwise waits for Wake. It is the daemon's loop; a caller that wants one
// step calls Next.
//
// The wait is the point. A loop that returned when the queue emptied would have
// to be re-entered by whoever enqueued the next task, and a loop that polled
// would spin a core in an idle daemon. Waiting on a channel means an idle daemon
// is genuinely idle and a task that arrives is picked up at once.
//
// A cancelled context is a normal end, not a failure: the daemon was told to
// stop, so this returns nil. Any other error is the store's, and is returned.
func (o *Orchestrator) Run(ctx context.Context) error {
	for {
		// Stopping is not a failure, so it leaves through a bare return: a
		// done context is how the daemon says stop, not an error to report.
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_, more, err := o.Next(ctx)
		if err != nil {
			// A failed task is a settled row, not a failed loop: one bad job must
			// not stop the queue behind it. Only a store failure is fatal here,
			// and Next returns those as errors of their own.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		if more {
			continue
		}

		select {
		case <-ctx.Done():
			return nil
		case <-o.wake:
		}
	}
}

// RunRecord is what one run of a task produced, as it is stored and read back.
//
// It is this package's own type rather than headless.Result because the record
// outlives the run and the reader: a field that the execution layer renames must
// not silently change what an old row means.
type RunRecord struct {
	// Run is the run number, the same one the task counted.
	Run int `json:"run"`
	// SessionID is the conversation the agent reported, so a later run can
	// continue it.
	SessionID string `json:"session_id,omitempty"`
	// StopReason is the agent's own word for why the turn ended.
	StopReason string `json:"stop_reason,omitempty"`
	// Output is the agent's final message text.
	Output string `json:"output,omitempty"`
	// Usage is what the run cost, as the agent reported it.
	Usage RunUsage `json:"usage,omitempty"`
	// Updates is everything the run streamed, in order.
	Updates []RunUpdate `json:"updates,omitempty"`
	// Error is why the run failed, empty when it did not. It is recorded with
	// the output because a failed run's partial output is the only account of
	// what it did before it stopped.
	Error string `json:"error,omitempty"`
}

// RunUsage is one run's cost.
type RunUsage struct {
	InputTokens     int64   `json:"input_tokens,omitempty"`
	OutputTokens    int64   `json:"output_tokens,omitempty"`
	ReasoningTokens int64   `json:"reasoning_tokens,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	// HasCost says whether the dialect priced the run at all. A zero cost and
	// an unknown cost are different, and a reader must not confuse them.
	HasCost bool `json:"has_cost,omitempty"`
}

// RunUpdate is one update a run streamed.
type RunUpdate struct {
	Seq        uint64 `json:"seq"`
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolTitle  string `json:"tool_title,omitempty"`
	ToolStatus string `json:"tool_status,omitempty"`
}

// OutputEffectFor renders the effect that stores what a run produced, or nil
// when there is nothing worth storing.
//
// A run that said nothing and cost nothing is not stored: a row of empty fields
// would claim the agent ran and was silent, which is a different fact from "the
// run never got that far". The error travels with the output rather than only in
// the task row, because the task row keeps one line and the run may have said
// a great deal before it failed.
func OutputEffectFor(taskID string, run int, res headless.Result, runErr error) (*store.Effect, error) {
	record := RunRecord{
		Run:        run,
		SessionID:  res.SessionID,
		StopReason: res.StopReason,
		Output:     res.Output,
		Usage: RunUsage{
			InputTokens:     res.Usage.InputTokens,
			OutputTokens:    res.Usage.OutputTokens,
			ReasoningTokens: res.Usage.ReasoningTokens,
			CostUSD:         res.Usage.CostUSD,
			HasCost:         res.Usage.HasCost,
		},
		Updates: make([]RunUpdate, 0, len(res.Updates)),
	}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	for _, u := range res.Updates {
		record.Updates = append(record.Updates, RunUpdate{
			Seq:        u.Seq,
			Kind:       u.Kind,
			Text:       u.Text,
			ToolCallID: u.ToolCallID,
			ToolTitle:  u.ToolTitle,
			ToolStatus: u.ToolStatus,
		})
	}

	if len(record.Updates) == 0 && record.Output == "" && record.Error == "" {
		return nil, nil
	}

	body, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode the record of task %q run %d: %w", taskID, run, err)
	}
	payload, err := json.Marshal(store.TaskOutputPayload{TaskID: taskID, Run: run, Body: body})
	if err != nil {
		return nil, fmt.Errorf("encode the output effect of task %q run %d: %w", taskID, run, err)
	}
	return &store.Effect{Name: store.OutputEffect, Payload: payload}, nil
}

// newID returns an id for a task nothing else will collide with.
func newID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("orch: generate a task id: %w", err)
	}
	return "task-" + hex.EncodeToString(raw[:]), nil
}

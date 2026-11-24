package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/f1shsmell/flotilla/internal/store"
)

// TestATaskRoundTrips is the whole contract of the task table: what a caller
// writes is what the next reader sees, including the numbers a budget and a run
// cap depend on.
func TestATaskRoundTrips(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	want := store.Task{
		ID:        "task-1",
		Prompt:    "add a test",
		Dialect:   "claude",
		Dir:       `C:\work\tree`,
		Access:    "acceptEdits",
		State:     store.TaskQueued,
		Parent:    "task-0",
		BudgetUSD: 2.5,
		SpentUSD:  0.25,
		Runs:      2,
		ResumeID:  "conv-9",
		LastError: "an earlier attempt said no",
	}
	if _, err := s.AppendTask(ctx, store.EventTaskQueued, want); err != nil {
		t.Fatalf("AppendTask: %v", err)
	}

	got, err := s.Task(ctx, want.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}

	// Timestamps are set by the store when the caller left them out, so they are
	// compared for presence rather than equality.
	if got.Created.IsZero() || got.Updated.IsZero() {
		t.Fatalf("timestamps = %v/%v, want both set", got.Created, got.Updated)
	}
	got.Created, got.Updated = want.Created, want.Updated

	if got != want {
		t.Fatalf("read back %+v, want %+v", got, want)
	}
}

// TestATaskChangeKeepsTheEventsBesideIt pins the pairing: a row without its
// event is a state nobody can explain, and an event without its row is a fact
// the queue does not act on.
func TestATaskChangeKeepsTheEventsBesideIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	task := store.Task{ID: "task-1", Prompt: "do it", Dialect: "claude", State: store.TaskQueued}
	if _, err := s.AppendTask(ctx, store.EventTaskQueued, task); err != nil {
		t.Fatalf("AppendTask: %v", err)
	}

	task.State = store.TaskRunning
	if _, err := s.AppendTask(ctx, store.EventTaskStarted, task); err != nil {
		t.Fatalf("AppendTask: %v", err)
	}

	events, err := s.Events(ctx, 0, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want queued and started", events)
	}
	if events[0].Kind != store.EventTaskQueued || events[1].Kind != store.EventTaskStarted {
		t.Fatalf("kinds = %q, %q, want the two changes in order", events[0].Kind, events[1].Kind)
	}
	for _, e := range events {
		if len(e.Payload) == 0 {
			t.Fatalf("event %q has no payload, want the task it is about", e.Kind)
		}
	}
}

// TestAnUnknownTaskStateIsRefused keeps the state column a closed set: a typo
// that reached disk would read later as a state the queue never acts on.
func TestAnUnknownTaskStateIsRefused(t *testing.T) {
	s := open(t)

	_, err := s.AppendTask(context.Background(), store.EventTaskQueued,
		store.Task{ID: "task-1", State: store.TaskState("in-progress")})
	if err == nil {
		t.Fatal("AppendTask accepted a state this build does not know")
	}
}

// TestTasksInStateIsOrderedAndScoped is what a worker reads after a restart:
// only the queued tasks, oldest first, so the queue's order survives a crash.
func TestTasksInStateIsOrderedAndScoped(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	queued := []store.Task{
		{ID: "task-a", Prompt: "first", Dialect: "claude", State: store.TaskQueued},
		{ID: "task-b", Prompt: "second", Dialect: "claude", State: store.TaskQueued},
	}
	for _, task := range queued {
		if _, err := s.AppendTask(ctx, store.EventTaskQueued, task); err != nil {
			t.Fatalf("AppendTask %q: %v", task.ID, err)
		}
	}
	// A task in another state must not appear in the queue a worker drains.
	done := store.Task{ID: "task-c", Prompt: "third", Dialect: "claude", State: store.TaskDone}
	if _, err := s.AppendTask(ctx, store.EventTaskFinished, done); err != nil {
		t.Fatalf("AppendTask done: %v", err)
	}

	got, err := s.TasksInState(ctx, store.TaskQueued)
	if err != nil {
		t.Fatalf("TasksInState: %v", err)
	}
	if len(got) != 2 || got[0].ID != "task-a" || got[1].ID != "task-b" {
		t.Fatalf("queued = %+v, want the two queued tasks in order", got)
	}
}

func TestAnAbsentTaskIsNamedAsSuch(t *testing.T) {
	s := open(t)

	if _, err := s.Task(context.Background(), "task-nowhere"); !errors.Is(err, store.ErrNoTask) {
		t.Fatalf("Task = %v, want ErrNoTask", err)
	}
}

// TestAnOlderDatabaseGainsTheTaskTable is the upgrade path: a database this
// build's own previous version wrote must open, gain the new tables, and record
// the new schema number. Without this the queue would work on a fresh install
// and fail on the maintainer's own machine.
func TestAnOlderDatabaseGainsTheTaskTable(t *testing.T) {
	path := dbPath(t)

	// Write a database the way schema version 1 left it: no tasks table, and the
	// old number in meta.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open a bare database: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO meta (key, value) VALUES ('schema_version', '1')`); err != nil {
		t.Fatalf("record the old version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the bare database: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open over a version 1 database: %v", err)
	}
	defer func() { _ = s.Close() }()

	// The new table is usable, which is the only thing "the upgrade worked"
	// means for a caller.
	task := store.Task{ID: "task-1", Prompt: "do it", Dialect: "claude", State: store.TaskQueued}
	if _, err := s.AppendTask(context.Background(), store.EventTaskQueued, task); err != nil {
		t.Fatalf("write a task into the upgraded database: %v", err)
	}

	// And the version now says so: a database that kept claiming version 1 would
	// redo this work on every start.
	var version string
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen the database: %v", err)
	}
	defer func() { _ = db2.Close() }()
	if err := db2.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read the schema version: %v", err)
	}
	if version == "1" {
		t.Fatalf("schema version = %q, want it raised past 1 after the upgrade", version)
	}
}

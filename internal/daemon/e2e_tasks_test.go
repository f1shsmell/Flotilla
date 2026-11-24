package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/store"
)

// The two checks here are the T1 acceptance criteria at process level: the
// daemon really is a separate executable, really is killed, and what it left in
// the database is what the next one acts on. They build the shipped binaries
// rather than the library, because "a restart repairs the queue" is a claim
// about the process the user runs.

// TestADaemonProcessParksTheTaskItDiedUnder seeds the state a crash leaves — a
// task row still saying running — starts the real daemon, and reads back what it
// did through the same status call a window uses.
func TestADaemonProcessParksTheTaskItDiedUnder(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a real daemon")
	}

	bin := buildBinaries(t)
	hostBinary = bin.host

	home := t.TempDir()
	const channel = "e2e-tasks"
	const taskID = "task-interrupted"

	// The row a daemon killed mid-run leaves behind: nothing holds it, and
	// nothing knows whether the agent got the instruction.
	seed(t, home, func(ctx context.Context, s *store.Store) {
		task := store.Task{
			ID:      taskID,
			Prompt:  "do the thing nobody watched",
			Dialect: "claude",
			Dir:     `C:\work`,
			State:   store.TaskRunning,
			Runs:    1,
		}
		if _, err := s.AppendTask(ctx, store.EventTaskStarted, task); err != nil {
			t.Fatalf("seed the interrupted task: %v", err)
		}
	})

	d := startDaemon(t, bin.daemon, home, channel)
	defer func() { _ = d.kill(t) }()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	client, err := connect(ctx, channel, home)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	st, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	var found bool
	for _, task := range st.Tasks {
		if task.ID != taskID {
			continue
		}
		found = true

		if task.State != string(store.TaskFailed) {
			t.Fatalf("state = %q, want failed: an unknown outcome is parked, not resumed", task.State)
		}
		if task.LastError == "" {
			t.Fatal("last error is empty, want the reason a person can read")
		}
		// Resuming would have started a second run, and the whole rule is that
		// this build does not do that.
		if task.Runs != 1 {
			t.Fatalf("runs = %d, want 1: recovering is not a run", task.Runs)
		}
	}
	if !found {
		t.Fatalf("tasks = %+v, want the interrupted task listed", st.Tasks)
	}
}

// TestADaemonProcessDrainsTheEffectOwedByItsPredecessor covers the other half: a
// daemon that died after committing a task and its work owed, but before writing
// the output, leaves a pending effect. The next daemon must drain it.
func TestADaemonProcessDrainsTheEffectOwedByItsPredecessor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a real daemon")
	}

	bin := buildBinaries(t)
	hostBinary = bin.host

	home := t.TempDir()
	const channel = "e2e-outbox"
	const taskID = "task-output-owed"

	body, err := json.Marshal(map[string]any{"run": 1, "output": "said before the crash"})
	if err != nil {
		t.Fatalf("encode the seeded body: %v", err)
	}
	payload, err := json.Marshal(store.TaskOutputPayload{TaskID: taskID, Run: 1, Body: body})
	if err != nil {
		t.Fatalf("encode the seeded payload: %v", err)
	}

	// A finished task whose output was queued but never written: the exact state
	// a crash between the commit and the effect leaves.
	seed(t, home, func(ctx context.Context, s *store.Store) {
		task := store.Task{
			ID:      taskID,
			Prompt:  "say something",
			Dialect: "claude",
			State:   store.TaskDone,
			Runs:    1,
		}
		effect := store.Effect{Name: store.OutputEffect, Payload: payload}
		if _, err := s.AppendTaskWithEffect(ctx, store.EventTaskFinished, task, &effect); err != nil {
			t.Fatalf("seed the owed effect: %v", err)
		}
	})

	d := startDaemon(t, bin.daemon, home, channel)
	defer func() { _ = d.kill(t) }()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	// The connect is the synchronisation point for "the daemon is serving". The
	// drain itself runs after it, so the read below is polled against a deadline
	// rather than assumed: there is no event to wait on across two processes.
	client, err := connect(ctx, channel, home)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	db, err := sql.Open("sqlite", filepath.Join(home, "sessions", "state.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	defer func() { _ = db.Close() }()

	var wrote int
	for {
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_output WHERE task_id = ? AND run = 1`, taskID).Scan(&wrote); err != nil {
			t.Fatalf("read the task output: %v", err)
		}
		if wrote == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the daemon never wrote the owed output (%d rows)", wrote)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// And the effect is settled rather than left owed: a row that stayed pending
	// would be redone by the next daemon, which is what the receipt prevents.
	var pending int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox WHERE state = 'pending'`).Scan(&pending); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending effects = %d, want the owed effect settled", pending)
	}
}

// seed writes to the state database before a daemon starts, which is how a crash
// is reproduced: the next process sees only rows, not the process that wrote
// them.
func seed(t *testing.T, home string, build func(ctx context.Context, s *store.Store)) {
	t.Helper()

	ctx := context.Background()
	s, err := store.Open(filepath.Join(home, "sessions", "state.db"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Fatalf("close the seeded database: %v", err)
		}
	}()

	build(ctx, s)
}

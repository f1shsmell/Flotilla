package store

import (
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/schedule"
)

func TestAppendTaskOnceRecordsTheFirstCallAndAnswersTheSameRowAfter(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	task := Task{
		ID:      "task-derived-1",
		Prompt:  "the same request",
		Dialect: "claude",
		State:   TaskQueued,
		Created: time.Now(),
		Updated: time.Now(),
	}
	first, created, err := s.AppendTaskOnce(ctx, EventTaskQueued, task)
	if err != nil {
		t.Fatalf("first AppendTaskOnce: %v", err)
	}
	if !created || first.ID != task.ID {
		t.Fatalf("first = %+v created=%v, want the task recorded", first, created)
	}

	// The retry asks again with a different prompt: the id is what makes it the
	// same request, and the stored row is the answer.
	retry := task
	retry.Prompt = "a different prompt from the same retrying caller"
	second, created, err := s.AppendTaskOnce(ctx, EventTaskQueued, retry)
	if err != nil {
		t.Fatalf("retry AppendTaskOnce: %v", err)
	}
	if created {
		t.Fatal("the retry reported it created a task")
	}
	if second.Prompt != task.Prompt {
		t.Fatalf("retry answered %q, want the stored prompt %q", second.Prompt, task.Prompt)
	}

	events, err := s.Events(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var queued int
	for _, e := range events {
		if e.Kind == EventTaskQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("%d task.queued events, want one: a retry writes no event", queued)
	}
}

func TestAppendTaskOnceRefusesAnEmptyID(t *testing.T) {
	s := openTestStore(t)
	if _, _, err := s.AppendTaskOnce(t.Context(), EventTaskQueued, Task{Prompt: "x", Dialect: "claude", State: TaskQueued}); err == nil {
		t.Fatal("AppendTaskOnce accepted a task with no id: every retry would be a new row")
	}
}

// TestAScheduleTriggerIsOneTaskAcrossARestart is the receipt the clock's entry
// depends on: the same request id, sent to a daemon that restarted in between,
// is still the one task.
func TestAScheduleTriggerIsOneTaskAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	task := Task{
		ID: schedule.TaskID(schedule.Fire{
			Schedule:  schedule.Schedule{ID: "sch-1"},
			Slot:      time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
			RequestID: "hook-9",
		}),
		Prompt: "fire once", Dialect: "claude", State: TaskQueued,
	}
	if _, _, err := first.AppendTaskOnce(t.Context(), EventTaskQueued, task); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	if _, created, err := second.AppendTaskOnce(t.Context(), EventTaskQueued, task); err != nil || created {
		t.Fatalf("retry after restart: created=%v err=%v, want the stored task", created, err)
	}
}

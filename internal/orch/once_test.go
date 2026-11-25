package orch_test

import (
	"testing"

	"github.com/f1shsmell/flotilla/internal/orch"
	"github.com/f1shsmell/flotilla/internal/store"
)

func TestEnqueueOnceRecordsTheFirstRequestAndAnswersTheSameTaskAfter(t *testing.T) {
	o, s := newOrch(t, orch.Config{Dialects: dialect})
	ctx := t.Context()

	task := store.Task{ID: "task-derived-1", Prompt: "do it", Dialect: "fake"}
	first, created, err := o.EnqueueOnce(ctx, task)
	if err != nil {
		t.Fatalf("EnqueueOnce: %v", err)
	}
	if !created || first.ID != task.ID || first.State != store.TaskQueued {
		t.Fatalf("first = %+v created=%v, want a queued task", first, created)
	}

	second, created, err := o.EnqueueOnce(ctx, task)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if created {
		t.Fatal("the same id was accepted twice")
	}
	if second.ID != first.ID {
		t.Fatalf("retry answered %q, want %q", second.ID, first.ID)
	}

	tasks, err := s.TasksInState(ctx, store.TaskQueued)
	if err != nil {
		t.Fatalf("TasksInState: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d queued tasks, want one", len(tasks))
	}
}

func TestEnqueueOnceNeedsTheCallersID(t *testing.T) {
	o, _ := newOrch(t, orch.Config{Dialects: dialect})
	if _, _, err := o.EnqueueOnce(t.Context(), store.Task{Prompt: "do it", Dialect: "fake"}); err == nil {
		t.Fatal("EnqueueOnce accepted a task with no id: every retry would be a new row")
	}
}

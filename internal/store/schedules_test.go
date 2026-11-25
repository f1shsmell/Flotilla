package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/schedule"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testSchedule(id, cwd string) schedule.Schedule {
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	return schedule.Schedule{
		ID:     id,
		Name:   "overnight",
		Prompt: "read the overnight builds",
		Cadence: schedule.Cadence{
			Type: "cron", Expression: "0 9 * * *", Timezone: "Asia/Shanghai",
		},
		Target: schedule.Target{
			Type:   "new-agent",
			Config: schedule.TargetConfig{Dialect: "claude", Cwd: cwd, Isolation: "worktree"},
		},
		Status:    schedule.StatusActive,
		MissedRun: schedule.MissedSkip,
		NextAt:    now.Add(time.Hour),
		Created:   now,
		Updated:   now,
	}
}

func TestAScheduleSurvivesARestartWithItsCadenceAndTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := testSchedule("sch-1", dir)
	if _, err := first.AppendSchedule(t.Context(), schedule.EventScheduleSet, want); err != nil {
		t.Fatalf("AppendSchedule: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	got, err := second.Schedule(t.Context(), "sch-1")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if got.Cadence != want.Cadence {
		t.Errorf("cadence = %+v, want %+v", got.Cadence, want.Cadence)
	}
	if got.Target != want.Target {
		t.Errorf("target = %+v, want %+v", got.Target, want.Target)
	}
	if got.Status != want.Status || got.MissedRun != want.MissedRun {
		t.Errorf("status/missed = %s/%s, want %s/%s", got.Status, got.MissedRun, want.Status, want.MissedRun)
	}
	if !got.NextAt.Equal(want.NextAt) {
		t.Errorf("next = %s, want %s", got.NextAt, want.NextAt)
	}
}

func TestAScheduleEditReplacesTheRowAndLeavesAnEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	sch := testSchedule("sch-1", t.TempDir())

	if _, err := s.AppendSchedule(ctx, schedule.EventScheduleSet, sch); err != nil {
		t.Fatalf("append: %v", err)
	}
	sch.Status = schedule.StatusPaused
	sch.NextAt = time.Time{}
	sch.Updated = sch.Updated.Add(time.Minute)
	if _, err := s.AppendSchedule(ctx, schedule.EventScheduleSet, sch); err != nil {
		t.Fatalf("edit: %v", err)
	}

	got, err := s.Schedule(ctx, sch.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if got.Status != schedule.StatusPaused {
		t.Errorf("status = %s, want paused", got.Status)
	}
	if !got.NextAt.IsZero() {
		t.Errorf("next = %s, want zero: a paused schedule has nothing pending", got.NextAt)
	}

	events, err := s.Events(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sets int
	for _, e := range events {
		if e.Kind == schedule.EventScheduleSet {
			sets++
		}
	}
	if sets != 2 {
		t.Errorf("%d schedule.set events, want one per write", sets)
	}
}

func TestRemovingAScheduleDropsTheRowAndKeepsTheEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	sch := testSchedule("sch-1", t.TempDir())
	if _, err := s.AppendSchedule(ctx, schedule.EventScheduleSet, sch); err != nil {
		t.Fatalf("append: %v", err)
	}

	if _, err := s.RemoveSchedule(ctx, sch.ID, time.Now()); err != nil {
		t.Fatalf("RemoveSchedule: %v", err)
	}
	if _, err := s.Schedule(ctx, sch.ID); !errors.Is(err, ErrNoSchedule) {
		t.Fatalf("Schedule after removal: %v, want ErrNoSchedule", err)
	}

	all, err := s.AllSchedules(ctx)
	if err != nil {
		t.Fatalf("AllSchedules: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("AllSchedules = %+v, want none", all)
	}

	events, err := s.Events(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var removed bool
	for _, e := range events {
		if e.Kind == schedule.EventScheduleRemoved {
			removed = true
		}
	}
	if !removed {
		t.Error("no schedule.removed event: the log cannot explain why the row is gone")
	}
}

func TestAllSchedulesIsOldestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	base := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)

	for i, id := range []string{"sch-late", "sch-early"} {
		sch := testSchedule(id, t.TempDir())
		sch.Created = base.Add(time.Duration(i) * time.Hour)
		if _, err := s.AppendSchedule(ctx, schedule.EventScheduleSet, sch); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	all, err := s.AllSchedules(ctx)
	if err != nil {
		t.Fatalf("AllSchedules: %v", err)
	}
	if len(all) != 2 || all[0].ID != "sch-late" || all[1].ID != "sch-early" {
		t.Fatalf("AllSchedules = %+v, want creation order", all)
	}
}

func TestTheStoreRefusesAScheduleThisBuildCannotFire(t *testing.T) {
	s := openTestStore(t)
	sch := testSchedule("sch-1", t.TempDir())
	sch.Cadence = schedule.Cadence{Type: "cron", Expression: "0 9 * * *"}

	if _, err := s.AppendSchedule(t.Context(), schedule.EventScheduleSet, sch); err == nil {
		t.Fatal("AppendSchedule accepted a cron schedule without a time zone")
	}
}

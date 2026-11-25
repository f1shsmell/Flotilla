package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/schedule"
)

// callSchedule drives the method table the way a CLI does, so these tests cover
// the wire shape a client depends on rather than the scheduler's internals.
func callSchedule(t *testing.T, d *Daemon, method string, params any) any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encode %s: %v", method, err)
	}
	resp, err := d.handleCall(t.Context(), nil, method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return resp
}

func setScheduleRequest(t *testing.T, cwd string) ScheduleSetRequest {
	t.Helper()
	return ScheduleSetRequest{
		Prompt: "read the overnight builds",
		Cadence: schedule.Cadence{
			Type: "every", EveryMS: int64(time.Hour / time.Millisecond),
		},
		Target: schedule.Target{
			Type:   "new-agent",
			Config: schedule.TargetConfig{Dialect: "claude", Cwd: cwd},
		},
	}
}

func TestAScheduleCanBeCreatedEditedListedAndRemovedOverTheWire(t *testing.T) {
	d, _, clock := clockDaemon(t, &fixedExecutor{})

	created := callSchedule(t, d, MethodScheduleSet, setScheduleRequest(t, t.TempDir()))
	sch, ok := created.(schedule.Schedule)
	if !ok {
		t.Fatalf("set answered %T, want a schedule", created)
	}
	if sch.ID == "" {
		t.Fatal("a new schedule came back with no id")
	}
	if sch.Status != schedule.StatusActive || sch.MissedRun != schedule.MissedSkip {
		t.Errorf("status/missed = %s/%s, want the defaults", sch.Status, sch.MissedRun)
	}

	listed := callSchedule(t, d, MethodScheduleList, struct{}{})
	list, ok := listed.(ScheduleListResponse)
	if !ok || len(list.Schedules) != 1 || list.Schedules[0].ID != sch.ID {
		t.Fatalf("list = %+v, want the created schedule", listed)
	}

	edit := setScheduleRequest(t, sch.Target.Config.Cwd)
	edit.ID = sch.ID
	edit.Name = "renamed"
	edited := callSchedule(t, d, MethodScheduleSet, edit)
	if got := edited.(schedule.Schedule); got.Name != "renamed" || !got.NextAt.Equal(sch.NextAt) {
		t.Errorf("edit = %+v, want the new name and the kept slot %s", got, sch.NextAt)
	}

	callSchedule(t, d, MethodScheduleRemove, ScheduleRemoveRequest{ID: sch.ID})
	if _, err := clock.store.Schedule(t.Context(), sch.ID); err == nil {
		t.Fatal("the removed schedule is still stored")
	}
}

// TestAnAgentTargetTravelsWithoutADialect pins the shape the CLI sends: an
// agent target names the preset, and the daemon resolves the unattended
// dialect from the catalog rather than making the caller know it.
func TestAnAgentTargetTravelsWithoutADialect(t *testing.T) {
	d, _, _ := clockDaemon(t, &fixedExecutor{})

	req := setScheduleRequest(t, t.TempDir())
	req.Target = schedule.Target{
		Type:    "agent",
		AgentID: "claude",
		Config:  schedule.TargetConfig{Cwd: t.TempDir()},
	}
	created := callSchedule(t, d, MethodScheduleSet, req).(schedule.Schedule)

	triggered := callSchedule(t, d, MethodScheduleTrigger, ScheduleTriggerRequest{
		ID: created.ID, RequestID: "manual-1",
	}).(ScheduleTriggerResponse)
	if !triggered.Created || triggered.Task.Dialect != "claude" {
		t.Fatalf("trigger = %+v, want a task under the catalog agent's dialect", triggered)
	}
}

func TestATriggerIsIdempotentPerRequestID(t *testing.T) {
	d, state, _ := clockDaemon(t, &fixedExecutor{})
	created := callSchedule(t, d, MethodScheduleSet, setScheduleRequest(t, t.TempDir())).(schedule.Schedule)

	first := callSchedule(t, d, MethodScheduleTrigger, ScheduleTriggerRequest{ID: created.ID, RequestID: "hook-1"}).(ScheduleTriggerResponse)
	if !first.Created {
		t.Fatal("the first trigger reported it made no task")
	}

	again := callSchedule(t, d, MethodScheduleTrigger, ScheduleTriggerRequest{ID: created.ID, RequestID: "hook-1"}).(ScheduleTriggerResponse)
	if again.Created {
		t.Fatal("the same request id was accepted twice")
	}
	if again.Task.ID != first.Task.ID {
		t.Fatalf("retry answered task %q, want the first request's %q", again.Task.ID, first.Task.ID)
	}

	tasks, err := state.AllTasks(t.Context(), 0)
	if err != nil {
		t.Fatalf("AllTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d tasks, want one: a retried trigger is a receipt", len(tasks))
	}

	other := callSchedule(t, d, MethodScheduleTrigger, ScheduleTriggerRequest{ID: created.ID, RequestID: "hook-2"}).(ScheduleTriggerResponse)
	if !other.Created || other.Task.ID == first.Task.ID {
		t.Fatalf("a new request id = %+v, want its own task", other)
	}
}

func TestATriggerNeedsARequestID(t *testing.T) {
	d, _, _ := clockDaemon(t, &fixedExecutor{})
	created := callSchedule(t, d, MethodScheduleSet, setScheduleRequest(t, t.TempDir())).(schedule.Schedule)

	raw, err := json.Marshal(ScheduleTriggerRequest{ID: created.ID})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := d.handleCall(t.Context(), nil, MethodScheduleTrigger, raw); err == nil {
		t.Fatal("a trigger with no request id was accepted: it cannot be retried safely")
	}
}

func TestScheduleCallsSaySoWhenTheQueueFailed(t *testing.T) {
	// A daemon whose database did not open has no clock, and must answer with a
	// refusal rather than a schedule it cannot fire. A state directory that is
	// a file is the smallest way to make Open fail.
	blocked := filepath.Join(t.TempDir(), "state-is-a-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed the blocked state dir: %v", err)
	}
	d := New(Config{Channel: "test", Token: "shared", StateDir: blocked, Log: discardLogger()}, nil)
	t.Cleanup(d.Detach)
	if d.clock != nil {
		t.Fatal("this test assumes the daemon has no clock")
	}

	if _, err := d.listSchedules(); err == nil {
		t.Fatal("listSchedules answered without a queue")
	}
	raw, _ := json.Marshal(setScheduleRequest(t, t.TempDir()))
	if _, err := d.handleCall(t.Context(), nil, MethodScheduleSet, raw); err == nil {
		t.Fatal("setSchedule accepted a schedule the daemon cannot fire")
	}
}

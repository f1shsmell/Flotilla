package schedule

import (
	"path/filepath"
	"testing"
	"time"
)

func validSchedule(t *testing.T) Schedule {
	t.Helper()
	return Schedule{
		ID:      "sch-1",
		Prompt:  "look at the overnight builds",
		Cadence: Cadence{Type: "every", EveryMS: 3_600_000},
		Target: Target{
			Type:   "new-agent",
			Config: TargetConfig{Dialect: "claude", Cwd: t.TempDir()},
		},
		Status:    StatusActive,
		MissedRun: MissedSkip,
	}
}

func TestScheduleValidationNamesWhatIsMissing(t *testing.T) {
	if err := validSchedule(t).Validate(); err != nil {
		t.Fatalf("a complete schedule was refused: %v", err)
	}

	cases := []struct {
		name   string
		change func(*Schedule)
	}{
		{"empty id", func(s *Schedule) { s.ID = "" }},
		{"empty prompt", func(s *Schedule) { s.Prompt = " " }},
		{"bad cadence", func(s *Schedule) { s.Cadence = Cadence{Type: "every"} }},
		{"empty target", func(s *Schedule) { s.Target.Type = "" }},
		{"relative cwd", func(s *Schedule) { s.Target.Config.Cwd = "elsewhere" }},
		{"unknown isolation", func(s *Schedule) { s.Target.Config.Isolation = "container" }},
		{"unknown status", func(s *Schedule) { s.Status = "sleeping" }},
		{"unknown missed policy", func(s *Schedule) { s.MissedRun = "replay_all" }},
	}
	for _, tc := range cases {
		s := validSchedule(t)
		tc.change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate accepted the schedule", tc.name)
		}
	}
}

func TestAnAgentTargetNamesAnAgentAndAWorkingDirectory(t *testing.T) {
	s := validSchedule(t)
	s.Target = Target{Type: "agent", AgentID: "claude", Config: TargetConfig{Cwd: t.TempDir()}}
	if err := s.Validate(); err != nil {
		t.Fatalf("agent target refused: %v", err)
	}

	s.Target.AgentID = ""
	if err := s.Validate(); err == nil {
		t.Fatal("an agent target without an agent id was accepted")
	}
}

func TestTaskIDIsStableAndDistinctPerSlotOrRequest(t *testing.T) {
	s := validSchedule(t)
	slot := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

	first := TaskID(Fire{Schedule: s, Slot: slot})
	if again := TaskID(Fire{Schedule: s, Slot: slot}); again != first {
		t.Fatalf("the same fire derived %q and %q: a retry would enqueue twice", first, again)
	}
	if next := TaskID(Fire{Schedule: s, Slot: slot.Add(time.Hour)}); next == first {
		t.Fatal("two slots derived one task id: the second fire would be deduplicated away")
	}
	if manual := TaskID(Fire{Schedule: s, Slot: slot, RequestID: "hook-7"}); manual == first {
		t.Fatal("a request id derived the clock slot's task id")
	}

	other := s
	other.ID = "sch-2"
	if otherID := TaskID(Fire{Schedule: other, Slot: slot}); otherID == first {
		t.Fatal("two schedules derived one task id")
	}
	// A fire's slot is recorded to the second, because the id becomes a branch
	// name: a slot's sub-second part must not become a different run.
	if across := TaskID(Fire{Schedule: s, Slot: slot.Add(time.Millisecond)}); across != first {
		t.Fatalf("the same second derived %q and %q", first, across)
	}
}

func TestTaskIDKeepsTheTaskNamingShape(t *testing.T) {
	id := TaskID(Fire{Schedule: Schedule{ID: "sch-1"}, Slot: time.Unix(0, 0)})
	if len(id) != len("task-")+24 {
		t.Fatalf("id = %q, want a task- prefix and 12 bytes of hex", id)
	}
	if filepath.Base(id) != id {
		t.Fatalf("id = %q is not path-safe", id)
	}
}

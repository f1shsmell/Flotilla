package main

import (
	"strings"
	"testing"
)

// TestScheduleAddRefusesWhatTheDaemonWouldFakeAnyway pins the local checks:
// each one is a request the daemon would either refuse or, worse, accept with a
// guessed value the person did not mean.
func TestScheduleAddRefusesWhatTheDaemonWouldFakeAnyway(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no prompt", []string{"--every", "1h", "--dialect", "claude", "--cwd", "."}, "--prompt"},
		{"neither cadence", []string{"--prompt", "x", "--dialect", "claude", "--cwd", "."}, "exactly one of --every or --cron"},
		{"both cadences", []string{"--prompt", "x", "--every", "1h", "--cron", "0 9 * * *", "--tz", "UTC", "--dialect", "claude", "--cwd", "."}, "exactly one of --every or --cron"},
		{"cron without a zone", []string{"--prompt", "x", "--cron", "0 9 * * *", "--dialect", "claude", "--cwd", "."}, "--tz"},
		{"no target", []string{"--prompt", "x", "--every", "1h", "--cwd", "."}, "--agent or --dialect"},
		{"no cwd", []string{"--prompt", "x", "--every", "1h", "--dialect", "claude"}, "--cwd"},
		{"sub-second cadence", []string{"--prompt", "x", "--every", "500ms", "--dialect", "claude", "--cwd", "."}, "below a second"},
	}
	for _, tc := range cases {
		var out strings.Builder
		err := addSchedule(tc.args, "test", false, &out)
		if err == nil {
			t.Errorf("%s: addSchedule succeeded", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestScheduleCommandsNameTheirUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}} {
		var out strings.Builder
		if err := scheduleCommand(args, "test", false, &out); err == nil {
			t.Errorf("scheduleCommand(%q) succeeded", args)
		}
	}

	var out strings.Builder
	if err := removeSchedule(nil, "test", &out); err == nil || !strings.Contains(err.Error(), "schedule rm") {
		t.Errorf("removeSchedule() = %v, want a usage error", err)
	}
}

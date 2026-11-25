package schedule

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestEveryCadenceStartsFromTheInstantItWasAsked(t *testing.T) {
	c := Cadence{Type: "every", EveryMS: 90_000}
	after := mustTime(t, "2026-11-26T10:00:00Z")

	next, err := c.Next(after)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := mustTime(t, "2026-11-26T10:01:30Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestCronReadsTheWallClockInItsOwnZone(t *testing.T) {
	c := Cadence{Type: "cron", Expression: "0 9 * * *", Timezone: "Asia/Shanghai"}

	// 00:30 UTC is 08:30 in Shanghai, so the next 09:00 is one half hour away.
	next, err := c.Next(mustTime(t, "2026-11-26T00:30:00Z"))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := mustTime(t, "2026-11-26T01:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s (09:00 in Shanghai)", next, want)
	}
}

func TestCronIsStrictlyAfterTheGivenInstant(t *testing.T) {
	c := Cadence{Type: "cron", Expression: "0 9 * * *", Timezone: "UTC"}

	// Asked at the fire itself, the answer is tomorrow's, not this one again.
	next, err := c.Next(mustTime(t, "2026-11-26T09:00:00Z"))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := mustTime(t, "2026-11-27T09:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestCronDayFieldsAreReadAsEitherWhenBothAreRestricted(t *testing.T) {
	// 2026-11-26 is a Thursday; the 27th is a Friday and the 30th a Monday.
	cases := []struct {
		expression string
		want       string
	}{
		{"0 0 1 * 5", "2026-11-27T00:00:00Z"}, // the Friday wins over the 1st
		{"0 0 1 * 1", "2026-11-30T00:00:00Z"}, // the Monday wins over the 1st
		{"0 0 1 * *", "2026-12-01T00:00:00Z"}, // only the day of month is set
		{"0 0 * * 1", "2026-11-30T00:00:00Z"}, // only the weekday is set
	}
	for _, tc := range cases {
		c := Cadence{Type: "cron", Expression: tc.expression, Timezone: "UTC"}
		next, err := c.Next(mustTime(t, "2026-11-26T12:00:00Z"))
		if err != nil {
			t.Fatalf("%s: Next: %v", tc.expression, err)
		}
		if want := mustTime(t, tc.want); !next.Equal(want) {
			t.Errorf("%s: next = %s, want %s", tc.expression, next, want)
		}
	}
}

func TestCronStepsAndRangesFireAtTheRightMinutes(t *testing.T) {
	c := Cadence{Type: "cron", Expression: "*/15 9-10 * * *", Timezone: "UTC"}

	next, err := c.Next(mustTime(t, "2026-11-26T09:07:00Z"))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := mustTime(t, "2026-11-26T09:15:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestCronAcceptsSevenAsSunday(t *testing.T) {
	c := Cadence{Type: "cron", Expression: "0 0 * * 7", Timezone: "UTC"}

	next, err := c.Next(mustTime(t, "2026-11-27T12:00:00Z"))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := mustTime(t, "2026-11-29T00:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want Sunday %s", next, want)
	}
}

func TestCadenceRefusesExpressionsItCannotFire(t *testing.T) {
	cases := []Cadence{
		{Type: "cron", Expression: "0 9 * *", Timezone: "UTC"},
		{Type: "cron", Expression: "60 9 * * *", Timezone: "UTC"},
		{Type: "cron", Expression: "0 9 * MON *", Timezone: "UTC"},
		{Type: "cron", Expression: "*/0 9 * * *", Timezone: "UTC"},
		{Type: "cron", Expression: "5/10 9 * * *", Timezone: "UTC"},
		{Type: "cron", Expression: "0 9 * * *"},
		{Type: "cron", Expression: "0 9 * * *", Timezone: "Not/AZone"},
		{Type: "every"},
		{Type: "every", EveryMS: -1},
		{Type: "every", EveryMS: 500},
		{Type: "sometimes", EveryMS: 1000},
	}
	for _, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%+v: Validate accepted a cadence this build cannot fire", c)
		}
	}
}

func TestCadenceDescribesItselfForAPerson(t *testing.T) {
	if got := (Cadence{Type: "every", EveryMS: 90_000}).Describe(); got != "every 1m30s" {
		t.Errorf("every: %q", got)
	}
	if got := (Cadence{Type: "cron", Expression: "0 9 * * *", Timezone: "UTC"}).Describe(); got != "cron 0 9 * * * (UTC)" {
		t.Errorf("cron: %q", got)
	}
}

// Package schedule is the daemon's timer for unattended work: the cadence a job
// runs on, when its next run is due, and the rules that keep a slow run from
// stacking up or a daemon restart from replaying every slot it missed.
package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Status is whether the clock fires a schedule.
type Status string

const (
	// StatusActive means the schedule fires at its cadence.
	StatusActive Status = "active"
	// StatusPaused means the clock never fires it. A person can still trigger
	// it by hand, and its runs that are already queued still run.
	StatusPaused Status = "paused"
)

// Valid reports whether s is a status this build knows.
func (s Status) Valid() bool { return s == StatusActive || s == StatusPaused }

// MissedRun is what happens to a slot that came due while no daemon was running.
type MissedRun string

const (
	// MissedSkip moves on to the next slot. It is the default: a nightly job
	// that missed a night does not want a pile of make-up runs at boot.
	MissedSkip MissedRun = "skip"
	// MissedRunOnce fires the single missed slot once, then resumes the
	// cadence. It is for work where one catch-up is the point, such as a
	// morning report read later in the day.
	MissedRunOnce MissedRun = "run_once"
)

// Valid reports whether m is a policy this build knows.
func (m MissedRun) Valid() bool { return m == MissedSkip || m == MissedRunOnce }

// Cadence is how often a schedule fires: a rolling interval, or a five-field
// cron expression read in one named time zone.
type Cadence struct {
	// Type is "every" or "cron".
	Type string `json:"type"`
	// EveryMS is the interval for "every", in milliseconds.
	EveryMS int64 `json:"every_ms,omitempty"`
	// Expression is the five-field cron expression for "cron".
	Expression string `json:"expression,omitempty"`
	// Timezone is the IANA zone the expression is read in. It is required:
	// a wall-clock cadence read in the daemon machine's zone would silently
	// mean a different hour after a move or a DST change.
	Timezone string `json:"timezone,omitempty"`
}

// Validate refuses a cadence this build could not fire.
func (c Cadence) Validate() error {
	switch c.Type {
	case "every":
		if c.EveryMS < int64(time.Second/time.Millisecond) {
			return fmt.Errorf("cadence every: %d ms is below a second", c.EveryMS)
		}
	case "cron":
		if strings.TrimSpace(c.Expression) == "" {
			return errors.New("cadence cron: empty expression")
		}
		if _, err := parseCron(c.Expression); err != nil {
			return err
		}
		if c.Timezone == "" {
			return errors.New("cadence cron: a time zone is required")
		}
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("cadence cron: time zone %q: %w", c.Timezone, err)
		}
	default:
		return fmt.Errorf("cadence type %q is not one this build fires", c.Type)
	}
	return nil
}

// Next returns the first fire strictly after after.
func (c Cadence) Next(after time.Time) (time.Time, error) {
	if err := c.Validate(); err != nil {
		return time.Time{}, err
	}
	if c.Type == "every" {
		return after.Add(time.Duration(c.EveryMS) * time.Millisecond), nil
	}

	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load time zone %q: %w", c.Timezone, err)
	}
	expr, err := parseCron(c.Expression)
	if err != nil {
		return time.Time{}, err
	}
	return nextCron(expr, loc, after)
}

// Describe renders the cadence for a terminal or a log line.
func (c Cadence) Describe() string {
	if c.Type == "every" {
		return "every " + time.Duration(c.EveryMS*int64(time.Millisecond)).String()
	}
	if c.Timezone != "" {
		return "cron " + c.Expression + " (" + c.Timezone + ")"
	}
	return "cron " + c.Expression
}

// TargetConfig is where and how one scheduled run works.
type TargetConfig struct {
	// Dialect names the headless dialect that runs the job. A "new-agent"
	// target needs it; an "agent" target resolves it from the preset unless
	// the field overrides it.
	Dialect string `json:"dialect,omitempty"`
	// Cwd is the absolute directory the run starts in.
	Cwd string `json:"cwd"`
	// Access is the dialect's own word for what the agent may do.
	Access string `json:"access,omitempty"`
	// Isolation is "local" (the default) or "worktree", which runs each fire
	// in a fresh git worktree of Cwd.
	Isolation string `json:"isolation,omitempty"`
}

// Target is what a fire runs: an agent the catalog knows, or a new unattended
// agent defined by the schedule itself.
type Target struct {
	// Type is "agent" or "new-agent".
	Type string `json:"type"`
	// AgentID names a catalog preset for "agent".
	AgentID string `json:"agent_id,omitempty"`
	// Config is where the run works; "new-agent" needs the dialect too.
	Config TargetConfig `json:"config"`
}

// Isolation reports the effective isolation, with the default filled in.
func (t Target) Isolation() string {
	if t.Config.Isolation == "" {
		return "local"
	}
	return t.Config.Isolation
}

// Validate refuses a target this build could not run.
func (t Target) Validate() error {
	switch t.Type {
	case "agent":
		if t.AgentID == "" {
			return errors.New("target agent: empty agent id")
		}
	case "new-agent":
		if t.Config.Dialect == "" {
			return errors.New("target new-agent: empty dialect")
		}
	default:
		return fmt.Errorf("target type %q is not one this build runs", t.Type)
	}
	if t.Config.Cwd == "" {
		return errors.New("target: empty working directory")
	}
	if !filepath.IsAbs(t.Config.Cwd) {
		return fmt.Errorf("target: working directory %q is not absolute", t.Config.Cwd)
	}
	switch t.Isolation() {
	case "local", "worktree":
	default:
		return fmt.Errorf("target: isolation %q is not local or worktree", t.Config.Isolation)
	}
	return nil
}

// Schedule is one timed job, as stored and as a client reads it.
type Schedule struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Prompt is the instruction each fire gives the agent.
	Prompt  string  `json:"prompt"`
	Cadence Cadence `json:"cadence"`
	Target  Target  `json:"target"`
	// Status is active or paused.
	Status Status `json:"status"`
	// MissedRun is the policy for a slot that came due while the daemon was
	// down.
	MissedRun MissedRun `json:"missed_run"`
	// NextAt is when the clock next fires it; zero means nothing is pending,
	// which is the honest value for a paused schedule.
	NextAt time.Time `json:"next_at,omitempty"`
	// LastAt is when a fire was last accepted.
	LastAt time.Time `json:"last_at,omitempty"`
	// LastTask is the task the last fire enqueued, empty before the first.
	LastTask string `json:"last_task,omitempty"`
	// LastError is why the last fire was skipped, cleared by a fire that
	// enqueued.
	LastError string `json:"last_error,omitempty"`
	// Skipped counts fires the daemon deliberately did not start: an overlap,
	// a missed slot or a failure to prepare one.
	Skipped int `json:"skipped,omitempty"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Validate refuses a schedule this build could not store and fire.
func (s Schedule) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("schedule: empty id")
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("schedule %q: empty prompt", s.ID)
	}
	if err := s.Cadence.Validate(); err != nil {
		return fmt.Errorf("schedule %q: %w", s.ID, err)
	}
	if err := s.Target.Validate(); err != nil {
		return fmt.Errorf("schedule %q: %w", s.ID, err)
	}
	if !s.Status.Valid() {
		return fmt.Errorf("schedule %q: status %q is not one this build knows", s.ID, s.Status)
	}
	if !s.MissedRun.Valid() {
		return fmt.Errorf("schedule %q: missed-run policy %q is not one this build knows", s.ID, s.MissedRun)
	}
	return nil
}

// The schedule event kinds. The row is what the scheduler acts on; the events
// beside it are how a person explains afterwards why a job ran, or did not.
const (
	// EventScheduleSet records a schedule created or edited.
	EventScheduleSet = "schedule.set"
	// EventScheduleRemoved records a schedule deleted.
	EventScheduleRemoved = "schedule.removed"
	// EventScheduleFired records a fire that enqueued a task.
	EventScheduleFired = "schedule.fired"
	// EventScheduleSkipped records a fire the daemon did not start, with the
	// reason in the row's LastError.
	EventScheduleSkipped = "schedule.skipped"
	// EventScheduleMissed records slots that passed while no daemon ran.
	EventScheduleMissed = "schedule.missed"
)

// Fire is one request to run a schedule: a slot the clock reached, or a request
// a person or a webhook made.
type Fire struct {
	Schedule Schedule
	// Slot is the scheduled instant for a clock fire. A manual fire carries
	// the instant the request arrived instead, and RequestID says so.
	Slot time.Time
	// RequestID is the caller's idempotency key for a manual fire. Empty for a
	// clock fire, whose slot is its key.
	RequestID string
}

// TaskID is the task a fire enqueues, derived from the schedule and either its
// slot or the caller's request id. Deriving it, rather than generating one, is
// what makes a retried trigger a receipt rather than a second run.
func TaskID(f Fire) string {
	key := "schedule:" + f.Schedule.ID
	if f.RequestID != "" {
		key += "|request:" + f.RequestID
	} else {
		// The slot is truncated to a whole second: a task id is also the seed of
		// a worktree branch and a key a person reads, and a cron slot lands on a
		// whole minute, so no two slots are made to collide by the loss.
		key += "|slot:" + f.Slot.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	sum := sha256.Sum256([]byte(key))
	return "task-" + hex.EncodeToString(sum[:12])
}

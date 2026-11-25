package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/schedule"
)

// scheduleCommand serves the daemon's timed jobs: the automations entry the
// plan puts beside the CLI and the socket API. A webhook is this same call with
// its own request id.
func scheduleCommand(args []string, channel string, asJSON bool, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: flt [--json] schedule <list|add|rm|run>")
	}
	switch args[0] {
	case "list":
		return listSchedules(args[1:], channel, asJSON, out)
	case "add":
		return addSchedule(args[1:], channel, asJSON, out)
	case "rm":
		return removeSchedule(args[1:], channel, out)
	case "run":
		return runSchedule(args[1:], channel, asJSON, out)
	default:
		return fmt.Errorf("unknown schedule command %q", args[0])
	}
}

// scheduleClient dials the daemon for one command.
func scheduleClient(channel string) (*daemon.Client, context.Context, context.CancelFunc, error) {
	paths, err := home.Resolve(channel)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve state paths: %w", err)
	}
	token, err := ipc.ReadTokenFile(paths.Token)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read the connection token: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	client, err := daemon.Connect(ctx, channel, token, paths.Sessions)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("the %s daemon is not running: %w", channel, err)
	}
	return client, ctx, cancel, nil
}

func listSchedules(args []string, channel string, asJSON bool, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("schedule list takes no arguments, got %q: flags go before the command", args)
	}
	client, ctx, cancel, err := scheduleClient(channel)
	if err != nil {
		return err
	}
	defer cancel()
	defer client.Close()

	list, err := client.Schedules(ctx)
	if err != nil {
		return fmt.Errorf("schedule list: %w", err)
	}
	if asJSON {
		return printJSON(out, daemon.ScheduleListResponse{Schedules: list}, "schedules")
	}
	if len(list) == 0 {
		return print(out, "no schedules")
	}
	for _, sch := range list {
		next := "-"
		if !sch.NextAt.IsZero() {
			next = sch.NextAt.Local().Format(time.RFC3339)
		}
		target := sch.Target.Type
		if sch.Target.AgentID != "" {
			target += ":" + sch.Target.AgentID
		} else {
			target += ":" + sch.Target.Config.Dialect
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
			sch.ID, sch.Status, next, sch.Cadence.Describe(), target, terminalText(sch.Prompt))
		if sch.LastError != "" {
			line += "\t" + terminalText(sch.LastError)
		}
		if err := print(out, line); err != nil {
			return err
		}
	}
	return nil
}

func addSchedule(args []string, channel string, asJSON bool, out io.Writer) error {
	fs := flag.NewFlagSet("flt schedule add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "schedule id; empty makes one")
	name := fs.String("name", "", "display name")
	prompt := fs.String("prompt", "", "instruction each fire gives the agent")
	every := fs.Duration("every", 0, "rolling interval, e.g. 30m")
	cron := fs.String("cron", "", "five-field cron expression")
	tz := fs.String("tz", "", "IANA time zone the cron expression is read in")
	agent := fs.String("agent", "", "catalog agent id the fire runs")
	dialect := fs.String("dialect", "", "headless dialect the fire runs; with --agent, overrides the preset's")
	cwd := fs.String("cwd", "", "absolute working directory")
	access := fs.String("access", "", "the dialect's own access level")
	isolation := fs.String("isolation", "local", "local or worktree")
	missed := fs.String("missed", string(schedule.MissedSkip), "skip or run_once")
	paused := fs.Bool("paused", false, "create it without putting it on the clock")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse schedule add: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("schedule add takes no arguments, got %q: flags go before the command", fs.Args())
	}
	if strings.TrimSpace(*prompt) == "" {
		return errors.New("schedule add: --prompt is required")
	}
	if (*every > 0) == (*cron != "") {
		return errors.New("schedule add: name exactly one of --every or --cron")
	}
	if *cron != "" && *tz == "" {
		return errors.New("schedule add: --cron needs --tz: a wall-clock cadence read in a guessed zone fires at the wrong hour")
	}
	if *agent == "" && *dialect == "" {
		return errors.New("schedule add: name --agent or --dialect")
	}
	if *cwd == "" {
		return errors.New("schedule add: --cwd is required")
	}
	abs, err := filepath.Abs(*cwd)
	if err != nil {
		return fmt.Errorf("resolve --cwd: %w", err)
	}

	req := daemon.ScheduleSetRequest{
		ID:     *id,
		Name:   *name,
		Prompt: *prompt,
		Target: schedule.Target{
			Type: "new-agent",
			Config: schedule.TargetConfig{
				Dialect:   *dialect,
				Cwd:       abs,
				Access:    *access,
				Isolation: *isolation,
			},
		},
		MissedRun: schedule.MissedRun(*missed),
	}
	if *agent != "" {
		req.Target = schedule.Target{
			Type:    "agent",
			AgentID: *agent,
			Config: schedule.TargetConfig{
				Dialect:   *dialect,
				Cwd:       abs,
				Access:    *access,
				Isolation: *isolation,
			},
		}
	}
	if *every > 0 {
		req.Cadence = schedule.Cadence{Type: "every", EveryMS: every.Milliseconds()}
		if *every < time.Second {
			return errors.New("schedule add: --every below a second is a loop, not a cadence")
		}
	} else {
		req.Cadence = schedule.Cadence{Type: "cron", Expression: *cron, Timezone: *tz}
	}
	if *paused {
		req.Status = schedule.StatusPaused
	}

	client, ctx, cancel, err := scheduleClient(channel)
	if err != nil {
		return err
	}
	defer cancel()
	defer client.Close()

	applied, err := client.SetSchedule(ctx, req)
	if err != nil {
		return fmt.Errorf("schedule add: %w", err)
	}
	if asJSON {
		return printJSON(out, applied, "schedule")
	}
	return print(out, applied.ID)
}

func removeSchedule(args []string, channel string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: flt schedule rm <id>")
	}
	client, ctx, cancel, err := scheduleClient(channel)
	if err != nil {
		return err
	}
	defer cancel()
	defer client.Close()

	if err := client.RemoveSchedule(ctx, args[0]); err != nil {
		return fmt.Errorf("schedule rm: %w", err)
	}
	return print(out, "removed "+args[0])
}

func runSchedule(args []string, channel string, asJSON bool, out io.Writer) error {
	fs := flag.NewFlagSet("flt schedule run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	requestID := fs.String("request-id", "", "idempotency key; the same id runs the job once")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse schedule run: %w", err)
	}
	if fs.NArg() != 1 {
		return errors.New("usage: flt schedule run [--request-id ID] <id>")
	}
	id := fs.Arg(0)

	request := *requestID
	if request == "" {
		generated, err := newRequestID()
		if err != nil {
			return err
		}
		request = generated
	}

	client, ctx, cancel, err := scheduleClient(channel)
	if err != nil {
		return err
	}
	defer cancel()
	defer client.Close()

	resp, err := client.TriggerSchedule(ctx, id, request)
	if err != nil {
		return fmt.Errorf("schedule run: %w", err)
	}
	if asJSON {
		return printJSON(out, resp, "trigger")
	}
	state := "queued"
	if !resp.Created {
		state = "already accepted"
	}
	return print(out, fmt.Sprintf("%s\t%s\trequest=%s", resp.Task.ID, state, request))
}

// newRequestID makes an idempotency key for a manual run. It is printed with
// the answer, so the same run can be retried deliberately.
func newRequestID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate a request id: %w", err)
	}
	return "cli-" + hex.EncodeToString(raw[:]), nil
}

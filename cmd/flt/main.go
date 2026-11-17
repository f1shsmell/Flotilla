// Command flt is the Flotilla CLI: the same service layer the GUI and daemon
// expose, reachable from a terminal and from agents.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
	"github.com/f1shsmell/flotilla/internal/acp/replay"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/transcript"
	"github.com/f1shsmell/flotilla/internal/version"
)

// callTimeout bounds one request to the daemon. A daemon that stopped reading
// must not hang the terminal.
const callTimeout = 10 * time.Second

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "flt:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("flt", flag.ContinueOnError)
	fs.SetOutput(out)
	showVersion := fs.Bool("version", false, "print the build version and exit")
	channel := fs.String("channel", version.Channel, "release channel to talk to: stable or preview")
	asJSON := fs.Bool("json", false, "print machine-readable output")
	fs.Usage = func() { _, _ = io.WriteString(out, usage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}

	if *showVersion {
		return print(out, version.String())
	}

	command := fs.Arg(0)
	switch command {
	case "":
		fs.Usage()
		return nil
	case "version":
		if err := rejectArgs(fs, command); err != nil {
			return err
		}
		return print(out, version.String())
	case "status":
		if err := rejectArgs(fs, command); err != nil {
			return err
		}
		return status(*channel, *asJSON, out)
	case "agents":
		if err := rejectArgs(fs, command); err != nil {
			return err
		}
		return listAgents(*channel, *asJSON, out)
	case "probe":
		return probeAgent(fs.Args()[1:], out)
	case "replay":
		return replayCommand(fs.Args()[1:], *channel, out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// rejectArgs refuses trailing arguments for a command that takes none. The flag
// package stops at the first non-flag, so ignoring them would hide a typo such
// as `flt status --json`, which then silently prints the human form.
func rejectArgs(fs *flag.FlagSet, command string) error {
	if fs.NArg() > 1 {
		return fmt.Errorf("%s takes no arguments, got %q: flags go before the command", command, fs.Args()[1:])
	}
	return nil
}

// status reports the fleet a running daemon serves.
func status(channel string, asJSON bool, out io.Writer) error {
	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	token, err := ipc.ReadTokenFile(paths.Token)
	if err != nil {
		return fmt.Errorf("read the connection token: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	client, err := daemon.Connect(ctx, channel, token, paths.Sessions)
	if err != nil {
		return fmt.Errorf("the %s daemon is not running: %w", channel, err)
	}
	defer client.Close()

	st, err := client.Status(ctx)
	if err != nil {
		return fmt.Errorf("read the fleet status: %w", err)
	}

	if asJSON {
		payload, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return fmt.Errorf("encode status: %w", err)
		}
		return print(out, string(payload))
	}

	if len(st.Sessions) == 0 {
		return print(out, "no sessions")
	}
	for _, s := range st.Sessions {
		state := "unreachable"
		if s.Reachable {
			state = "reachable"
		}
		mode := string(s.Mode)
		if mode == "" {
			mode = "pty"
		}
		if err := print(out, fmt.Sprintf("%s\t%s\t%s\tpid=%d\t%s", s.ID, mode, state, s.HostPID, s.Command)); err != nil {
			return err
		}
	}
	return nil
}

// listAgents reports the agents a running daemon offers, with the switches the
// settings page writes. It is the picker's data from a terminal.
func listAgents(channel string, asJSON bool, out io.Writer) error {
	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	token, err := ipc.ReadTokenFile(paths.Token)
	if err != nil {
		return fmt.Errorf("read the connection token: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	client, err := daemon.Connect(ctx, channel, token, paths.Sessions)
	if err != nil {
		return fmt.Errorf("the %s daemon is not running: %w", channel, err)
	}
	defer client.Close()

	list, err := client.Agents(ctx)
	if err != nil {
		return fmt.Errorf("read the agent catalogue: %w", err)
	}

	if asJSON {
		payload, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return fmt.Errorf("encode agents: %w", err)
		}
		return print(out, string(payload))
	}

	if len(list.Agents) == 0 {
		return print(out, "no agents")
	}
	for _, line := range agentLines(list) {
		if err := print(out, line); err != nil {
			return err
		}
	}
	// A file the daemon could not read is part of the answer: a shorter list
	// with no reason would look like an agent that was removed.
	for _, problem := range list.Problems {
		if err := print(out, "! "+problem); err != nil {
			return err
		}
	}
	return nil
}

// agentLines renders one line per agent. The switches and the install state are
// printed as words, so a disabled or missing agent reads as such without a
// second lookup.
func agentLines(list agent.Listing) []string {
	lines := make([]string, 0, len(list.Agents))
	for _, e := range list.Agents {
		state := "missing"
		if e.Installed {
			state = "installed"
		}

		switches := make([]string, 0, 3)
		for _, on := range []struct {
			name string
			set  bool
		}{
			{"enabled", e.Enabled},
			{"chat", e.Chat},
			{"cli", e.CLI},
		} {
			if on.set {
				switches = append(switches, on.name)
			}
		}

		mode := string(e.Mode)
		if mode == "" {
			mode = "acp"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			e.ID, mode, state, strings.Join(switches, ","), e.Command))
	}
	return lines
}

// probeTimeout bounds one probe turn. A real agent turn is slow, but a hung
// one must not hold the terminal for good.
const probeTimeout = 5 * time.Minute

// probeAgent runs one real turn against an agent and reports what came back.
// It spends the user's own tokens, so it runs only when named explicitly.
func probeAgent(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("flt probe", flag.ContinueOnError)
	fs.SetOutput(out)
	command := fs.String("command", "", "agent command to probe, as a binary and its arguments")
	dir := fs.String("dir", "", "working directory for the turn")
	prompt := fs.String("prompt", probe.DefaultPrompt, "prompt to send")
	timeout := fs.Duration("timeout", probeTimeout, "give up after this long")

	fs.Usage = func() {
		_, _ = fmt.Fprint(out, "usage: flt probe [flags] [agent]\n\n"+
			"Runs one real turn and prints what came back. It spends your own tokens.\n"+
			"Tool calls are auto-approved; file access stays inside --dir; terminals\n"+
			"are refused. A missing binary or login fails before the turn starts.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}

	agent, err := probeTarget(*command, fs.Args())
	if err != nil {
		return err
	}
	// A missing binary or a missing login fails before any token is spent.
	if err := agent.Available(); err != nil {
		return fmt.Errorf("probe %s: %w", agent.Command, err)
	}

	workspace := *dir
	if workspace == "" {
		if workspace, err = os.Getwd(); err != nil {
			return fmt.Errorf("resolve the working directory: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	res, err := probe.Run(ctx, agent, workspace, *prompt)
	if err != nil {
		return fmt.Errorf("probe %s: %w", agent.Command, err)
	}
	return print(out, res.Report())
}

// probeTarget resolves what to probe. With --command the remaining arguments
// belong to the agent, which is what `--` is for; without it the first argument
// names a known agent and any other is a typo worth refusing.
func probeTarget(command string, rest []string) (probe.Agent, error) {
	if command != "" {
		fields := strings.Fields(command)
		return probe.Agent{
			Name:    fields[0],
			Command: fields[0],
			Args:    append(fields[1:], rest...),
		}, nil
	}

	switch {
	case len(rest) == 0:
		return probe.Agent{}, fmt.Errorf("name an agent (%s) or pass --command", strings.Join(probe.Names(), ", "))
	case len(rest) > 1:
		return probe.Agent{}, fmt.Errorf("unexpected arguments %q: flags go before the agent name", rest[1:])
	}

	agent, err := probe.Lookup(rest[0])
	if err != nil {
		return probe.Agent{}, fmt.Errorf("resolve the agent to probe: %w", err)
	}
	return agent, nil
}

// replayCommand records a session's turn as a scenario the replay agent can
// answer from. It reads the daemon's own transcript rather than a live session,
// so a recording can be made after the turn that produced it, and without a
// daemon running.
func replayCommand(args []string, channel string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: flt replay export <session> [--out <path>]")
	}
	if args[0] != "export" {
		return fmt.Errorf("unknown replay command %q: only export is supported", args[0])
	}

	fs := flag.NewFlagSet("flt replay export", flag.ContinueOnError)
	fs.SetOutput(out)
	target := fs.String("out", "", "file to write the scenario to; empty prints it")

	fs.Usage = func() {
		_, _ = fmt.Fprint(out, "usage: flt replay export <session> [--out <path>]\n\n"+
			"Turns a session's stored log into a scenario the replay agent answers from.\n"+
			"The person's own messages are the turn boundaries and are not kept: the\n"+
			"replay records each prompt as it arrives, so keeping them would show every\n"+
			"question twice.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	// The flag package stops at the first non-flag, so `replay export sess --out
	// path` would leave --out unread and write to stdout instead. The same
	// refusal probe uses keeps a stray flag from being silently ignored.
	if fs.NArg() != 1 {
		return fmt.Errorf("name one session to export, got %q: flags go before the session", fs.Args())
	}
	id := fs.Arg(0)

	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	store := transcript.NewStore(filepath.Join(paths.Sessions, "transcripts"))
	defer func() { _ = store.Close() }()

	page, err := store.Page(id, transcript.Window{})
	if err != nil {
		return fmt.Errorf("read the log of session %q: %w", id, err)
	}
	if len(page.Records) == 0 {
		return fmt.Errorf("session %q has no stored log: %w", id, replay.ErrNothingToExport)
	}

	raw := make([]json.RawMessage, 0, len(page.Records))
	for _, rec := range page.Records {
		raw = append(raw, rec.Update)
	}
	scenario, err := replay.ExportJSON(raw)
	if err != nil {
		return fmt.Errorf("export session %q: %w", id, err)
	}
	scenario.Name = id

	if *target == "" {
		payload, err := json.MarshalIndent(scenario, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the scenario: %w", err)
		}
		return print(out, string(payload))
	}
	if err := scenario.Save(*target); err != nil {
		return err
	}
	return print(out, fmt.Sprintf("wrote %d turns to %s", len(scenario.Turns), *target))
}

const usage = "usage: flt [--channel stable|preview] <command>\n\ncommands:\n  version  print the build version\n  status   list the sessions a running daemon serves\n  agents   list the agents the machine offers, with their switches\n  probe    run one real turn against an agent and report what came back\n  replay   export a session's log as a replay scenario\n"

func print(out io.Writer, line string) error {
	if _, err := io.WriteString(out, line+"\n"); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

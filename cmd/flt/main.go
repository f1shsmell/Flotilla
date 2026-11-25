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
	"unicode"
	"unicode/utf8"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
	"github.com/f1shsmell/flotilla/internal/acp/replay"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/drift"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/mcpserver"
	"github.com/f1shsmell/flotilla/internal/registry"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
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
	channel := fs.String("channel", defaultChannel(), "release channel to talk to: stable or preview")
	asJSON := fs.Bool("json", false, "print machine-readable output")
	failOnDrift := fs.Bool("fail-on-drift", false, "exit non-zero when `registry drift` finds a pin behind its registry")
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
	case "registry":
		return registryCommand(fs.Args()[1:], *channel, *asJSON, *failOnDrift, out)
	case "schedule":
		return scheduleCommand(fs.Args()[1:], *channel, *asJSON, out)
	case "install":
		return installCommand(fs.Args()[1:], *channel, *asJSON, os.Stdin, out)
	case "probe":
		return probeAgent(fs.Args()[1:], *asJSON, out)
	case "replay":
		return replayCommand(fs.Args()[1:], *channel, out)
	case "mcp":
		return mcpCommand(fs.Args()[1:], *channel)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// defaultChannel is the channel a command talks to when --channel is not given.
// A process inside a session carries FLT_CHANNEL, and its commands must reach
// the daemon that serves that session rather than the default one.
func defaultChannel() string {
	if channel := os.Getenv("FLT_CHANNEL"); channel != "" {
		return channel
	}
	return version.Channel
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
	// An agent inside a session sees its own part of the fleet and nothing
	// else: FLT_SESSION_ID is what the host put into its environment.
	if id := os.Getenv("FLT_SESSION_ID"); id != "" {
		st = daemon.Scoped(st, session.ID(id))
	}

	if asJSON {
		payload, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return fmt.Errorf("encode status: %w", err)
		}
		return print(out, string(payload))
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
	if len(st.Sessions) == 0 {
		if err := print(out, "no sessions"); err != nil {
			return err
		}
	}

	// The queue is printed after the fleet rather than folded into it: a task
	// has no host and no window, and a caller scanning session ids must not meet
	// one it cannot attach to. It is still printed when the fleet is empty,
	// because a queue of unattended work is exactly what a daemon with no
	// sessions is doing.
	if len(st.Tasks) == 0 {
		return nil
	}
	if err := print(out, ""); err != nil {
		return err
	}
	for _, t := range st.Tasks {
		cost := ""
		if t.BudgetUSD > 0 {
			cost = fmt.Sprintf("\t$%.4f/$%.2f", t.SpentUSD, t.BudgetUSD)
		} else if t.SpentUSD > 0 {
			cost = fmt.Sprintf("\t$%.4f", t.SpentUSD)
		}
		detail := t.LastError
		if detail == "" {
			detail = t.Prompt
		}
		if err := print(out, fmt.Sprintf("%s\t%s\t%s\truns=%d%s\t%s",
			t.ID, t.Dialect, t.State, t.Runs, cost, oneLine(detail))); err != nil {
			return err
		}
	}
	return nil
}

// oneLine renders a task's prompt or error as a single line. A task carries
// whole instructions and multi-line failures, and either would tear apart a
// table that a person reads row by row.
//
// The cut is on a rune boundary, not a byte: half a multi-byte character is
// invalid UTF-8, and a table that shows a replacement glyph where a word should
// be is a table a reader stops trusting. A Chinese prompt hits this immediately.
func oneLine(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	joined := strings.Join(fields, " ")
	const limit = 100
	if len(joined) <= limit {
		return joined
	}

	// Back up to the start of the rune the cut landed inside.
	cut := limit
	for cut > 0 && !utf8.RuneStart(joined[cut]) {
		cut--
	}
	return joined[:cut] + "…"
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

	if len(list.Agents) == 0 && len(list.Registry) == 0 {
		return print(out, "no agents")
	}
	for _, line := range agentListingLines(list) {
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

// agentLines renders one line per agent. The switches and the discovery tier are
// printed as words, so a disabled or missing agent reads as such without a
// second lookup.
func agentLines(list agent.Listing) []string {
	lines := make([]string, 0, len(list.Agents))
	for _, e := range list.Agents {
		state := string(e.Tier)
		if state == "" {
			// A daemon older than this build sends no tier; the install flag is
			// what it does answer, and it must not read as missing.
			state = string(agent.TierMissing)
			if e.Installed {
				state = "installed"
			}
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

// agentListingLines renders the catalog and then the directory's extra agents:
// a reader sees what can start, then what only needs installing.
func agentListingLines(list agent.Listing) []string {
	lines := agentLines(list)
	if len(list.Registry) == 0 {
		return lines
	}
	lines = append(lines, "ACP Registry (not installed):")
	for _, entry := range list.Registry {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s", entry.ID, entry.Version, entry.Recipe))
	}
	return lines
}

// registryFetchTimeout bounds one refresh from the terminal. The daemon's loop
// has its own bound; this one only has to outlast a slow download.
const registryFetchTimeout = 30 * time.Second

// registryCommand serves the cached ACP agent directory. list prints what the
// last refresh left; refresh fetches the published registry and replaces it;
// drift asks the package registries whether the pins are still the latest.
func registryCommand(args []string, channel string, asJSON, failOnDrift bool, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: flt [--json] registry <list|refresh|drift>")
	}
	switch args[0] {
	case "list":
		return listRegistry(args[1:], channel, asJSON, out)
	case "refresh":
		return refreshRegistry(args[1:], channel, asJSON, out)
	case "drift":
		return driftCommand(args[1:], channel, asJSON, failOnDrift, drift.Checker{}, out)
	default:
		return fmt.Errorf("unknown registry command %q", args[0])
	}
}

// listRegistry prints the cached directory. A missing cache is answered with
// the command that fills it, never with an empty list.
func listRegistry(args []string, channel string, asJSON bool, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("registry list takes no arguments, got %q: flags go before the command", args)
	}

	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}
	path := registry.CachePath(paths.Cache)
	cached, err := registry.Load(path)
	if err != nil {
		return fmt.Errorf("read the cached agent registry (run `flt registry refresh`): %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read the age of the agent registry cache: %w", err)
	}

	if asJSON {
		payload, err := json.MarshalIndent(struct {
			registry.Registry
			CachedAt time.Time `json:"cached_at"`
		}{cached, info.ModTime()}, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the registry: %w", err)
		}
		return print(out, string(payload))
	}

	for _, a := range cached.Agents {
		if err := print(out, fmt.Sprintf("%s\t%s\t%s\t%s",
			terminalText(a.ID), terminalText(a.Version),
			terminalText(a.Distribution.Summary()), terminalText(a.Name))); err != nil {
			return err
		}
	}
	// The age is part of the answer: an offline copy is still a copy, but a
	// directory printed exactly like a fresh one would be a stale label.
	return print(out, cacheNote(time.Since(info.ModTime())))
}

// terminalText replaces control characters in fetched data, so a name cannot
// rewrite the terminal row it is printed on.
func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// cacheNote renders how old the cached directory is, and says when it is past
// the refresh interval.
func cacheNote(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	var note string
	switch {
	case age < time.Minute:
		note = "cached just now"
	case age < time.Hour:
		note = fmt.Sprintf("cached %dm ago", int(age.Minutes()))
	default:
		hours, minutes := int(age.Hours()), int(age.Minutes())%60
		if minutes == 0 {
			note = fmt.Sprintf("cached %dh ago", hours)
		} else {
			note = fmt.Sprintf("cached %dh%02dm ago", hours, minutes)
		}
	}
	if age >= registry.TTL {
		return note + " (stale; run `flt registry refresh`)"
	}
	return note
}

// refreshRegistry fetches the published registry and replaces the cache. What
// lands is the validated subset this build can describe, and the count says how
// much of the answer that was.
func refreshRegistry(args []string, channel string, asJSON bool, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("registry refresh takes no arguments, got %q: flags go before the command", args)
	}

	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}
	path := registry.CachePath(paths.Cache)

	ctx, cancel := context.WithTimeout(context.Background(), registryFetchTimeout)
	defer cancel()
	answer, err := registry.Refresh(ctx, nil, path, registry.URL)
	if err != nil {
		return fmt.Errorf("refresh the agent registry: %w", err)
	}

	if asJSON {
		payload, err := json.MarshalIndent(struct {
			Path    string `json:"path"`
			Version string `json:"version"`
			Agents  int    `json:"agents"`
		}{path, answer.Version, len(answer.Agents)}, "", "  ")
		if err != nil {
			return fmt.Errorf("encode the refresh: %w", err)
		}
		return print(out, string(payload))
	}
	return print(out, fmt.Sprintf("wrote %d agents to %s", len(answer.Agents), path))
}

// probeTimeout bounds one probe turn. A real agent turn is slow, but a hung
// one must not hold the terminal for good.
const probeTimeout = 5 * time.Minute

// probeAgent runs one real turn against an agent and reports what came back.
// It spends the user's own tokens, so it runs only when named explicitly.
//
// With --handshake it stops at the session being open: no prompt is sent, so
// nothing reaches the provider and no token is spent. That is the mode a
// support-matrix row is recorded with, because it works on a machine whose agent
// has no usable login.
func probeAgent(args []string, asJSON bool, out io.Writer) error {
	fs := flag.NewFlagSet("flt probe", flag.ContinueOnError)
	fs.SetOutput(out)
	command := fs.String("command", "", "agent command to probe, as a binary and its arguments")
	dir := fs.String("dir", "", "working directory for the turn")
	prompt := fs.String("prompt", probe.DefaultPrompt, "prompt to send")
	timeout := fs.Duration("timeout", probeTimeout, "give up after this long")
	handshakeOnly := fs.Bool("handshake", false,
		"stop once the session is open: read what the agent announced, send no prompt, spend no tokens")

	fs.Usage = func() {
		_, _ = fmt.Fprint(out, "usage: flt probe [flags] [agent]\n\n"+
			"Runs one real turn and prints what came back. It spends your own tokens.\n"+
			"Tool calls are auto-approved; file access stays inside --dir; terminals\n"+
			"are refused. A missing binary or login fails before the turn starts.\n\n"+
			"With --handshake it opens the session, prints the modes and configuration\n"+
			"options the agent announced, and closes it without sending a prompt: that\n"+
			"costs nothing and works without a usable login.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}

	// A prompt that --handshake will never send is refused rather than dropped:
	// silently ignoring it would let a maintainer believe a turn ran.
	if *handshakeOnly {
		prompted := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "prompt" {
				prompted = true
			}
		})
		if prompted {
			return fmt.Errorf("--prompt says nothing to --handshake, which sends no prompt: drop one of them")
		}
	}

	agent, err := probeTarget(*command, fs.Args())
	if err != nil {
		return err
	}
	// A missing binary or a missing login fails before any token is spent. The
	// spawn applies this same resolution, so the gate and the launch cannot name
	// different binaries.
	if _, err := agent.Resolve(); err != nil {
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

	if *handshakeOnly {
		res, err := probe.Handshake(ctx, agent, workspace)
		if err != nil {
			return fmt.Errorf("probe %s: %w", agent.Command, err)
		}
		return printHandshake(out, res, asJSON)
	}

	res, err := probe.Run(ctx, agent, workspace, *prompt)
	if err != nil {
		return fmt.Errorf("probe %s: %w", agent.Command, err)
	}
	if asJSON {
		return printJSON(out, res, "encode the probe result")
	}
	return print(out, res.Report())
}

// printHandshake renders a zero-token probe. The text form is the report plus one
// line per announced mode and option, because the whole point of the mode is to
// let a maintainer read what an agent says about itself without a JSON viewer.
func printHandshake(out io.Writer, res probe.HandshakeResult, asJSON bool) error {
	if asJSON {
		return printJSON(out, res, "encode the handshake result")
	}
	for _, line := range res.Lines() {
		if err := print(out, line); err != nil {
			return err
		}
	}
	return nil
}

// printJSON writes one value as indented JSON, which is what --json means for
// every command that serves it.
func printJSON(out io.Writer, value any, what string) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return print(out, string(payload))
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

// mcpCommand serves the fleet as MCP tools over stdio. Stdout belongs to the
// protocol, so even usage text goes to stderr.
func mcpCommand(args []string, channel string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: flt mcp serve [--channel stable|preview]")
	}

	fs := flag.NewFlagSet("flt mcp serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&channel, "channel", channel, "release channel to talk to: stable or preview")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse arguments: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("mcp serve takes no arguments, got %q", fs.Args())
	}

	paths, err := home.Resolve(channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err = mcpserver.Serve(ctx, mcpserver.Dialer{
		Channel:   channel,
		TokenPath: paths.Token,
		StateDir:  paths.Sessions,
	}, mcpserver.Options{
		SessionID: session.ID(os.Getenv("FLT_SESSION_ID")),
		Policy:    os.Getenv("FLT_POLICY"),
	})
	if err != nil {
		return fmt.Errorf("mcp serve: %w", err)
	}
	return nil
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

	// The daemon writes its state to one database; this reads the same file, so
	// an export sees what a running daemon recorded. A read-only open would be
	// kinder to a live daemon, but the store's own migration needs a write when
	// it is the first thing to touch the file.
	state, err := store.Open(filepath.Join(paths.Sessions, "state.db"))
	if err != nil {
		return fmt.Errorf("open the state database: %w", err)
	}
	defer func() { _ = state.Close() }()

	page, err := state.TranscriptPage(context.Background(), id, store.TranscriptWindow{})
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
		return fmt.Errorf("write the scenario to %q: %w", *target, err)
	}
	return print(out, fmt.Sprintf("wrote %d turns to %s", len(scenario.Turns), *target))
}

const usage = "usage: flt [--channel stable|preview] <command>\n\ncommands:\n  version  print the build version\n  status   list the sessions a running daemon serves\n  agents   list the agents the machine offers, with their switches\n  registry list, refresh or drift-check the cached ACP agent directory\n  install  confirm and run one agent's install recipe, writing its adapter\n  probe    run one real turn against an agent, or --handshake to read what it announces for free\n  schedule list, add, rm or run the daemon's timed jobs\n  replay   export a session's log as a replay scenario\n  mcp      serve the fleet tools over stdio for an agent inside a session\n"

func print(out io.Writer, line string) error {
	if _, err := io.WriteString(out, line+"\n"); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

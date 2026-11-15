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
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp/probe"
	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/ipc"
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
	case "probe":
		return probeAgent(fs.Args()[1:], out)
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

	if err := fs.Parse(args); err != nil {
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

const usage = "usage: flt [--channel stable|preview] <command>\n\ncommands:\n  version  print the build version\n  status   list the sessions a running daemon serves\n  probe    run one real turn against an agent and report what came back\n"

func print(out io.Writer, line string) error {
	if _, err := io.WriteString(out, line+"\n"); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

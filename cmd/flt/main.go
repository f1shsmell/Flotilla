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
	"time"

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
		return print(out, version.String())
	case "status":
		return status(*channel, *asJSON, out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
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

const usage = "usage: flt [--channel stable|preview] <command>\n\ncommands:\n  version  print the build version\n  status   list the sessions a running daemon serves\n"

func print(out io.Writer, line string) error {
	if _, err := io.WriteString(out, line+"\n"); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// Command flt-session-host holds one session's agent handles — the ConPTY or
// the ACP stdio pair — so a daemon restart never costs a live session.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "flt-session-host:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("flt-session-host", flag.ContinueOnError)

	var (
		sessionID  = fs.String("session", "", "session id this host holds")
		command    = fs.String("command", "", "agent command to run under the PTY")
		dir        = fs.String("dir", "", "working directory for the agent")
		cols       = fs.Int("cols", 80, "initial PTY columns")
		rows       = fs.Int("rows", 24, "initial PTY rows")
		channel    = fs.String("channel", version.Channel, "release channel this host serves")
		showVer    = fs.Bool("version", false, "print the build version and exit")
		flagOutput = fs.Output()
	)

	fs.Usage = func() {
		_, _ = fmt.Fprintf(flagOutput, "usage: flt-session-host --session <id> --command <agent> [flags] -- [agent args]\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}

	if *showVer {
		_, _ = fmt.Fprintln(flagOutput, version.String())
		return nil
	}
	if *sessionID == "" {
		fs.Usage()
		return errors.New("--session is required")
	}
	if *command == "" {
		fs.Usage()
		return errors.New("--command is required")
	}

	paths, err := home.Resolve(*channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	token, err := ipc.ReadTokenFile(paths.Token)
	if err != nil {
		return fmt.Errorf("read the connection token: %w", err)
	}

	logger, closeLog, err := newLogger(paths.Logs, "flt-session-host")
	if err != nil {
		return err
	}
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h, err := host.Start(host.Config{
		ID:       session.ID(*sessionID),
		Token:    token,
		StateDir: paths.Sessions,
		Command:  *command,
		Args:     fs.Args(),
		Dir:      *dir,
		Cols:     *cols,
		Rows:     *rows,
	}, session.NewRegistry(paths.Sessions), logger)
	if err != nil {
		return fmt.Errorf("start the session host: %w", err)
	}
	defer func() { _ = h.Close() }()

	if err := h.Serve(ctx); err != nil {
		return fmt.Errorf("serve session %q: %w", *sessionID, err)
	}
	return nil
}

func newLogger(dir, name string) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create log directory %q: %w", dir, err)
	}

	f, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s log: %w", name, err)
	}

	return slog.New(slog.NewTextHandler(f, nil)), func() { _ = f.Close() }, nil
}

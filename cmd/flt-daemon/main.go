// Command flt-daemon is the control plane: it routes sessions, owns worktrees
// and the state store, and reconnects to session hosts after a restart.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "flt-daemon:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("flt-daemon", flag.ContinueOnError)
	channel := fs.String("channel", version.Channel, "release channel this daemon serves: stable or preview")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}

	paths, err := home.Resolve(*channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	logger, closeLog, err := newLogger(paths.Logs)
	if err != nil {
		return err
	}
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("daemon started", "version", version.String(), "channel", *channel, "root", paths.Root)

	<-ctx.Done()
	logger.Info("daemon stopped")
	return nil
}

func newLogger(dir string) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create log directory %q: %w", dir, err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "flt-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open daemon log: %w", err)
	}

	return slog.New(slog.NewTextHandler(f, nil)), func() { _ = f.Close() }, nil
}

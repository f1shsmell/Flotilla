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

	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/home"
	"github.com/f1shsmell/flotilla/internal/ipc"
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

	var (
		channel = fs.String("channel", version.Channel, "release channel this daemon serves: stable or preview")
		hostBin = fs.String("host-binary", "", "flt-session-host executable to spawn for new sessions")
		showVer = fs.Bool("version", false, "print the build version and exit")
		logOut  = fs.Output()
	)

	fs.Usage = func() {
		_, _ = fmt.Fprintf(logOut, "usage: flt-daemon [flags]\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if *showVer {
		_, _ = fmt.Fprintln(logOut, version.String())
		return nil
	}

	paths, err := home.Resolve(*channel)
	if err != nil {
		return fmt.Errorf("resolve state paths: %w", err)
	}

	logger, closeLog, err := newLogger(paths.Logs, "flt-daemon")
	if err != nil {
		return err
	}
	defer closeLog()

	token, err := ensureToken(paths.Token)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hostBinary := *hostBin
	if hostBinary == "" {
		hostBinary = defaultHostBinary()
	}

	d := daemon.New(daemon.Config{
		Channel:    *channel,
		Token:      token,
		StateDir:   paths.Sessions,
		ConfigDir:  paths.Config,
		HostBinary: hostBinary,
		Log:        logger,
	}, daemon.ExecSpawner(logger))

	logger.Info("daemon starting", "version", version.String(), "channel", *channel, "root", paths.Root)

	if err := d.Run(ctx); err != nil {
		return fmt.Errorf("run the daemon: %w", err)
	}
	logger.Info("daemon stopped")
	return nil
}

// ensureToken returns the channel's token, creating it on first start. Every
// process on the channel reads the same file, so the daemon is the only writer.
func ensureToken(path string) (string, error) {
	if token, err := ipc.ReadTokenFile(path); err == nil {
		return token, nil
	}

	token, err := ipc.NewToken()
	if err != nil {
		return "", fmt.Errorf("generate a connection token: %w", err)
	}
	if err := ipc.WriteTokenFile(path, token); err != nil {
		return "", fmt.Errorf("write the connection token: %w", err)
	}
	return token, nil
}

// defaultHostBinary looks for the host next to this executable, which is how a
// release ships them.
func defaultHostBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "flt-session-host"
	}
	candidate := filepath.Join(filepath.Dir(exe), "flt-session-host")
	if _, err := os.Stat(candidate + ".exe"); err == nil {
		return candidate + ".exe"
	}
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return "flt-session-host"
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

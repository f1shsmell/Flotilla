package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// bins holds the three built executables for one test run.
type bins struct {
	daemon string
	host   string
	cli    string
}

// repoRoot walks up from the package directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// buildBinaries compiles the real daemon, session host and CLI into the test's
// own directory, so the end-to-end check exercises the shipped processes rather
// than the library, and leaves nothing behind.
func buildBinaries(t *testing.T) bins {
	t.Helper()

	root := repoRoot(t)
	out := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatalf("create the binary directory: %v", err)
	}

	entries := []struct {
		name string
		pkg  string
	}{
		{"flt-daemon", "./cmd/flt-daemon"},
		{"flt-session-host", "./cmd/flt-session-host"},
		{"flt", "./cmd/flt"},
	}

	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		target := filepath.Join(out, e.name)
		if filepath.Separator == '\\' {
			target += ".exe"
		}

		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, e.pkg)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v: %s", e.name, err, output)
		}
		paths = append(paths, target)
	}

	return bins{daemon: paths[0], host: paths[1], cli: paths[2]}
}

// daemonProcess is one running flt-daemon, killed the hard way on restart.
type daemonProcess struct {
	cmd  *exec.Cmd
	logf string
	file *os.File
}

// startDaemon launches the real daemon with FLT_HOME pointing at home.
func startDaemon(t *testing.T, bin, home, channel string) *daemonProcess {
	t.Helper()

	logPath := filepath.Join(home, "daemon-stderr.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open the daemon log: %v", err)
	}

	cmd := exec.CommandContext(context.Background(), bin, "--channel", channel, "--host-binary", builtHostBinary(t))
	cmd.Env = append(os.Environ(),
		"FLT_HOME="+home,
		helperEnv+"=1",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetached(cmd)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start the daemon: %v", err)
	}

	return &daemonProcess{cmd: cmd, logf: logPath, file: logFile}
}

// kill terminates the daemon the way a crash would: no shutdown, no goodbye.
func (d *daemonProcess) kill(t *testing.T) string {
	t.Helper()

	if err := d.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the daemon: %v", err)
	}
	_ = d.cmd.Wait()
	if d.file != nil {
		_ = d.file.Close()
		d.file = nil
	}

	log, err := os.ReadFile(d.logf)
	if err != nil {
		return ""
	}
	return string(log)
}

// hostBinary is where the test run put flt-session-host. It is a package-level
// so a daemon started by a helper can find it without threading it through.
var hostBinary string

func builtHostBinary(t *testing.T) string {
	t.Helper()

	if hostBinary == "" {
		t.Fatal("hostBinary is unset: buildBinaries must run first")
	}
	return hostBinary
}

// connect retries until the daemon is listening and has written its token. The
// daemon publishes no record, so the retry is the wait: it is bounded by ctx,
// never by a sleep long enough to "usually" work.
func connect(ctx context.Context, channel, home string) (*Client, error) {
	stateDir := filepath.Join(home, "sessions")

	var lastErr error
	for {
		token, err := readToken(home)
		if err == nil {
			client, dialErr := Connect(ctx, channel, token, stateDir)
			if dialErr == nil {
				return client, nil
			}
			lastErr = dialErr
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the daemon never became reachable: %w", lastErr)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func readToken(home string) (string, error) {
	payload, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		return "", fmt.Errorf("read the daemon token: %w", err)
	}
	return string(trimSpaceBytes(payload)), nil
}

func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
}

// killByPID ends a process the test did not start, which is what a session host
// is from this side.
func killByPID(t *testing.T, pid int) {
	t.Helper()

	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}

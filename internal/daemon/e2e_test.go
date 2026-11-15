package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/session"
)

// TestTenDaemonProcessDeathsKeepOneSession is the W1-2 acceptance check at
// process level: the daemon is really killed ten times, the session host is a
// separate real process, and the agent keeps its PTY throughout.
//
// The agent is this test binary running as a helper, so the check needs no
// vendor CLI and no credentials.
func TestTenDaemonProcessDeathsKeepOneSession(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs three real processes")
	}

	bin := buildBinaries(t)
	hostBinary = bin.host

	home := t.TempDir()
	const channel = "e2e"
	const sessionID = "sess-e2e"

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}

	// The host the daemon spawns must run the fake agent. The daemon passes the
	// child's environment through, so exporting it here is enough.
	t.Setenv(helperEnv, "1")

	hostPID := 0

	for attempt := 0; attempt < restarts; attempt++ {
		d := startDaemon(t, bin.daemon, home, channel)

		ctx, cancel := context.WithTimeout(context.Background(), waitFor)

		client, err := connect(ctx, channel, home)
		if err != nil {
			cancel()
			logs := d.kill(t)
			t.Fatalf("attempt %d: connect: %v\n%s", attempt+1, err, logs)
		}

		if attempt == 0 {
			resp, err := client.StartSession(ctx, StartRequest{
				ID:      sessionID,
				Command: exe,
				Args:    []string{"-test.run=TestHelperEchoAgent", "-test.v=false"},
			})
			if err != nil {
				cancel()
				client.Close()
				logs := d.kill(t)
				t.Fatalf("start the session: %v\n%s", err, logs)
			}
			hostPID = resp.HostPID
		}

		st, err := client.Status(ctx)
		if err != nil {
			cancel()
			client.Close()
			logs := d.kill(t)
			t.Fatalf("attempt %d: status: %v\n%s", attempt+1, err, logs)
		}

		if len(st.Sessions) != 1 {
			cancel()
			client.Close()
			logs := d.kill(t)
			t.Fatalf("attempt %d: the daemon serves %d sessions, want 1 (failures: %v)\n%s",
				attempt+1, len(st.Sessions), st.Failures, logs)
		}

		got := st.Sessions[0]
		if got.ID != sessionID {
			cancel()
			client.Close()
			d.kill(t)
			t.Fatalf("attempt %d: the daemon attached to %q", attempt+1, got.ID)
		}
		if got.HostPID != hostPID {
			cancel()
			client.Close()
			logs := d.kill(t)
			t.Fatalf("attempt %d: the host pid changed from %d to %d: the session was restarted, not reattached\n%s",
				attempt+1, hostPID, got.HostPID, logs)
		}

		client.Close()
		cancel()

		// The kill is the point: no shutdown, no flush, no goodbye.
		d.kill(t)
	}

	// One last daemon, to prove the session still answers after the tenth death.
	d := startDaemon(t, bin.daemon, home, channel)
	defer d.kill(t)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	client, err := connect(ctx, channel, home)
	if err != nil {
		t.Fatalf("connect after the last restart: %v", err)
	}
	defer client.Close()

	st, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("status after the last restart: %v", err)
	}
	if len(st.Sessions) != 1 || st.Sessions[0].HostPID != hostPID {
		t.Fatalf("after %d restarts the fleet is %+v, want the same host pid %d", restarts, st.Sessions, hostPID)
	}

	agentSaid, err := talkToSession(ctx, home, sessionID, "still here")
	if err != nil {
		t.Fatalf("the agent did not answer after %d restarts: %v (output: %q)", restarts, err, agentSaid)
	}
	if !strings.Contains(agentSaid, "echo: still here") {
		t.Fatalf("the agent's answer was %q, want the echoed line", agentSaid)
	}

	killByPID(t, hostPID)
}

// talkToSession dials the session host directly, the way the GUI will, and asks
// the agent to echo a line.
func talkToSession(ctx context.Context, home, id, line string) (string, error) {
	rec, err := session.NewRegistry(filepath.Join(home, "sessions")).Read(session.ID(id))
	if err != nil {
		return "", err
	}

	token, err := readToken(home)
	if err != nil {
		return "", err
	}

	return echoOnce(ctx, rec, filepath.Join(home, "sessions"), token, line)
}

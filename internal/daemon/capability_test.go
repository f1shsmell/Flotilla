package daemon

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestStatusTrimsDiagnosticsForAnOlderPeer pins the first place the capability
// set actually withholds something. A peer that did not name diagnostics must
// still get its fleet: refusing it would be the version-equality break the
// capability gate exists to replace. What it loses is the failure list, which
// nothing branches on — a quieter report, not a wrong one.
func TestStatusTrimsDiagnosticsForAnOlderPeer(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-live", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-live"); err != nil {
		t.Fatalf("the live host never published a record: %v", err)
	}

	// A record whose process stands behind it but whose endpoint answers
	// nobody: the daemon has one real failure to report, so a trimmed answer is
	// distinguishable from an empty fleet.
	reg := session.NewRegistry(dir)
	if err := reg.Publish(session.Record{
		ID:       "sess-gone",
		Endpoint: ipc.Address{Channel: "sess-gone", Name: "host"},
		PID:      os.Getpid(),
		Started:  time.Now(),
	}); err != nil {
		t.Fatalf("publish an unreachable record: %v", err)
	}

	// A short attach timeout keeps the unreachable host from costing the
	// default five seconds; this test is about the reply, not the scan.
	runDaemonWith(t, Config{
		Channel:       "test",
		Token:         token,
		StateDir:      dir,
		AttachTimeout: 500 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	current := dialDaemonWith(t, dir, token, "gui-current", nil, nil, nil)
	// An empty, non-nil set is an old build: it named no capabilities at all.
	// Nil would mean "this build's own set", which is the opposite case.
	older := dialDaemonWith(t, dir, token, "gui-old", []ipc.Capability{}, nil, nil)

	currentStatus := callStatus(t, ctx, current)
	olderStatus := callStatus(t, ctx, older)

	if len(currentStatus.Failures) == 0 {
		t.Fatal("the peer that named diagnostics got no failure list, so this test cannot tell a trim from a fleet with nothing to report")
	}
	if len(olderStatus.Failures) != 0 {
		t.Fatalf("a peer that named no diagnostics received %d failures, want them withheld", len(olderStatus.Failures))
	}

	// The rest of the report is what both peers asked for, and neither may lose
	// it: the trim is the diagnostic tail, not the fleet.
	for _, st := range []Status{currentStatus, olderStatus} {
		if !st.Ready {
			t.Fatal("status reports not ready")
		}
		if len(st.Sessions) != 1 || st.Sessions[0].ID != "sess-live" {
			t.Fatalf("status lists %+v, want the one live session in both reports", st.Sessions)
		}
	}
}

// callStatus asks the daemon for its fleet over a real connection.
func callStatus(t *testing.T, ctx context.Context, conn *ipc.Conn) Status {
	t.Helper()

	raw, err := conn.Call(ctx, MethodStatus, struct{}{})
	if err != nil {
		t.Fatalf("daemon.status: %v", err)
	}

	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode daemon.status: %v", err)
	}
	return st
}

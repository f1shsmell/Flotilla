package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/session"
)

// fetchStatus asks the daemon what it serves.
func fetchStatus(t *testing.T, ctx context.Context, client interface {
	Call(context.Context, string, any) (json.RawMessage, error)
},
) Status {
	t.Helper()

	raw, err := client.Call(ctx, MethodStatus, struct{}{})
	if err != nil {
		t.Fatalf("ask for status: %v", err)
	}

	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return st
}

// waitForNoSessions waits until the daemon stops listing a session. Status is
// polled rather than asserted once because a host's death reaches the daemon on
// its own goroutine.
func waitForNoSessions(t *testing.T, ctx context.Context, client interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}, id session.ID,
) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	var last []StatusEntry
	for time.Now().Before(deadline) {
		last = fetchStatus(t, ctx, client).Sessions
		gone := true
		for _, entry := range last {
			if entry.ID == id {
				gone = false
			}
		}
		if gone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %q is still listed after its host stopped: %+v", id, last)
}

// TestStopEndsTheSessionAndItsRow pins both halves of session.stop: the host is
// gone, and the fleet stops listing it. A status that keeps a stopped session on
// screen is what makes the list untrustworthy.
func TestStopEndsTheSessionAndItsRow(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-stop", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-stop"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if len(fetchStatus(t, ctx, client).Sessions) != 1 {
		t.Fatal("the daemon does not list the session it is serving")
	}

	if _, err := client.Call(ctx, MethodSessionStop, session.StopRequest{ID: "sess-stop"}); err != nil {
		t.Fatalf("stop the session: %v", err)
	}

	waitForNoSessions(t, ctx, client, "sess-stop")

	// The record is what a later daemon scan would attach to; leaving it behind
	// would breathe the session back to life on the next restart.
	if _, err := session.NewRegistry(dir).Read("sess-stop"); !errors.Is(err, session.ErrNoRecord) {
		t.Fatalf("the stopped session still has a record: %v", err)
	}
}

// TestAHostThatStopsLeavesTheRow pins the other way a session goes: the host
// ends by itself, and the daemon must stop reporting it rather than keep an
// unreachable row forever.
//
// The host runs in this test process, so its pid stays alive and its record is
// removed by Close. That is exactly the real shutdown order (the record goes as
// the host stops), and it is what makes the row disappear without the daemon
// having to trust a pid.
func TestAHostThatStopsLeavesTheRow(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	h := newHost(t, dir, "sess-crash", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-crash"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if len(fetchStatus(t, ctx, client).Sessions) != 1 {
		t.Fatal("the daemon does not list the session it is serving")
	}

	if err := h.Close(); err != nil {
		t.Fatalf("stop the host: %v", err)
	}

	waitForNoSessions(t, ctx, client, "sess-crash")

	// The record is what a later scan would attach to; a stopped host must not
	// leave one behind, or the next daemon start would resurrect the row.
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if _, err := session.NewRegistry(dir).Read("sess-crash"); errors.Is(err, session.ErrNoRecord) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the stopped host's record survived, so a daemon restart would relist it")
}

// TestStoppingASessionThatIsGoneIsNotAnError pins the idempotence a window needs:
// a stop that races a host's own death must not report a failure to the user.
func TestStoppingASessionThatIsGoneIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, MethodSessionStop, session.StopRequest{ID: "sess-never"}); err != nil {
		t.Fatalf("stopping an unknown session reported an error: %v", err)
	}
}

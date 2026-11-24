package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
)

// reclaimConfig is a daemon that reclaims whatever is quiet. The threshold is
// one nanosecond and the host reads milliseconds, so the quiet period it asks
// for is zero: the tests are about the decision, never the clock.
func reclaimConfig(dir, token string) Config {
	return Config{
		Channel:     "test",
		Token:       token,
		StateDir:    dir,
		ReclaimIdle: time.Nanosecond,
	}
}

// TestReclaimEndsAQuietSession is the working case: a session nobody watches,
// with nothing running and nothing queued, is closed by the sweep.
func TestReclaimEndsAQuietSession(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-idle", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-idle"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if len(fetchStatus(t, ctx, client).Sessions) != 1 {
		t.Fatal("the daemon does not list the session it is serving")
	}

	d.reclaimOnce(ctx)

	waitForNoSessions(t, ctx, client, "sess-idle")
	waitForNoRecord(t, session.NewRegistry(dir), "sess-idle")
}

// TestReclaimDisabledIsTheDefault pins the zero value: a daemon that was never
// told a threshold does not close anything.
func TestReclaimDisabledIsTheDefault(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-default", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-default"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, Config{Channel: "test", Token: token, StateDir: dir})

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	d.reclaimOnce(ctx)
	if sessions := fetchStatus(t, ctx, client).Sessions; len(sessions) != 1 {
		t.Fatalf("a daemon with reclamation off closed a session: %+v", sessions)
	}
}

// TestTheSweepRunsOnItsOwn pins the wiring: a daemon with a short sweep period
// finds an idle session without anybody calling reclaimOnce.
func TestTheSweepRunsOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-swept", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-swept"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	cfg := reclaimConfig(dir, token)
	cfg.ReclaimSweep = time.Millisecond
	runDaemonWith(t, cfg)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	waitForNoSessions(t, ctx, client, "sess-swept")
	waitForNoRecord(t, session.NewRegistry(dir), "sess-swept")
}

// TestProtectedSessionsAreNeverReclaimed is the acceptance rule: a pinned
// session survives every sweep until the pin is lifted.
func TestProtectedSessionsAreNeverReclaimed(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-keep", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-keep"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodProtect, session.ProtectRequest{
		ID:        "sess-keep",
		Protected: true,
	}); err != nil {
		t.Fatalf("protect: %v", err)
	}

	st := fetchStatus(t, ctx, client)
	if len(st.Sessions) != 1 || !st.Sessions[0].Protected {
		t.Fatalf("status = %+v, want the session listed as protected", st.Sessions)
	}

	d.reclaimOnce(ctx)
	if sessions := fetchStatus(t, ctx, client).Sessions; len(sessions) != 1 {
		t.Fatalf("a protected session was reclaimed: %+v", sessions)
	}

	if _, err := client.Call(ctx, session.MethodProtect, session.ProtectRequest{
		ID:        "sess-keep",
		Protected: false,
	}); err != nil {
		t.Fatalf("unprotect: %v", err)
	}

	d.reclaimOnce(ctx)
	waitForNoSessions(t, ctx, client, "sess-keep")
}

// TestAPinDuringTheSweepIsRefusedRatherThanIgnored pins what the sweep offers a
// writer that arrives mid-question: the host is already being asked, so the pin
// is refused instead of recorded and silently overruled by the answer.
func TestAPinDuringTheSweepIsRefusedRatherThanIgnored(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-late-pin", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-late-pin"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, ok := d.beginReclaim("sess-late-pin"); !ok {
		t.Fatal("the sweep could not ask about a quiet session")
	}

	_, err := client.Call(ctx, session.MethodProtect, session.ProtectRequest{
		ID:        "sess-late-pin",
		Protected: true,
	})
	if err == nil {
		t.Fatal("a pin was accepted on a session the sweep was already closing")
	}
	if got := ipc.CodeOf(err); got != ipc.CodeUnknownSession {
		t.Fatalf("protect error code = %d, want %d", got, ipc.CodeUnknownSession)
	}
}

// TestAWindowCannotAttachToASessionTheSweepIsClosing pins the same ordering for
// a window: attaching to a session whose host is being asked would show the
// user a session that disappears under them.
func TestAWindowCannotAttachToASessionTheSweepIsClosing(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-late-attach", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-late-attach"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, ok := d.beginReclaim("sess-late-attach"); !ok {
		t.Fatal("the sweep could not ask about a quiet session")
	}

	_, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-late-attach"})
	if err == nil {
		t.Fatal("a window attached to a session the sweep was already closing")
	}
	if got := ipc.CodeOf(err); got != ipc.CodeUnknownSession {
		t.Fatalf("attach error code = %d, want %d", got, ipc.CodeUnknownSession)
	}
}

// TestReclaimLeavesAQueuedMessageForItsAgent pins the queue's claim: a message
// waiting to be delivered is work the session still owes.
func TestReclaimLeavesAQueuedMessageForItsAgent(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-queued", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-queued"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := d.relay.enqueue("sess-queued", "later"); err != nil {
		t.Fatalf("queue a message: %v", err)
	}

	d.reclaimOnce(ctx)
	if sessions := fetchStatus(t, ctx, client).Sessions; len(sessions) != 1 {
		t.Fatalf("a session with a queued message was reclaimed: %+v", sessions)
	}

	if _, err := d.relay.removeQueued("sess-queued", "q1"); err != nil {
		t.Fatalf("remove the queued message: %v", err)
	}

	d.reclaimOnce(ctx)
	waitForNoSessions(t, ctx, client, "sess-queued")
}

// TestReclaimWaitsForAnUnreadFinishedTurn pins the inbox's claim: a finished
// turn nobody has looked at is waiting on a person, not idle.
func TestReclaimWaitsForAnUnreadFinishedTurn(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-unread", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-unread"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	// A terminal host cannot report done; the sweep reads the state the fleet
	// holds, which is where a host's notification would have put it.
	d.mu.Lock()
	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == "sess-unread" {
			d.fleet.Attachments[i].Info.State = session.StateDone
		}
	}
	d.mu.Unlock()

	d.relay.track("gui-test")

	d.reclaimOnce(ctx)
	if sessions := fetchStatus(t, ctx, client).Sessions; len(sessions) != 1 {
		t.Fatalf("an unread finished turn was reclaimed: %+v", sessions)
	}

	d.relay.markSeen("sess-unread", "gui-test")

	d.reclaimOnce(ctx)
	waitForNoSessions(t, ctx, client, "sess-unread")
}

// TestReclaimLeavesAWatchedSessionAlone pins the window's claim: a session
// somebody has open is not a session to close under them.
func TestReclaimLeavesAWatchedSessionAlone(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-watched", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-watched"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	watcher := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := watcher.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-watched"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	d.reclaimOnce(ctx)

	control := dialDaemon(t, dir, token, nil, nil)
	if sessions := fetchStatus(t, ctx, control).Sessions; len(sessions) != 1 {
		t.Fatalf("a watched session was reclaimed: %+v", sessions)
	}
}

// TestProtectionLoadsOnRestart pins durability at the layer that reads it: a new
// daemon over the same state directory starts with the pins a previous one left.
func TestProtectionLoadsOnRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	state, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open the state: %v", err)
	}
	if err := state.SetProtected(ctx, "sess-pin", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close the state: %v", err)
	}

	d := New(Config{Channel: "test", Token: "shared", StateDir: dir, Log: discardLogger()}, nil)
	t.Cleanup(d.Detach)

	d.mu.Lock()
	pinned := d.protected["sess-pin"]
	d.mu.Unlock()
	if !pinned {
		t.Fatal("a pin recorded before the restart was not loaded")
	}
}

// TestProtectionDoesNotOutliveItsSession pins the other half: a pin names one
// live session, and a later session reusing the id must not inherit it.
func TestProtectionDoesNotOutliveItsSession(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-ended", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-ended"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemonWith(t, reclaimConfig(dir, token))

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodProtect, session.ProtectRequest{
		ID:        "sess-ended",
		Protected: true,
	}); err != nil {
		t.Fatalf("protect: %v", err)
	}
	if _, err := client.Call(ctx, MethodSessionStop, session.StopRequest{ID: "sess-ended"}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	pins, err := d.state.ProtectedIDs(ctx)
	if err != nil {
		t.Fatalf("read the pins: %v", err)
	}
	if len(pins) != 0 {
		t.Fatalf("pins after the session ended = %v, want none", pins)
	}
}

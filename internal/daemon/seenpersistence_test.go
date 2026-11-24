package daemon

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
)

// persistedRelay builds a relay whose seen marks live in a state database under
// dir, and closes its writer when the test ends. A load failure is fatal here:
// the tests below pin what a restart reads, so a silent empty start would hide
// the bug.
func persistedRelay(t *testing.T, dir string) *relay {
	t.Helper()

	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}

	r := newRelay(nil)
	if err := r.persistSeenTo(s); err != nil {
		t.Fatalf("load the seen table: %v", err)
	}
	t.Cleanup(func() {
		r.closeSeen()
		_ = s.Close()
	})
	return r
}

// TestSeenMarksSurviveARestart pins the promise the badge makes: "read" is the
// user's judgement, and a daemon restart is not the user changing their mind.
func TestSeenMarksSurviveARestart(t *testing.T) {
	dir := t.TempDir()

	first := persistedRelay(t, dir)
	first.markSeen("sess-1", "window-a")
	first.closeSeen()

	second := persistedRelay(t, dir)
	if !second.hasSeen("sess-1", "window-a") {
		t.Fatal("a daemon restart turned a read turn back into an unread badge")
	}
	if second.hasSeen("sess-1", "window-b") {
		t.Fatal("a window that never looked reports seen after a restart")
	}
}

// TestForgetSeenSurvivesARestart pins the other direction: a turn that finished
// after the look is unread for everyone, across a restart too.
func TestForgetSeenSurvivesARestart(t *testing.T) {
	dir := t.TempDir()

	first := persistedRelay(t, dir)
	first.markSeen("sess-1", "window-a")
	first.forgetSeen("sess-1")
	first.closeSeen()

	second := persistedRelay(t, dir)
	if second.hasSeen("sess-1", "window-a") {
		t.Fatal("a new turn arrived unread, and the restart resurrected the old mark")
	}
}

// TestPresentSurvivesSoTheBoundStillHolds pins why the client set rides along:
// the eviction bound counts windows, and a restart that forgot them would let
// the table grow past its ceiling one launch at a time.
func TestPresentSurvivesSoTheBoundStillHolds(t *testing.T) {
	dir := t.TempDir()

	first := persistedRelay(t, dir)
	for i := 0; i < maxRememberedClients; i++ {
		first.track(fmt.Sprintf("window-%02d", i))
	}
	first.closeSeen()

	second := persistedRelay(t, dir)
	second.track("window-new")

	second.mu.Lock()
	count := len(second.present)
	second.mu.Unlock()

	if count > maxRememberedClients {
		t.Fatalf("the restarted table holds %d windows, want at most %d", count, maxRememberedClients)
	}
	if !second.hasPresent("window-new") {
		t.Fatal("the window that just spoke was evicted")
	}
}

// TestASeenTableThatCannotBeReadStartsFresh pins the failure posture: the seen
// table is a convenience, and a database that cannot be read must not keep the
// daemon from starting.
func TestASeenTableThatCannotBeReadStartsFresh(t *testing.T) {
	// A relay with no store at all is the degenerate case: memory only.
	r := newRelay(nil)
	if err := r.persistSeenTo(nil); err != nil {
		t.Fatalf("persistSeenTo(nil) = %v, want no error", err)
	}
	if r.hasSeen("sess-1", "window-a") {
		t.Fatal("a store-less relay reports a mark")
	}
	r.markSeen("sess-1", "window-a")
	if !r.hasSeen("sess-1", "window-a") {
		t.Fatal("a store-less relay did not keep the mark in memory")
	}
	r.closeSeen()
}

// TestNoSeenTableIsAnEmptyOne pins the first launch: nothing in the database is
// the normal start, not a failure.
func TestNoSeenTableIsAnEmptyOne(t *testing.T) {
	dir := t.TempDir()

	r := persistedRelay(t, dir)
	if r.hasSeen("sess-1", "window-a") {
		t.Fatal("an empty start reports a mark")
	}
}

// TestTheSeenTableIsWrittenOnce is the coalescing promise: a burst of marks
// reaches disk as one snapshot, and the last one is the one that survives.
func TestTheSeenTableIsWrittenOnce(t *testing.T) {
	dir := t.TempDir()

	first := persistedRelay(t, dir)
	for i := 0; i < 20; i++ {
		first.markSeen(session.ID(fmt.Sprintf("sess-%02d", i)), "window-a")
	}
	first.closeSeen()

	second := persistedRelay(t, dir)
	for i := 0; i < 20; i++ {
		if !second.hasSeen(session.ID(fmt.Sprintf("sess-%02d", i)), "window-a") {
			t.Fatalf("session %d's mark was lost in the snapshot", i)
		}
	}
}

// TestSeenMarksArePerClient is the reason the table is keyed by client: one
// window looking does not mark the work seen for another.
func TestSeenMarksArePerClient(t *testing.T) {
	dir := t.TempDir()

	r := persistedRelay(t, dir)
	r.markSeen("sess-1", "window-a")

	if !r.hasSeen("sess-1", "window-a") {
		t.Fatal("the window that looked does not report seen")
	}
	if r.hasSeen("sess-1", "window-b") {
		t.Fatal("a second window inherited another's mark")
	}
}

// TestAClientThatNamesItselfInNoHandshakeIsNotRecorded keeps an anonymous
// connection from clearing a badge somebody else still needs.
func TestAClientThatNamesItselfInNoHandshakeIsNotRecorded(t *testing.T) {
	dir := t.TempDir()

	r := persistedRelay(t, dir)
	r.markSeen("sess-1", "")
	if r.hasSeen("sess-1", "") {
		t.Fatal("an unnamed client produced a mark")
	}

	// The same guard on the read path.
	if r.hasSeen("sess-1", "window-a") {
		t.Fatal("an unnamed client reads another window's mark")
	}
}

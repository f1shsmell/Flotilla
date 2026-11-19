package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// persistedRelay builds a relay whose seen marks live at path, and closes its
// writer when the test ends. A load failure is fatal here: the tests below
// pin what a restart reads, so a silent empty start would hide the bug.
func persistedRelay(t *testing.T, path string) *relay {
	t.Helper()

	r := newRelay(nil)
	if err := r.persistSeenTo(path); err != nil {
		t.Fatalf("load the seen table: %v", err)
	}
	t.Cleanup(r.closeSeen)
	return r
}

// TestSeenMarksSurviveARestart pins the promise the badge makes: "read" is the
// user's judgement, and a daemon restart is not the user changing their mind.
func TestSeenMarksSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")

	first := persistedRelay(t, path)
	first.markSeen("sess-1", "window-a")
	first.closeSeen()

	second := persistedRelay(t, path)
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
	path := filepath.Join(t.TempDir(), "seen.json")

	first := persistedRelay(t, path)
	first.markSeen("sess-1", "window-a")
	first.forgetSeen("sess-1")
	first.closeSeen()

	second := persistedRelay(t, path)
	if second.hasSeen("sess-1", "window-a") {
		t.Fatal("a new turn arrived unread, and the restart resurrected the old mark")
	}
}

// TestPresentSurvivesSoTheBoundStillHolds pins why present rides along: the
// eviction bound counts windows, and a restart that forgot them would let the
// table grow past its ceiling one launch at a time.
func TestPresentSurvivesSoTheBoundStillHolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")

	first := persistedRelay(t, path)
	for i := 0; i < maxRememberedClients; i++ {
		first.track(fmt.Sprintf("window-%02d", i))
	}
	first.closeSeen()

	second := persistedRelay(t, path)
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

// TestACorruptSeenTableStartsFresh pins the failure posture: the seen table is
// a convenience, and a file a crash left half-written must not keep the daemon
// from starting — nor poison the writes that follow.
func TestACorruptSeenTableStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")
	if err := os.WriteFile(path, []byte("{half-written"), 0o600); err != nil {
		t.Fatalf("plant the corrupt file: %v", err)
	}

	first := persistedRelay(t, path)
	if first.hasSeen("sess-1", "window-a") {
		t.Fatal("a corrupt file produced a mark")
	}
	first.markSeen("sess-1", "window-a")
	first.closeSeen()

	second := persistedRelay(t, path)
	if !second.hasSeen("sess-1", "window-a") {
		t.Fatal("the repaired file did not carry the mark written after the corrupt load")
	}
}

// TestNoSeenFileIsAnEmptyTable pins the first launch: nothing on disk is the
// normal start, not a failure.
func TestNoSeenFileIsAnEmptyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")

	r := persistedRelay(t, path)
	if r.hasSeen("sess-1", "window-a") {
		t.Fatal("an empty start reports a mark")
	}
}
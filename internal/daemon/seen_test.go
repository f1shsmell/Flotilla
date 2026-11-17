package daemon

import (
	"fmt"
	"testing"
)

// TestASeenMarkSurvivesAnotherConnection pins the semantics a window depends
// on: "read" is the user's judgement, and a later connection from the same
// window is not the user changing their mind. A poll that failed and reconnected
// — or a daemon that restarted — must not turn a turn the user already read back
// into an unread badge.
func TestASeenMarkSurvivesAnotherConnection(t *testing.T) {
	r := newRelay(nil)

	r.track("window-a")
	r.markSeen("sess-1", "window-a")

	// The connection goes; the window's name does not come back, and that is
	// exactly why the mark has to stay.
	r.track("window-a")

	if !r.hasSeen("sess-1", "window-a") {
		t.Fatal("a reconnect dropped the window's seen mark")
	}
}

// TestAnotherWindowsMarksSurviveAnothersArrival pins the boundary: one window's
// presence must not touch another's marks, because the per-client map exists so
// a second window's badge outlives the first window's look.
func TestAnotherWindowsMarksSurviveAnothersArrival(t *testing.T) {
	r := newRelay(nil)

	r.track("window-a")
	r.track("window-b")
	r.markSeen("sess-1", "window-a")

	if r.hasSeen("sess-1", "window-b") {
		t.Fatal("a window that never looked reports seen")
	}
	if !r.hasSeen("sess-1", "window-a") {
		t.Fatal("the window that looked lost its mark")
	}
}

// TestTheSeenTableIsBounded pins the one thing that does drop a mark: the table
// has a ceiling, because a window names itself once per launch and never
// returns. Without it a long-lived daemon would keep one entry per launch.
func TestTheSeenTableIsBounded(t *testing.T) {
	r := newRelay(nil)

	for i := 0; i < maxRememberedClients*2; i++ {
		client := fmt.Sprintf("window-%d", i)
		r.track(client)
		r.markSeen("sess-1", client)
	}

	if len(r.present) > maxRememberedClients {
		t.Fatalf("the daemon remembers %d windows, more than %d", len(r.present), maxRememberedClients)
	}

	// Every remembered client still has its mark: eviction goes with the name it
	// evicted, and never leaves a mark behind for one it forgot.
	for client := range r.present {
		if !r.hasSeen("sess-1", client) {
			t.Fatalf("the remembered window %q lost its mark", client)
		}
	}
	for client := range r.seen["sess-1"] {
		if _, ok := r.present[client]; !ok {
			t.Fatalf("the seen map holds a mark for the forgotten window %q", client)
		}
	}
}

// TestAnEmptyClientNameIsNeverRecorded pins the anonymous case: a connection
// that named no client has nothing to attribute a look to, and guessing would
// clear a badge another window still needs.
func TestAnEmptyClientNameIsNeverRecorded(t *testing.T) {
	r := newRelay(nil)

	r.track("")
	r.markSeen("sess-1", "")

	if len(r.present) != 0 || len(r.seen) != 0 {
		t.Fatalf("an anonymous client was recorded: present=%v seen=%v", r.present, r.seen)
	}
	if r.hasSeen("sess-1", "") {
		t.Fatal("an anonymous client reports seen")
	}
}

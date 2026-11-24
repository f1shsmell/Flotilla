package daemon

import (
	"testing"

	"github.com/f1shsmell/flotilla/internal/session"
)

// subtreeStatus is one fleet with a two-level worker chain and an unrelated
// session, plus the machine-wide lists a scoped view must drop.
func subtreeStatus() Status {
	return Status{
		Channel: "test",
		PID:     42,
		Ready:   true,
		Sessions: []StatusEntry{
			{ID: "root", State: session.StateWorking},
			{ID: "child", Parent: "root", State: session.StateDone},
			{ID: "grandchild", Parent: "child", State: session.StateIdle},
			{ID: "other", State: session.StateBlocked},
		},
		Archived: []session.ArchivedEntry{{ID: "old"}},
		Tasks:    []TaskEntry{{ID: "task-1", Prompt: "someone else's work"}},
		Inbox: []InboxEntry{
			{ID: "child", Kind: InboxUnread},
			{ID: "other", Kind: InboxInput, Title: "not yours"},
		},
		Failures: []string{"host of other is unreachable"},
	}
}

// TestScopedKeepsTheSubtreeAndDropsTheMachine is the scope gate: an agent sees
// itself and the workers it started, through any depth, and nothing else.
func TestScopedKeepsTheSubtreeAndDropsTheMachine(t *testing.T) {
	scoped := Scoped(subtreeStatus(), "root")

	got := make(map[session.ID]bool, len(scoped.Sessions))
	for _, s := range scoped.Sessions {
		got[s.ID] = true
	}
	for _, want := range []session.ID{"root", "child", "grandchild"} {
		if !got[want] {
			t.Errorf("scoped view is missing %q: %+v", want, scoped.Sessions)
		}
	}
	if got["other"] {
		t.Errorf("scoped view shows an unrelated session: %+v", scoped.Sessions)
	}

	if len(scoped.Archived) != 0 || len(scoped.Tasks) != 0 || len(scoped.Failures) != 0 {
		t.Errorf("scoped view kept machine-wide lists: archived=%+v tasks=%+v failures=%v",
			scoped.Archived, scoped.Tasks, scoped.Failures)
	}

	if len(scoped.Inbox) != 1 || scoped.Inbox[0].ID != "child" {
		t.Errorf("scoped inbox = %+v, want only the child's item", scoped.Inbox)
	}
	if scoped.Channel != "test" || scoped.PID != 42 || !scoped.Ready {
		t.Errorf("scoped view lost the daemon's identity: %+v", scoped)
	}
}

// TestScopedDoesNotReachAMissingParent pins the direction of the walk: a subtree
// grows downwards from the root, so a name pointing outside it cannot pull a
// session in.
func TestScopedDoesNotReachAMissingParent(t *testing.T) {
	st := Status{Sessions: []StatusEntry{
		{ID: "root"},
		{ID: "orphan", Parent: "gone"},
		{ID: "stranger", Parent: "other-root"},
	}}

	scoped := Scoped(st, "root")
	if len(scoped.Sessions) != 1 || scoped.Sessions[0].ID != "root" {
		t.Errorf("scoped view = %+v, want only the root", scoped.Sessions)
	}
}

// TestScopedRootThatIsGoneSeesNothing keeps a stale FLT_SESSION_ID from showing
// the machine: a session the daemon does not serve owns no workers.
func TestScopedRootThatIsGoneSeesNothing(t *testing.T) {
	scoped := Scoped(subtreeStatus(), "gone")
	if len(scoped.Sessions) != 0 || len(scoped.Inbox) != 0 {
		t.Errorf("a missing root saw %+v", scoped.Sessions)
	}
}

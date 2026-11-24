package daemon

import "github.com/f1shsmell/flotilla/internal/session"

// Subtree returns root and every session under it, walking the parent names one
// status report carries. A child whose parent is not in the report is not under
// root: a stale name cannot widen the set.
func Subtree(st Status, root session.ID) map[session.ID]bool {
	children := make(map[session.ID][]session.ID, len(st.Sessions))
	for _, s := range st.Sessions {
		if s.Parent != "" {
			children[s.Parent] = append(children[s.Parent], s.ID)
		}
	}

	in := make(map[session.ID]bool, len(st.Sessions))
	queue := []session.ID{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if in[id] {
			continue
		}
		in[id] = true
		queue = append(queue, children[id]...)
	}
	return in
}

// Scoped projects a status report onto one session's part of the fleet: the
// session itself plus every worker it started, directly or through one. The
// queue, the archive and the failures are dropped because they describe the
// whole machine, and an agent living in one session is not its owner.
func Scoped(st Status, root session.ID) Status {
	own := Subtree(st, root)
	scoped := Status{Channel: st.Channel, PID: st.PID, Ready: st.Ready}
	for _, s := range st.Sessions {
		if own[s.ID] {
			scoped.Sessions = append(scoped.Sessions, s)
		}
	}
	for _, item := range st.Inbox {
		if own[item.ID] {
			scoped.Inbox = append(scoped.Inbox, item)
		}
	}
	return scoped
}

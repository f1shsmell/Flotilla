package daemon

import "github.com/f1shsmell/flotilla/internal/session"

// maxRememberedClients bounds how many windows the daemon remembers for their
// seen marks. A desktop has a handful; the bound is what keeps a tab opened
// once a day from accumulating for the life of the daemon.
const maxRememberedClients = 32

// track records that client is present, and keeps the record bounded. A window
// names itself once per launch; its mark has to outlive a dropped connection,
// because a poll that failed and reconnected must not turn a turn the user
// already read back into an unread badge.
func (r *relay) track(client string) {
	if client == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.present[client] = struct{}{}
	r.evictLocked(client)
	r.scheduleSeenWrite()
}

// evictLocked drops windows once the table outgrows its bound, and every seen
// mark they left behind. keep is never a candidate: it is the window that just
// spoke, and evicting it would drop the mark the caller is about to set.
//
// The iteration order of a map is not a claim about age, so the table is a set:
// any window beyond the bound is as good a candidate as another, and a window
// that is still open re-registers itself on its next connection.
func (r *relay) evictLocked(keep string) {
	for len(r.present) > maxRememberedClients {
		evicted := false
		for client := range r.present {
			if client == keep {
				continue
			}
			delete(r.present, client)
			r.forgetClientLocked(client)
			evicted = true
			break
		}
		if !evicted {
			// Every remaining entry is the window being kept, which cannot fill
			// the table on its own; stopping is what keeps this from spinning.
			return
		}
	}
}

// forgetClientLocked removes one client's marks. The per-session map goes when
// its last client does, so an eviction leaves no empty shells behind.
func (r *relay) forgetClientLocked(client string) {
	for id, marked := range r.seen {
		delete(marked, client)
		if len(marked) == 0 {
			delete(r.seen, id)
		}
	}
}

// markSeen records that client has looked at id's finished turn. A client that
// named itself in no handshake is not recorded: there is nothing to attribute
// the look to, and guessing would clear a badge somebody else still needs.
func (r *relay) markSeen(id session.ID, client string) {
	if client == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// A window that looked is a window that is present: recording it here is
	// what keeps an eviction from choosing the caller's own name, which would
	// delete the mark set two lines below.
	r.present[client] = struct{}{}
	if r.seen[id] == nil {
		r.seen[id] = make(map[string]struct{})
	}
	r.seen[id][client] = struct{}{}
	r.evictLocked(client)
	r.scheduleSeenWrite()
}

// hasSeen reports whether client has looked at id's finished turn.
func (r *relay) hasSeen(id session.ID, client string) bool {
	if client == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.seen[id][client]
	return ok
}

// forgetSeen clears every client's seen flag for id. A turn that just finished
// is unread for everyone, however many windows read the turn before it.
func (r *relay) forgetSeen(id session.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.seen, id)
	r.scheduleSeenWrite()
}

// hasPresent reports whether the daemon remembers a window by name.
func (r *relay) hasPresent(client string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.present[client]
	return ok
}

// projectSeen projects the seen flag onto the lifecycle one client should see:
// a finished turn it has already looked at is idle for it, and stays done for
// every other client.
func projectSeen(state session.State, seen bool) session.State {
	if state == session.StateDone && seen {
		return session.StateIdle
	}
	return state
}

package daemon

import (
	"context"
	"encoding/json"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// questionEntry is what one session waits on: the host connection that asked,
// and every ask not answered yet. A host can ask twice for parallel tool calls,
// so a session's inbox entry may stand for more than one card.
type questionEntry struct {
	owner *ipc.Conn
	asks  []session.PermissionQuestion
}

// rememberQuestion records one ask from owner. A notification from a host the
// relay has dropped cannot speak for the run that took the id over.
func (r *relay) rememberQuestion(id session.ID, owner *ipc.Conn, q session.PermissionQuestion) {
	if owner == nil || !owner.Alive() {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.asks[id]
	if entry == nil || entry.owner != owner {
		entry = &questionEntry{owner: owner}
		r.asks[id] = entry
	}
	entry.asks = append(entry.asks, q)
}

// forgetQuestion drops one answered ask; the others stay, because a turn can
// wait on more than one card.
func (r *relay) forgetQuestion(id session.ID, owner *ipc.Conn, qid string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.asks[id]
	if entry == nil || entry.owner != owner {
		return
	}
	kept := entry.asks[:0]
	for _, q := range entry.asks {
		if q.ID != qid {
			kept = append(kept, q)
		}
	}
	entry.asks = kept
	if len(entry.asks) == 0 {
		delete(r.asks, id)
	}
}

// clearQuestion forgets every ask of a session that stopped waiting. A state
// from a replaced host cannot clear the question the new one is waiting on.
func (r *relay) clearQuestion(id session.ID, owner *ipc.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry := r.asks[id]; entry != nil && (owner == nil || entry.owner == owner) {
		delete(r.asks, id)
	}
}

// question reports the newest ask a session waits on, if this daemon saw one.
// A daemon that restarts before the host replays it has none; the status still
// carries blocked, so the entry survives without a title.
func (r *relay) question(id session.ID) (session.PermissionQuestion, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.asks[id]
	if entry == nil || len(entry.asks) == 0 {
		return session.PermissionQuestion{}, false
	}
	return entry.asks[len(entry.asks)-1], true
}

// pendingQuestions snapshots every card a session waits on, so a client that
// attaches after the ask can be handed them.
func (r *relay) pendingQuestions(id session.ID) []session.PermissionQuestion {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := r.asks[id]
	if entry == nil {
		return nil
	}
	return append([]session.PermissionQuestion(nil), entry.asks...)
}

// hostHooks build the callbacks for one host connection: its notifications go to
// every attached client, and its terminal bytes go to them the same way.
func (d *Daemon) hostHooks(id session.ID) HostHooks {
	return HostHooks{
		Handler: func(_ context.Context, owner *ipc.Conn, method string, params json.RawMessage) (any, error) {
			// A state change is also the daemon's own business: status reports
			// read it, so it is recorded before being fanned out. A daemon that
			// only forwarded it would answer "unknown" for a session the host
			// had already classified.
			if method == session.MethodState {
				var update session.StateUpdate
				if err := json.Unmarshal(params, &update); err == nil {
					d.recordState(id, owner, update.State)
				}
			}

			// The question is kept for the inbox before it is fanned out: the
			// status poll is what lists a blocked session to a window that has
			// not attached yet.
			if method == session.MethodPermission {
				var q session.PermissionQuestion
				if err := json.Unmarshal(params, &q); err == nil {
					d.relay.rememberQuestion(id, owner, q)
				}
			}

			// An agent update is stored before it is forwarded, so a window that
			// attaches later can be told what it missed. What goes out carries
			// the sequence the store gave it, which is how a client discards an
			// update it already has from its attach history. A replay of work the
			// log has already dropped is forwarded to nobody: it would put a line
			// from before the window's whole log at the end of it.
			forwarded := params
			if method == session.MethodUpdate {
				var keep bool
				forwarded, keep = d.recordUpdate(id, params)
				if !keep {
					return struct{}{}, nil
				}
			}

			for _, peer := range d.relay.subscribers(id) {
				err := d.relay.deliver(peer, func() error { return peer.Notify(method, forwarded) })
				if err != nil {
					d.cfg.Log.Debug("forward a session notification failed", "session", id, "err", err.Error())
				}
			}
			return struct{}{}, nil
		},
		OnData: func(_ *ipc.Conn, stream uint32, payload []byte) {
			for _, peer := range d.relay.subscribers(id) {
				// A failed send reaches the client's own connection close, which
				// is the event the daemon acts on; a logged line per failed byte
				// would be a flood of its own.
				_ = d.relay.deliver(peer, func() error { return peer.SendData(stream, payload) })
			}
		},
	}
}

// recordState applies one lifecycle a host reported, with the inbox facts it
// implies: a finished turn is unread again, and a session that stopped waiting
// shows no question. The side effects run only for a host this daemon serves,
// so a replayed attach state cannot resurrect a turn a window already read.
func (d *Daemon) recordState(id session.ID, owner *ipc.Conn, state session.State) {
	if !state.Valid() {
		return
	}

	d.mu.Lock()
	// Registered before the unlock, so it runs after it: the goroutine it starts
	// must find the finished turn under d.mu.
	finished := false
	defer func() {
		if finished {
			// The snapshot is taken before the queue's next turn starts, so a
			// checkpoint names the tree its own turn left behind.
			go func() {
				d.captureCheckpoint(id)
				d.kickQueue(id)
			}()
		}
	}()
	defer d.mu.Unlock()

	found := false
	previous := session.StateUnknown
	for i := range d.fleet.Attachments {
		att := &d.fleet.Attachments[i]
		if att.Record.ID != id {
			continue
		}
		if owner != nil && att.Conn != owner {
			return
		}
		previous = att.Info.State
		att.Info.State = state
		found = true
		break
	}
	if !found {
		return
	}

	// Lock order is d.mu then relay.mu, as forget and fleetStatus already take
	// them, so a poll cannot see the new state with the previous turn's flags.
	// A finish is a transition into done: a host repeating the done it already
	// reported — the replay every attach sends — must not make a read turn
	// unread again, nor release a queue that never saw that turn run.
	finished = state == session.StateDone && previous != session.StateDone
	if finished {
		d.relay.forgetSeen(id)
	}
	if state != session.StateBlocked {
		d.relay.clearQuestion(id, owner)
	}
}

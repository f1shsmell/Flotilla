package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// reclaimSweepDefault is how often the daemon looks when no period is set.
const reclaimSweepDefault = time.Minute

// reclaimTimeout bounds the question to one host. A host that cannot answer
// this quickly is left for the next sweep.
const reclaimTimeout = 5 * time.Second

// reclaimLoop looks for idle families until ctx ends.
func (d *Daemon) reclaimLoop(ctx context.Context) {
	period := d.cfg.ReclaimSweep
	if period <= 0 {
		period = reclaimSweepDefault
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.reclaimOnce(ctx)
		}
	}
}

// reclaimOnce asks every eligible host to close itself. The host decides: it
// owns the family's activity and may veto, and a host that allows takes its own
// process down. Nothing here closes a session a host did not agree to close.
func (d *Daemon) reclaimOnce(ctx context.Context) {
	if d.cfg.ReclaimIdle <= 0 {
		return
	}

	for _, id := range d.fleetIDs() {
		conn, ok := d.beginReclaim(id)
		if !ok {
			continue
		}

		callCtx, cancel := context.WithTimeout(ctx, reclaimTimeout)
		raw, err := conn.Call(callCtx, session.MethodReclaim, session.ReclaimRequest{
			IdleForMs: d.cfg.ReclaimIdle.Milliseconds(),
		})
		cancel()

		if !d.reclaimAnswered(id, conn, raw, err) {
			d.mu.Lock()
			delete(d.reclaiming, id)
			d.mu.Unlock()
		}
	}
}

// reclaimAnswered reads the host's answer and says whether the session is going
// away. A lost pipe is the same yes as an allow: the host closed as it replied.
func (d *Daemon) reclaimAnswered(id session.ID, conn *ipc.Conn, raw json.RawMessage, err error) bool {
	switch {
	case err != nil && !conn.Alive():
		d.cfg.Log.Info("session reclaimed as idle", "session", id)
		return true
	case err != nil:
		d.cfg.Log.Debug("idle reclaim failed", "session", id, "err", err.Error())
		return false
	}

	var resp session.ReclaimResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		d.cfg.Log.Debug("idle reclaim answer is unreadable", "session", id, "err", err.Error())
		return false
	}
	if !resp.Allow {
		d.cfg.Log.Debug("idle reclaim vetoed", "session", id, "reason", resp.Reason)
		return false
	}
	d.cfg.Log.Info("session reclaimed as idle", "session", id)
	return true
}

// fleetIDs lists the sessions this daemon serves, under one lock.
func (d *Daemon) fleetIDs() []session.ID {
	d.mu.Lock()
	defer d.mu.Unlock()

	ids := make([]session.ID, 0, len(d.fleet.Attachments))
	for i := range d.fleet.Attachments {
		ids = append(ids, d.fleet.Attachments[i].Record.ID)
	}
	return ids
}

// beginReclaim decides whether one session may be questioned and marks it
// first. The mark is what a pin, a window or a queued message meets while the
// host is answering: without it they would be accepted into a session whose
// close was already under way, and silently lost.
func (d *Daemon) beginReclaim(id session.ID) (*ipc.Conn, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	att := d.attachmentLocked(id)
	if att == nil || d.reclaiming[id] || d.reclaimSkippedLocked(att) {
		return nil, false
	}
	d.reclaiming[id] = true
	return att.Conn, true
}

// reclaimSkippedLocked reports whether attention or a pin keeps a session
// alive. The caller holds d.mu, so the answer cannot go stale before the
// question is sent: every writer that could change it takes d.mu too.
func (d *Daemon) reclaimSkippedLocked(att *Attachment) bool {
	id := att.Record.ID
	switch {
	case !att.Conn.Alive():
		return true
	case d.protected[id]:
		return true
	case att.Info.State == session.StateWorking || att.Info.State == session.StateBlocked:
		return true
	case len(d.relay.subscribers(id)) > 0:
		return true
	case att.Info.State == session.StateDone && d.relay.unseen(id):
		return true
	case d.relay.delivering(id):
		// The message left the queue and is on its way to the host, so the
		// snapshot below cannot see it.
		return true
	case len(d.relay.queueSnapshot(id)) > 0:
		return true
	}
	return false
}

// unseen reports whether some remembered window has not looked at the session's
// finished turn. That turn is waiting on a person, not idle.
func (r *relay) unseen(id session.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := r.seen[id]
	for client := range r.present {
		if _, ok := seen[client]; !ok {
			return true
		}
	}
	return false
}

// protectSession pins or unpins one session from a control connection.
func (d *Daemon) protectSession(params json.RawMessage) (any, error) {
	var req session.ProtectRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodProtect, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: empty session id: %w", session.MethodProtect, session.ErrNoRecord)
	}
	return struct{}{}, d.setProtected(req.ID, req.Protected)
}

// setProtected records one pin in memory and on disk. A pin names a session the
// daemon serves and is not already closing: pinning either would outlive it and
// catch whatever session took the id next. The memory pin is what the next
// sweep reads, so a failed write still protects a running session; the error
// says it will not survive a restart.
func (d *Daemon) setProtected(id session.ID, on bool) error {
	d.mu.Lock()
	served := false
	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == id {
			served = true
			break
		}
	}
	if on && (!served || d.reclaiming[id]) {
		d.mu.Unlock()
		return fmt.Errorf("%s on session %q: %w", session.MethodProtect, id, ErrUnknownSession)
	}
	if on {
		d.protected[id] = true
	} else {
		delete(d.protected, id)
	}
	d.mu.Unlock()

	d.cfg.Log.Info("session protection changed", "session", id, "protected", on)
	if d.state == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusReadTimeout)
	defer cancel()
	if err := d.state.SetProtected(ctx, string(id), on); err != nil {
		return fmt.Errorf("%s on session %q: %w", session.MethodProtect, id, err)
	}
	return nil
}

// forgetProtected drops a pin when its session ends: a later session reusing
// the id must not inherit it.
func (d *Daemon) forgetProtected(id session.ID) {
	d.mu.Lock()
	_, pinned := d.protected[id]
	retaken := false
	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == id {
			retaken = true
			break
		}
	}
	if retaken {
		// A newer session took the id after this one ended; its pin is not
		// this call's to clear.
		d.mu.Unlock()
		return
	}
	delete(d.protected, id)
	d.mu.Unlock()

	if !pinned || d.state == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusReadTimeout)
	defer cancel()
	if err := d.state.SetProtected(ctx, string(id), false); err != nil {
		d.cfg.Log.Warn("cannot clear the protection of a finished session",
			"session", id, "err", err.Error())
	}
}

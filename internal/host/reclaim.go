package host

import (
	"errors"
	"fmt"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// errReclaimed reports a prompt aimed at a host that has already agreed to
// close as idle. The turn has nowhere left to run.
var errReclaimed = ipc.Class(errors.New("the session is closing as idle"), ipc.CodeUnknownSession)

// reclaim answers whether this host may close as idle. It never closes the
// host itself: the caller's reply has to reach the pipe first.
func (h *Host) reclaim(req session.ReclaimRequest) session.ReclaimResponse {
	h.mu.Lock()
	resp, children := h.reclaimLocked(req)
	h.mu.Unlock()

	if len(children) > 0 {
		h.log.Debug("reclaim vetoed by a working child", "session", h.cfg.ID, "children", children)
	}
	return resp
}

// reclaimLocked decides the answer and, when it allows, marks the host as
// closing under the same lock a turn takes to start: a prompt either ran before
// this decision, or is refused rather than lost with the process.
func (h *Host) reclaimLocked(req session.ReclaimRequest) (session.ReclaimResponse, []string) {
	switch h.stateLocked() {
	case session.StateWorking:
		return session.ReclaimResponse{Reason: session.ReclaimBusy}, nil
	case session.StateBlocked:
		return session.ReclaimResponse{Reason: session.ReclaimWaiting}, nil
	}

	var children []string
	for id, child := range h.family {
		if child == "running" || child == "requires_action" {
			children = append(children, id)
		}
	}
	if len(children) > 0 {
		return session.ReclaimResponse{Reason: session.ReclaimChildWorking}, children
	}
	if quiet := time.Duration(req.IdleForMs) * time.Millisecond; quiet > 0 && time.Since(h.lastActivity) < quiet {
		return session.ReclaimResponse{Reason: session.ReclaimActive}, nil
	}

	h.reclaiming = true
	return session.ReclaimResponse{Allow: true}, nil
}

// beginTurn claims this host for a turn and records the sign of life the
// reclaim decision reads.
func (h *Host) beginTurn() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.reclaiming {
		return fmt.Errorf("session %q: %w", h.cfg.ID, errReclaimed)
	}
	h.touchLocked()
	return nil
}

// touchLocked records a sign of family life. h.mu must be held.
func (h *Host) touchLocked() { h.lastActivity = time.Now() }

// touch records a sign of family life from a caller not holding h.mu.
func (h *Host) touch() {
	h.mu.Lock()
	h.touchLocked()
	h.mu.Unlock()
}

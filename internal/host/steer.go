package host

import (
	"context"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/session"
)

// steerSupported reports whether this host's agent advertised steering. A
// terminal has none: typing into it is the user's own act, not this method.
func (h *Host) steerSupported() bool {
	if h.pty != nil || h.acp == nil {
		return false
	}
	sess := h.acpAgent()
	return sess != nil && sess.SteerSupported()
}

// steer hands one message to the turn that is running. A false answer means the
// turn did not take it, which the caller answers by queueing; a failed call is
// an error, because the message may already be inside the agent.
func (h *Host) steer(ctx context.Context, req session.SteerRequest) (session.SteerResponse, error) {
	if h.pty != nil {
		return session.SteerResponse{}, fmt.Errorf("session %q has no turn to steer: %w", h.cfg.ID, errWrongMode)
	}
	if !h.steerSupported() {
		return session.SteerResponse{}, nil
	}

	h.mu.Lock()
	running := h.stateLocked() == session.StateWorking
	h.mu.Unlock()
	if !running {
		return session.SteerResponse{}, nil
	}

	delivered, err := h.acpAgent().Steer(ctx, req.Text)
	if err != nil {
		return session.SteerResponse{}, fmt.Errorf("session %q: %w", h.cfg.ID, err)
	}
	if !delivered {
		return session.SteerResponse{}, nil
	}
	h.forwardUpdate(acp.Update{Kind: session.KindUser, Text: req.Text})
	return session.SteerResponse{Steered: true}, nil
}

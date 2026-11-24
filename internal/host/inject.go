package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// errAgentBlocked reports a prompt aimed at a session that is waiting for the
// user. Typing into an approval dialog answers a question nobody saw.
var errAgentBlocked = ipc.Class(errors.New("the agent is waiting for the user"), ipc.CodeAgentBlocked)

// promptPTY types one structured prompt into the terminal and submits it. The
// reply says injected, never done: a terminal turn has no observable end.
// Keystrokes are not gated this way — a user typing into a blocked session is
// answering on purpose; only a programmatic injection is refused.
func (h *Host) promptPTY(ctx context.Context, req session.PromptRequest) (session.PromptResponse, error) {
	if !h.injectMu.TryLock() {
		return session.PromptResponse{}, fmt.Errorf("session %q is already receiving a prompt: %w", h.cfg.ID, ipc.ErrLocked)
	}
	defer h.injectMu.Unlock()

	if h.CurrentState() == session.StateBlocked {
		return session.PromptResponse{}, fmt.Errorf("session %q: %w", h.cfg.ID, errAgentBlocked)
	}

	writes := h.cfg.Adapter.PasteWrites([]byte(req.Text))
	// A token from before the paste is not the agent answering this paste:
	// consuming it would let the second line race the first.
	select {
	case <-h.outputTick:
	default:
	}
	for i, write := range writes {
		if i > 0 {
			// A line-mode paste waits for the agent to answer before the next
			// line, so a paste does not become a burst of unanswered prompts.
			select {
			case <-h.outputTick:
			case <-ctx.Done():
				return session.PromptResponse{}, fmt.Errorf("paste cancelled: %w", ctx.Err())
			case <-h.donec:
				return session.PromptResponse{}, fmt.Errorf("session %q closed mid-paste", h.cfg.ID)
			}
			// The answer may itself be a new question: re-check before every
			// line rather than typing the rest into a fresh dialog.
			if h.CurrentState() == session.StateBlocked {
				return session.PromptResponse{}, fmt.Errorf("session %q: %w", h.cfg.ID, errAgentBlocked)
			}
		}
		if _, err := h.pty.Write(append(write, '\r')); err != nil {
			return session.PromptResponse{}, fmt.Errorf("write to the agent: %w", err)
		}
	}
	h.touch()

	return session.PromptResponse{StopReason: "injected"}, nil
}

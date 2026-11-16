package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// bufferedUpdates bounds how many agent updates a detached ACP session keeps for
// its next client. Past that the oldest go. This is the first of two layers: the
// host keeps the last few for a client that is only briefly away, and the daemon
// stores every update for a window that opens later, or for one that arrives
// after this process is gone.
const bufferedUpdates = 256

// permissionWait is how long an ACP turn waits for the client's answer before
// the question counts as refused. A turn must not hang on a client that went
// away, and a refusal is the answer that cannot be wrong.
const permissionWait = 5 * time.Minute

// errWrongMode reports a call that does not fit this session's mode.
var errWrongMode = errors.New("host: wrong session mode")

// errBadSize reports a resize that names no terminal size.
var errBadSize = errors.New("host: a terminal needs positive columns and rows")

// acpSession is the ACP half of a host: the agent conversation plus the pending
// permission questions the client has not answered yet.
type acpSession struct {
	sess *acp.Session

	mu      sync.Mutex
	pending map[string]chan acp.PermissionAnswer
	nextID  int
	buffer  []session.UpdateMessage
	// nextSeq numbers every update this host produces. A host hands its buffer
	// to a daemon that reconnects, and this is the number that lets that daemon
	// tell a replay of work it already stored from new work.
	nextSeq uint64
}

// startACP opens an ACP conversation for this host. Policy zero is Supervised,
// so a caller that names none gets the cautious one.
func (h *Host) startACP(ctx context.Context) error {
	sess, err := acp.Open(ctx, acp.Launch{
		Command: h.cfg.Command,
		Args:    h.cfg.Args,
		Dir:     h.cfg.Dir,
		Env:     h.cfg.Env,
	}, acp.Config{
		Workspace:    h.cfg.Dir,
		Policy:       h.cfg.Policy,
		Asker:        h.askClient,
		MaxReadBytes: ipc.MaxFrameSize,
		OnUpdate:     h.forwardUpdate,
	})
	if err != nil {
		return fmt.Errorf("open the acp connection: %w", err)
	}

	h.acp = &acpSession{sess: sess, pending: make(map[string]chan acp.PermissionAnswer)}
	return nil
}

// forwardUpdate pushes one agent update to the attached client. With no client
// it is buffered, so a reattach shows what happened while nobody listened. An
// update too large for one frame is split: a streaming client already renders
// pieces of one message, so dropping it would lose what splitting keeps.
//
// Every piece carries the number this host gave it, because a daemon that
// reconnects is handed the buffer again and must recognise what it already has.
func (h *Host) forwardUpdate(u acp.Update) {
	msg := session.UpdateMessage{
		Kind:       u.Kind,
		Text:       u.Text,
		ToolCallID: u.ToolCallID,
		ToolTitle:  u.ToolTitle,
		ToolStatus: u.ToolStatus,
	}

	for _, part := range chunkUpdate(msg, updateTextBudget) {
		h.deliverUpdate(part)
	}
}

// deliverUpdate pushes one update to the client, or buffers it when nobody is
// listening.
func (h *Host) deliverUpdate(msg session.UpdateMessage) {
	msg.HostSeq = h.nextUpdateSeq()

	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		h.acp.mu.Lock()
		if len(h.acp.buffer) >= bufferedUpdates {
			h.acp.buffer = h.acp.buffer[1:]
		}
		h.acp.buffer = append(h.acp.buffer, msg)
		h.acp.mu.Unlock()
		return
	}

	if err := conn.Notify(session.MethodUpdate, msg); err != nil {
		h.log.Debug("forward an update failed", "session", h.cfg.ID, "err", err.Error())
	}
}

// nextUpdateSeq hands out the next number for an update. It is one counter for
// the whole session rather than one for the buffer: a number has to keep its
// meaning after the update it belongs to has been trimmed away.
func (h *Host) nextUpdateSeq() uint64 {
	h.acp.mu.Lock()
	defer h.acp.mu.Unlock()

	h.acp.nextSeq++
	return h.acp.nextSeq
}

// updateTextBudget bounds the text one update carries. A frame holds
// ipc.MaxFrameSize bytes, JSON escaping can cost six bytes per byte, and the
// envelope needs room of its own.
const updateTextBudget = ipc.MaxFrameSize / 8

// chunkUpdate splits an update whose text does not fit the budget into
// consecutive updates of the same kind, cutting on rune boundaries so a split
// never breaks a character.
func chunkUpdate(msg session.UpdateMessage, budget int) []session.UpdateMessage {
	if budget <= 0 || len(msg.Text) <= budget {
		return []session.UpdateMessage{msg}
	}

	parts := make([]session.UpdateMessage, 0, len(msg.Text)/budget+1)
	text := msg.Text

	for len(text) > budget {
		cut := budget
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			cut = budget
		}

		part := msg
		part.Text = text[:cut]
		parts = append(parts, part)
		text = text[cut:]
	}

	part := msg
	part.Text = text
	return append(parts, part)
}

// askClient sends a permission question and waits for the answer. A client that
// disconnects, or a question nobody answers, becomes a refusal.
func (h *Host) askClient(ctx context.Context, req acp.PermissionRequest) (acp.PermissionAnswer, error) {
	h.acp.mu.Lock()
	h.acp.nextID++
	id := fmt.Sprintf("perm-%d", h.acp.nextID)
	answer := make(chan acp.PermissionAnswer, 1)
	h.acp.pending[id] = answer
	h.acp.mu.Unlock()

	defer func() {
		h.acp.mu.Lock()
		delete(h.acp.pending, id)
		h.acp.mu.Unlock()
	}()

	options := make([]string, 0, len(req.Options))
	for _, opt := range req.Options {
		options = append(options, opt.ID)
	}

	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		return acp.PermissionAnswer{}, nil
	}

	// The agent is waiting on the user. This is the exact case `blocked` names:
	// an approval or question UI the session cannot get past on its own. Unlike
	// a PTY session, ACP tells us rather than leaving us to recognise a dialog.
	h.setState(session.StateBlocked)

	if err := conn.Notify(session.MethodPermission, session.PermissionQuestion{
		ID:      id,
		Title:   req.Title,
		Kind:    req.Kind,
		Options: options,
	}); err != nil {
		return acp.PermissionAnswer{}, nil
	}

	timer := time.NewTimer(permissionWait)
	defer timer.Stop()

	select {
	case a := <-answer:
		// Answered: the turn resumes, so the session is working again.
		h.setState(session.StateWorking)
		return a, nil
	case <-ctx.Done():
		return acp.PermissionAnswer{}, nil
	case <-timer.C:
		h.log.Warn("permission unanswered", "session", h.cfg.ID, "title", req.Title)
		return acp.PermissionAnswer{}, nil
	case <-h.donec:
		return acp.PermissionAnswer{}, nil
	}
}

// answerPermission resolves a pending question. An unknown id is a stale answer
// from a client that reconnected, so it is not an error.
func (h *Host) answerPermission(reply session.PermissionReply) {
	if h.acp == nil {
		return
	}

	h.acp.mu.Lock()
	ch, ok := h.acp.pending[reply.ID]
	h.acp.mu.Unlock()

	if !ok {
		return
	}
	select {
	case ch <- acp.PermissionAnswer{OptionID: reply.OptionID}:
	default:
	}
}

// replayBuffered sends the updates a detached client missed.
func (h *Host) replayBuffered(conn *ipc.Conn) {
	if h.acp == nil {
		return
	}

	h.acp.mu.Lock()
	buffered := h.acp.buffer
	h.acp.buffer = nil
	h.acp.mu.Unlock()

	for _, msg := range buffered {
		if err := conn.Notify(session.MethodUpdate, msg); err != nil {
			return
		}
	}
}

// promptACP runs one turn.
func (h *Host) promptACP(ctx context.Context, req session.PromptRequest) (session.PromptResponse, error) {
	if h.acp == nil {
		return session.PromptResponse{}, fmt.Errorf("session %q is not an ACP session: %w", h.cfg.ID, errWrongMode)
	}

	// The turn is now running. An ACP session knows this exactly: the call is
	// synchronous and returns when the agent stops.
	h.setState(session.StateWorking)

	resp, err := h.acp.sess.Prompt(ctx, sdk.TextBlock(req.Text))
	if err != nil {
		// A failed turn is not "done": the work did not happen, and a caller
		// that reads done would think there is something to look at. Unknown
		// says what this build knows.
		h.setState(session.StateUnknown)
		return session.PromptResponse{}, fmt.Errorf("run the turn: %w", err)
	}

	// The agent stopped. Whether the user has seen the result is the client's
	// question, so the host reports done and the client marks it seen.
	h.setState(session.StateDone)
	return session.PromptResponse{StopReason: string(resp.StopReason)}, nil
}

// cancelACP stops the running turn.
func (h *Host) cancelACP(ctx context.Context) error {
	if h.acp == nil {
		return fmt.Errorf("session %q is not an ACP session: %w", h.cfg.ID, errWrongMode)
	}
	if err := h.acp.sess.Cancel(ctx); err != nil {
		return fmt.Errorf("cancel the turn: %w", err)
	}
	return nil
}

func (h *Host) closeACP() []error {
	if h.acp == nil {
		return nil
	}
	if err := h.acp.sess.Close(); err != nil {
		return []error{fmt.Errorf("close the acp session: %w", err)}
	}
	return nil
}

// decodeParams decodes one call's parameters. Empty parameters are allowed: a
// method with no arguments is a normal method.
func decodeParams[T any](params json.RawMessage) (T, error) {
	var out T
	if len(params) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(params, &out); err != nil {
		return out, fmt.Errorf("decode parameters: %w", err)
	}
	return out, nil
}

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// bufferedUpdates bounds how many agent updates a detached ACP session keeps for
// its next client. Past that the oldest go: a reattach shows recent work, not a
// full transcript, because the transcript belongs in the daemon's store.
const bufferedUpdates = 256

// permissionWait is how long an ACP turn waits for the client's answer before
// the question counts as refused. A turn must not hang on a client that went
// away, and a refusal is the answer that cannot be wrong.
const permissionWait = 5 * time.Minute

// errWrongMode reports a call that does not fit this session's mode.
var errWrongMode = errors.New("host: wrong session mode")

// acpSession is the ACP half of a host: the agent conversation plus the pending
// permission questions the client has not answered yet.
type acpSession struct {
	sess *acp.Session

	mu      sync.Mutex
	pending map[string]chan acp.PermissionAnswer
	nextID  int
	buffer  []session.UpdateMessage
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
		Workspace: h.cfg.Dir,
		Policy:    h.cfg.Policy,
		Asker:     h.askClient,
		OnUpdate:  h.forwardUpdate,
	})
	if err != nil {
		return fmt.Errorf("open the acp connection: %w", err)
	}

	h.acp = &acpSession{sess: sess, pending: make(map[string]chan acp.PermissionAnswer)}
	return nil
}

// forwardUpdate pushes one agent update to the attached client. With no client
// it is buffered, so a reattach shows what happened while nobody listened.
func (h *Host) forwardUpdate(u acp.Update) {
	msg := session.UpdateMessage{
		Kind:       u.Kind,
		Text:       u.Text,
		ToolCallID: u.ToolCallID,
		ToolTitle:  u.ToolTitle,
		ToolStatus: u.ToolStatus,
	}

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

	resp, err := h.acp.sess.Prompt(ctx, sdk.TextBlock(req.Text))
	if err != nil {
		return session.PromptResponse{}, fmt.Errorf("run the turn: %w", err)
	}
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

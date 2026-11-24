package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// The relay says what went wrong with a queue call; the daemon handler gives
// it the wire class, because that is the layer that knows the caller.
var (
	// errNoQueuedMessage reports an edit, a move or a removal that named a
	// message no longer in the queue, usually because its turn has started.
	errNoQueuedMessage = errors.New("no such queued message")
	// errEmptyMessage reports a queued message with no text.
	errEmptyMessage = errors.New("a queued message needs text")
	// errQueuePosition reports a move to a place the queue does not have.
	errQueuePosition = errors.New("no such queue position")
)

// errQueueNeedsAgent reports a queue for a session whose mode cannot take a
// prompt. A terminal has no turns to line messages up behind.
var errQueueNeedsAgent = ipc.Class(errors.New("daemon: only an agent session queues messages"), ipc.CodeWrongMode)

// messageQueue is one session's messages waiting for its turn to end. A second
// prompt mid-turn is the agent's own call, so the daemon keeps one delivery
// running at a time and never hands a host two prompts at once.
type messageQueue struct {
	messages []session.QueuedMessage
	// nextID numbers messages within the session, so an id is never reused.
	nextID int
	// delivering is true while a goroutine hands these messages to the host.
	delivering bool
}

// queueSnapshot copies the messages waiting for a session.
func (r *relay) queueSnapshot(id session.ID) []session.QueuedMessage {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return []session.QueuedMessage{}
	}
	return append([]session.QueuedMessage(nil), q.messages...)
}

// delivering reports whether a queued message is on its way to the host. The
// queue is empty while that prompt runs, so this is the only sign of the work.
func (r *relay) delivering(id session.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	return q != nil && q.delivering
}

// enqueue appends one message and answers with the queue it joined.
func (r *relay) enqueue(id session.ID, text string) ([]session.QueuedMessage, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errEmptyMessage
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		q = &messageQueue{}
		r.msgs[id] = q
	}
	q.nextID++
	q.messages = append(q.messages, session.QueuedMessage{
		ID:   fmt.Sprintf("q%d", q.nextID),
		Text: text,
	})
	return append([]session.QueuedMessage(nil), q.messages...), nil
}

// editQueued replaces one message's text. An edit is also the user's answer to
// a failed delivery, so the old failure no longer describes the message.
func (r *relay) editQueued(id session.ID, qid, text string) ([]session.QueuedMessage, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errEmptyMessage
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return nil, errNoQueuedMessage
	}
	for i := range q.messages {
		if q.messages[i].ID == qid {
			q.messages[i].Text = text
			q.messages[i].Error = ""
			return append([]session.QueuedMessage(nil), q.messages...), nil
		}
	}
	return nil, errNoQueuedMessage
}

// removeQueued drops one message.
func (r *relay) removeQueued(id session.ID, qid string) ([]session.QueuedMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return nil, errNoQueuedMessage
	}
	for i := range q.messages {
		if q.messages[i].ID != qid {
			continue
		}
		q.messages = append(q.messages[:i], q.messages[i+1:]...)
		return append([]session.QueuedMessage(nil), q.messages...), nil
	}
	return nil, errNoQueuedMessage
}

// moveQueued puts one message at index to, shifting the others around it.
func (r *relay) moveQueued(id session.ID, qid string, to int) ([]session.QueuedMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return nil, errNoQueuedMessage
	}
	from := -1
	for i := range q.messages {
		if q.messages[i].ID == qid {
			from = i
			break
		}
	}
	if from < 0 {
		return nil, errNoQueuedMessage
	}
	if to < 0 || to >= len(q.messages) {
		return nil, errQueuePosition
	}
	if from != to {
		moved := q.messages[from]
		q.messages = append(q.messages[:from], q.messages[from+1:]...)
		q.messages = append(q.messages, session.QueuedMessage{})
		copy(q.messages[to+1:], q.messages[to:])
		q.messages[to] = moved
	}
	return append([]session.QueuedMessage(nil), q.messages...), nil
}

// takeQueued removes the message at the front; the caller owns it until it is
// delivered or restored.
func (r *relay) takeQueued(id session.ID) (session.QueuedMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil || len(q.messages) == 0 {
		return session.QueuedMessage{}, false
	}
	msg := q.messages[0]
	q.messages = append([]session.QueuedMessage(nil), q.messages[1:]...)
	return msg, true
}

// restoreQueued puts a message that could not be delivered back at the front,
// carrying the failure's identity. A queue that no longer exists is a session
// that ended: the message goes with it rather than resurrecting an entry no
// window can see.
func (r *relay) restoreQueued(id session.ID, msg session.QueuedMessage, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return
	}
	msg.Error = failure
	q.messages = append([]session.QueuedMessage{msg}, q.messages...)
}

// beginDelivery takes the delivery token, or reports false when a delivery
// already runs or nothing waits.
func (r *relay) beginDelivery(id session.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil || q.delivering || len(q.messages) == 0 {
		return false
	}
	q.delivering = true
	return true
}

// finishDelivery ends a delivery loop. It reports true when messages arrived
// while the loop was ending, so the caller keeps the token and carries on
// rather than leaving them to a kick that already found delivery running.
func (r *relay) finishDelivery(id session.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return false
	}
	if len(q.messages) > 0 {
		return true
	}
	delete(r.msgs, id)
	return false
}

// abandonDelivery releases the token with messages still waiting. They wait for
// the next finished turn: a cancelled turn or a refused prompt is a decision
// left to the user, not a retry to run on its own.
func (r *relay) abandonDelivery(id session.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	q := r.msgs[id]
	if q == nil {
		return
	}
	q.delivering = false
	if len(q.messages) == 0 {
		delete(r.msgs, id)
	}
}

// listQueue answers the messages waiting for a session's turn to end.
func (d *Daemon) listQueue(id session.ID) any {
	return session.QueueResponse{Messages: d.relay.queueSnapshot(id)}
}

// queueMessage appends one message. A message added after the turn it was
// typed into already ended runs now: the window cannot see that race, and
// leaving the message queued would wait for a turn nobody promised.
func (d *Daemon) queueMessage(id session.ID, params json.RawMessage) (any, error) {
	var req session.QueueAddRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodQueueAdd, err)
	}

	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueAdd, id, ErrUnknownSession)
	}
	if !att.Conn.Alive() {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueAdd, id, ErrHostUnreachable)
	}
	if att.Info.Mode != string(session.ModeACP) {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueAdd, id, errQueueNeedsAgent)
	}

	// The append and the state read share one critical section: a finish or a
	// new turn cannot slip between the decision and the queue it decides for.
	d.mu.Lock()
	state, served := d.stateUnderLock(id)
	if !served {
		d.mu.Unlock()
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueAdd, id, ErrUnknownSession)
	}
	messages, err := d.relay.enqueue(id, req.Text)
	d.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueAdd, id,
			ipc.Class(err, ipc.CodeBadRequest))
	}

	// A message added after the turn it was typed into ended runs now; an idle
	// turn starts nothing, because only a finished turn says the agent is free.
	if state == session.StateDone {
		d.kickQueue(id)
	}
	return session.QueueResponse{Messages: messages}, nil
}

// stateUnderLock reads the lifecycle this daemon last recorded for a session.
// The caller holds d.mu.
func (d *Daemon) stateUnderLock(id session.ID) (session.State, bool) {
	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == id {
			return d.fleet.Attachments[i].Info.State, true
		}
	}
	return session.StateUnknown, false
}

// editQueueMessage replaces one queued message's text.
func (d *Daemon) editQueueMessage(id session.ID, params json.RawMessage) (any, error) {
	var req session.QueueEditRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodQueueEdit, err)
	}
	messages, err := d.relay.editQueued(id, req.ID, req.Text)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueEdit, id,
			ipc.Class(err, ipc.CodeBadRequest))
	}
	return session.QueueResponse{Messages: messages}, nil
}

// removeQueueMessage deletes one queued message.
func (d *Daemon) removeQueueMessage(id session.ID, params json.RawMessage) (any, error) {
	var req session.QueueRemoveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodQueueRemove, err)
	}
	messages, err := d.relay.removeQueued(id, req.ID)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueRemove, id,
			ipc.Class(err, ipc.CodeBadRequest))
	}
	return session.QueueResponse{Messages: messages}, nil
}

// moveQueueMessage changes one message's place in the queue.
func (d *Daemon) moveQueueMessage(id session.ID, params json.RawMessage) (any, error) {
	var req session.QueueMoveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodQueueMove, err)
	}
	messages, err := d.relay.moveQueued(id, req.ID, req.To)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodQueueMove, id,
			ipc.Class(err, ipc.CodeBadRequest))
	}
	return session.QueueResponse{Messages: messages}, nil
}

// kickQueue starts delivering a session's queued messages. The token in the
// relay decides whether a delivery already runs, so the finished-turn
// transition and a late add can both call it.
func (d *Daemon) kickQueue(id session.ID) {
	if !d.relay.beginDelivery(id) {
		return
	}
	go d.runQueue(id)
}

// runQueue hands queued messages to the host one turn at a time. Each prompt is
// a whole turn, so this loop spans minutes and takes no context of its own: a
// slow agent is not a failed agent, and the call returns when it stops.
func (d *Daemon) runQueue(id session.ID) {
	for {
		msg, ok := d.relay.takeQueued(id)
		if !ok {
			if d.relay.finishDelivery(id) {
				// A message landed while the loop was ending: this goroutine
				// still holds the token, so two prompts cannot interleave.
				continue
			}
			return
		}

		att := d.lookup(id)
		if att == nil || !att.Conn.Alive() {
			d.relay.restoreQueued(id, msg, session.DeliveryHostUnreachable)
			d.relay.abandonDelivery(id)
			return
		}

		raw, err := att.Conn.Call(context.Background(), session.MethodPrompt, session.PromptRequest{Text: msg.Text})
		if err != nil {
			d.cfg.Log.Warn("deliver a queued message failed", "session", id, "err", err.Error())
			d.relay.restoreQueued(id, msg, session.DeliveryPromptFailed)
			d.relay.abandonDelivery(id)
			return
		}
		// A cancelled turn is the user stopping this very message: the rest of
		// the queue waits rather than starting the next turn over the stop.
		var resp session.PromptResponse
		if json.Unmarshal(raw, &resp) == nil && resp.StopReason == "cancelled" {
			d.relay.abandonDelivery(id)
			return
		}
	}
}

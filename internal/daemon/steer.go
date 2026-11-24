package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// steerTimeout bounds the question to one turn. A turn that cannot take the
// message this quickly is over before the message would matter.
const steerTimeout = 10 * time.Second

// steerMessage hands one message to the running turn, or queues it when there
// is none. A steer that finds no turn is not an error: the message the user
// sent is the queue's, and refusing it would lose it.
func (d *Daemon) steerMessage(id session.ID, params json.RawMessage) (any, error) {
	var req session.SteerRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodSteer, err)
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodSteer, id,
			ipc.Class(errEmptyMessage, ipc.CodeBadRequest))
	}

	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodSteer, id, ErrUnknownSession)
	}
	if att.Info.Mode != string(session.ModeACP) {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodSteer, id, errQueueNeedsAgent)
	}

	if att.Info.Steer && att.Conn.Alive() {
		steered, err := takeSteer(att.Conn, req)
		if err != nil {
			return nil, fmt.Errorf("%s on session %q: %w", session.MethodSteer, id, err)
		}
		if steered {
			return session.SteerDelivery{Route: session.SteerSteered}, nil
		}
	}
	return d.queueSteer(id, req.Text)
}

// takeSteer asks one host to hand the message to its turn.
func takeSteer(conn *ipc.Conn, req session.SteerRequest) (bool, error) {
	callCtx, cancel := context.WithTimeout(context.Background(), steerTimeout)
	defer cancel()

	raw, err := conn.Call(callCtx, session.MethodSteer, req)
	if err != nil {
		if !conn.Alive() {
			return false, ErrHostUnreachable
		}
		return false, fmt.Errorf("call %s: %w", session.MethodSteer, err)
	}
	var resp session.SteerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false, fmt.Errorf("decode %s response: %w", session.MethodSteer, err)
	}
	return resp.Steered, nil
}

// queueSteer keeps a message the turn could not take for the next turn. It goes
// through the queue add path, so the ordering and the finished-turn kick are
// the queue's, not a second copy of them.
func (d *Daemon) queueSteer(id session.ID, text string) (any, error) {
	params, err := json.Marshal(session.QueueAddRequest{Text: text})
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodSteer, id, err)
	}
	queued, err := d.queueMessage(id, params)
	if err != nil {
		return nil, err
	}
	resp, _ := queued.(session.QueueResponse)
	return session.SteerDelivery{Route: session.SteerQueued, Messages: resp.Messages}, nil
}

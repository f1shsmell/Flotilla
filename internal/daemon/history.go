package daemon

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/transcript"
)

// historyTail is how many updates a window gets when it asks for a session's
// log without saying how much it wants. It is a screenful of recent work, not
// the whole turn: a window that opens an hour-long session wants to see where
// the agent is, and reading further back is a separate request.
const historyTail = 200

// historyBudget bounds one history answer in stored bytes, well under the frame
// limit so the JSON envelope always fits. A page that would be larger is cut
// and says there is more.
const historyBudget = ipc.MaxFrameSize / 2

// recordUpdate stores one agent update and returns what to forward, and whether
// to forward it at all. A store failure is logged rather than raised: the live
// update is the session's present, and dropping it over a full disk would be
// worse than a gap in the log.
//
// A replay of an update the log already holds is not an error either. A host
// hands its buffer to a daemon that reconnects, and the same paragraph must not
// become two lines; the copy keeps the sequence it was first stored under, so a
// client that already folded it in recognises it.
//
// A replay whose record has since been trimmed away is the one case that is not
// forwarded. Its text belongs to the part of the turn the log chose to drop, and
// handing a window a line from before everything it holds would place it out of
// order — with no sequence, because the record that carried one is gone.
func (d *Daemon) recordUpdate(id session.ID, params json.RawMessage) (json.RawMessage, bool) {
	var msg session.UpdateMessage
	if err := json.Unmarshal(params, &msg); err != nil {
		return params, true
	}

	seq, err := d.transcripts.Append(string(id), msg.HostSeq, params)
	switch {
	case err == nil:
	case errors.Is(err, transcript.ErrTrimmed):
		return nil, false
	case errors.Is(err, transcript.ErrDuplicate):
		// Stored already; the copy keeps the sequence it was first given.
	case err != nil:
		d.cfg.Log.Warn("cannot store an agent update", "session", id, "err", err.Error())
		return params, true
	}

	msg.Seq = seq
	stored, err := json.Marshal(msg)
	if err != nil {
		return params, true
	}
	return stored, true
}

// history answers a request for a session's stored updates. The daemon serves it
// from its own transcript, not from the agent: what was stored is readable
// without asking the process that produced it.
func (d *Daemon) history(id session.ID, params json.RawMessage) (any, error) {
	var req session.HistoryRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("decode %s: %w", session.MethodHistory, err)
		}
	}

	updates, more, err := d.readHistory(id, req)
	if err != nil {
		return nil, err
	}
	return session.HistoryResponse{Updates: updates, More: more}, nil
}

// readHistory is the one place a transcript is turned into updates a client can
// render: the attach path and the history call differ in what they ask for, not
// in how they read it.
func (d *Daemon) readHistory(id session.ID, req session.HistoryRequest) ([]session.UpdateMessage, bool, error) {
	tail := req.Tail
	if tail <= 0 {
		tail = historyTail
	}

	page, err := d.transcripts.Page(string(id), transcript.Window{
		After:  req.After,
		Tail:   tail,
		Budget: historyBudget,
	})
	if err != nil {
		return nil, false, fmt.Errorf("%s on session %q: %w", session.MethodHistory, id, err)
	}

	updates := make([]session.UpdateMessage, 0, len(page.Records))
	for _, rec := range page.Records {
		var msg session.UpdateMessage
		if err := json.Unmarshal(rec.Update, &msg); err != nil {
			// A record that no longer decodes is skipped rather than failing the
			// answer: the rest of the log is still true.
			continue
		}
		msg.Seq = rec.Seq
		updates = append(updates, msg)
	}
	return updates, page.More, nil
}

// releaseTranscript closes the handle a finished session's log was written
// through. The file stays: the session is gone, what it did is not.
func (d *Daemon) releaseTranscript(id session.ID) {
	if err := d.transcripts.Release(string(id)); err != nil {
		d.cfg.Log.Debug("cannot release a transcript", "session", id, "err", err.Error())
	}
}

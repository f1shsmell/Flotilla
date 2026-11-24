package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
)

// archiveTail is how much of a finished run's log an archive read returns. The
// whole log is at most MaxBytes, but a reader opening a finished session wants
// to see how it ended, not to load a megabyte into a window.
const archiveTail = 400

// archiveLimit bounds how many finished runs the fleet reports. The log
// directory is kept forever, and a machine that has been used for months would
// otherwise push a few hundred cards into the sidebar. The newest are the ones
// a person returns to; the rest stay on disk and are still readable by id.
const archiveLimit = 50

// liveIDs snapshots the sessions this daemon still serves, which is what an
// archive listing has to subtract: a running session also has a log.
func (d *Daemon) liveIDs() map[session.ID]struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make(map[session.ID]struct{}, len(d.fleet.Attachments))
	for i := range d.fleet.Attachments {
		out[d.fleet.Attachments[i].Record.ID] = struct{}{}
	}
	return out
}

// archivedEntries lists the sessions this daemon no longer serves. The read
// happens outside the fleet lock: it touches the disk, and a status poll must
// not hold the fleet while a slow disk answers.
//
// A log whose metadata cannot be read is still listed by its id: the run
// happened, and hiding it because a side file is corrupt would be a worse
// answer than one with empty fields.
func (d *Daemon) archivedEntries() []session.ArchivedEntry {
	if d.state == nil {
		return nil
	}
	metas, err := d.state.TranscriptList(context.Background())
	if err != nil {
		d.cfg.Log.Warn("cannot list finished sessions", "err", err.Error())
		return nil
	}
	if len(metas) == 0 {
		return nil
	}

	live := d.liveIDs()
	out := make([]session.ArchivedEntry, 0, len(metas))
	for _, meta := range metas {
		id := session.ID(meta.ID)
		if _, running := live[id]; running {
			continue
		}
		if len(out) == archiveLimit {
			// The list is newest first, so the bound cuts the oldest. They stay
			// on disk and are still readable by id: the fleet is shorter, the
			// record is not lost.
			break
		}
		out = append(out, session.ArchivedEntry{
			ID:      id,
			Mode:    session.Mode(meta.Mode),
			Command: meta.Command,
			Dir:     meta.Dir,
			Started: meta.Started,
			Ended:   meta.Ended,
		})
	}
	return out
}

// archive answers a read of a finished session. It refuses a session this
// daemon is serving: that one is a live session, and the caller should attach
// to it rather than read a snapshot that cannot be continued.
func (d *Daemon) archive(params json.RawMessage) (any, error) {
	var req session.ArchiveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodArchive, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: empty session id: %w", session.MethodArchive, session.ErrNoRecord)
	}

	if _, running := d.liveIDs()[req.ID]; running {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodArchive, req.ID, ErrUnknownSession)
	}

	// Whether this build recorded anything is the existence test, not which
	// fields the record happens to carry: a session that only ever got an end
	// stamp is still a session that ran.
	recorded := d.state != nil
	meta := store.TranscriptMeta{ID: string(req.ID)}
	if d.state != nil {
		stored, err := d.state.ReadTranscriptMeta(context.Background(), string(req.ID))
		switch {
		case errors.Is(err, store.ErrNoMeta):
			// A log written before this build kept metadata: the id is all
			// there is, and the reader says so rather than filling the gaps.
			recorded = false
		case err != nil:
			return nil, fmt.Errorf("%s on session %q: %w", session.MethodArchive, req.ID, err)
		default:
			meta = stored
		}
	} else {
		recorded = false
	}

	updates, more, err := d.readHistory(req.ID, session.HistoryRequest{Tail: archiveTail})
	if err != nil {
		return nil, err
	}
	if !recorded && len(updates) == 0 {
		// Neither a log nor metadata: the id names nothing this daemon wrote.
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodArchive, req.ID, session.ErrNoRecord)
	}

	return session.ArchiveResponse{
		Session: session.ArchivedEntry{
			ID:      req.ID,
			Mode:    session.Mode(meta.Mode),
			Command: meta.Command,
			Dir:     meta.Dir,
			Started: meta.Started,
			Ended:   meta.Ended,
		},
		Updates: updates,
		More:    more,
	}, nil
}

// recordStart stores what a session is about as it starts. A failure is logged
// rather than raised: the session is already running, and losing its label is
// not a reason to kill it.
func (d *Daemon) recordStart(id session.ID, mode session.Mode, command, dir string) {
	if d.state == nil {
		return
	}
	if err := d.state.WriteTranscriptMeta(context.Background(), store.TranscriptMeta{
		ID:      string(id),
		Mode:    string(mode),
		Command: command,
		Dir:     dir,
		Started: time.Now(),
	}); err != nil {
		d.cfg.Log.Warn("cannot record a session's start", "session", id, "err", err.Error())
	}
}

// recordEnd stamps a session as finished at the moment this daemon stopped
// serving it. It reads the stored metadata first so the run's own facts
// survive: a write here replaces the file, and the start time and command are
// not this function's to lose.
//
// The stamp is an observation, not a reconstruction: a host that died while no
// daemon was watching keeps a zero end, because inventing one would claim the
// run stopped when the daemon happened to look.
func (d *Daemon) recordEnd(id session.ID) {
	if d.state == nil {
		return
	}
	meta, err := d.state.ReadTranscriptMeta(context.Background(), string(id))
	if err != nil {
		if !errors.Is(err, store.ErrNoMeta) {
			d.cfg.Log.Warn("cannot read a session's metadata to end it", "session", id, "err", err.Error())
			return
		}
		meta = store.TranscriptMeta{ID: string(id)}
	}
	if !meta.Ended.IsZero() {
		// The first end is the observed one; a second call is bookkeeping that
		// arrived late and must not move the time.
		return
	}

	meta.Ended = time.Now()
	if err := d.state.WriteTranscriptMeta(context.Background(), meta); err != nil {
		d.cfg.Log.Warn("cannot record a session's end", "session", id, "err", err.Error())
	}
}

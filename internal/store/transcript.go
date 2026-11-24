package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxTranscriptBytes bounds one session's transcript. This is a log to read,
// not an archive: past the bound the oldest rows go and the session keeps
// working.
const MaxTranscriptBytes = 1 << 20

// ErrDuplicate reports an update the log already holds. A host replays its
// buffer to a daemon that reconnects, so the same update arrives twice; the
// number the host gave it is what tells the two copies apart.
var ErrDuplicate = errors.New("store: update already stored")

// ErrTrimmed reports an update the log has moved past: it was stored once, and
// a trim has since dropped it. There is no sequence left to hand back, so a
// caller cannot pass the copy on as one a client already has — and storing it
// again would place work from before the kept tail after it.
var ErrTrimmed = errors.New("store: update was trimmed")

// ErrNoMeta reports a session whose transcript has no metadata row. A log
// written by an older build has none, which is why the id alone still names
// the session.
var ErrNoMeta = errors.New("store: no metadata")

// TranscriptRecord is one stored update and its place in the session's log.
type TranscriptRecord struct {
	// Seq starts at one and keeps counting across trims, so a client can tell
	// the updates it has already from the ones it does not.
	Seq uint64 `json:"seq"`
	// HostSeq is the number the host gave the update before sending it.
	HostSeq uint64 `json:"host_seq,omitempty"`
	// Update is the update as the host sent it.
	Update json.RawMessage `json:"update"`
}

// TranscriptWindow selects a page.
type TranscriptWindow struct {
	// After restricts the page to updates past this sequence.
	After uint64
	// Tail keeps only the last n of the selected updates.
	Tail int
	// Budget bounds the page by stored bytes; one record is always included.
	Budget int
}

// TranscriptPage is what a window selected, and whether more exists.
type TranscriptPage struct {
	Records []TranscriptRecord
	// More is true when records outside the page exist.
	More bool
}

// AppendTranscript stores one update and reports the sequence it was given.
//
// An update whose host number the log already holds returns ErrDuplicate and
// the sequence it was stored under. One whose record was trimmed returns
// ErrTrimmed: it belongs before the kept tail and must not be reinserted.
func (s *Store) AppendTranscript(ctx context.Context, id string, hostSeq uint64, update json.RawMessage) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin storing an update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var last, lastHost uint64
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0), COALESCE(MAX(host_seq), 0)
		   FROM transcripts WHERE session_id = ?`, id).Scan(&last, &lastHost)
	if err != nil {
		return 0, fmt.Errorf("read the tail of session %q: %w", id, err)
	}

	if hostSeq != 0 && hostSeq <= lastHost {
		var seq uint64
		err := tx.QueryRowContext(ctx,
			`SELECT seq FROM transcripts WHERE session_id = ? AND host_seq = ?`,
			id, hostSeq).Scan(&seq)
		switch {
		case err == nil:
			return seq, ErrDuplicate
		case errors.Is(err, sql.ErrNoRows):
			return 0, ErrTrimmed
		default:
			return 0, fmt.Errorf("look up an update of session %q: %w", id, err)
		}
	}

	seq := last + 1
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transcripts (session_id, seq, host_seq, body, at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, seq, hostSeq, []byte(update), time.Now().UnixNano()); err != nil {
		return 0, fmt.Errorf("store an update for session %q: %w", id, err)
	}

	// A trim is not an error: the update is stored, the oldest half is gone,
	// and the caller still gets the sequence it was given.
	if _, err := trimTranscript(ctx, tx, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit an update for session %q: %w", id, err)
	}
	return seq, nil
}

// trimTranscript drops the oldest half of an oversized log, keeping the tail
// whole. It runs inside the append's transaction so a crash cannot leave the
// log trimmed but the row unrecorded.
func trimTranscript(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var size int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(body)), 0) FROM transcripts WHERE session_id = ?`,
		id).Scan(&size); err != nil {
		return false, fmt.Errorf("measure the transcript of session %q: %w", id, err)
	}
	if size <= MaxTranscriptBytes {
		return false, nil
	}

	// Find the sequence to cut below: walk the tail until the kept half is
	// under the bound, then delete everything older.
	target := int64(MaxTranscriptBytes / 2)
	var kept int64
	var cut uint64
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, LENGTH(body) FROM transcripts WHERE session_id = ? ORDER BY seq DESC`,
		id)
	if err != nil {
		return false, fmt.Errorf("plan the trim of session %q: %w", id, err)
	}
	for rows.Next() {
		var seq uint64
		var length int64
		if err := rows.Scan(&seq, &length); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("plan the trim of session %q: %w", id, err)
		}
		kept += length
		if kept > target {
			cut = seq
			break
		}
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("plan the trim of session %q: %w", id, err)
	}
	if cut == 0 {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM transcripts WHERE session_id = ? AND seq < ?`, id, cut); err != nil {
		return false, fmt.Errorf("trim the transcript of session %q: %w", id, err)
	}
	return true, nil
}

// TranscriptPage reads the stored updates w selects, oldest first.
func (s *Store) TranscriptPage(ctx context.Context, id string, w TranscriptWindow) (TranscriptPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.transcriptPageLocked(ctx, id, w)
}

func (s *Store) transcriptPageLocked(ctx context.Context, id string, w TranscriptWindow) (TranscriptPage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, host_seq, body, LENGTH(body)
		   FROM transcripts WHERE session_id = ? AND seq > ? ORDER BY seq`,
		id, w.After)
	if err != nil {
		return TranscriptPage{}, fmt.Errorf("read the transcript of session %q: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	type sized struct {
		rec  TranscriptRecord
		size int
	}
	var all []sized
	for rows.Next() {
		var s sized
		var update []byte
		if err := rows.Scan(&s.rec.Seq, &s.rec.HostSeq, &update, &s.size); err != nil {
			return TranscriptPage{}, fmt.Errorf("read the transcript of session %q: %w", id, err)
		}
		s.rec.Update = update
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return TranscriptPage{}, fmt.Errorf("read the transcript of session %q: %w", id, err)
	}

	kept := all
	more := false

	if w.Budget > 0 {
		size := 0
		for i := len(kept) - 1; i >= 0; i-- {
			size += kept[i].size
			// One record is always included, so a page always advances the
			// caller's cursor.
			if size > w.Budget && len(kept)-i > 1 {
				kept = kept[i+1:]
				more = true
				break
			}
		}
	}
	if w.Tail > 0 && len(kept) > w.Tail {
		kept = kept[len(kept)-w.Tail:]
		more = true
	}

	out := make([]TranscriptRecord, 0, len(kept))
	for _, s := range kept {
		out = append(out, s.rec)
	}
	return TranscriptPage{Records: out, More: more}, nil
}

// LastHostSeq reports the largest host number the session's log holds, or zero
// for an empty log. A reused session id numbers past it; from one again, its
// first update would look like a replay of the old run's.
func (s *Store) LastHostSeq(ctx context.Context, id string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var newest uint64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(host_seq), 0) FROM transcripts WHERE session_id = ?`,
		id).Scan(&newest); err != nil {
		return 0, fmt.Errorf("read the last host number of session %q: %w", id, err)
	}
	return newest, nil
}

// TranscriptMeta is what a session's log is about.
type TranscriptMeta struct {
	ID      string    `json:"id"`
	Mode    string    `json:"mode,omitempty"`
	Command string    `json:"command,omitempty"`
	Dir     string    `json:"dir,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
}

// WriteTranscriptMeta stores m, replacing any earlier copy.
func (s *Store) WriteTranscriptMeta(ctx context.Context, m TranscriptMeta) error {
	if m.ID == "" {
		return fmt.Errorf("write session metadata: empty id: %w", ErrNoMeta)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO transcript_meta (session_id, mode, command, dir, started, ended)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id) DO UPDATE SET
		   mode = excluded.mode, command = excluded.command, dir = excluded.dir,
		   started = excluded.started, ended = excluded.ended`,
		m.ID, m.Mode, m.Command, m.Dir, unixOrZero(m.Started), unixOrZero(m.Ended)); err != nil {
		return fmt.Errorf("write metadata for session %q: %w", m.ID, err)
	}
	return nil
}

// ReadTranscriptMeta returns the metadata stored for one session.
func (s *Store) ReadTranscriptMeta(ctx context.Context, id string) (TranscriptMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.readMetaLocked(ctx, id)
}

func (s *Store) readMetaLocked(ctx context.Context, id string) (TranscriptMeta, error) {
	var m TranscriptMeta
	var started, ended int64
	err := s.db.QueryRowContext(ctx,
		`SELECT session_id, mode, command, dir, started, ended
		   FROM transcript_meta WHERE session_id = ?`, id).
		Scan(&m.ID, &m.Mode, &m.Command, &m.Dir, &started, &ended)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TranscriptMeta{}, ErrNoMeta
	case err != nil:
		return TranscriptMeta{}, fmt.Errorf("read metadata for session %q: %w", id, err)
	}
	m.Started = timeFromUnix(started)
	m.Ended = timeFromUnix(ended)
	return m, nil
}

// TranscriptList returns one meta per session this store has ever written,
// newest first. A transcript with no metadata row still appears: its id is all
// there is, and the fields stay empty rather than being guessed.
func (s *Store) TranscriptList(ctx context.Context) ([]TranscriptMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT session_id FROM (
		   SELECT session_id FROM transcripts
		   UNION
		   SELECT session_id FROM transcript_meta
		 ) ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("list the transcripts: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list the transcripts: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list the transcripts: %w", err)
	}

	out := make([]TranscriptMeta, 0, len(ids))
	for _, id := range ids {
		m, err := s.readMetaLocked(ctx, id)
		if errors.Is(err, ErrNoMeta) {
			m = TranscriptMeta{ID: id}
		} else if err != nil {
			return nil, err
		}
		out = append(out, m)
	}

	// Newest first: what ended most recently is what a person is most likely
	// to come back for. The id breaks ties so the order is stable.
	sortMetas(out)
	return out, nil
}

func sortMetas(list []TranscriptMeta) {
	last := func(m TranscriptMeta) time.Time {
		if !m.Ended.IsZero() {
			return m.Ended
		}
		return m.Started
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			a, b := list[j-1], list[j]
			if last(a).After(last(b)) || (last(a).Equal(last(b)) && a.ID <= b.ID) {
				break
			}
			list[j-1], list[j] = b, a
		}
	}
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func timeFromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoComment reports a comment id this store does not hold.
var ErrNoComment = errors.New("store: no such comment")

// Comment is one note on one line of one file.
type Comment struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Line is the line number on Side.
	Line int    `json:"line"`
	Side string `json:"side"`
	Text string `json:"text"`
	// Author names who wrote it.
	Author string    `json:"author"`
	At     time.Time `json:"at"`
}

// Comments returns a session's notes, oldest first.
func (s *Store) Comments(ctx context.Context, session string) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, path, line, side, text, author, at
		   FROM comments WHERE session_id = ? ORDER BY at, id`, session)
	if err != nil {
		return nil, fmt.Errorf("read the comments of session %q: %w", session, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Comment
	for rows.Next() {
		var c Comment
		var at int64
		if err := rows.Scan(&c.ID, &c.Path, &c.Line, &c.Side, &c.Text, &c.Author, &at); err != nil {
			return nil, fmt.Errorf("read a comment of session %q: %w", session, err)
		}
		c.At = timeFromUnix(at)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the comments of session %q: %w", session, err)
	}
	return out, nil
}

// ErrBadComment reports a comment that cannot be stored as asked.
var ErrBadComment = errors.New("store: invalid comment")

// maxCommentBytes bounds one note. A review note is a sentence or two; the
// bound keeps one runaway client from filling the disk with prose.
const maxCommentBytes = 4 << 10

// validateComment is the write path's own gate. It lives here because this is
// the only place a note is stored: a caller that skipped it would be a second
// rule about what a comment is, and the two would drift.
func validateComment(c Comment) error {
	if strings.TrimSpace(c.Text) == "" {
		return fmt.Errorf("comment on %q: empty text: %w", c.Path, ErrBadComment)
	}
	if len(c.Text) > maxCommentBytes {
		return fmt.Errorf("comment on %q: %d bytes of text, over %d: %w",
			c.Path, len(c.Text), maxCommentBytes, ErrBadComment)
	}
	if c.Path == "" {
		return fmt.Errorf("comment: empty path: %w", ErrBadComment)
	}
	// A root-relative path is absolute enough: on Windows "/x" has no drive
	// letter and IsAbs says no, but it still leaves the repository.
	if filepath.IsAbs(c.Path) || c.Path[0] == '/' || c.Path[0] == '\\' {
		return fmt.Errorf("comment on %q: absolute path: %w", c.Path, ErrBadComment)
	}
	cleaned := filepath.Clean(c.Path)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("comment on %q: path escapes the repository: %w", c.Path, ErrBadComment)
	}
	if c.Line < 1 {
		return fmt.Errorf("comment on %q: line %d: %w", c.Path, c.Line, ErrBadComment)
	}
	if c.Side != "new" && c.Side != "old" {
		return fmt.Errorf("comment on %q: side %q: %w", c.Path, c.Side, ErrBadComment)
	}
	return nil
}

// AddComment stores one note. The id is the caller's, so a retry that reaches
// the store twice keeps one note rather than two.
func (s *Store) AddComment(ctx context.Context, session string, c Comment) error {
	if err := validateComment(c); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO comments (id, session_id, path, line, side, text, author, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, session, c.Path, c.Line, c.Side, c.Text, c.Author, at.UnixNano()); err != nil {
		return fmt.Errorf("store a comment on session %q: %w", session, err)
	}
	return nil
}

// RemoveComment deletes one note.
func (s *Store) RemoveComment(ctx context.Context, session, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM comments WHERE session_id = ? AND id = ?`, session, id)
	if err != nil {
		return fmt.Errorf("remove a comment on session %q: %w", session, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove a comment on session %q: %w", session, err)
	}
	if affected == 0 {
		return fmt.Errorf("comment %q on session %q: %w", id, session, ErrNoComment)
	}
	return nil
}

// MarkSeen records that client has looked at session's finished turn. Seen is
// per client: one window looking does not mark the work seen for another.
func (s *Store) MarkSeen(ctx context.Context, client, session string) error {
	if client == "" {
		// A client that named itself in no handshake is not recorded: there is nothing to
		// attribute the look to, and guessing would clear a badge somebody else
		// still needs.
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixNano()
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO clients (name, at) VALUES (?, ?)`, client, now); err != nil {
		return fmt.Errorf("record client %q: %w", client, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO seen (client, session_id, at) VALUES (?, ?, ?)`,
		client, session, now); err != nil {
		return fmt.Errorf("mark session %q seen for %q: %w", session, client, err)
	}
	return s.evictClientsLocked(ctx)
}

// HasSeen reports whether client has looked at session's finished turn.
func (s *Store) HasSeen(ctx context.Context, client, session string) (bool, error) {
	if client == "" {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM seen WHERE client = ? AND session_id = ?`, client, session).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the seen mark: %w", err)
	}
	return true, nil
}

// ForgetSeen clears every client's flag for a session. A turn that just
// finished is unread for everyone, however many windows read the turn before.
func (s *Store) ForgetSeen(ctx context.Context, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM seen WHERE session_id = ?`, session); err != nil {
		return fmt.Errorf("clear the seen marks of session %q: %w", session, err)
	}
	return nil
}

// maxClients bounds the windows this store remembers. A desktop has a handful;
// the bound is what keeps a tab opened once a day from accumulating forever.
const maxClients = 32

// evictClientsLocked drops the oldest windows past the bound, and every mark
// they left behind. The set is a set, not an age list: any window beyond the
// bound is as good a candidate as another, and one still open re-registers on
// its next connection.
func (s *Store) evictClientsLocked(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients`).Scan(&count); err != nil {
		return fmt.Errorf("count the clients: %w", err)
	}
	if count <= maxClients {
		return nil
	}

	// Oldest first: `at` is the last time the window spoke, so the least
	// recently seen is the safest to forget.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM seen WHERE client IN (
		   SELECT name FROM clients ORDER BY at ASC LIMIT ?
		 )`, count-maxClients); err != nil {
		return fmt.Errorf("evict the seen marks of an old client: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM clients WHERE name IN (
		   SELECT name FROM clients ORDER BY at ASC LIMIT ?
		 )`, count-maxClients); err != nil {
		return fmt.Errorf("evict an old client: %w", err)
	}
	return nil
}

// SeenTable is the whole per-client read table, in the shape the daemon keeps
// it in memory. It is loaded at start and written back as one snapshot, so a
// reader never sees half a table.
type SeenTable struct {
	// Clients is every window the daemon has seen.
	Clients []string
	// Seen maps a session to the clients that have read its finished turn.
	Seen map[string][]string
}

// LoadSeen reads the whole table.
func (s *Store) LoadSeen(ctx context.Context) (SeenTable, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := SeenTable{Seen: map[string][]string{}}

	rows, err := s.db.QueryContext(ctx, `SELECT name FROM clients`)
	if err != nil {
		return SeenTable{}, fmt.Errorf("read the clients: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return SeenTable{}, fmt.Errorf("read a client: %w", err)
		}
		out.Clients = append(out.Clients, name)
	}
	if err := rows.Close(); err != nil {
		return SeenTable{}, fmt.Errorf("read the clients: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `SELECT session_id, client FROM seen`)
	if err != nil {
		return SeenTable{}, fmt.Errorf("read the seen marks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var session, client string
		if err := rows.Scan(&session, &client); err != nil {
			return SeenTable{}, fmt.Errorf("read a seen mark: %w", err)
		}
		out.Seen[session] = append(out.Seen[session], client)
	}
	if err := rows.Err(); err != nil {
		return SeenTable{}, fmt.Errorf("read the seen marks: %w", err)
	}
	return out, nil
}

// ReplaceSeen writes the whole table in one transaction: the daemon's memory is
// the authority while it runs, and this is how a snapshot of it reaches disk.
func (s *Store) ReplaceSeen(ctx context.Context, table SeenTable) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin writing the seen table: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM seen`); err != nil {
		return fmt.Errorf("clear the seen marks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM clients`); err != nil {
		return fmt.Errorf("clear the clients: %w", err)
	}

	now := time.Now().UnixNano()
	for _, name := range table.Clients {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO clients (name, at) VALUES (?, ?)`, name, now); err != nil {
			return fmt.Errorf("write client %q: %w", name, err)
		}
	}
	for session, clients := range table.Seen {
		for _, client := range clients {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO clients (name, at) VALUES (?, ?)`, client, now); err != nil {
				return fmt.Errorf("write client %q: %w", client, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO seen (client, session_id, at) VALUES (?, ?, ?)`,
				client, session, now); err != nil {
				return fmt.Errorf("write a seen mark: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the seen table: %w", err)
	}
	return nil
}

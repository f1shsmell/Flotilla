// Package store is the daemon's durable state: one SQLite database holding the
// events it has accepted, the projections readers query, the receipts of
// effects already applied, and the outbox of effects still owed.
//
// The four live in one file because they commit together. An event that is
// recorded while its projection or its effect is not is a state the daemon
// cannot recover from on the next start: it would replay work it already did,
// or serve a view of work it never recorded.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // pure-Go driver: cross-compilation keeps working
)

// schemaVersion is bumped whenever a migration changes the layout. A database
// written by a newer build is refused rather than read with older assumptions.
//
// 2 adds the task tables the orchestrator keeps its queue in.
const schemaVersion = 2

// ErrNewerSchema reports a database this build is too old to read.
var ErrNewerSchema = errors.New("store: the database was written by a newer build")

// Store is the daemon's state. It is safe for concurrent use: SQLite serialises
// writers, and the mutex keeps one transaction from interleaving with another
// on the same connection pool.
type Store struct {
	db   *sql.DB
	path string

	mu sync.Mutex
}

// Open opens (or creates) the database at path and brings its schema up to
// schemaVersion. The directory is created if it does not exist.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create the state directory: %w", err)
		}
	}

	// WAL keeps a reader from blocking the writer, which matters because the
	// fleet poll reads while turns append. busy_timeout turns the one remaining
	// writer-writer contention into a wait instead of an error.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open the state database: %w", err)
	}
	// One writer is what SQLite wants; a pool larger than that only adds
	// contention. Readers share the connection through WAL.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, path: path}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database. A store that is never closed still works: the
// file is closed when the process exits.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close the state database: %w", err)
	}
	return nil
}

// Path reports the file this store holds.
func (s *Store) Path() string { return s.path }

// migrate creates the schema, or refuses a database from a newer build. It runs
// in one transaction so a crash mid-migration leaves the previous schema whole.
func (s *Store) migrate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the schema migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create the schema: %w", err)
	}

	var version int
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES ('schema_version', ?)`,
			fmt.Sprint(schemaVersion)); err != nil {
			return fmt.Errorf("record the schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read the schema version: %w", err)
	case version > schemaVersion:
		return fmt.Errorf("%s holds schema %d, this build reads %d: %w",
			s.path, version, schemaVersion, ErrNewerSchema)
	case version < schemaVersion:
		// Every statement in schema is idempotent, so an older database has
		// already been brought up to this layout by the Exec above. Recording
		// the new number is what stops the next start from repeating the work
		// and, more importantly, is what makes the version a fact rather than a
		// guess about what ran.
		if _, err := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'schema_version'`,
			fmt.Sprint(schemaVersion)); err != nil {
			return fmt.Errorf("record the schema version: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the schema migration: %w", err)
	}
	return nil
}

// schema is the whole layout. Every table is created with IF NOT EXISTS so a
// second start is a no-op, and every foreign key is declared: a projection row
// whose event is gone is a bug the database should refuse to hold.
const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- events is the log: one row per fact the daemon accepted. seq is the order
-- facts arrived in; a replay reads it to rebuild anything derived.
CREATE TABLE IF NOT EXISTS events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	kind       TEXT    NOT NULL,
	session_id TEXT    NOT NULL DEFAULT '',
	payload    BLOB,
	at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_session ON events (session_id, seq);

-- receipts records an effect already applied, keyed by the event that asked
-- for it and the effect's name. A worker that dies after doing the work but
-- before marking the outbox finds the receipt and does not do it twice.
CREATE TABLE IF NOT EXISTS receipts (
	event_seq  INTEGER NOT NULL REFERENCES events (seq) ON DELETE CASCADE,
	effect     TEXT    NOT NULL,
	at         INTEGER NOT NULL,
	detail     TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (event_seq, effect)
);

-- outbox is the work still owed. A row is written in the same transaction as
-- its event, so a crash cannot leave an accepted event with no effect queued.
CREATE TABLE IF NOT EXISTS outbox (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	event_seq  INTEGER NOT NULL REFERENCES events (seq) ON DELETE CASCADE,
	effect     TEXT    NOT NULL,
	payload    BLOB,
	attempts   INTEGER NOT NULL DEFAULT 0,
	state      TEXT    NOT NULL DEFAULT 'pending',
	last_error TEXT    NOT NULL DEFAULT '',
	at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox (state, id);

-- transcripts is the agent's own log per session: the updates a window folds
-- into its view. seq is the session's own counter (it keeps counting across a
-- trim), not the event log's. The column is named body because update is a
-- reserved word in SQLite.
CREATE TABLE IF NOT EXISTS transcripts (
	session_id TEXT    NOT NULL,
	seq        INTEGER NOT NULL,
	host_seq   INTEGER NOT NULL DEFAULT 0,
	body       BLOB    NOT NULL,
	at         INTEGER NOT NULL,
	PRIMARY KEY (session_id, seq)
);

-- transcript_meta says what a transcript is about. A session with no row here
-- was written before this build recorded metadata, and its fields stay empty.
CREATE TABLE IF NOT EXISTS transcript_meta (
	session_id TEXT PRIMARY KEY,
	mode       TEXT NOT NULL DEFAULT '',
	command    TEXT NOT NULL DEFAULT '',
	dir        TEXT NOT NULL DEFAULT '',
	started    INTEGER NOT NULL DEFAULT 0,
	ended      INTEGER NOT NULL DEFAULT 0
);

-- seen is the per-client read marker: one row per (client, session). It is a
-- projection, not an event, because a read is a view of the work and not work.
CREATE TABLE IF NOT EXISTS seen (
	client     TEXT NOT NULL,
	session_id TEXT NOT NULL,
	at         INTEGER NOT NULL,
	PRIMARY KEY (client, session_id)
);

-- clients is the set of windows the daemon has seen. It bounds the seen table:
-- a window's name never returns, so without a bound a long-lived daemon would
-- keep one entry per launch forever.
CREATE TABLE IF NOT EXISTS clients (
	name TEXT PRIMARY KEY,
	at   INTEGER NOT NULL
);

-- comments are a reviewer's notes on a diff line.
CREATE TABLE IF NOT EXISTS comments (
	id         TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	path       TEXT NOT NULL,
	line       INTEGER NOT NULL,
	side       TEXT NOT NULL,
	text       TEXT NOT NULL,
	author     TEXT NOT NULL,
	at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_session ON comments (session_id);

-- tasks is the orchestrator's queue: one row per unattended job. It is a
-- projection, not an event, in the same sense as seen — the queue is what the
-- daemon is going to do, and the events beside it say how it got there. state
-- is the closed set in internal/orch; a row whose state this build does not
-- know is left alone rather than guessed at.
CREATE TABLE IF NOT EXISTS tasks (
	id         TEXT PRIMARY KEY,
	prompt     TEXT    NOT NULL,
	dialect    TEXT    NOT NULL,
	dir        TEXT    NOT NULL DEFAULT '',
	access     TEXT    NOT NULL DEFAULT '',
	state      TEXT    NOT NULL,
	parent     TEXT    NOT NULL DEFAULT '',
	-- budget_usd bounds what this task may spend; zero means unbounded.
	budget_usd REAL    NOT NULL DEFAULT 0,
	-- spent_usd accumulates what its runs reported. A dialect that reports no
	-- cost leaves this where it was: an unpriced run is not a free one.
	spent_usd  REAL    NOT NULL DEFAULT 0,
	-- runs counts how many times this task was started, and is what the crash
	-- cap counts. It is not reset by a retry: the cap exists for the case where
	-- retrying is the problem.
	runs       INTEGER NOT NULL DEFAULT 0,
	-- resume_id is the conversation a later run continues, when the dialect
	-- reported one.
	resume_id  TEXT    NOT NULL DEFAULT '',
	last_error TEXT    NOT NULL DEFAULT '',
	created    INTEGER NOT NULL,
	updated    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_state ON tasks (state, created);
`

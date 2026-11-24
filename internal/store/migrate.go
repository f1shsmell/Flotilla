package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LegacyLayout names the files a build before this one kept beside the
// database. They are read once, on the first start of a build that has a
// database, and left in place afterwards.
type LegacyLayout struct {
	// Transcripts is the directory of "<hex id>.jsonl" and "<hex id>.meta.json".
	Transcripts string
	// Seen is the path of seen.json.
	Seen string
	// Comments is the directory of "<sanitised id>.json".
	Comments string
}

// MigrationReport says what an import found and did. A count is reported even
// when it is zero, because "nothing to import" and "the import failed" are
// different answers.
type MigrationReport struct {
	// Ran is false when a previous start already imported this layout.
	Ran bool
	// Transcripts, Clients, Seen and Comments are what was imported.
	Transcripts int
	Clients     int
	Seen        int
	Comments    int
	// Problems names files that could not be read. They are reported rather
	// than fatal: one corrupt file must not stop the rest of a user's history
	// from coming across.
	Problems []string
}

// Migrate imports the pre-database layout. It runs at most once: the meta table
// records when, and a second call is a no-op that reports Ran=false.
//
// The files are read, never removed. A user whose history mattered enough to
// keep is not asked to trust an importer with the only copy of it, and a bug
// found later can be re-run against the originals.
func (s *Store) Migrate(ctx context.Context, legacy LegacyLayout) (MigrationReport, error) {
	var report MigrationReport

	s.mu.Lock()
	done, err := s.migratedLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		return report, err
	}
	if done {
		return report, nil
	}

	report.Ran = true

	// Each part imports in its own transaction: one unreadable file must not
	// roll back the parts that are fine.
	if err := s.importTranscripts(ctx, legacy.Transcripts, &report); err != nil {
		return report, err
	}
	if err := s.importSeen(ctx, legacy.Seen, &report); err != nil {
		return report, err
	}
	if err := s.importComments(ctx, legacy.Comments, &report); err != nil {
		return report, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES ('migrated_at', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return report, fmt.Errorf("record the migration: %w", err)
	}
	return report, nil
}

// migratedLocked reports whether a previous start already imported the layout.
func (s *Store) migratedLocked(ctx context.Context) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'migrated_at'`).Scan(&value)
	switch {
	case err == nil:
		return value != "", nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("read the migration record: %w", err)
	}
}

// importTranscripts reads every "<hex id>.jsonl" and its metadata.
func (s *Store) importTranscripts(ctx context.Context, dir string, report *MigrationReport) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read the transcript directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".jsonl")
		raw, err := hex.DecodeString(name)
		if err != nil || len(raw) == 0 {
			report.Problems = append(report.Problems, entry.Name()+": not a session id")
			continue
		}
		id := string(raw)

		records, err := readLegacyTranscript(filepath.Join(dir, entry.Name()))
		if err != nil {
			report.Problems = append(report.Problems, entry.Name()+": "+err.Error())
			continue
		}

		if err := s.writeTranscriptBatch(ctx, id, records); err != nil {
			return err
		}
		report.Transcripts += len(records)

		// The metadata file sits beside the log and may be absent: a log
		// written before this build recorded metadata has none.
		if meta, err := readLegacyMeta(filepath.Join(dir, name+".meta.json")); err == nil {
			if err := s.WriteTranscriptMeta(ctx, meta); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			report.Problems = append(report.Problems, name+".meta.json: "+err.Error())
		}
	}
	return nil
}

// writeTranscriptBatch inserts a session's whole log in one transaction, so an
// interrupted import leaves either the session or nothing rather than a prefix
// that a client would read as the whole history.
func (s *Store) writeTranscriptBatch(ctx context.Context, id string, records []TranscriptRecord) error {
	if len(records) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin importing session %q: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixNano()
	for _, rec := range records {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO transcripts (session_id, seq, host_seq, body, at)
			 VALUES (?, ?, ?, ?, ?)`,
			id, rec.Seq, rec.HostSeq, []byte(rec.Update), now); err != nil {
			return fmt.Errorf("import session %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit importing session %q: %w", id, err)
	}
	return nil
}

// readLegacyTranscript decodes one JSONL log. A line that does not decode is
// skipped: a crash mid-append leaves a partial last line, and the rest is true.
func readLegacyTranscript(path string) ([]TranscriptRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var out []TranscriptRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		var rec TranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// readLegacyMeta decodes one metadata file.
func readLegacyMeta(path string) (TranscriptMeta, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return TranscriptMeta{}, fmt.Errorf("read %s: %w", path, err)
	}
	var meta TranscriptMeta
	if err := json.Unmarshal(payload, &meta); err != nil {
		return TranscriptMeta{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return meta, nil
}

// legacySeenFile is the shape the daemon wrote before the database.
type legacySeenFile struct {
	Present []string            `json:"present"`
	Seen    map[string][]string `json:"seen"`
}

// importSeen reads seen.json: which windows the daemon knew, and which turns
// each of them had read.
func (s *Store) importSeen(ctx context.Context, path string, report *MigrationReport) error {
	if path == "" {
		return nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read the seen table: %w", err)
	}

	var file legacySeenFile
	if err := json.Unmarshal(payload, &file); err != nil {
		report.Problems = append(report.Problems, "seen.json: "+err.Error())
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin importing the seen table: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixNano()
	for _, client := range file.Present {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO clients (name, at) VALUES (?, ?)`, client, now); err != nil {
			return fmt.Errorf("import client %q: %w", client, err)
		}
		report.Clients++
	}
	for id, clients := range file.Seen {
		for _, client := range clients {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO seen (client, session_id, at) VALUES (?, ?, ?)`,
				client, id, now); err != nil {
				return fmt.Errorf("import a seen mark: %w", err)
			}
			report.Seen++
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit importing the seen table: %w", err)
	}
	return nil
}

// importComments reads one JSON array per session.
func (s *Store) importComments(ctx context.Context, dir string, report *MigrationReport) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read the comment directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		session := strings.TrimSuffix(entry.Name(), ".json")

		payload, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			report.Problems = append(report.Problems, entry.Name()+": "+err.Error())
			continue
		}
		var list []legacyComment
		if err := json.Unmarshal(payload, &list); err != nil {
			report.Problems = append(report.Problems, entry.Name()+": "+err.Error())
			continue
		}

		if err := s.writeCommentBatch(ctx, session, list); err != nil {
			return err
		}
		report.Comments += len(list)
	}
	return nil
}

// legacyComment is the comment shape the file store wrote.
type legacyComment struct {
	ID     string    `json:"id"`
	Path   string    `json:"path"`
	Line   int       `json:"line"`
	Side   string    `json:"side"`
	Text   string    `json:"text"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
}

func (s *Store) writeCommentBatch(ctx context.Context, session string, list []legacyComment) error {
	if len(list) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin importing the comments of %q: %w", session, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, c := range list {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO comments
			   (id, session_id, path, line, side, text, author, at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, session, c.Path, c.Line, c.Side, c.Text, c.Author, unixOrZero(c.At)); err != nil {
			return fmt.Errorf("import a comment of %q: %w", session, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit importing the comments of %q: %w", session, err)
	}
	return nil
}

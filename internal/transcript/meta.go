package transcript

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNoMeta reports a session whose log has no metadata file. A log written by
// an older build has none, which is why the id alone still names the session.
var ErrNoMeta = errors.New("transcript: no metadata")

// Meta is what a session's log is about: which agent produced it, where it ran,
// and when it stopped. It sits beside the log rather than inside it, so a trim
// of the log's oldest half never costs the session its identity.
//
// Ended is zero when this build never watched the session end: the daemon
// stopped, or the host went with it. That is not the same as "still running",
// and a reader must say so rather than invent a time.
type Meta struct {
	ID      string    `json:"id"`
	Mode    string    `json:"mode,omitempty"`
	Command string    `json:"command,omitempty"`
	Dir     string    `json:"dir,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
}

// WriteMeta stores m beside the session's log, replacing any earlier copy. The
// write is atomic, so a daemon that dies mid-write leaves the previous metadata
// rather than a half file.
func (s *Store) WriteMeta(m Meta) error {
	if m.ID == "" {
		return fmt.Errorf("write session metadata: empty id: %w", ErrNoMeta)
	}

	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode metadata for session %q: %w", m.ID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create the transcript directory: %w", err)
	}

	final := s.metaPath(m.ID)
	tmp, err := os.CreateTemp(s.dir, ".meta-*.json")
	if err != nil {
		return fmt.Errorf("create temporary metadata: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write metadata for session %q: %w", m.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close metadata for session %q: %w", m.ID, err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fmt.Errorf("publish metadata for session %q: %w", m.ID, err)
	}
	return nil
}

// ReadMeta returns the metadata stored for one session.
func (s *Store) ReadMeta(id string) (Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return readMeta(s.metaPath(id))
}

// List returns one Meta per session this store has ever written, newest first
// by the last thing known about it. A log with no metadata still appears: its
// id is decoded from the file name, and the fields this build never recorded
// stay empty rather than being guessed from a timestamp.
func (s *Store) List() ([]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the transcript directory: %w", err)
	}

	// One entry per session, whichever of its two files was read first: a log
	// and its metadata name the same id, and a session listed twice would be a
	// row the user cannot tell from a second run of the same name.
	byID := map[string]Meta{}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		switch {
		case strings.HasSuffix(name, ".meta.json"):
			meta, err := readMeta(filepath.Join(s.dir, name))
			if err != nil || meta.ID == "" {
				continue
			}
			byID[meta.ID] = meta
		case strings.HasSuffix(name, ".jsonl"):
			id, ok := decodeName(strings.TrimSuffix(name, ".jsonl"))
			if !ok {
				continue
			}
			// Metadata read earlier is the richer copy; a bare id only stands
			// in for a log written before this build recorded any.
			if _, exists := byID[id]; !exists {
				byID[id] = Meta{ID: id}
			}
		}
	}

	out := make([]Meta, 0, len(byID))
	for _, meta := range byID {
		out = append(out, meta)
	}

	// Newest first: what ended most recently is what a user is most likely to
	// come back for. The id breaks ties, so two sessions with nothing recorded
	// keep a stable order rather than one that changes per read.
	sort.Slice(out, func(i, j int) bool {
		a, b := lastSeen(out[i]), lastSeen(out[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// lastSeen is the newest moment known about a session, which orders the list
// without inventing one for a log that recorded nothing.
func lastSeen(m Meta) time.Time {
	if !m.Ended.IsZero() {
		return m.Ended
	}
	return m.Started
}

// readMeta decodes one metadata file. A missing file is ErrNoMeta, which a
// caller treats as "this build recorded nothing" rather than as a failure.
func readMeta(path string) (Meta, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Meta{}, ErrNoMeta
		}
		return Meta{}, fmt.Errorf("read %s: %w", path, err)
	}

	var meta Meta
	if err := json.Unmarshal(payload, &meta); err != nil {
		return Meta{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return meta, nil
}

// metaPath names a session's metadata file the same way its log is named, so
// the two cannot disagree about which id they belong to.
func (s *Store) metaPath(id string) string {
	return filepath.Join(s.dir, hex.EncodeToString([]byte(id))+".meta.json")
}

// decodeName turns a log file's name back into the session id it was written
// for. A name this store did not write is skipped rather than guessed at.
func decodeName(name string) (string, bool) {
	raw, err := hex.DecodeString(name)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

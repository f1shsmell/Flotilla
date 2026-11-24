// Package comments stores the notes a reviewer leaves on a diff line, one file
// per session under the state root.
//
// A comment is an annotation, not a change: it never touches the repository, and
// a lost one costs a sentence, so the store writes atomically and keeps the last
// good file rather than half of a new one.
package comments

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
)

// ErrNoComment reports a comment id this store does not hold.
var ErrNoComment = ipc.Class(errors.New("comments: no such comment"), ipc.CodeNoComment)

// ErrBadComment reports a comment that cannot be stored as asked.
var ErrBadComment = ipc.Class(errors.New("comments: invalid comment"), ipc.CodeBadComment)

// maxTextBytes bounds one note. A review note is a sentence or two; the bound is
// what keeps one runaway client from filling the disk with prose.
const maxTextBytes = 4 << 10

// Side names which version of the file a line number belongs to.
type Side string

const (
	// NewSide is the file after the change, which is what an added line has.
	NewSide Side = "new"
	// OldSide is the file before it, which is what a deleted line has.
	OldSide Side = "old"
)

// Comment is one note on one line of one file.
type Comment struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Line is the line number on Side.
	Line int    `json:"line"`
	Side Side   `json:"side"`
	Text string `json:"text"`
	// Author names who wrote it; "user" until a session can speak for itself.
	Author string    `json:"author"`
	At     time.Time `json:"at"`
}

// Store keeps comments on disk, one file per session.
type Store struct {
	dir string
	mu  *sync.Mutex
}

// NewStore returns a store rooted at dir. It creates nothing until a comment is
// added.
func NewStore(dir string) Store {
	return Store{dir: dir, mu: &sync.Mutex{}}
}

// List returns a session's comments, oldest first.
func (s Store) List(session string) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.read(session)
}

// Add stores one comment and returns it with the identity it was given.
func (s Store) Add(session string, c Comment) (Comment, error) {
	if err := validate(c); err != nil {
		return Comment{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.read(session)
	if err != nil {
		return Comment{}, err
	}

	id, err := newID()
	if err != nil {
		return Comment{}, err
	}

	c.ID = id
	c.At = time.Now().UTC()
	if c.Author == "" {
		c.Author = "user"
	}

	return c, s.write(session, append(existing, c))
}

// Remove deletes one comment.
func (s Store) Remove(session, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.read(session)
	if err != nil {
		return err
	}

	kept := make([]Comment, 0, len(existing))
	for _, c := range existing {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(existing) {
		return fmt.Errorf("remove comment %q from session %q: %w", id, session, ErrNoComment)
	}

	return s.write(session, kept)
}

// validate refuses a comment a reviewer could not have meant: no path, no text,
// a line that does not exist, a side nobody draws.
func validate(c Comment) error {
	if strings.TrimSpace(c.Text) == "" {
		return fmt.Errorf("comment on %q: empty text: %w", c.Path, ErrBadComment)
	}
	if len(c.Text) > maxTextBytes {
		return fmt.Errorf("comment on %q: %d bytes of text, over %d: %w", c.Path, len(c.Text), maxTextBytes, ErrBadComment)
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
	if c.Side != NewSide && c.Side != OldSide {
		return fmt.Errorf("comment on %q: side %q: %w", c.Path, c.Side, ErrBadComment)
	}
	return nil
}

// read loads one session's comments. A missing file is an empty list.
func (s Store) read(session string) ([]Comment, error) {
	payload, err := os.ReadFile(s.path(session))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the comments of session %q: %w", session, err)
	}

	var stored []Comment
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, fmt.Errorf("decode the comments of session %q: %w", session, err)
	}
	return stored, nil
}

// write replaces the file atomically, so a crash leaves the previous list whole.
func (s Store) write(session string, list []Comment) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create the comment directory: %w", err)
	}

	payload, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the comments of session %q: %w", session, err)
	}

	tmp, err := os.CreateTemp(s.dir, ".comments-*.json")
	if err != nil {
		return fmt.Errorf("create a temporary comment file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the comments of session %q: %w", session, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the comments of session %q: %w", session, err)
	}
	if err := os.Rename(tmp.Name(), s.path(session)); err != nil {
		return fmt.Errorf("store the comments of session %q: %w", session, err)
	}
	return nil
}

func (s Store) path(session string) string {
	return filepath.Join(s.dir, sanitize(session)+".json")
}

// sanitize keeps a session id usable as a file name, the way the host registry
// does: a record read from disk decides which file a later read opens.
func sanitize(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// NewID generates the id a new comment is stored under. It is exported because
// the daemon writes comments through the state database now, and an id has to
// be generated the same way whichever store holds the note.
func NewID() (string, error) { return newID() }

func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a comment id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Package session holds the daemon↔host contract and the host registry, so
// both processes read one definition instead of mirroring each other.
//
// A host publishes its record before it starts serving, so a daemon that starts
// later finds it by reading the state directory rather than by remembering
// anything the previous daemon knew.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
)

// ID identifies one session for the life of its host.
type ID string

// Data streams carried on the binary plane for one session.
const (
	// StreamOutput carries agent output from the host to its client.
	StreamOutput uint32 = 1
	// StreamInput carries user keystrokes from the client to the agent.
	StreamInput uint32 = 2
)

// MethodInfo is the call a client uses to ask what a host holds.
const MethodInfo = "session.info"

// Info is what a host reports about the session it holds. It answers "is this
// the same session I left?" without moving any process state.
type Info struct {
	ID ID `json:"id"`
	// Mode is "pty" or "acp": a terminal has no structure to query, and the
	// client renders the two differently.
	Mode    string `json:"mode"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	// State is the lifecycle the session is in when this was asked. A client
	// that attaches gets it in one round trip instead of racing a separate
	// query against a state change.
	State State `json:"state"`
	// Steer is whether the agent takes a message into the running turn.
	Steer bool `json:"steer"`
}

// Record is what a host publishes about itself. It holds no secrets: the
// endpoint and the host process are enough to reach it.
type Record struct {
	// ID is the session this host holds.
	ID ID `json:"id"`
	// Endpoint is structured rather than pre-rendered, so no side has to parse
	// a name back into its parts.
	Endpoint ipc.Address `json:"endpoint"`
	// PID is the host process, so a stale record can be told from a live one.
	PID int `json:"pid"`
	// Started is when the host took the session.
	Started time.Time `json:"started"`
	// Parent is the session that started this one; empty for a person's session.
	Parent ID `json:"parent,omitempty"`
}

// ErrNoRecord reports a session whose host is not registered on this machine.
var ErrNoRecord = ipc.Class(errors.New("session: no record"), ipc.CodeNoRecord)

// Registry is the directory of host records under the state root.
type Registry struct {
	dir string
}

// NewRegistry returns a registry rooted at dir. It creates nothing until a
// record is written.
func NewRegistry(dir string) Registry {
	return Registry{dir: dir}
}

// Publish writes rec, replacing any record for the same session. The write is
// atomic, so a daemon reading concurrently never sees a half-written file.
func (r Registry) Publish(rec Record) error {
	if rec.ID == "" {
		return fmt.Errorf("publish host record: empty session id: %w", ErrNoRecord)
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode host record: %w", err)
	}

	final := r.path(rec.ID)
	tmp, err := os.CreateTemp(r.dir, ".host-*.json")
	if err != nil {
		return fmt.Errorf("create temporary host record: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write host record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close host record: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fmt.Errorf("publish host record %q: %w", final, err)
	}
	return nil
}

// Unpublish removes the record for id. A host that never published is not an
// error: shutdown must not fail on bookkeeping that was never done.
func (r Registry) Unpublish(id ID) error {
	if err := os.Remove(r.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove host record for %q: %w", id, err)
	}
	return nil
}

// List returns every published record. A malformed record is skipped rather
// than failing the whole scan: one bad file must not hide the rest of the fleet.
func (r Registry) List() ([]Record, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session directory %q: %w", r.dir, err)
	}

	records := make([]Record, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		rec, err := r.read(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// Read returns the record for one session.
func (r Registry) Read(id ID) (Record, error) {
	rec, err := r.read(r.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("session %q: %w", id, ErrNoRecord)
		}
		return Record{}, err
	}
	return rec, nil
}

func (r Registry) read(path string) (Record, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err //nolint:wrapcheck // the caller answers existence from errors.Is
	}

	var rec Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return Record{}, fmt.Errorf("decode host record %q: %w", path, err)
	}
	if rec.ID == "" || rec.Endpoint.Name == "" {
		return Record{}, fmt.Errorf("host record %q is incomplete: %w", path, ErrNoRecord)
	}
	return rec, nil
}

func (r Registry) path(id ID) string {
	return filepath.Join(r.dir, sanitize(id)+".json")
}

// sanitize keeps a session id usable as a file name. Ids are generated, but a
// record read from disk decides which file a later read opens.
func sanitize(id ID) string {
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

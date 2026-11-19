package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/f1shsmell/flotilla/internal/session"
)

// seenFile is the on-disk shape of the seen table. Present rides along because
// the eviction bound counts windows: a restart that forgot them would let the
// table grow past its ceiling one launch at a time.
type seenFile struct {
	Present []string            `json:"present"`
	Seen    map[string][]string `json:"seen"`
}

// persistSeenTo loads the seen table from path and keeps it written there. A
// corrupt file starts fresh rather than refusing to serve: the table is a
// convenience, and the next write repairs the file.
func (r *relay) persistSeenTo(path string) error {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// The first launch has nothing to load.
	case err != nil:
		return fmt.Errorf("read the seen table: %w", err)
	default:
		var file seenFile
		if err := json.Unmarshal(data, &file); err != nil {
			r.reportWrite(fmt.Errorf("discard a corrupt seen table: %w", err))
		} else {
			r.mu.Lock()
			for _, client := range file.Present {
				r.present[client] = struct{}{}
			}
			for id, clients := range file.Seen {
				if r.seen[session.ID(id)] == nil {
					r.seen[session.ID(id)] = make(map[string]struct{})
				}
				for _, client := range clients {
					r.seen[session.ID(id)][client] = struct{}{}
				}
			}
			r.mu.Unlock()
		}
	}

	r.seenPath = path
	r.seenWake = make(chan struct{}, 1)
	r.seenStop = make(chan struct{})
	r.seenDone = make(chan struct{})
	go r.writeSeenLoop()
	return nil
}

// scheduleSeenWrite asks the writer to take a fresh snapshot. The send never
// blocks and a pending wake already means "write the latest", so bursts of
// marks coalesce into one write.
func (r *relay) scheduleSeenWrite() {
	if r.seenPath == "" {
		return
	}
	select {
	case r.seenWake <- struct{}{}:
	default:
	}
}

// closeSeen stops the writer after one final snapshot, so a graceful shutdown
// leaves the last mark on disk. It is safe to call more than once.
func (r *relay) closeSeen() {
	if r.seenPath == "" {
		return
	}
	r.seenOnce.Do(func() {
		close(r.seenStop)
		<-r.seenDone
	})
}

// writeSeenLoop is the only writer of the seen file. Snapshots are taken under
// the relay lock and written outside it, so a slow disk never holds the relay.
func (r *relay) writeSeenLoop() {
	defer close(r.seenDone)
	for {
		select {
		case <-r.seenWake:
			r.writeSeenSnapshot()
		case <-r.seenStop:
			r.writeSeenSnapshot()
			return
		}
	}
}

// writeSeenSnapshot copies the table under the lock and publishes it through a
// temporary file, the same atomic shape the registry uses: a reader never sees
// half a table, whatever kills this process mid-write.
func (r *relay) writeSeenSnapshot() {
	r.mu.Lock()
	file := seenFile{
		Present: make([]string, 0, len(r.present)),
		Seen:    make(map[string][]string, len(r.seen)),
	}
	for client := range r.present {
		file.Present = append(file.Present, client)
	}
	for id, marked := range r.seen {
		for client := range marked {
			file.Seen[string(id)] = append(file.Seen[string(id)], client)
		}
	}
	r.mu.Unlock()

	sort.Strings(file.Present)
	for _, clients := range file.Seen {
		sort.Strings(clients)
	}
	payload, err := json.Marshal(file)
	if err != nil {
		r.reportWrite(fmt.Errorf("encode the seen table: %w", err))
		return
	}

	dir := filepath.Dir(r.seenPath)
	tmp, err := os.CreateTemp(dir, ".seen-*.json")
	if err != nil {
		r.reportWrite(fmt.Errorf("create a temporary seen table: %w", err))
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		r.reportWrite(fmt.Errorf("write the seen table: %w", err))
		return
	}
	if err := tmp.Close(); err != nil {
		r.reportWrite(fmt.Errorf("close the seen table: %w", err))
		return
	}
	if err := os.Rename(tmp.Name(), r.seenPath); err != nil {
		r.reportWrite(fmt.Errorf("publish the seen table: %w", err))
	}
}

package daemon

import (
	"context"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
)

// persistSeenTo loads the seen table from the state database and keeps it
// written there. The table is loaded once at start; after that the relay's
// memory is the authority and a background writer snapshots it.
//
// A table that cannot be read is a warning, not a refusal to serve: seen is a
// convenience, and a daemon that started without it still runs the fleet.
func (r *relay) persistSeenTo(state *store.Store) error {
	if state == nil {
		return nil
	}

	table, err := state.LoadSeen(context.Background())
	if err != nil {
		return fmt.Errorf("load the seen table: %w", err)
	}

	r.mu.Lock()
	for _, client := range table.Clients {
		r.present[client] = struct{}{}
	}
	for id, clients := range table.Seen {
		if r.seen[session.ID(id)] == nil {
			r.seen[session.ID(id)] = make(map[string]struct{})
		}
		for _, client := range clients {
			r.seen[session.ID(id)][client] = struct{}{}
		}
	}
	r.mu.Unlock()

	r.state = state
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
	if r.state == nil {
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
	if r.state == nil {
		return
	}
	r.seenOnce.Do(func() {
		close(r.seenStop)
		<-r.seenDone
	})
}

// writeSeenLoop is the only writer of the seen table. Snapshots are taken under
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

// writeSeenSnapshot copies the table under the lock and hands it to the store,
// which writes it in one transaction: a reader never sees half a table,
// whatever kills this process mid-write.
func (r *relay) writeSeenSnapshot() {
	r.mu.Lock()
	table := store.SeenTable{
		Clients: make([]string, 0, len(r.present)),
		Seen:    make(map[string][]string, len(r.seen)),
	}
	for client := range r.present {
		table.Clients = append(table.Clients, client)
	}
	for id, marked := range r.seen {
		for client := range marked {
			table.Seen[string(id)] = append(table.Seen[string(id)], client)
		}
	}
	state := r.state
	r.mu.Unlock()

	if state == nil {
		return
	}
	if err := state.ReplaceSeen(context.Background(), table); err != nil {
		r.reportWrite(fmt.Errorf("write the seen table: %w", err))
	}
}

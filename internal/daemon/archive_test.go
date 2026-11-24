package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/store"
)

// seedFinishedSession writes what a run leaves behind: a log and the metadata
// that says what it was. It is the on-disk state a daemon finds after a restart,
// which is the whole point of the archive — nothing here is in memory.
//
// The database is the one the daemon reads, StateDir/state.db: seeding anything
// else would pass a test that never exercised the real path.
func seedFinishedSession(t *testing.T, dir, id string, meta store.TranscriptMeta, texts ...string) {
	t.Helper()

	s := openSeedStore(t, dir)

	for i, text := range texts {
		payload, err := json.Marshal(session.UpdateMessage{Kind: "message", Text: text})
		if err != nil {
			t.Fatalf("encode an update: %v", err)
		}
		if _, err := s.AppendTranscript(context.Background(), id, uint64(i+1), payload); err != nil {
			t.Fatalf("store an update for %q: %v", id, err)
		}
	}
	if err := s.WriteTranscriptMeta(context.Background(), meta); err != nil {
		t.Fatalf("write metadata for %q: %v", id, err)
	}
	// Closed before the daemon opens it: the archive is read by a process that
	// did not write these rows, which is the case being tested.
	if err := s.Close(); err != nil {
		t.Fatalf("close the seeded store: %v", err)
	}
}

// openSeedStore opens the state database a daemon in this test would open. The
// caller closes it before starting the daemon, so the test exercises the same
// file across a process boundary rather than a shared handle.
func openSeedStore(t *testing.T, dir string) *store.Store {
	t.Helper()

	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open the seeded store: %v", err)
	}
	return s
}

// archivedOf reads one session out of a status report.
func archivedOf(t *testing.T, st Status, id session.ID) (session.ArchivedEntry, bool) {
	t.Helper()

	for _, entry := range st.Archived {
		if entry.ID == id {
			return entry, true
		}
	}
	return session.ArchivedEntry{}, false
}

// TestARestartListsTheSessionsItNoLongerServes is the dogfooding case the
// archive exists for: the window was closed, the daemon was restarted, and the
// work that happened is still findable. It is a listing, not a resurrection —
// the session is not in Sessions, and nothing was spawned to produce this row.
func TestARestartListsTheSessionsItNoLongerServes(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	ended := started.Add(12 * time.Minute)
	seedFinishedSession(t, dir, "sess-finished", store.TranscriptMeta{
		ID:      "sess-finished",
		Mode:    "acp",
		Command: "claude-agent-acp",
		Dir:     `C:\work\repo`,
		Started: started,
		Ended:   ended,
	}, "what the agent said")

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	st := fetchStatus(t, ctx, client)
	if len(st.Sessions) != 0 {
		t.Fatalf("a finished session is listed as a live one: %+v", st.Sessions)
	}

	entry, ok := archivedOf(t, st, "sess-finished")
	if !ok {
		t.Fatalf("the finished session is missing from the archive: %+v", st.Archived)
	}
	if entry.Mode != session.ModeACP || entry.Command != "claude-agent-acp" {
		t.Errorf("archived entry lost what the run was: %+v", entry)
	}
	if !entry.Ended.Equal(ended) {
		t.Errorf("Ended = %v, want %v", entry.Ended, ended)
	}
}

// TestAnArchivedSessionReadsBackItsLog pins the read path: the answer carries
// the run's stored updates, so a window that opens a finished session shows
// what the agent did rather than an empty pane.
func TestAnArchivedSessionReadsBackItsLog(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	seedFinishedSession(t, dir, "sess-log", store.TranscriptMeta{ID: "sess-log", Mode: "acp"},
		"first", "second")

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := client.Call(ctx, MethodSessionArchive, session.ArchiveRequest{ID: "sess-log"})
	if err != nil {
		t.Fatalf("read the archive: %v", err)
	}
	var resp session.ArchiveResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the archive answer: %v", err)
	}

	if len(resp.Updates) != 2 {
		t.Fatalf("archive returned %d updates, want 2: %+v", len(resp.Updates), resp.Updates)
	}
	if resp.Updates[0].Text != "first" || resp.Updates[1].Text != "second" {
		t.Errorf("updates out of order: %+v", resp.Updates)
	}
}

// TestTheArchiveRefusesALiveSession keeps the two paths apart: a session the
// daemon is serving has a process behind it, and reading a snapshot of it would
// invite a reader to type into something that cannot answer.
func TestTheArchiveRefusesALiveSession(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-live", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-live"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, MethodSessionArchive, session.ArchiveRequest{ID: "sess-live"}); err == nil {
		t.Fatal("the archive served a session the daemon is still running")
	}
}

// TestASessionWithNoMetadataIsStillListed pins the upgrade path: a log written
// before this build recorded metadata is still findable, with the fields it
// never had left empty rather than filled from a guess.
func TestASessionWithNoMetadataIsStillListed(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	seed := openSeedStore(t, dir)
	payload, err := json.Marshal(session.UpdateMessage{Kind: "message", Text: "old work"})
	if err != nil {
		t.Fatalf("encode an update: %v", err)
	}
	if _, err := seed.AppendTranscript(context.Background(), "sess-old", 1, payload); err != nil {
		t.Fatalf("store an update: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	entry, ok := archivedOf(t, fetchStatus(t, ctx, client), "sess-old")
	if !ok {
		t.Fatal("a log with no metadata is missing from the archive")
	}
	if entry.Command != "" || entry.Mode != "" || !entry.Ended.IsZero() {
		t.Errorf("the archive invented what it never recorded: %+v", entry)
	}

	raw, err := client.Call(ctx, MethodSessionArchive, session.ArchiveRequest{ID: "sess-old"})
	if err != nil {
		t.Fatalf("read a metadata-less session: %v", err)
	}
	var resp session.ArchiveResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the archive answer: %v", err)
	}
	if len(resp.Updates) != 1 || resp.Updates[0].Text != "old work" {
		t.Errorf("the log of a metadata-less session was not served: %+v", resp.Updates)
	}
}

// TestAnUnknownArchiveIdIsRefused keeps the archive from answering for a name
// nothing wrote: an empty answer would look like a session with no history.
func TestAnUnknownArchiveIdIsRefused(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, MethodSessionArchive, session.ArchiveRequest{ID: "sess-never"}); err == nil {
		t.Fatal("the archive answered for a session nothing ever wrote")
	}
}

// TestAStoppedSessionMovesToTheArchive is the live-to-finished transition: a
// session that was running becomes an archived row once it is stopped, and the
// run's own facts travel with it.
func TestAStoppedSessionMovesToTheArchive(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-ends", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-ends"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	d := runDaemon(t, dir, token)
	// The daemon only learns a session's facts by serving it, so the start is
	// recorded here the way session.start records it.
	d.recordStart("sess-ends", session.ModePTY, "echo-agent", dir)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, MethodSessionStop, session.StopRequest{ID: "sess-ends"}); err != nil {
		t.Fatalf("stop the session: %v", err)
	}
	waitForNoSessions(t, ctx, client, "sess-ends")

	entry, ok := archivedOf(t, fetchStatus(t, ctx, client), "sess-ends")
	if !ok {
		t.Fatal("a stopped session did not reach the archive")
	}
	if entry.Command != "echo-agent" || entry.Mode != session.ModePTY {
		t.Errorf("the archive lost what the run was: %+v", entry)
	}
	if entry.Ended.IsZero() {
		t.Error("the daemon stopped the session itself and recorded no end time")
	}
}

// TestTheArchiveStopsAtItsBound keeps a long-lived machine from pushing every
// run it ever had into the sidebar. The bound cuts the oldest, which is the end
// the list puts last, and the ones left out stay readable by id.
func TestTheArchiveStopsAtItsBound(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	base := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	seed := openSeedStore(t, dir)
	for i := 0; i < archiveLimit+5; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		if err := seed.WriteTranscriptMeta(context.Background(), store.TranscriptMeta{
			ID:    id,
			Ended: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("write metadata for %q: %v", id, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seeded store: %v", err)
	}

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	st := fetchStatus(t, ctx, client)
	if len(st.Archived) != archiveLimit {
		t.Fatalf("the archive lists %d runs, want its bound of %d", len(st.Archived), archiveLimit)
	}

	// Newest first, so the first row is the last run and the oldest five are
	// the ones left out.
	if string(st.Archived[0].ID) != fmt.Sprintf("sess-%03d", archiveLimit+4) {
		t.Errorf("the archive starts at %q, want the newest run", st.Archived[0].ID)
	}
	if _, listed := archivedOf(t, st, "sess-000"); listed {
		t.Error("the archive kept its oldest run past the bound")
	}

	// A run outside the listing is still a record: reading it by id works.
	if _, err := client.Call(ctx, MethodSessionArchive, session.ArchiveRequest{ID: "sess-000"}); err != nil {
		t.Fatalf("read a run the listing left out: %v", err)
	}
}

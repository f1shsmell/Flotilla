package transcript_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/transcript"
)

// TestALogWithNoMetadataIsStillListed pins the upgrade path: a session written
// by a build that recorded no metadata has a log on disk and nothing else, and
// the fleet must still be able to name it rather than hide a run that happened.
func TestALogWithNoMetadataIsStillListed(t *testing.T) {
	store, _ := newStore(t)
	push(t, store, "older-run", 1, "work from before this build")

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d sessions, want 1: %+v", len(list), list)
	}
	if list[0].ID != "older-run" {
		t.Errorf("id = %q, want the id the log was written for", list[0].ID)
	}
	// Nothing was recorded, so nothing is claimed: an empty command is a field
	// the reader shows as unknown, not a guess made from the file's timestamp.
	if list[0].Command != "" || list[0].Mode != "" || list[0].Dir != "" {
		t.Errorf("metadata invented for a log that recorded none: %+v", list[0])
	}
	if !list[0].Ended.IsZero() {
		t.Errorf("Ended = %v, want zero: this build never watched it end", list[0].Ended)
	}
}

// TestOneSessionIsListedOnce is the guard against the two files a session has
// producing two rows: the log and the metadata name the same session.
func TestOneSessionIsListedOnce(t *testing.T) {
	store, _ := newStore(t)
	push(t, store, "one-run", 1, "a turn")

	started := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	ended := started.Add(4 * time.Minute)
	if err := store.WriteMeta(transcript.Meta{
		ID:      "one-run",
		Mode:    "acp",
		Command: "claude-agent-acp",
		Dir:     `C:\work\repo`,
		Started: started,
		Ended:   ended,
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d sessions, want 1: %+v", len(list), list)
	}

	got := list[0]
	if got.Mode != "acp" || got.Command != "claude-agent-acp" || got.Dir != `C:\work\repo` {
		t.Errorf("metadata lost: %+v", got)
	}
	if !got.Ended.Equal(ended) {
		t.Errorf("Ended = %v, want %v", got.Ended, ended)
	}
}

// TestTheNewestEndedSessionComesFirst is the order a person reads the archive
// in: what stopped most recently is what they are most likely to return to.
func TestTheNewestEndedSessionComesFirst(t *testing.T) {
	store, _ := newStore(t)
	base := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

	for i, id := range []string{"oldest", "middle", "newest"} {
		push(t, store, id, 1, "a turn")
		if err := store.WriteMeta(transcript.Meta{
			ID:    id,
			Ended: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("WriteMeta(%q): %v", id, err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"newest", "middle", "oldest"}
	if len(list) != len(want) {
		t.Fatalf("List returned %d sessions, want %d", len(list), len(want))
	}
	for i, id := range want {
		if list[i].ID != id {
			t.Errorf("position %d is %q, want %q", i, list[i].ID, id)
		}
	}
}

// TestWriteMetaReplacesTheEarlierCopy keeps a session's metadata one file: a
// second write is the same session telling a newer truth, not a second record.
func TestWriteMetaReplacesTheEarlierCopy(t *testing.T) {
	store, dir := newStore(t)

	if err := store.WriteMeta(transcript.Meta{ID: "run", Mode: "acp"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := store.WriteMeta(transcript.Meta{ID: "run", Mode: "pty", Command: "claude"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	meta, err := store.ReadMeta("run")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Mode != "pty" || meta.Command != "claude" {
		t.Errorf("the second write did not replace the first: %+v", meta)
	}

	// One file, and the temporary write is gone: a leftover would be a second
	// record the next read has to disambiguate.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the transcript directory: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %d files, want 1: %v", len(entries), names)
	}
}

// TestReadMetaReportsAMissingFile keeps "nothing recorded" distinguishable from
// "recorded as empty": the first is an older build, the second is not a state a
// writer can produce.
func TestReadMetaReportsAMissingFile(t *testing.T) {
	store, _ := newStore(t)

	if _, err := store.ReadMeta("never-written"); err == nil {
		t.Fatal("ReadMeta of a session with no metadata returned no error")
	}
}

// TestTheMetadataFileIsNotReadAsALog guards the naming: a store that scanned
// its own directory for logs must not pick up the metadata files it writes.
func TestTheMetadataFileIsNotReadAsALog(t *testing.T) {
	store, dir := newStore(t)
	if err := store.WriteMeta(transcript.Meta{ID: "run", Mode: "acp"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("unexpected files: %v", entries)
	}

	// The metadata file holds a Meta, not a Record: decoding it as a log would
	// be a silent shape mismatch, which is why the suffixes differ.
	payload, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read the metadata file: %v", err)
	}
	var meta transcript.Meta
	if err := json.Unmarshal(payload, &meta); err != nil {
		t.Fatalf("decode the metadata file as Meta: %v", err)
	}
	if meta.ID != "run" {
		t.Errorf("metadata id = %q", meta.ID)
	}
}

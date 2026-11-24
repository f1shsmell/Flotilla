package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/comments"
	"github.com/f1shsmell/flotilla/internal/store"
	"github.com/f1shsmell/flotilla/internal/transcript"
)

// legacyTree builds the on-disk layout a build before the database left
// behind, using the packages that wrote it. A hand-written fixture would test
// this importer against my idea of the old format rather than the old format.
func legacyTree(t *testing.T) store.LegacyLayout {
	t.Helper()

	root := t.TempDir()
	layout := store.LegacyLayout{
		Transcripts: filepath.Join(root, "transcripts"),
		Seen:        filepath.Join(root, "seen.json"),
		Comments:    filepath.Join(root, "comments"),
	}

	// A transcript with metadata.
	logs := transcript.NewStore(layout.Transcripts)
	for i, text := range []string{"first", "second"} {
		payload, err := json.Marshal(map[string]string{"kind": "message", "text": text})
		if err != nil {
			t.Fatalf("marshal an update: %v", err)
		}
		if _, err := logs.Append("sess-old", uint64(i+1), payload); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	started := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	if err := logs.WriteMeta(transcript.Meta{
		ID:      "sess-old",
		Mode:    "acp",
		Command: "claude-agent-acp",
		Dir:     `C:\work\repo`,
		Started: started,
		Ended:   started.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := logs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A transcript with no metadata: the upgrade path for an older log.
	logs = transcript.NewStore(layout.Transcripts)
	payload, err := json.Marshal(map[string]string{"kind": "message", "text": "bare"})
	if err != nil {
		t.Fatalf("marshal an update: %v", err)
	}
	if _, err := logs.Append("sess-bare", 1, payload); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := logs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The seen table.
	seen := map[string]any{
		"present": []string{"gui-a", "gui-b"},
		"seen":    map[string][]string{"sess-old": {"gui-a"}},
	}
	payload, err = json.Marshal(seen)
	if err != nil {
		t.Fatalf("marshal the seen table: %v", err)
	}
	if err := os.WriteFile(layout.Seen, payload, 0o600); err != nil {
		t.Fatalf("write seen.json: %v", err)
	}

	// Comments on one session.
	notes := comments.NewStore(layout.Comments)
	if _, err := notes.Add("sess-old", comments.Comment{
		Path: "main.go", Line: 12, Side: comments.NewSide, Text: "look here",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	return layout
}

// TestMigrationBringsTheOldLayoutAcross is the upgrade's contract: every kind
// of state a build before the database kept is readable after it.
func TestMigrationBringsTheOldLayoutAcross(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	legacy := legacyTree(t)

	report, err := s.Migrate(ctx, legacy)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !report.Ran {
		t.Fatal("Ran = false, want true on the first import")
	}
	if len(report.Problems) != 0 {
		t.Fatalf("problems = %v, want none", report.Problems)
	}
	if report.Transcripts != 3 {
		t.Errorf("transcripts = %d, want 3", report.Transcripts)
	}
	if report.Clients != 2 {
		t.Errorf("clients = %d, want 2", report.Clients)
	}
	if report.Seen != 1 {
		t.Errorf("seen = %d, want 1", report.Seen)
	}
	if report.Comments != 1 {
		t.Errorf("comments = %d, want 1", report.Comments)
	}

	// The transcript reads back in order, with its metadata.
	page, err := s.TranscriptPage(ctx, "sess-old", store.TranscriptWindow{})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if got := strings.Join(texts(t, page), ","); got != "first,second" {
		t.Fatalf("transcript = %q, want first,second", got)
	}
	meta, err := s.ReadTranscriptMeta(ctx, "sess-old")
	if err != nil {
		t.Fatalf("ReadTranscriptMeta: %v", err)
	}
	if meta.Command != "claude-agent-acp" || meta.Mode != "acp" {
		t.Fatalf("metadata = %+v, want what the old build recorded", meta)
	}

	// The bare log came across with no metadata invented for it.
	list, err := s.TranscriptList(ctx)
	if err != nil {
		t.Fatalf("TranscriptList: %v", err)
	}
	var bare *store.TranscriptMeta
	for i := range list {
		if list[i].ID == "sess-bare" {
			bare = &list[i]
		}
	}
	if bare == nil {
		t.Fatal("the metadata-less log was not imported")
	}
	if bare.Mode != "" || bare.Command != "" {
		t.Fatalf("metadata invented for a bare log: %+v", *bare)
	}
}

// TestMigrationRunsOnce is the idempotence guard: a second start must not
// double every transcript.
func TestMigrationRunsOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	legacy := legacyTree(t)

	if _, err := s.Migrate(ctx, legacy); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	second, err := s.Migrate(ctx, legacy)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if second.Ran {
		t.Fatal("Ran = true on the second call, want false")
	}

	page, err := s.TranscriptPage(ctx, "sess-old", store.TranscriptWindow{})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("records = %d, want 2: the import ran twice", len(page.Records))
	}
}

// TestMigrationLeavesTheOldFilesInPlace is the promise made to a user whose
// history is the only copy: the importer reads, it does not move.
func TestMigrationLeavesTheOldFilesInPlace(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	legacy := legacyTree(t)

	if _, err := s.Migrate(ctx, legacy); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	entries, err := os.ReadDir(legacy.Transcripts)
	if err != nil {
		t.Fatalf("read the transcript directory: %v", err)
	}
	// Two logs and the one metadata file that existed: the bare log never had
	// any, and the importer must not have invented one.
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %d files, want 3: %v", len(entries), names)
	}
	if _, err := os.Stat(legacy.Seen); err != nil {
		t.Fatalf("seen.json is gone: %v", err)
	}
}

// TestMigrationSkipsACorruptFileRatherThanStopping is the failure mode that
// matters: one bad file must not cost the rest of the history.
func TestMigrationSkipsACorruptFileRatherThanStopping(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	legacy := legacyTree(t)

	// A log whose name is not a session id, and one whose metadata is garbage.
	if err := os.WriteFile(filepath.Join(legacy.Transcripts, "not-hex.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write the bad log: %v", err)
	}

	report, err := s.Migrate(ctx, legacy)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(report.Problems) == 0 {
		t.Fatal("problems = none, want the bad file reported")
	}
	// The good sessions still came across.
	if report.Transcripts != 3 {
		t.Fatalf("transcripts = %d, want the three good updates", report.Transcripts)
	}
}

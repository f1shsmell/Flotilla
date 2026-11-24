package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/store"
)

// update marshals one stored update in the shape a host sends it.
func update(t *testing.T, text string) json.RawMessage {
	t.Helper()

	payload, err := json.Marshal(map[string]string{"kind": "message", "text": text})
	if err != nil {
		t.Fatalf("marshal an update: %v", err)
	}
	return payload
}

// push stores one update under a host number and returns its sequence.
func push(t *testing.T, s *store.Store, id string, hostSeq uint64, text string) uint64 {
	t.Helper()

	seq, err := s.AppendTranscript(context.Background(), id, hostSeq, update(t, text))
	if err != nil {
		t.Fatalf("AppendTranscript(%q): %v", text, err)
	}
	return seq
}

// texts renders a page as the texts it carries.
func texts(t *testing.T, page store.TranscriptPage) []string {
	t.Helper()

	out := make([]string, 0, len(page.Records))
	for _, rec := range page.Records {
		var msg struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(rec.Update, &msg); err != nil {
			t.Fatalf("decode a stored update: %v", err)
		}
		out = append(out, msg.Text)
	}
	return out
}

// TestATranscriptSurvivesAReopen is the reason the transcript is durable at
// all: a daemon restart opens the database again and a window still reads what
// the agent did.
func TestATranscriptSurvivesAReopen(t *testing.T) {
	path := dbPath(t)
	ctx := context.Background()

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i, text := range []string{"one", "two", "three"} {
		if _, err := first.AppendTranscript(ctx, "sess-1", uint64(i+1), update(t, text)); err != nil {
			t.Fatalf("AppendTranscript: %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	page, err := second.TranscriptPage(ctx, "sess-1", store.TranscriptWindow{})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if got := strings.Join(texts(t, page), ","); got != "one,two,three" {
		t.Fatalf("transcript = %q, want the three updates in order", got)
	}
}

// TestSequencesKeepCountingAcrossAReopen pins what a client depends on: the
// second open must not hand out a sequence the first one already used, or a
// window asking for "everything after 3" would be told it is up to date.
func TestSequencesKeepCountingAcrossAReopen(t *testing.T) {
	path := dbPath(t)
	ctx := context.Background()

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := uint64(1); i <= 3; i++ {
		if _, err := first.AppendTranscript(ctx, "sess-1", i, update(t, "early")); err != nil {
			t.Fatalf("AppendTranscript: %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	seq, err := second.AppendTranscript(ctx, "sess-1", 4, update(t, "late"))
	if err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}
	if seq != 4 {
		t.Fatalf("sequence = %d, want 4: the counter restarted", seq)
	}
}

// TestAReplayedUpdateIsNotStoredTwice pins the host-number guard: a
// reconnecting daemon is handed the host's buffer again, and one paragraph must
// not become two lines.
func TestAReplayedUpdateIsNotStoredTwice(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	seq, err := s.AppendTranscript(ctx, "sess-1", 7, update(t, "once"))
	if err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}

	again, err := s.AppendTranscript(ctx, "sess-1", 7, update(t, "once"))
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("second Append = %v, want ErrDuplicate", err)
	}
	if again != seq {
		t.Fatalf("duplicate sequence = %d, want %d", again, seq)
	}

	page, err := s.TranscriptPage(ctx, "sess-1", store.TranscriptWindow{})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("records = %d, want 1: the replay was stored twice", len(page.Records))
	}
}

// TestLastHostSeqContinuesAcrossAReusedID is the id-reuse case: a session id
// can come back after its first host is gone, and the new host has to number
// past the old run's or its first update looks like a replay.
func TestLastHostSeqContinuesAcrossAReusedID(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	push(t, s, "sess-1", 5, "old run")

	last, err := s.LastHostSeq(ctx, "sess-1")
	if err != nil {
		t.Fatalf("LastHostSeq: %v", err)
	}
	if last != 5 {
		t.Fatalf("LastHostSeq = %d, want 5", last)
	}
}

// TestATailKeepsTheRecentWorkAndSaysThereIsMore pins the window a client asks
// for: the end of a long log, and an honest flag that older lines exist.
func TestATailKeepsTheRecentWorkAndSaysThereIsMore(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	for i, text := range []string{"a", "b", "c", "d"} {
		push(t, s, "sess-1", uint64(i+1), text)
	}

	page, err := s.TranscriptPage(ctx, "sess-1", store.TranscriptWindow{Tail: 2})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if got := strings.Join(texts(t, page), ","); got != "c,d" {
		t.Fatalf("tail = %q, want c,d", got)
	}
	if !page.More {
		t.Fatal("More = false, want true: older updates exist")
	}
}

// TestAfterResumesFromACursor is what a reconnecting client sends.
func TestAfterResumesFromACursor(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	for i, text := range []string{"a", "b", "c"} {
		push(t, s, "sess-1", uint64(i+1), text)
	}

	page, err := s.TranscriptPage(ctx, "sess-1", store.TranscriptWindow{After: 2})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if got := strings.Join(texts(t, page), ","); got != "c" {
		t.Fatalf("after = %q, want c", got)
	}
}

// TestABudgetAlwaysIncludesOneRecord pins the page's promise: a record larger
// than the budget still comes back, so a caller's cursor always advances.
func TestABudgetAlwaysIncludesOneRecord(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	push(t, s, "sess-1", 1, strings.Repeat("x", 400))
	push(t, s, "sess-1", 2, "small")

	page, err := s.TranscriptPage(ctx, "sess-1", store.TranscriptWindow{Budget: 10})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("records = %d, want exactly 1: a page must advance", len(page.Records))
	}
	if !page.More {
		t.Fatal("More = false, want true: a record was left out")
	}
}

// TestTheTranscriptIsTrimmedWhenItGrowsTooLarge pins the bound: this is a log
// to read, not an archive, and the session keeps working past it.
func TestTheTranscriptIsTrimmedWhenItGrowsTooLarge(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	// Each update is a quarter of the bound, so the fifth crosses it.
	chunk := strings.Repeat("y", store.MaxTranscriptBytes/4)
	for i := 1; i <= 5; i++ {
		push(t, s, "sess-1", uint64(i), chunk)
	}

	page, err := s.TranscriptPage(ctx, "sess-1", store.TranscriptWindow{})
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(page.Records) >= 5 {
		t.Fatalf("records = %d, want the oldest to be gone", len(page.Records))
	}
	// What survives is the tail, and its numbering still points at the newest.
	last := page.Records[len(page.Records)-1]
	if last.Seq != 5 {
		t.Fatalf("newest sequence = %d, want 5: numbering must not restart after a trim", last.Seq)
	}
}

// TestMetadataRemembersWhatARunWas is the archive's read path.
func TestMetadataRemembersWhatARunWas(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.WriteTranscriptMeta(ctx, store.TranscriptMeta{
		ID: "sess-1", Mode: "acp", Command: "claude-agent-acp", Dir: `C:\work`,
	}); err != nil {
		t.Fatalf("WriteTranscriptMeta: %v", err)
	}

	meta, err := s.ReadTranscriptMeta(ctx, "sess-1")
	if err != nil {
		t.Fatalf("ReadTranscriptMeta: %v", err)
	}
	if meta.Mode != "acp" || meta.Command != "claude-agent-acp" {
		t.Fatalf("metadata = %+v, want what was written", meta)
	}

	// A second write replaces the first rather than adding a second row.
	if err := s.WriteTranscriptMeta(ctx, store.TranscriptMeta{ID: "sess-1", Mode: "pty"}); err != nil {
		t.Fatalf("WriteTranscriptMeta: %v", err)
	}
	meta, err = s.ReadTranscriptMeta(ctx, "sess-1")
	if err != nil {
		t.Fatalf("ReadTranscriptMeta: %v", err)
	}
	if meta.Mode != "pty" {
		t.Fatalf("mode = %q, want pty: the second write did not replace the first", meta.Mode)
	}
}

// TestAMissingMetadataRowIsNotAnEmptyOne keeps "nothing recorded" apart from
// "recorded as empty": the first is an older build.
func TestAMissingMetadataRowIsNotAnEmptyOne(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.ReadTranscriptMeta(ctx, "never-written"); !errors.Is(err, store.ErrNoMeta) {
		t.Fatalf("ReadTranscriptMeta = %v, want ErrNoMeta", err)
	}
}

// TestALogWithNoMetadataIsStillListed pins the upgrade path: a transcript
// written before this build recorded metadata is still findable by its id.
func TestALogWithNoMetadataIsStillListed(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	push(t, s, "old-run", 1, "work from before")

	list, err := s.TranscriptList(ctx)
	if err != nil {
		t.Fatalf("TranscriptList: %v", err)
	}
	if len(list) != 1 || list[0].ID != "old-run" {
		t.Fatalf("list = %+v, want the one session", list)
	}
	if list[0].Command != "" || list[0].Mode != "" || !list[0].Ended.IsZero() {
		t.Fatalf("metadata invented for a log that recorded none: %+v", list[0])
	}
}

// TestOneSessionIsListedOnce is the guard against the two sources (transcript
// and metadata) producing two rows for one session.
func TestOneSessionIsListedOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	push(t, s, "sess-1", 1, "a turn")
	if err := s.WriteTranscriptMeta(ctx, store.TranscriptMeta{ID: "sess-1", Mode: "acp"}); err != nil {
		t.Fatalf("WriteTranscriptMeta: %v", err)
	}

	list, err := s.TranscriptList(ctx)
	if err != nil {
		t.Fatalf("TranscriptList: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %+v, want one row per session", list)
	}
	if list[0].Mode != "acp" {
		t.Fatalf("metadata lost: %+v", list[0])
	}
}

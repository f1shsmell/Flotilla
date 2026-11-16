package transcript_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/transcript"
)

// newStore returns a store over a temporary directory that is closed when the
// test ends. The close is not cosmetic: on Windows an open handle keeps the
// directory from being removed.
func newStore(t *testing.T) (*transcript.Store, string) {
	t.Helper()

	dir := t.TempDir()
	store := transcript.NewStore(dir)
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}

// update marshals one store entry in the shape the host sends it.
func update(t *testing.T, text string) json.RawMessage {
	t.Helper()

	payload, err := json.Marshal(session.UpdateMessage{Kind: "message", Text: text})
	if err != nil {
		t.Fatalf("marshal an update: %v", err)
	}
	return payload
}

// push stores one update under a host number and returns its sequence.
func push(t *testing.T, store *transcript.Store, id string, hostSeq uint64, text string) uint64 {
	t.Helper()

	seq, err := store.Append(id, hostSeq, update(t, text))
	if err != nil {
		t.Fatalf("Append(%q): %v", text, err)
	}
	return seq
}

// texts renders a page as the update texts it carries, which is what a test can
// assert on without depending on the envelope.
func texts(t *testing.T, page transcript.Page) []string {
	t.Helper()

	out := make([]string, 0, len(page.Records))
	for _, rec := range page.Records {
		var msg session.UpdateMessage
		if err := json.Unmarshal(rec.Update, &msg); err != nil {
			t.Fatalf("decode a stored update: %v", err)
		}
		out = append(out, msg.Text)
	}
	return out
}

// TestATranscriptSurvivesANewStore is the reason this package exists: a daemon
// restart opens the file again and a window still reads what the agent did.
func TestATranscriptSurvivesANewStore(t *testing.T) {
	store, dir := newStore(t)

	for i, text := range []string{"one", "two", "three"} {
		push(t, store, "sess-1", uint64(i+1), text)
	}
	// A restart releases the handles and opens the same directory again.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := transcript.NewStore(dir)
	t.Cleanup(func() { _ = reopened.Close() })

	page, err := reopened.Page("sess-1", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := texts(t, page); strings.Join(got, ",") != "one,two,three" {
		t.Fatalf("transcript = %v, want the three updates in order", got)
	}
}

// TestSequencesKeepCountingAcrossAReopen pins what a client depends on: the
// second store must not hand out a sequence the first one already used, or a
// window asking for "everything after 3" would be told it is up to date.
func TestSequencesKeepCountingAcrossAReopen(t *testing.T) {
	store, dir := newStore(t)

	for i := uint64(1); i <= 3; i++ {
		push(t, store, "sess-1", i, "early")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := transcript.NewStore(dir)
	t.Cleanup(func() { _ = reopened.Close() })

	seq := push(t, reopened, "sess-1", 4, "late")
	if seq != 4 {
		t.Fatalf("the new update got sequence %d, want 4", seq)
	}

	page, err := reopened.Page("sess-1", transcript.Window{After: 3})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := texts(t, page); len(got) != 1 || got[0] != "late" {
		t.Fatalf("page after 3 = %v, want only the later update", got)
	}
}

// TestASessionWithNoTranscriptIsEmpty pins the first open: a session nobody has
// spoken to yet has no file, which is an empty log rather than an error.
func TestASessionWithNoTranscriptIsEmpty(t *testing.T) {
	store, _ := newStore(t)

	page, err := store.Page("nobody-was-here", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Records) != 0 || page.More {
		t.Fatalf("page = %+v, want an empty log", page)
	}
}

// TestAReplayIsStoredOnce pins the reason a host numbers its updates: a daemon
// that reconnects is handed the host's buffer again, and the same paragraph
// must not become two lines in the log.
func TestAReplayIsStoredOnce(t *testing.T) {
	store, _ := newStore(t)

	first := push(t, store, "sess-1", 7, "the agent's sentence")
	seq, err := store.Append("sess-1", 7, update(t, "the agent's sentence"))
	if !errors.Is(err, transcript.ErrDuplicate) {
		t.Fatalf("a replayed update = %v, want ErrDuplicate", err)
	}
	if seq != first {
		t.Fatalf("the replay reported sequence %d, want the stored %d", seq, first)
	}

	page, err := store.Page("sess-1", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("the log holds %d records, want the update stored once", len(page.Records))
	}

	// A newer update is not a replay, and it continues the log.
	if next := push(t, store, "sess-1", 8, "the next sentence"); next != first+1 {
		t.Fatalf("the next update got sequence %d, want %d", next, first+1)
	}
}

// TestAReplayAfterAReopenIsStillRecognised pins the case a daemon restart
// creates: the buffer the host hands back holds updates the reopened store has
// not seen in this process.
func TestAReplayAfterAReopenIsStillRecognised(t *testing.T) {
	store, dir := newStore(t)

	for i := uint64(1); i <= 2; i++ {
		push(t, store, "sess-1", i, "before the restart")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := transcript.NewStore(dir)
	t.Cleanup(func() { _ = reopened.Close() })

	// The replay is recognised from the stored host number, which a reopen
	// recovers by reading the file's tail.
	stream, err := reopened.Append("sess-1", 2, update(t, "before the restart"))
	if !errors.Is(err, transcript.ErrDuplicate) {
		t.Fatalf("a replay after a reopen = %v, want ErrDuplicate", err)
	}
	if stream != 2 {
		t.Fatalf("the replay reported sequence %d, want 2", stream)
	}

	page, err := reopened.Page("sess-1", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("the log holds %d records after a restart's replay, want 2", len(page.Records))
	}

	// An update with no host number is always stored: a client talking to a host
	// directly numbers nothing, and dropping its updates would lose the log.
	if _, err := reopened.Append("sess-1", 0, update(t, "unnumbered")); err != nil {
		t.Fatalf("Append with no host sequence: %v", err)
	}
	after, err := reopened.Page("sess-1", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(after.Records) != 3 {
		t.Fatalf("the log holds %d records, want the unnumbered one stored", len(after.Records))
	}
}

// TestATailKeepsTheRecentWork pins what a window opening on a long session
// asks for: the last screenful, and an honest note that older work exists.
func TestATailKeepsTheRecentWork(t *testing.T) {
	store, _ := newStore(t)

	for i := uint64(1); i <= 10; i++ {
		push(t, store, "sess-1", i, "line")
	}

	page, err := store.Page("sess-1", transcript.Window{Tail: 4})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Records) != 4 {
		t.Fatalf("tail of 4 returned %d records", len(page.Records))
	}
	if !page.More {
		t.Fatal("a truncated page did not say there was more")
	}
	if page.Records[0].Seq != 7 || page.Records[3].Seq != 10 {
		t.Fatalf("tail holds sequences %d..%d, want 7..10",
			page.Records[0].Seq, page.Records[3].Seq)
	}

	// A tail larger than the log returns the log and claims nothing more.
	whole, err := store.Page("sess-1", transcript.Window{Tail: 99})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(whole.Records) != 10 || whole.More {
		t.Fatalf("tail of 99 = %d records (more=%v), want all 10 and no more",
			len(whole.Records), whole.More)
	}
}

// TestABudgetBoundsOneAnswer pins the wire bound: a page must fit a frame, and
// a cost too high for even one record must still make progress rather than
// answer with nothing forever.
func TestABudgetBoundsOneAnswer(t *testing.T) {
	store, _ := newStore(t)

	for i := uint64(1); i <= 10; i++ {
		push(t, store, "sess-1", i, strings.Repeat("x", 500))
	}

	tight, err := store.Page("sess-1", transcript.Window{Budget: 1024})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(tight.Records) == 0 || len(tight.Records) == 10 {
		t.Fatalf("a 1 KiB budget returned %d records, want a bounded page", len(tight.Records))
	}
	if !tight.More {
		t.Fatal("a bounded page did not say there was more")
	}

	// The newest record is the one a caller needs, so the page is its tail.
	if last := tight.Records[len(tight.Records)-1].Seq; last != 10 {
		t.Fatalf("the page ends at sequence %d, want the newest", last)
	}

	// A budget smaller than one record still returns that record.
	one, err := store.Page("sess-1", transcript.Window{Budget: 10})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(one.Records) != 1 {
		t.Fatalf("a 10-byte budget returned %d records, want one", len(one.Records))
	}
}

// TestAnOversizedTranscriptKeepsItsTail pins the cost: a long turn must not grow
// a file without limit, and what is kept has to be whole records.
func TestAnOversizedTranscriptKeepsItsTail(t *testing.T) {
	store, dir := newStore(t)

	body := strings.Repeat("y", 4<<10)
	for i := uint64(1); i <= 400; i++ {
		push(t, store, "sess-1", i, body)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "736573732d31.jsonl")) // "sess-1"
	if err != nil {
		t.Fatalf("stat the transcript: %v", err)
	}
	if info.Size() > 2*transcript.MaxBytes {
		t.Fatalf("transcript reached %d bytes, past twice the %d bound",
			info.Size(), transcript.MaxBytes)
	}

	reopened := transcript.NewStore(dir)
	t.Cleanup(func() { _ = reopened.Close() })

	page, err := reopened.Page("sess-1", transcript.Window{})
	if err != nil {
		t.Fatalf("Page after trimming: %v", err)
	}
	if len(page.Records) == 0 {
		t.Fatal("trimming kept nothing")
	}
	if len(page.Records) == 400 {
		t.Fatal("trimming dropped nothing")
	}

	// Sequences still increase by one, so the kept tail is contiguous even
	// though its head is gone.
	for i := 1; i < len(page.Records); i++ {
		if page.Records[i].Seq != page.Records[i-1].Seq+1 {
			t.Fatalf("record %d has sequence %d after %d",
				i, page.Records[i].Seq, page.Records[i-1].Seq)
		}
	}
}

// TestAPartialLineDoesNotSpoilTheLog pins the crash case: a write cut in half
// leaves one unreadable line, and the rest of the transcript still reads.
func TestAPartialLineDoesNotSpoilTheLog(t *testing.T) {
	store, dir := newStore(t)

	for i, text := range []string{"kept", "also kept"} {
		push(t, store, "sess-1", uint64(i+1), text)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(dir, "736573732d31.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	if _, err := file.WriteString(`{"seq":3,"update":{"kind":"mess`); err != nil {
		t.Fatalf("write a partial line: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close the transcript: %v", err)
	}

	reopened := transcript.NewStore(dir)
	t.Cleanup(func() { _ = reopened.Close() })

	page, err := reopened.Page("sess-1", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := texts(t, page); strings.Join(got, ",") != "kept,also kept" {
		t.Fatalf("transcript = %v, want the two whole records", got)
	}

	// The partial line must not reuse a sequence: the next append continues
	// after the last whole record.
	if seq := push(t, reopened, "sess-1", 3, "after the crash"); seq != 3 {
		t.Fatalf("the next update got sequence %d, want 3", seq)
	}
}

// TestTwoSessionsDoNotShareAFile pins the key: ids are arbitrary text, and an
// encoding that collapses two ids onto one file would show one session's log in
// another session's window.
func TestTwoSessionsDoNotShareAFile(t *testing.T) {
	store, _ := newStore(t)

	push(t, store, "sess/one", 1, "first session")
	push(t, store, "sess-one", 1, "second session")

	page, err := store.Page("sess/one", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := texts(t, page); len(got) != 1 || got[0] != "first session" {
		t.Fatalf("page = %v, want only the first session's update", got)
	}

	other, err := store.Page("sess-one", transcript.Window{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := texts(t, other); len(got) != 1 || got[0] != "second session" {
		t.Fatalf("page = %v, want only the second session's update", got)
	}
}

package adapters

import (
	"strings"
	"testing"
)

// TestADetectorWithoutPatternsNeverMatches pins the off switch: an agent nobody
// described is never classified, which is the cautious default.
func TestADetectorWithoutPatternsNeverMatches(t *testing.T) {
	d := NewDetector(nil)
	if d.Armed() {
		t.Fatal("a detector with no patterns is armed")
	}
	if pattern, ok := d.Push([]byte("Do you trust this folder?\n")); ok {
		t.Fatalf("an empty detector matched %q", pattern)
	}
}

// TestADetectorMatchesAPrompt pins the reason the detector exists: the line the
// agent prints when it waits for an answer turns the session blocked.
func TestADetectorMatchesAPrompt(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	pattern, ok := d.Push([]byte("Welcome\nDo you trust the files in this folder?\n"))
	if !ok {
		t.Fatal("the prompt did not match")
	}
	if pattern != "do you trust" {
		t.Fatalf("matched %q", pattern)
	}
}

// TestADetectorMatchesALineSplitAcrossReads pins the carried tail: a PTY read
// can end in the middle of the prompt, and the split line is still one line.
func TestADetectorMatchesALineSplitAcrossReads(t *testing.T) {
	d := NewDetector([]string{"allow this tool"})

	if _, ok := d.Push([]byte("Allow th")); ok {
		t.Fatal("matched a half line")
	}
	if _, ok := d.Push([]byte("is tool? [y/N]")); !ok {
		t.Fatal("the second half did not complete the match")
	}
}

// TestADetectorSeesThroughColour pins the escape handling: a prompt wrapped in a
// colour code is still the prompt, and the bytes on the wire carry the code.
func TestADetectorSeesThroughColour(t *testing.T) {
	d := NewDetector([]string{"approve this edit"})

	if _, ok := d.Push([]byte("\x1b[1mApprove this edit\x1b[0m? ")); !ok {
		t.Fatal("a styled prompt did not match")
	}
}

// TestADetectorKeepsItsTailBounded pins the flood path: a writer with no newline
// must not grow the carried line without limit.
func TestADetectorKeepsItsTailBounded(t *testing.T) {
	d := NewDetector([]string{"never appears"})

	for i := 0; i < 4; i++ {
		d.Push([]byte(strings.Repeat("x", 32<<10)))
	}
	if len(d.pending) > maxPendingLine {
		t.Fatalf("the carried line grew to %d bytes", len(d.pending))
	}
}

// TestANewLineDoesNotJoinTheNextOne pins the opposite failure: two complete
// lines must not be spliced into one, or a prompt could be assembled from the
// end of one line and the start of another.
func TestANewLineDoesNotJoinTheNextOne(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	if _, ok := d.Push([]byte("do you trust\n")); !ok {
		t.Fatal("a complete line did not match")
	}

	// The next read starts a new line: nothing carried over, so a pattern split
	// across the boundary cannot match.
	other := NewDetector([]string{"do you trust"})
	if _, ok := other.Push([]byte("nothing here\n")); ok {
		t.Fatal("an unrelated line matched")
	}
	if _, ok := other.Push([]byte("do you")); ok {
		t.Fatal("a fragment after a complete line matched")
	}
}

// TestResetDropsTheCarriedLine pins the cleared-screen case: text before a clear
// is not part of the prompt after it.
func TestResetDropsTheCarriedLine(t *testing.T) {
	d := NewDetector([]string{"trust this folder"})

	d.Push([]byte("trust this "))
	d.Reset()
	if _, ok := d.Push([]byte("folder")); ok {
		t.Fatal("a reset detector completed a line from before the reset")
	}
}

// TestPasteWritesKeepsBracketedAsGiven pins the default: xterm already wrapped
// the paste, so the host adds nothing.
func TestPasteWritesKeepsBracketedAsGiven(t *testing.T) {
	var a Adapter
	writes := a.PasteWrites([]byte("one\ntwo"))
	if len(writes) != 1 || string(writes[0]) != "one\ntwo" {
		t.Fatalf("PasteWrites = %q", writes)
	}
}

// TestPasteWritesLineSplitsOnLineEndings pins the line mode: one write per line,
// with CRLF handled, and no empty trailing submit.
func TestPasteWritesLineSplitsOnLineEndings(t *testing.T) {
	a := Adapter{Paste: Line}
	writes := a.PasteWrites([]byte("one\r\ntwo\n\nthree\n"))

	want := []string{"one", "two", "three"}
	if len(writes) != len(want) {
		t.Fatalf("PasteWrites = %q, want %q", writes, want)
	}
	for i := range want {
		if string(writes[i]) != want[i] {
			t.Fatalf("write %d = %q, want %q", i, writes[i], want[i])
		}
	}
}

// TestPasteWritesCopiesTheBytes pins the memory rule: the caller's buffer may be
// reused by the next read, so every write owns its bytes.
func TestPasteWritesCopiesTheBytes(t *testing.T) {
	var a Adapter
	source := []byte("hello")
	writes := a.PasteWrites(source)
	source[0] = 'j'

	if string(writes[0]) != "hello" {
		t.Fatalf("the write followed the caller's buffer: %q", writes[0])
	}
}

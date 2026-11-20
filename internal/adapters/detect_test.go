package adapters

import (
	"testing"
)

// TestADetectorWithoutPatternsNeverMatches pins the off switch: an agent nobody
// described is never classified, which is the cautious default.
func TestADetectorWithoutPatternsNeverMatches(t *testing.T) {
	d := NewDetector(nil)
	if d.Armed() {
		t.Fatal("a detector with no patterns is armed")
	}
	if pattern, ok := d.Observe("Do you trust this folder?"); ok {
		t.Fatalf("an empty detector matched %q", pattern)
	}
}

// TestADetectorMatchesAPrompt pins the reason the detector exists: the line the
// agent prints when it waits for an answer turns the session blocked.
func TestADetectorMatchesAPrompt(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	pattern, ok := d.Observe("Welcome\nDo you trust the files in this folder?")
	if !ok {
		t.Fatal("the prompt did not match")
	}
	if pattern != "do you trust" {
		t.Fatalf("matched %q", pattern)
	}
}

// TestAnUnchangedSnapshotDoesNotMatchAgain pins the flood path: a screen that
// keeps printing the same prompt is one question, not a new one on every read.
func TestAnUnchangedSnapshotDoesNotMatchAgain(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	if _, ok := d.Observe("Do you trust this folder?"); !ok {
		t.Fatal("the first sight of the prompt did not match")
	}
	if _, ok := d.Observe("Do you trust this folder?"); ok {
		t.Fatal("the same screen matched twice")
	}
}

// TestAnAnsweredPromptStillOnScreenDoesNotRematch pins the way out: after the
// user answers, the prompt may still be visible above the agent's reply. Only
// new or changed lines are read, so the old question cannot block the session
// again while it waits to scroll away.
func TestAnAnsweredPromptStillOnScreenDoesNotRematch(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	if _, ok := d.Observe("Do you trust this folder?"); !ok {
		t.Fatal("the prompt did not match")
	}
	if _, ok := d.Observe("Do you trust this folder?\ny"); ok {
		t.Fatal("the answered prompt re-matched after the reply arrived")
	}
}

// TestAChangedLineMatchesAgain pins the other half of the diff: a line that
// grows is new text. A prompt completed across two reads is one question.
func TestAChangedLineMatchesAgain(t *testing.T) {
	d := NewDetector([]string{"allow this tool"})

	if _, ok := d.Observe("Allow th"); ok {
		t.Fatal("matched a half line")
	}
	if _, ok := d.Observe("Allow this tool? [y/N]"); !ok {
		t.Fatal("the completed line did not match")
	}
}

// TestARedrawnPromptMatches pins the approval-UI case: a full-screen agent
// paints its question with cursor addressing over whatever was there before.
// The painted line is changed text, so it is read.
func TestARedrawnPromptMatches(t *testing.T) {
	d := NewDetector([]string{"approve this edit"})

	if _, ok := d.Observe("an earlier frame"); ok {
		t.Fatal("an unrelated frame matched")
	}
	if _, ok := d.Observe("Approve this edit?"); !ok {
		t.Fatal("the redrawn prompt did not match")
	}
}

// TestAClearAndIdenticalRedrawMatchesAgain pins the re-prompt case: an agent
// that clears the screen and asks the same question again is asking again. The
// clear empties the remembered screen, so the identical text is new.
func TestAClearAndIdenticalRedrawMatchesAgain(t *testing.T) {
	d := NewDetector([]string{"trust this folder"})

	if _, ok := d.Observe("Trust this folder?"); !ok {
		t.Fatal("the first prompt did not match")
	}
	if _, ok := d.Observe(""); ok {
		t.Fatal("a cleared screen matched")
	}
	if _, ok := d.Observe("Trust this folder?"); !ok {
		t.Fatal("the identical re-prompt after a clear did not match")
	}
}

// TestADetectorIsCaseInsensitive pins the matching rule: patterns are declared
// in lower case and the screen is folded to meet them.
func TestADetectorIsCaseInsensitive(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	if _, ok := d.Observe("DO YOU TRUST THIS FOLDER?"); !ok {
		t.Fatal("an upper-case prompt did not match")
	}
}

// TestTwoLinesAreNotSplicedIntoOne pins the opposite failure: a pattern must
// not be assembled from the end of one line and the start of another.
func TestTwoLinesAreNotSplicedIntoOne(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	if _, ok := d.Observe("do you\ntrust"); ok {
		t.Fatal("a pattern split across two lines matched")
	}
}

// TestAPromptCompletedAfterAnAnswerDoesNotRematch pins the split-read race: the
// session blocked on the first half of a prompt, the user answered, and the
// rest of the prompt arrived afterwards. The pattern was already on the screen,
// so its completion is not a new question.
func TestAPromptCompletedAfterAnAnswerDoesNotRematch(t *testing.T) {
	d := NewDetector([]string{"do you trust"})

	if _, ok := d.Observe("Do you trust this fol"); !ok {
		t.Fatal("the first half of the prompt did not match")
	}
	if _, ok := d.Observe("Do you trust this folder?\ny"); ok {
		t.Fatal("the prompt's completion re-matched after the answer")
	}
}

// TestAnAnswerEchoedOntoThePromptLineDoesNotRematch pins the readline case: many
// agents read the answer on the prompt's own line, so the echo changes that
// line. The pattern never left the screen, so it is not asked again.
func TestAnAnswerEchoedOntoThePromptLineDoesNotRematch(t *testing.T) {
	d := NewDetector([]string{"allow this edit"})

	if _, ok := d.Observe("Allow this edit? [y/N]"); !ok {
		t.Fatal("the prompt did not match")
	}
	if _, ok := d.Observe("Allow this edit? [y/N] y"); ok {
		t.Fatal("the answer echoed onto the prompt line re-matched")
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

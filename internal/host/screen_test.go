package host

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

func testScreen(t *testing.T, cols, rows int) *screen {
	t.Helper()
	s := newScreen(cols, rows)
	t.Cleanup(s.close)
	return s
}

// TestScreenJoinsSoftWrappedLines pins the recent-unwrapped half of the read
// source: a prompt the terminal folded at its width is one line to a pattern,
// not two fragments a match can fall between.
func TestScreenJoinsSoftWrappedLines(t *testing.T) {
	s := testScreen(t, 12, 4)
	s.write([]byte("Do you trust this folder?"))

	got := s.detectionText()
	if !strings.Contains(got, "Do you trust this folder?") {
		t.Fatalf("the wrapped prompt was not joined: %q", got)
	}
}

// TestScreenJoinsAtASpaceBoundary pins the fold that lands on a space: the
// terminal broke the line with the space at the last column, and the joined
// prompt keeps that space instead of gluing two words together.
func TestScreenJoinsAtASpaceBoundary(t *testing.T) {
	s := testScreen(t, 13, 4)
	s.write([]byte("Do you trust this folder?"))

	got := s.detectionText()
	if !strings.Contains(got, "Do you trust this folder?") {
		t.Fatalf("the space-boundary fold was not joined: %q", got)
	}
}

// TestScreenReadsRedrawnText pins the visible half: a full-screen agent paints
// its question with cursor addressing, and the read is what the screen shows,
// not the bytes that arrived.
func TestScreenReadsRedrawnText(t *testing.T) {
	s := testScreen(t, 40, 4)
	s.write([]byte("an earlier frame\r\n\x1b[1;1HDo you trust this folder?"))

	got := s.detectionText()
	if !strings.Contains(got, "Do you trust this folder?") {
		t.Fatalf("the redrawn prompt is missing: %q", got)
	}
	if strings.Contains(got, "an earlier frame") {
		t.Fatalf("the overwritten frame is still read: %q", got)
	}
}

// TestScreenClearDropsText pins the clear boundary: text before a clear is not
// part of the screen after it.
func TestScreenClearDropsText(t *testing.T) {
	s := testScreen(t, 40, 4)
	s.write([]byte("Do you trust this folder?"))
	s.write([]byte("\x1b[2J\x1b[H"))

	if got := s.detectionText(); strings.Contains(got, "trust") {
		t.Fatalf("a cleared screen still reads %q", got)
	}
}

// TestScreenKeepsCJKText pins the width handling: Chinese occupies two columns
// on a real terminal, and the read must not lose or space out the characters.
func TestScreenKeepsCJKText(t *testing.T) {
	s := testScreen(t, 20, 4)
	s.write([]byte("是否信任此文件夹？"))

	if got := s.detectionText(); !strings.Contains(got, "是否信任此文件夹？") {
		t.Fatalf("the CJK prompt was mangled: %q", got)
	}
}

// TestScreenResizeReflows pins the resize path: the window narrowed, and the
// screen the detector reads re-wraps the text it already holds instead of
// truncating it the way an in-place resize would.
func TestScreenResizeReflows(t *testing.T) {
	s := testScreen(t, 40, 4)
	s.write([]byte("Do you trust this folder?"))
	s.resize(12, 4)

	got := s.detectionText()
	if !strings.Contains(got, "Do you trust this folder?") {
		t.Fatalf("the prompt did not survive the resize: %q", got)
	}
}

// TestScreenDoesNotJoinShortLines pins the limit of the wrap heuristic: a line
// that ended before the width is a line of its own, and two of them are not
// spliced into one.
func TestScreenDoesNotJoinShortLines(t *testing.T) {
	s := testScreen(t, 40, 4)
	s.write([]byte("do you\r\ntrust"))

	if got := s.detectionText(); !strings.Contains(got, "do you\ntrust") {
		t.Fatalf("two short lines were spliced: %q", got)
	}
}

// TestScreenAnswersTheAgentsQueries pins the drain: an agent that asks for the
// cursor position waits for the answer, and a model nobody reads from would
// stall the read pump on the question.
func TestScreenAnswersTheAgentsQueries(t *testing.T) {
	s := testScreen(t, 20, 4)
	s.write([]byte("\x1b[6n\x1b[cDo you trust this folder?"))

	if got := s.detectionText(); !strings.Contains(got, "Do you trust this folder?") {
		t.Fatalf("the prompt after the queries is missing: %q", got)
	}
}

// TestScreenReportsAWipe pins the reset signal: a chunk that clears the screen
// tells the detector to forget the old question, so an identical re-prompt in
// the same chunk reads as new.
func TestScreenReportsAWipe(t *testing.T) {
	s := testScreen(t, 40, 4)
	if s.write([]byte("ordinary output")) {
		t.Fatal("an ordinary write reported a wipe")
	}
	if !s.write([]byte("y\r\n\x1b[2J\x1b[HDo you trust this folder?")) {
		t.Fatal("a clear in the chunk went unreported")
	}
}

// TestAClearedScreenRepromptMatchesAgain pins the whole reset path with the
// detector: answer, clear and identical re-prompt arrive in one chunk, and the
// re-prompt is a new question.
func TestAClearedScreenRepromptMatchesAgain(t *testing.T) {
	s := testScreen(t, 40, 4)
	d := adapters.NewDetector([]string{"do you trust"})

	s.write([]byte("Do you trust this folder?"))
	if _, ok := d.Observe(s.detectionText()); !ok {
		t.Fatal("the first prompt did not match")
	}

	if s.write([]byte("y\r\n\x1b[2J\x1b[HDo you trust this folder?")) {
		d.Observe("")
	}
	if _, ok := d.Observe(s.detectionText()); !ok {
		t.Fatal("the identical re-prompt after a clear did not match")
	}
}

// TestScreenReportsAltScreenSwitch pins the other wipe: a full-screen agent
// entering the alternate screen leaves the old question behind, so the
// detector must forget it just as on a clear.
func TestScreenReportsAltScreenSwitch(t *testing.T) {
	s := testScreen(t, 20, 4)
	if !s.write([]byte("\x1b[?1049h")) {
		t.Fatal("entering the alternate screen went unreported")
	}
}

// TestScreenResizeReplayLeavesNoWipe pins the reset boundary: an alt-screen
// switch replayed into the rebuilt model belongs to the resize, not to the
// next write.
func TestScreenResizeReplayLeavesNoWipe(t *testing.T) {
	s := testScreen(t, 20, 4)
	s.write([]byte("\x1b[?1049h"))
	s.resize(30, 4)
	if s.write([]byte("ordinary output")) {
		t.Fatal("a replayed alt-screen switch leaked into the next write")
	}
}

// TestScreenAltScreenExitIsNoWipe pins the direction: leaving the alternate
// screen restores the main screen's old content, so an answered prompt coming
// back into view is not a new question.
func TestScreenAltScreenExitIsNoWipe(t *testing.T) {
	s := testScreen(t, 20, 4)
	s.write([]byte("\x1b[?1049h"))
	if s.write([]byte("\x1b[?1049l")) {
		t.Fatal("leaving the alternate screen reported a wipe")
	}
}

// TestScreenRingTrimsInBulkAligned pins the flood bound: the raw history grows
// to twice the limit before one aligned cut, so a flood does not pay a full
// copy per chunk and a replay never starts mid-line.
func TestScreenRingTrimsInBulkAligned(t *testing.T) {
	s := testScreen(t, 20, 4)
	line := append(bytes.Repeat([]byte("x"), 99), '\n')
	s.write(bytes.Repeat(line, 3000))
	s.mu.Lock()
	held := len(s.ring)
	s.mu.Unlock()
	if held != 300000 {
		t.Fatalf("a buffer under twice the limit was trimmed to %d", held)
	}

	s.write(bytes.Repeat(line, 2500))
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) > ptyBufferLimit {
		t.Fatalf("the ring held %d bytes after the bulk cut", len(s.ring))
	}
	if s.ring[0] != 'x' {
		t.Fatalf("the ring starts mid-line: %q", s.ring[:20])
	}
}

// TestScreenWriteAfterCloseIsHarmless pins the teardown window: the read pump
// can deliver one more chunk after the host closed the model, and a query in
// that chunk must not stall on a pipe nobody reads.
func TestScreenWriteAfterCloseIsHarmless(t *testing.T) {
	s := newScreen(20, 4)
	s.close()
	s.write([]byte("\x1b[6nDo you trust this folder?"))
	if got := s.detectionText(); !strings.Contains(got, "Do you trust this folder?") {
		t.Fatalf("the chunk after close did not render: %q", got)
	}
}

// TestScreenDoesNotHoldScrollback pins the flood bound: the detector reads the
// visible screen only, so lines that scroll off are dropped instead of piling
// up for a reader that never comes.
func TestScreenDoesNotHoldScrollback(t *testing.T) {
	s := testScreen(t, 20, 4)
	for i := 0; i < 40; i++ {
		s.write([]byte("a line of output\r\n"))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if got := s.em.ScrollbackLen(); got != 0 {
		t.Fatalf("the model held %d scrollback lines", got)
	}
}

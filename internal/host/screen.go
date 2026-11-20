package host

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/x/vt"
)

// screen is the read source for blocked detection: a model of the visible
// screen fed the same bytes the client renders, so a folded or redrawn prompt
// is read as the user sees it. Only a session with declared patterns keeps one.
// vt v0.1.0 drops wide graphemes straddling its wrap boundary, so a CJK prompt
// folded mid-character can read short; keystrokes are never gated on this.
type screen struct {
	mu      sync.Mutex
	em      *vt.Emulator
	ring    []byte // raw history, replayed into a rebuilt model on resize
	cleared bool   // a write wiped the screen; the next write resets this
}

func newScreen(cols, rows int) *screen {
	s := &screen{}
	s.em = s.emulator(cols, rows)
	return s
}

// emulator builds one model with its response pipe drained: the agent's
// cursor-position queries are answered synchronously, and an unread answer
// would stall the write that carries it.
func (s *screen) emulator(cols, rows int) *vt.Emulator {
	em := vt.NewEmulator(cols, rows)
	// Only entering wipes: leaving restores the main screen's old content.
	em.SetCallbacks(vt.Callbacks{AltScreen: func(on bool) {
		if on {
			s.cleared = true
		}
	}})
	go func() { _, _ = io.Copy(io.Discard, em) }()
	return em
}

// write feeds one chunk and reports whether it wiped the screen: a clear or
// entering the alternate screen ends the old question, so the detector must
// forget what it saw. The erase sequences are recognised in the bytes because
// the emulator offers no erase callback.
func (s *screen) write(chunk []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ring = append(s.ring, chunk...)
	if len(s.ring) >= 2*ptyBufferLimit {
		s.trimRing()
	}
	s.cleared = false
	_, _ = s.em.Write(chunk)
	// The detector never reads scrollback; letting it grow would hold a flood's
	// worth of lines for nothing.
	s.em.Scrollback().Clear()
	return s.cleared ||
		bytes.Contains(chunk, []byte("\x1b[2J")) ||
		bytes.Contains(chunk, []byte("\x1b[3J")) ||
		bytes.Contains(chunk, []byte("\x1bc"))
}

// trimRing cuts the raw history back to the limit in one aligned slice: a
// per-chunk copy would cost a full memmove under flood, and a cut mid-line
// would replay half an escape sequence into the rebuilt model.
func (s *screen) trimRing() {
	start := len(s.ring) - ptyBufferLimit
	if cut := bytes.IndexByte(s.ring[start:], '\n'); cut >= 0 {
		start += cut + 1
	} else {
		start = len(s.ring)
	}
	s.ring = append(make([]byte, 0, len(s.ring)-start), s.ring[start:]...)
}

// resize rebuilds the model at the new size and replays the raw history into
// it: plain text re-wraps at the new width the way a fresh client's terminal
// would, and a full-screen agent repaints itself on the resize anyway.
func (s *screen) resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	em := s.emulator(cols, rows)
	_, _ = em.Write(s.ring)
	em.Scrollback().Clear()
	old := s.em
	s.em = em
	s.stop(old)
}

func (s *screen) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop(s.em)
}

// stop ends a model's drain. vt.Close writes a flag the drain reads without
// synchronization, so the response pipe is closed directly instead: the drain
// sees EOF and exits, and the flag is never touched.
func (s *screen) stop(em *vt.Emulator) {
	//nolint:errcheck // the assertion must panic if vt stops exposing a closable pipe
	_ = em.InputPipe().(io.Closer).Close()
}

// detectionText renders the visible screen as plain text, joining soft wraps: a
// line whose content reaches the last column, or ends one space short of it,
// folded at the width and continues on the next line. Shorter lines keep their
// break — two of them are never spliced into one.
func (s *screen) detectionText() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	cols, rows := s.em.Width(), s.em.Height()
	lines := make([]string, 0, rows)
	var pending string
	for y := 0; y < rows; y++ {
		var line strings.Builder
		last := -1
		for x := 0; x < cols; x++ {
			cell := s.em.CellAt(x, y)
			if cell == nil {
				continue
			}
			line.WriteString(cell.Content)
			if cell.Content != " " {
				last = x
			}
		}
		text := line.String()
		if last >= 0 && last >= cols-2 && y < rows-1 {
			pending += text
			continue
		}
		lines = append(lines, strings.TrimRight(pending+text, " "))
		pending = ""
	}
	return strings.Join(lines, "\n")
}

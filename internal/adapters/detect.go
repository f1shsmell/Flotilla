package adapters

import (
	"strings"
)

// maxPendingLine bounds the partial line a Detector carries between reads. A
// terminal line longer than this is not a prompt; carrying it would grow with a
// log flood, which is exactly when the detector must stay cheap.
const maxPendingLine = 8 << 10

// Detector answers one question about a terminal stream: does this agent look
// like it is waiting for the user. It holds the bytes of an unfinished line
// between reads, because a prompt can be split across two PTY reads.
//
// It is the one place in this build that reads wording (FLOTILLA.md names the
// PTY adapter boundary as the exception), and the answer never leaves the host:
// a match sets the session's lifecycle, and nothing else branches on it.
type Detector struct {
	patterns []string
	pending  []byte
}

// NewDetector builds a detector for one agent's patterns. An empty list yields a
// detector that never matches, which is the cautious default: an agent nobody
// described is not guessed at.
func NewDetector(patterns []string) *Detector {
	kept := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if trimmed := strings.TrimSpace(strings.ToLower(pattern)); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return &Detector{patterns: kept}
}

// Armed reports whether this detector can match at all, so a caller can skip the
// whole path for an agent that named no patterns.
func (d *Detector) Armed() bool { return len(d.patterns) > 0 }

// Push reads one chunk of terminal output and reports the pattern that made it
// look like a prompt, if any. It returns on the first match: one question is
// enough to classify the session, and scanning a flood of output for every
// pattern at once is what a detector must not do.
func (d *Detector) Push(chunk []byte) (string, bool) {
	if !d.Armed() || len(chunk) == 0 {
		return "", false
	}

	// The pending tail joins this read so a line split across reads is one
	// line. It is dropped when it grows past the bound: no prompt is that long.
	line := make([]byte, 0, len(d.pending)+len(chunk))
	line = append(line, d.pending...)
	line = append(line, chunk...)
	d.pending = d.pending[:0]

	text := stripANSI(string(line))

	if !strings.HasSuffix(text, "\n") {
		// An unfinished last line is kept for the next read, bounded so a
		// writer with no newline cannot grow it forever.
		keep := len(text)
		if keep > maxPendingLine {
			keep = maxPendingLine
		}
		d.pending = append(d.pending[:0], text[len(text)-keep:]...)
	}

	lowered := strings.ToLower(text)
	for _, pattern := range d.patterns {
		if strings.Contains(lowered, pattern) {
			return pattern, true
		}
	}
	return "", false
}

// Reset drops the carried line, which is what a caller does when the screen is
// cleared: the text before a clear is not part of the prompt after it.
func (d *Detector) Reset() {
	d.pending = d.pending[:0]
}

// stripANSI removes the escape sequences a terminal writes around styled text.
// Without this a prompt wrapped in a colour code would never match: the bytes on
// the wire are "Allow? ESC[0m", not "Allow?".
func stripANSI(text string) string {
	if !strings.ContainsRune(text, '\x1b') {
		return text
	}

	var out strings.Builder
	out.Grow(len(text))

	for i := 0; i < len(text); i++ {
		if text[i] != '\x1b' {
			out.WriteByte(text[i])
			continue
		}
		// CSI: ESC '[' … final byte in 0x40..0x7e. Anything else is a
		// two-byte escape and the introducer is all that is dropped.
		if i+1 < len(text) && text[i+1] == '[' {
			j := i + 2
			for j < len(text) && (text[j] < 0x40 || text[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		i++
	}
	return out.String()
}

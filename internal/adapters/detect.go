package adapters

import (
	"strings"
)

// Detector answers one question about a terminal session: does the screen the
// agent is showing look like it is waiting for the user. It reads rendered
// snapshots from the host's screen model, where soft wraps are joined and
// cursor addressing applied, so a snapshot line is a line the user can see.
// The answer never leaves the host: a match sets the session's lifecycle.
type Detector struct {
	patterns []string
	// prev is the last folded snapshot: a pattern already on it is not asked again.
	prev string
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

// Observe reads one rendered-screen snapshot and reports the first pattern that
// is new to it. A clear empties the screen, so an identical re-prompt after a
// clear is read as the new question it is; a prompt that never left the screen
// is not asked twice.
func (d *Detector) Observe(text string) (string, bool) {
	if !d.Armed() {
		return "", false
	}

	lowered := strings.ToLower(text)
	prev := d.prev
	d.prev = lowered
	for _, pattern := range d.patterns {
		if strings.Contains(lowered, pattern) && !strings.Contains(prev, pattern) {
			return pattern, true
		}
	}
	return "", false
}

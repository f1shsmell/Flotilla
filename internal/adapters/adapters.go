// Package adapters is the operational knowledge a terminal agent needs, as
// data. A new agent should be a JSON file, not a branch in the host: this
// package defines the fields, validates them, and gives the host one place to
// ask "what does this agent look like when it waits".
//
// None of it crosses the wire. Approval-UI recognition is heuristic by nature,
// and FLOTILLA.md keeps it at the PTY adapter boundary: a renderer that received
// the patterns could start classifying lines itself.
package adapters

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ErrInvalid reports operational knowledge no session could use.
var ErrInvalid = fmt.Errorf("adapter: invalid definition")

// Adapter is the JSON part of an agent definition that the host engine reads.
// Every field is optional: an agent that names none gets the cautious path.
type Adapter struct {
	// Env is the environment the agent's process needs over and above the
	// host's own, keyed by variable name. A value that names an executable on
	// PATH resolves to its absolute path at spawn: an adapter cannot know
	// where a machine keeps its binaries, and a consumer that does not search
	// PATH would fail on a bare name.
	Env map[string]string `json:"env,omitempty"`
	// Blocked lists the patterns that mean "the agent is waiting for the user":
	// an approval prompt, a trust dialog, a question. The host matches them
	// against a terminal line and never uses the match for anything but that
	// classification.
	Blocked []string `json:"blocked,omitempty"`
	// Paste names how the agent's terminal reads a multi-line paste. Empty
	// means bracketed, which is what every current CLI accepts.
	Paste Paste `json:"paste,omitempty"`
	// TrustArgs are the arguments that skip a first-run trust prompt. They are
	// added only when the caller asked for an unattended start.
	TrustArgs []string `json:"trust_args,omitempty"`
	// ResumeArgs are the arguments that reopen a saved session, with the
	// session id substituted for {id}. Empty means this build cannot resume this
	// agent, which the README states rather than pretending otherwise.
	ResumeArgs []string `json:"resume_args,omitempty"`
	// ResumeCommand replaces Command for a resume, for a bridge whose resume is
	// a different binary. Empty means ResumeArgs are given to Command.
	ResumeCommand string `json:"resume_command,omitempty"`
}

// Paste says how a terminal agent reads a multi-line paste.
type Paste string

const (
	// Bracketed wraps a paste in the terminal's bracketed-paste markers, so the
	// agent reads it as one insertion instead of a sequence of enters.
	Bracketed Paste = "bracketed"
	// Line sends a paste line by line, waiting for the agent to be ready
	// between them. It is the fallback for a CLI that has no bracketed mode.
	Line Paste = "line"
	// Raw writes the bytes as given, which is what a full-screen editor
	// expects.
	Raw Paste = "raw"
)

// Valid reports whether p is a paste mode this build understands. Empty is valid
// and means Bracketed.
func (p Paste) Valid() bool {
	switch p {
	case "", Bracketed, Line, Raw:
		return true
	}
	return false
}

// Mode reports the paste behaviour, defaulting an unset field to bracketed.
func (p Paste) Mode() Paste {
	if p == "" {
		return Bracketed
	}
	return p
}

// Bounds on the operational knowledge one adapter may declare. They exist
// because the daemon hands the definition to the host on its command line: a
// pattern list without a ceiling would turn a data file into an argument no
// process can be started with.
const (
	// MaxPatterns bounds how many blocked patterns one agent may declare. A real
	// agent needs a handful; the ceiling keeps the whole definition small.
	MaxPatterns = 64
	// MaxPatternLen bounds one pattern, in bytes.
	MaxPatternLen = 200
	// MaxBytes bounds the whole definition once encoded, which is what the
	// command line carries. It leaves the rest of a Windows command line free
	// for the agent's own arguments.
	MaxBytes = 8 << 10
)

// Validate refuses knowledge a PTY session could not use. An empty field is
// fine, so an adapter that only overrides a command keeps the rest.
func (a Adapter) Validate() error {
	if !a.Paste.Valid() {
		return fmt.Errorf("paste %q is not one of %s, %s or %s: %w",
			a.Paste, Bracketed, Line, Raw, ErrInvalid)
	}
	if len(a.Blocked) > MaxPatterns {
		return fmt.Errorf("%d blocked patterns exceeds %d: %w", len(a.Blocked), MaxPatterns, ErrInvalid)
	}
	for _, pattern := range a.Blocked {
		// An empty pattern matches every line, which would classify a healthy
		// session as blocked for the rest of its life.
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("an empty blocked pattern matches every line: %w", ErrInvalid)
		}
		if len(pattern) > MaxPatternLen {
			return fmt.Errorf("a blocked pattern of %d bytes exceeds %d: %w", len(pattern), MaxPatternLen, ErrInvalid)
		}
	}

	encoded, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode the adapter: %w", err)
	}
	if len(encoded) > MaxBytes {
		return fmt.Errorf("the adapter needs %d bytes, more than %d: %w", len(encoded), MaxBytes, ErrInvalid)
	}
	return nil
}

// Empty reports whether the adapter carries no knowledge at all, so a caller can
// skip sending it to the host.
func (a Adapter) Empty() bool {
	return len(a.Blocked) == 0 &&
		a.Paste == "" &&
		len(a.TrustArgs) == 0 &&
		len(a.ResumeArgs) == 0 &&
		a.ResumeCommand == "" &&
		len(a.Env) == 0
}

// ResumeArgsFor substitutes one session id into the resume arguments. An
// adapter that names no resume arguments has nothing to substitute, and the
// caller learns that from the empty result rather than from a guess.
func (a Adapter) ResumeArgsFor(id string) []string {
	if len(a.ResumeArgs) == 0 {
		return nil
	}

	out := make([]string, 0, len(a.ResumeArgs))
	for _, arg := range a.ResumeArgs {
		out = append(out, strings.ReplaceAll(arg, "{id}", id))
	}
	return out
}

// PasteWrites turns one paste into the writes the agent's terminal expects.
//
// The default, bracketed, adds nothing: xterm already wraps a paste in
// bracketed-paste markers, so wrapping it again would reach the agent as text
// inside text. The mode exists for the two agents that need something else —
// Line for a CLI that reads a multi-line paste as a sequence of submits, and Raw
// for a full-screen editor that must see the bytes untouched.
func (a Adapter) PasteWrites(data []byte) [][]byte {
	switch a.Paste.Mode() {
	case Line:
		// One write per line, without the newline: the host sends the next line
		// only once the agent has printed something, which is what keeps a paste
		// from turning into a burst of unanswered prompts.
		return splitLines(data)
	case Raw:
		return [][]byte{append([]byte(nil), data...)}
	default:
		return [][]byte{append([]byte(nil), data...)}
	}
}

// splitLines cuts a paste on its line endings. An empty trailing line is not a
// write: a paste that ends with a newline should not submit one extra prompt.
func splitLines(data []byte) [][]byte {
	out := make([][]byte, 0, 8)

	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		line := data[start:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			out = append(out, append([]byte(nil), line...))
		}
		start = i + 1
	}
	if start < len(data) {
		out = append(out, append([]byte(nil), data[start:]...))
	}
	return out
}

// Inherit fills an overriding adapter's blanks from the definition it replaces,
// field by field: an adapter that corrects the command but says nothing about
// blocked patterns keeps the ones it inherited. A nil slice is an omission; an
// explicit empty slice is a decision to clear the inherited list.
func Inherit(base, override Adapter) Adapter {
	out := override
	if out.Blocked == nil {
		out.Blocked = base.Blocked
	}
	if out.Paste == "" {
		out.Paste = base.Paste
	}
	if out.TrustArgs == nil {
		out.TrustArgs = base.TrustArgs
	}
	if out.ResumeArgs == nil {
		out.ResumeArgs = base.ResumeArgs
	}
	if out.ResumeCommand == "" {
		out.ResumeCommand = base.ResumeCommand
	}
	return out
}

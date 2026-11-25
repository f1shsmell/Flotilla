package adapters

import (
	"errors"
	"strings"
	"testing"
)

// TestAnEmptyAdapterIsTheCautiousPath pins the default: an agent that says
// nothing about itself gets no heuristics at all, rather than a guess that would
// classify a healthy session as blocked.
func TestAnEmptyAdapterIsTheCautiousPath(t *testing.T) {
	var a Adapter
	if !a.Empty() {
		t.Fatal("a zero adapter is not empty")
	}
	if got := a.Paste.Mode(); got != Bracketed {
		t.Fatalf("the default paste mode is %q, want %q", got, Bracketed)
	}
	if got := a.ResumeArgsFor("sess-1"); got != nil {
		t.Fatalf("an adapter with no resume args produced %q", got)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("an empty adapter was refused: %v", err)
	}
}

// TestAnEmptyBlockedPatternIsRefused pins the one pattern that cannot mean
// anything: an empty string matches every line, which would label the session
// blocked for the rest of its life.
func TestAnEmptyBlockedPatternIsRefused(t *testing.T) {
	err := Adapter{Blocked: []string{"Do you trust", "  "}}.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("Validate = %q, want it to name the field", err)
	}
}

// TestAnUnknownPasteModeIsRefused pins the closed set: a typo in the mode is a
// refusal, not a silent fallback to bracketed.
func TestAnUnknownPasteModeIsRefused(t *testing.T) {
	err := Adapter{Paste: Paste("bracket")}.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate = %v, want ErrInvalid", err)
	}
	for _, mode := range []Paste{"", Bracketed, Line, Raw} {
		if err := (Adapter{Paste: mode}).Validate(); err != nil {
			t.Fatalf("paste %q was refused: %v", mode, err)
		}
	}
}

// TestResumeArgsSubstituteTheSessionId pins the one place an adapter's text
// becomes an argument: {id} is replaced everywhere it appears, and an argument
// without it passes through unchanged.
func TestResumeArgsSubstituteTheSessionId(t *testing.T) {
	a := Adapter{ResumeArgs: []string{"resume", "--id={id}", "--log", "sess-{id}.jsonl"}}

	got := a.ResumeArgsFor("abc123")
	want := []string{"resume", "--id=abc123", "--log", "sess-abc123.jsonl"}
	if len(got) != len(want) {
		t.Fatalf("ResumeArgsFor = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argument %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestInheritKeepsWhatAnOverrideLeavesOut pins the merge: correcting a command
// must not silently drop the patterns that recognise a blocked agent.
func TestInheritKeepsWhatAnOverrideLeavesOut(t *testing.T) {
	base := Adapter{
		Blocked:       []string{"allow this?"},
		Paste:         Line,
		TrustArgs:     []string{"--yes"},
		ResumeArgs:    []string{"resume", "{id}"},
		ResumeCommand: "resume-binary",
		Env:           map[string]string{"THE_BINARY": "binary"},
	}

	got := Inherit(base, Adapter{Blocked: nil})
	if len(got.Blocked) != 1 || got.Paste != Line || len(got.TrustArgs) != 1 ||
		len(got.ResumeArgs) != 2 || got.ResumeCommand != "resume-binary" {
		t.Fatalf("Inherit dropped a field: %+v", got)
	}
	if got.Env["THE_BINARY"] != "binary" {
		t.Fatalf("Env = %v, want the inherited variable", got.Env)
	}
}

// TestAnExplicitEmptyListClearsTheInheritedOne pins the other direction: a
// wrapper that must not receive the inherited arguments has to be able to say so,
// and an empty list is that statement.
func TestAnExplicitEmptyListClearsTheInheritedOne(t *testing.T) {
	base := Adapter{
		Blocked:   []string{"allow this?"},
		TrustArgs: []string{"--yes"},
		Env:       map[string]string{"THE_BINARY": "binary"},
	}

	got := Inherit(base, Adapter{Blocked: []string{}, TrustArgs: []string{}, Env: map[string]string{}})
	if len(got.Blocked) != 0 {
		t.Fatalf("Blocked = %q, want it cleared", got.Blocked)
	}
	if len(got.TrustArgs) != 0 {
		t.Fatalf("TrustArgs = %q, want it cleared", got.TrustArgs)
	}
	if len(got.Env) != 0 {
		t.Fatalf("Env = %v, want it cleared", got.Env)
	}
}

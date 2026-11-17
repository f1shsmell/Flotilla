package main

import (
	"strings"
	"testing"
)

// TestAnEmptyAdapterFlagIsTheCautiousPath pins the default: a host started with
// no definition behaves exactly as before, with no heuristics at all.
func TestAnEmptyAdapterFlagIsTheCautiousPath(t *testing.T) {
	adapter, err := parseAdapter("")
	if err != nil {
		t.Fatalf("parseAdapter(\"\") = %v", err)
	}
	if !adapter.Empty() {
		t.Fatalf("an empty flag produced %+v", adapter)
	}
}

// TestTheAdapterFlagRoundTrips pins the one transport the daemon uses: the JSON
// the catalogue produced is the JSON this process reads back.
func TestTheAdapterFlagRoundTrips(t *testing.T) {
	const encoded = `{"blocked":["Do you trust"],"paste":"line","trust_args":["--yes"],"resume_args":["resume","{id}"]}`

	adapter, err := parseAdapter(encoded)
	if err != nil {
		t.Fatalf("parseAdapter: %v", err)
	}
	if len(adapter.Blocked) != 1 || adapter.Paste != "line" {
		t.Fatalf("adapter = %+v", adapter)
	}
	args := adapter.ResumeArgsFor("sess-9")
	if len(args) != 2 || args[1] != "sess-9" {
		t.Fatalf("ResumeArgsFor = %q", args)
	}
}

// TestAnUnknownFieldInTheAdapterFlagIsRefused pins the strictness: this process
// acts on the definition, so a field it silently skipped would be a pattern its
// author believed was active.
func TestAnUnknownFieldInTheAdapterFlagIsRefused(t *testing.T) {
	_, err := parseAdapter(`{"blocked":["x"],"blocked_patterns":["y"]}`)
	if err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if !strings.Contains(err.Error(), "blocked_patterns") {
		t.Fatalf("error %q does not name the field", err)
	}
}

// TestAnInvalidAdapterFlagIsRefused pins validation at the process boundary: a
// definition the daemon should have refused does not get to run here either.
func TestAnInvalidAdapterFlagIsRefused(t *testing.T) {
	_, err := parseAdapter(`{"paste":"bracket"}`)
	if err == nil {
		t.Fatal("an invalid paste mode was accepted")
	}
}

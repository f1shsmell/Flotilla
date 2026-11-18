package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp/replay"
	"github.com/f1shsmell/flotilla/internal/session"
	"github.com/f1shsmell/flotilla/internal/transcript"
)

// storeTranscript writes a session's log under a fresh FLT_HOME and returns that
// home. The command reads the same path a daemon writes, so the test exercises
// the real location rather than a stand-in.
func storeTranscript(t *testing.T, id string, updates ...session.UpdateMessage) string {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("FLT_HOME", homeDir)

	store := transcript.NewStore(filepath.Join(homeDir, "sessions", "transcripts"))
	t.Cleanup(func() { _ = store.Close() })

	for i, update := range updates {
		payload, err := json.Marshal(update)
		if err != nil {
			t.Fatalf("marshal update %d: %v", i, err)
		}
		if _, err := store.Append(id, uint64(i+1), payload); err != nil {
			t.Fatalf("store update %d: %v", i, err)
		}
	}
	return homeDir
}

// TestReplayExportPrintsTheScenario pins the command's happy path: the stored
// log comes out as a scenario, with the person's message left out.
func TestReplayExportPrintsTheScenario(t *testing.T) {
	storeTranscript(t, "sess-export",
		session.UpdateMessage{Kind: session.KindUser, Text: "what is this?"},
		session.UpdateMessage{Kind: "message", Text: "a fleet manager"},
	)

	var out bytes.Buffer
	if err := run([]string{"replay", "export", "sess-export"}, &out); err != nil {
		t.Fatalf("replay export: %v", err)
	}

	var scenario replay.Scenario
	if err := json.Unmarshal(out.Bytes(), &scenario); err != nil {
		t.Fatalf("the output is not a scenario: %v\n%s", err, out.String())
	}
	if scenario.Name != "sess-export" {
		t.Fatalf("scenario name = %q, want the session id", scenario.Name)
	}
	if len(scenario.Turns) != 1 {
		t.Fatalf("exported %d turns, want the one the log holds", len(scenario.Turns))
	}

	// The question is not in the answer: the replay records each prompt as it
	// arrives, so a scenario carrying it too would show it twice.
	if strings.Contains(out.String(), "what is this?") {
		t.Fatalf("the exported scenario carries the question:\n%s", out.String())
	}
	if got := scenario.Turns[0].Updates[0].Text; got != "a fleet manager" {
		t.Fatalf("the turn's first update is %q", got)
	}
}

// TestReplayExportWritesAFile pins --out: the file has to load back through the
// same gate the replay agent uses, or the recording would fail at run time.
// Flags go before the session, which is the same order probe requires.
func TestReplayExportWritesAFile(t *testing.T) {
	storeTranscript(t, "sess-file",
		session.UpdateMessage{Kind: session.KindUser, Text: "go"},
		session.UpdateMessage{Kind: "message", Text: "done"},
	)

	target := filepath.Join(t.TempDir(), "scenario.json")
	var out bytes.Buffer
	if err := run([]string{"replay", "export", "--out", target, "sess-file"}, &out); err != nil {
		t.Fatalf("replay export --out: %v", err)
	}

	loaded, err := replay.Load(target)
	if err != nil {
		t.Fatalf("the written scenario does not load: %v", err)
	}
	if len(loaded.Turns) != 1 {
		t.Fatalf("loaded %d turns, want one", len(loaded.Turns))
	}
	if !strings.Contains(out.String(), target) {
		t.Fatalf("the command did not report where it wrote: %q", out.String())
	}
}

// TestReplayExportRefusesFlagsAfterTheSession pins the loud failure the flag
// package would otherwise swallow: `replay export sess --out path` leaves --out
// unread and writes to stdout, so the command says so instead.
func TestReplayExportRefusesFlagsAfterTheSession(t *testing.T) {
	storeTranscript(t, "sess-order",
		session.UpdateMessage{Kind: session.KindUser, Text: "go"},
		session.UpdateMessage{Kind: "message", Text: "done"},
	)

	var out bytes.Buffer
	err := run([]string{"replay", "export", "sess-order", "--out", "x.json"}, &out)
	if err == nil || !strings.Contains(err.Error(), "flags go before") {
		t.Fatalf("replay export with a trailing flag = %v, want it refused", err)
	}
}

// TestReplayExportRefusesAnEmptyLog pins the refusal: a scenario built from a
// session that never ran would answer every prompt with nothing.
func TestReplayExportRefusesAnEmptyLog(t *testing.T) {
	t.Setenv("FLT_HOME", t.TempDir())

	var out bytes.Buffer
	err := run([]string{"replay", "export", "no-such-session"}, &out)
	if err == nil {
		t.Fatal("replay export accepted a session with no log")
	}
	if !errors.Is(err, replay.ErrNothingToExport) {
		t.Fatalf("error = %v, want it to wrap ErrNothingToExport", err)
	}
}

// TestReplayNamesItsOwnSyntax keeps a bare or mistyped command from doing
// something surprising: it says what the command is instead.
func TestReplayNamesItsOwnSyntax(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"replay"}, &out); err == nil || !strings.Contains(err.Error(), "export") {
		t.Fatalf("a bare replay = %v, want it to name export", err)
	}

	var unknown bytes.Buffer
	if err := run([]string{"replay", "frobnicate"}, &unknown); err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("an unknown replay command = %v, want it named", err)
	}
}

// TestReplayExportNeedsExactlyOneSession pins the arity: naming none or two
// would silently export the wrong session.
func TestReplayExportNeedsExactlyOneSession(t *testing.T) {
	t.Setenv("FLT_HOME", t.TempDir())

	var out bytes.Buffer
	if err := run([]string{"replay", "export"}, &out); err == nil {
		t.Fatal("replay export accepted no session")
	}

	var two bytes.Buffer
	if err := run([]string{"replay", "export", "a", "b"}, &two); err == nil {
		t.Fatal("replay export accepted two sessions")
	}
}

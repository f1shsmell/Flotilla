package replay

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/f1shsmell/flotilla/internal/session"
)

// log builds a stored log the way the daemon writes one: the person's message,
// then whatever the agent streamed in answer to it.
func log(entries ...session.UpdateMessage) []session.UpdateMessage {
	return entries
}

func user(text string) session.UpdateMessage {
	return session.UpdateMessage{Kind: session.KindUser, Text: text}
}

func message(text string) session.UpdateMessage {
	return session.UpdateMessage{Kind: "message", Text: text}
}

// TestExportSplitsTurnsOnTheUsersMessage pins the boundary: the log records no
// turn marker, and the person's own message is the only thing that says where
// one turn ended and the next began.
func TestExportSplitsTurnsOnTheUsersMessage(t *testing.T) {
	scenario, err := Export(log(
		user("first question"),
		message("first answer"),
		user("second question"),
		message("second answer"),
	))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if len(scenario.Turns) != 2 {
		t.Fatalf("exported %d turns, want the two the log holds", len(scenario.Turns))
	}
	if got := scenario.Turns[1].Updates[0].Text; got != "second answer" {
		t.Fatalf("the second turn starts with %q", got)
	}
}

// TestExportLeavesTheQuestionOut is the reason the boundary and the content are
// different things: the host records each prompt as it arrives, so a scenario
// that also carried the question would show it twice.
func TestExportLeavesTheQuestionOut(t *testing.T) {
	scenario, err := Export(log(
		user("what does this do?"),
		message("it reads files"),
	))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	for i, turn := range scenario.Turns {
		for j, update := range turn.Updates {
			if update.Kind == session.KindUser {
				t.Fatalf("turn %d update %d is the person's own message", i, j)
			}
			if update.Text == "what does this do?" {
				t.Fatalf("turn %d update %d carries the question", i, j)
			}
		}
	}
}

// TestExportKeepsEachKind pins the shape: a thought, a tool call and a message
// survive the trip, because a replay that dropped them would show a different
// session than the one it stands for.
func TestExportKeepsEachKind(t *testing.T) {
	scenario, err := Export(log(
		user("go"),
		session.UpdateMessage{Kind: "thought", Text: "thinking"},
		session.UpdateMessage{Kind: "tool", ToolCallID: "call-1", ToolTitle: "read a.txt"},
		session.UpdateMessage{Kind: "tool", ToolCallID: "call-1", ToolStatus: "completed"},
		session.UpdateMessage{Kind: "plan", Text: "step one"},
		message("done"),
	))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	var kinds []string
	for _, update := range scenario.Turns[0].Updates {
		kinds = append(kinds, update.Kind)
	}
	want := []string{"thought", "tool", "tool", "plan", "message"}
	if len(kinds) != len(want) {
		t.Fatalf("exported %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("exported %v, want %v", kinds, want)
		}
	}
}

// TestExportRefusesALogWithNoAgentUpdate pins the refusal: a scenario built from
// a log the agent never wrote to would answer every prompt with nothing.
func TestExportRefusesALogWithNoAgentUpdate(t *testing.T) {
	if _, err := Export(log(user("hello"))); !errors.Is(err, ErrNothingToExport) {
		t.Fatalf("Export = %v, want ErrNothingToExport", err)
	}
	if _, err := Export(nil); !errors.Is(err, ErrNothingToExport) {
		t.Fatalf("Export(nil) = %v, want ErrNothingToExport", err)
	}
}

// TestExportDropsATurnThatAnsweredNothing keeps the replay from returning an
// empty turn for a prompt that produced no work.
func TestExportDropsATurnThatAnsweredNothing(t *testing.T) {
	scenario, err := Export(log(
		user("first"),
		message("answer"),
		user("second"),
	))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(scenario.Turns) != 1 {
		t.Fatalf("exported %d turns, want the one that produced work", len(scenario.Turns))
	}
}

// TestExportMarksEveryTurnAsFinished pins the only honest stop reason: the log
// does not record why a turn stopped, so "end_turn" is the value that does not
// claim a cancellation nobody made.
func TestExportMarksEveryTurnAsFinished(t *testing.T) {
	scenario, err := Export(log(user("go"), message("done")))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if scenario.Turns[0].StopReason != StopEndTurn {
		t.Fatalf("stop reason = %q, want %q", scenario.Turns[0].StopReason, StopEndTurn)
	}
}

// TestExportHandlesALogWithNoBoundary keeps a log from before prompts were
// recorded replayable: it becomes one turn rather than being refused, because
// the work is real even when its boundaries were never written down.
func TestExportHandlesALogWithNoBoundary(t *testing.T) {
	scenario, err := Export(log(message("old work")))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(scenario.Turns) != 1 || len(scenario.Turns[0].Updates) != 1 {
		t.Fatalf("exported %+v, want one turn with the one update", scenario.Turns)
	}
}

// TestExportedScenarioIsReplayable closes the loop: what Export produces has to
// pass the same checks a hand-written fixture does, or the recording would fail
// at load time instead of at export time.
func TestExportedScenarioIsReplayable(t *testing.T) {
	scenario, err := Export(log(
		user("go"),
		session.UpdateMessage{Kind: "tool", ToolCallID: "call-1", ToolTitle: "read"},
		message("done"),
	))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Decode is the gate a file goes through; an exported scenario has to pass
	// it unchanged.
	payload, err := json.Marshal(scenario)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Decode(payload); err != nil {
		t.Fatalf("the exported scenario is not replayable: %v", err)
	}
}

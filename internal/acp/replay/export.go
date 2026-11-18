package replay

import (
	"encoding/json"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/session"
)

// ErrNothingToExport reports a log with no agent update. A scenario built from
// it would answer every prompt with nothing, which is the hung agent the format
// already refuses.
var ErrNothingToExport = fmt.Errorf("replay: the log holds no agent update: %w", ErrInvalidScenario)

// ErrUnreadableRecord reports a stored record this build cannot decode. It is
// separate from ErrNothingToExport: a log that holds records but none this build
// can read is a version problem, and saying "no update" would send the reader
// looking for a turn that is not missing.
var ErrUnreadableRecord = fmt.Errorf("replay: a stored record cannot be decoded: %w", ErrInvalidScenario)

// Export turns a session's stored log into a scenario a replay can answer from.
//
// The person's own messages are the turn boundaries and are not kept: the host
// records each prompt as it arrives, so a scenario that carried them too would
// show every question twice.
//
// A log that predates the recorded prompt has no boundary to split on. It is
// exported as one turn rather than refused: the work the agent did is still
// worth replaying, and inventing boundaries would be a guess.
func Export(updates []session.UpdateMessage) (Scenario, error) {
	var turns []Turn
	var current *Turn
	agentUpdates := 0

	for _, msg := range updates {
		if msg.Kind == session.KindUser {
			// The person's message opens a turn; a prompt too long for one frame
			// arrives as several of them, and the empty turns they leave behind
			// are dropped below.
			turns = append(turns, Turn{})
			current = &turns[len(turns)-1]
			continue
		}

		update, ok := exportUpdate(msg)
		if !ok {
			continue
		}
		agentUpdates++

		if current == nil {
			turns = append(turns, Turn{})
			current = &turns[len(turns)-1]
		}
		current.Updates = append(current.Updates, update)
	}

	if agentUpdates == 0 {
		return Scenario{}, ErrNothingToExport
	}

	// A turn the agent answered with nothing is dropped: replaying it would
	// return an empty stop reason for a prompt that produced no work, and the
	// next turn answers that prompt instead.
	kept := turns[:0]
	for _, turn := range turns {
		if len(turn.Updates) == 0 {
			continue
		}
		turn.StopReason = StopEndTurn
		kept = append(kept, turn)
	}
	if len(kept) == 0 {
		return Scenario{}, ErrNothingToExport
	}

	return Scenario{Name: "exported", Turns: kept}, nil
}

// exportUpdate maps one stored update onto the scenario's shape. The kinds a
// scenario cannot express are dropped: "other" carries no text, and a tool
// update with no call id would reach the renderer as a blank line.
func exportUpdate(msg session.UpdateMessage) (Update, bool) {
	switch msg.Kind {
	case "message", "thought", "plan":
		if msg.Text == "" {
			return Update{}, false
		}
		return Update{Kind: msg.Kind, Text: msg.Text}, true
	case "tool":
		if msg.ToolCallID == "" {
			return Update{}, false
		}
		return Update{
			Kind:       "tool",
			ToolCallID: msg.ToolCallID,
			ToolTitle:  msg.ToolTitle,
			ToolStatus: msg.ToolStatus,
		}, true
	default:
		return Update{}, false
	}
}

// ExportJSON is Export for a caller holding the log as stored records.
//
// A record this build cannot decode is reported rather than skipped: the
// transcript already dropped lines that would not parse, so one that arrives
// here and fails means the update's shape changed. Skipping it would turn a
// version mismatch into "this session did nothing".
func ExportJSON(raw []json.RawMessage) (Scenario, error) {
	updates := make([]session.UpdateMessage, 0, len(raw))
	for i, payload := range raw {
		var msg session.UpdateMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			return Scenario{}, fmt.Errorf("record %d: %w", i, ErrUnreadableRecord)
		}
		updates = append(updates, msg)
	}
	return Export(updates)
}

// StopEndTurn is the reason a turn exported from a log reports. The log does not
// record why a turn stopped, so the value that means "the agent finished" is the
// only honest one.
const StopEndTurn = "end_turn"

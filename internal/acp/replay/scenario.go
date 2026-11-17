// Package replay is an ACP agent that answers from a recorded scenario instead
// of a model. Demo turns, GIF recordings and the GUI smoke run on it, so none of
// them spend a provider's tokens or depend on a login.
//
// A scenario is the shape the daemon already stores: the updates one turn
// produced and the reason it stopped. Recording is therefore a read of the
// transcript, not a second capture path, and a replayed turn reaches the
// renderer through exactly the messages a live one does.
package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrInvalidScenario reports a file that cannot describe a turn.
var ErrInvalidScenario = errors.New("replay: invalid scenario")

// Scenario is one recorded conversation: what the agent said, in the order it
// said it, for each turn the client prompted.
type Scenario struct {
	// Name identifies the scenario in a log or a picker.
	Name string `json:"name"`
	// Capabilities are what the agent advertised, so a replay exercises the
	// same client paths a live agent would: resume, load, or neither.
	Capabilities Capabilities `json:"capabilities,omitempty"`
	// Turns are answered in order. A client that prompts more often than the
	// scenario has turns falls back to the last one rather than failing: a demo
	// must not break because somebody typed twice.
	Turns []Turn `json:"turns"`
}

// Capabilities is the subset of the agent's advertisement a replay needs to be
// honest about: a client that probes for resume has to see what the recorded
// agent said.
type Capabilities struct {
	LoadSession bool `json:"load_session,omitempty"`
	Resume      bool `json:"resume,omitempty"`
}

// Turn is one prompt's answer: the updates the agent streamed, and the reason
// the turn ended.
type Turn struct {
	// Updates are streamed in order, as the agent sent them.
	Updates []Update `json:"updates,omitempty"`
	// StopReason is the agent's own word: "end_turn", "cancelled" and so on.
	StopReason string `json:"stop_reason,omitempty"`
}

// Update is one streamed update, in the shape the daemon forwards.
type Update struct {
	// Kind is "message", "thought", "tool" or "plan".
	Kind string `json:"kind"`
	// Text is the message or thought text.
	Text string `json:"text,omitempty"`
	// ToolCallID, ToolTitle and ToolStatus describe a tool event.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolTitle  string `json:"tool_title,omitempty"`
	ToolStatus string `json:"tool_status,omitempty"`
}

// Load reads a scenario from path and refuses one that cannot be replayed.
func Load(path string) (Scenario, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("read the replay scenario %q: %w", path, err)
	}
	return Decode(payload)
}

// Decode reads a scenario from bytes. A file that names no turn is refused
// rather than accepted: it would open a session that answers nothing, which is
// indistinguishable from a hung agent.
func Decode(payload []byte) (Scenario, error) {
	var s Scenario
	if err := json.Unmarshal(payload, &s); err != nil {
		return Scenario{}, fmt.Errorf("decode the replay scenario: %w", err)
	}
	if len(s.Turns) == 0 {
		return Scenario{}, fmt.Errorf("a scenario needs at least one turn: %w", ErrInvalidScenario)
	}
	for i, turn := range s.Turns {
		if err := turn.validate(); err != nil {
			return Scenario{}, fmt.Errorf("turn %d: %w", i, err)
		}
	}
	return s, nil
}

// validate refuses a turn the replay cannot express. An empty turn would answer
// a prompt with nothing, which is the session that looks hung; an unknown kind
// would reach the renderer as a blank line, which is a lie about what the agent
// did.
func (t Turn) validate() error {
	if len(t.Updates) == 0 {
		return fmt.Errorf("the turn carries no update: %w", ErrInvalidScenario)
	}
	for i, u := range t.Updates {
		switch u.Kind {
		case "message", "thought", "plan":
			if u.Text == "" {
				return fmt.Errorf("update %d of kind %q carries no text: %w", i, u.Kind, ErrInvalidScenario)
			}
		case "tool":
			if u.ToolCallID == "" {
				return fmt.Errorf("tool update %d names no call: %w", i, ErrInvalidScenario)
			}
		default:
			return fmt.Errorf("update %d has kind %q, want message, thought, tool or plan: %w",
				i, u.Kind, ErrInvalidScenario)
		}
	}
	return nil
}

// Save writes a scenario, so a transcript can be turned into a fixture that
// ships with the build.
func (s Scenario) Save(path string) error {
	payload, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the replay scenario: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("write the replay scenario %q: %w", path, err)
	}
	return nil
}

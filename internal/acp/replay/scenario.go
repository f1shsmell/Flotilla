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

	"github.com/f1shsmell/flotilla/internal/session"
)

// ErrInvalidScenario reports a file that cannot describe a turn.
var ErrInvalidScenario = errors.New("replay: invalid scenario")

// Scenario is one recorded conversation: what the agent said, in the order it
// said it, for each turn the client prompted.
type Scenario struct {
	// Name identifies the scenario in a log or a picker.
	Name string `json:"name"`
	// Capabilities are what the agent advertised, so a replay exercises the same client paths.
	Capabilities Capabilities `json:"capabilities,omitempty"`
	// Surface is what the recorded agent let the client adjust, so a demo exercises its composer bar.
	Surface []session.Control `json:"surface,omitempty"`
	// Turns are answered in order; prompting past the last one repeats it rather than failing.
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
	seen := make(map[string]bool, len(s.Surface))
	for i, control := range s.Surface {
		if seen[control.ID] {
			return Scenario{}, fmt.Errorf("surface %d repeats control %q: %w", i, control.ID, ErrInvalidScenario)
		}
		seen[control.ID] = true
		if err := validateControl(control); err != nil {
			return Scenario{}, fmt.Errorf("surface %d: %w", i, err)
		}
	}
	return s, nil
}

// validateControl refuses a setting the replay cannot honour: a value outside
// the offered choices would render a select whose current value is not in its
// own list, and a write of it would have nowhere to land.
func validateControl(c session.Control) error {
	if c.ID == "" || c.Name == "" {
		return fmt.Errorf("control %q carries no id or name: %w", c.ID, ErrInvalidScenario)
	}
	switch c.Kind {
	case session.ControlBoolean:
		if c.Current != "true" && c.Current != "false" {
			return fmt.Errorf("boolean control %q current %q is not true or false: %w", c.ID, c.Current, ErrInvalidScenario)
		}
	case session.ControlMode, session.ControlSelect:
		if len(c.Choices) == 0 {
			return fmt.Errorf("control %q offers no choice: %w", c.ID, ErrInvalidScenario)
		}
		offered := make(map[string]bool, len(c.Choices))
		grouped, loose := false, false
		for _, choice := range c.Choices {
			if choice.Value == "" {
				return fmt.Errorf("control %q offers a choice with no value: %w", c.ID, ErrInvalidScenario)
			}
			if offered[choice.Value] {
				return fmt.Errorf("control %q offers %q twice: %w", c.ID, choice.Value, ErrInvalidScenario)
			}
			offered[choice.Value] = true
			if choice.Group != "" {
				grouped = true
			} else {
				loose = true
			}
		}
		// The wire's select options are a oneOf, flat or grouped: a mixed list
		// would lose half its choices in the announcement.
		if grouped && loose {
			return fmt.Errorf("control %q mixes grouped and flat choices: %w", c.ID, ErrInvalidScenario)
		}
		if !offered[c.Current] {
			return fmt.Errorf("control %q current %q is not among its choices: %w", c.ID, c.Current, ErrInvalidScenario)
		}
	default:
		return fmt.Errorf("control %q has kind %q, want mode, select or boolean: %w", c.ID, c.Kind, ErrInvalidScenario)
	}
	return nil
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

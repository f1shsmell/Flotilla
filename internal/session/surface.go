package session

// The surface is what an agent lets the client adjust: its mode, its model,
// any toggle it announced. These methods read and write it; the host answers
// them for an ACP session, and a terminal session serves none of them.
const (
	// MethodSurface reads the controls the agent currently exposes.
	MethodSurface = "session.surface"
	// MethodSurfaceSet writes one control's value.
	MethodSurfaceSet = "session.surface.set"
	// MethodSurfaceUpdate is the notification carrying the controls after the
	// agent changed them. It is not a transcript update: a mode switch is a
	// fact about the session, not a line of its conversation.
	MethodSurfaceUpdate = "session.surface.update"
)

// The kinds of control a surface carries. A client renders each kind its own
// way, but the value is always a string so one write path serves them all.
const (
	// ControlMode is the agent's operating mode, from a mode-category config
	// option or from the older session-mode state.
	ControlMode = "mode"
	// ControlSelect is a single-choice option with a list of values.
	ControlSelect = "select"
	// ControlBoolean is an on/off toggle whose current value is "true" or
	// "false".
	ControlBoolean = "boolean"
)

// Control is one adjustable setting, projected from what the agent announced.
// The projection is the host's: the wire carries this shape, never the SDK's.
type Control struct {
	// ID names the control in a write.
	ID string `json:"id"`
	// Name is the label the agent gave it.
	Name string `json:"name"`
	// Kind is mode, select or boolean.
	Kind string `json:"kind"`
	// Current is the value in force, as the agent reports it.
	Current string `json:"current"`
	// Choices are the values a select or mode accepts, in the agent's order.
	// A boolean carries none: its two values are the kind itself.
	Choices []Choice `json:"choices,omitempty"`
}

// Choice is one value a control accepts.
type Choice struct {
	// Value is what a write sends.
	Value string `json:"value"`
	// Name is the label the agent gave the value.
	Name string `json:"name"`
	// Group is the label of the section this value belongs to, empty when the
	// agent offered a flat list.
	Group string `json:"group,omitempty"`
}

// SurfaceResponse is the whole surface, the reply to a read and the payload of
// an update notification alike.
type SurfaceResponse struct {
	Controls []Control `json:"controls"`
}

// SurfaceSetRequest writes one control. A boolean's value is "true" or "false".
type SurfaceSetRequest struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

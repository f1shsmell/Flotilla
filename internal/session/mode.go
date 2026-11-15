package session

// Mode says how a session speaks to its agent.
type Mode string

const (
	// ModePTY runs the agent on a ConPTY and moves raw bytes. It is the
	// fallback that makes any CLI usable.
	ModePTY Mode = "pty"
	// ModeACP speaks the Agent Client Protocol over stdio. Structured sessions
	// are the first-class path.
	ModeACP Mode = "acp"
)

// Valid reports whether m is a mode this build understands.
func (m Mode) Valid() bool { return m == ModePTY || m == ModeACP }

// Methods a client uses on an ACP session. PTY sessions serve none of them: a
// terminal has no structure to query.
const (
	// MethodPrompt sends one turn to the agent.
	MethodPrompt = "session.prompt"
	// MethodCancel stops the current turn.
	MethodCancel = "session.cancel"
	// MethodUpdate is the notification carrying one agent update.
	MethodUpdate = "session.update"
	// MethodPermission is the notification asking the client to decide a tool
	// call.
	MethodPermission = "session.permission"
	// MethodPermissionReply answers a permission request.
	MethodPermissionReply = "session.permission.reply"
)

// MethodAttach binds one client connection to one session. The daemon serves it,
// not the host: after it succeeds, that connection carries the session's calls,
// its updates and its terminal bytes.
const MethodAttach = "session.attach"

// AttachRequest names the session a client wants to drive.
type AttachRequest struct {
	ID ID `json:"id"`
}

// AttachResponse reports what the session holds once the client is bound.
type AttachResponse struct {
	ID   ID   `json:"id"`
	Info Info `json:"info"`
}

// PromptRequest is one turn's input.
type PromptRequest struct {
	// Text is the user's message.
	Text string `json:"text"`
}

// PromptResponse reports why the turn ended.
type PromptResponse struct {
	// StopReason is the agent's own word: "end_turn", "cancelled" and so on.
	StopReason string `json:"stop_reason"`
}

// PermissionQuestion asks the client to decide one tool call.
type PermissionQuestion struct {
	// ID answers this question. The client echoes it back.
	ID string `json:"id"`
	// Title names the tool call in the user's terms.
	Title string `json:"title"`
	// Kind is the tool kind, such as "edit".
	Kind string `json:"kind"`
	// Options are the choices the agent offered.
	Options []string `json:"options"`
}

// PermissionReply is the client's answer to a question.
type PermissionReply struct {
	// ID echoes the question.
	ID string `json:"id"`
	// OptionID is one of the offered options; empty cancels the tool call.
	OptionID string `json:"option_id,omitempty"`
}

// UpdateMessage is one agent update as the daemon and the GUI receive it.
type UpdateMessage struct {
	// Kind is "message", "thought", "tool", "plan" or "other".
	Kind string `json:"kind"`
	// Text is the message or thought text.
	Text string `json:"text,omitempty"`
	// ToolCallID, ToolTitle and ToolStatus describe a tool event.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolTitle  string `json:"tool_title,omitempty"`
	ToolStatus string `json:"tool_status,omitempty"`
}

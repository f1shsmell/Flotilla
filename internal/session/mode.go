package session

import (
	"github.com/f1shsmell/flotilla/internal/comments"
	"github.com/f1shsmell/flotilla/internal/diff"
)

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

// State is the lifecycle a session reports. The five values are a closed set
// (FLOTILLA.md): a caller that cannot classify a session says unknown, and
// unknown is never treated as complete.
type State string

const (
	// StateWorking means the agent is running a turn or producing output.
	StateWorking State = "working"
	// StateBlocked means the agent is waiting for the user to answer it.
	StateBlocked State = "blocked"
	// StateDone means a turn finished and nobody has looked since.
	StateDone State = "done"
	// StateIdle means a turn finished and its result has been seen, or that no
	// turn has run yet. Both mean "ready for input".
	StateIdle State = "idle"
	// StateUnknown means this build cannot classify the session. It does not
	// mean complete: a timeout or a silent terminal proves nothing.
	StateUnknown State = "unknown"
)

// Valid reports whether s is one of the five states.
func (s State) Valid() bool {
	switch s {
	case StateWorking, StateBlocked, StateDone, StateIdle, StateUnknown:
		return true
	}
	return false
}

// MethodState is the notification a host sends when its session's lifecycle
// changes, and the call a client uses to ask for the current value. A state is
// an event: polling a host for it would make the fleet view as slow as its
// slowest host.
const MethodState = "session.state"

// StateUpdate carries one lifecycle change.
type StateUpdate struct {
	ID    ID    `json:"id"`
	State State `json:"state"`
}

// MethodSeen marks a session's finished work as looked at, which is what turns
// done into idle. Seen state is per client: one window looking does not mark
// the work seen for another.
const MethodSeen = "session.seen"

// MethodStop ends a session: the host kills its agent, removes its record and
// stops serving. The daemon answers it on the control plane; on a bound
// connection it forwards to the host, which reaches the same end.
const MethodStop = "session.stop"

// StopRequest names the session to end.
type StopRequest struct {
	ID ID `json:"id"`
}

// MethodReplay asks a host to resend the terminal output it is holding. The
// daemon calls it when a window starts watching a session the daemon has been
// attached to all along: those bytes went to the daemon with no subscriber, and
// this is what brings them back.
const MethodReplay = "session.replay"

// MethodResize sets the terminal's size. A PTY session serves it; a structured
// session has no terminal to size.
const MethodResize = "session.resize"

// ResizeRequest is one terminal resize, in columns and rows.
type ResizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// MethodDiff reads what changed in the session's repository. The daemon answers
// it, not the host: the host holds the agent, the daemon holds the checkout.
const MethodDiff = "session.diff"

// DiffRequest asks for a file list, or for one file's patch.
type DiffRequest struct {
	// Scope is "unstaged", "branch" or "turn".
	Scope string `json:"scope"`
	// Base is the commit a branch left from, or the commit a turn started at.
	// Empty means the daemon's own answer for the session.
	Base string `json:"base,omitempty"`
	// Path asks for one file's patch instead of the list.
	Path string `json:"path,omitempty"`
}

// DiffResponse carries either the list or one patch.
type DiffResponse struct {
	// Files is the changed paths, present when the request named no path.
	Files []diff.File `json:"files,omitempty"`
	// Patch is one file's unified diff, present when the request named a path.
	Patch string `json:"patch,omitempty"`
}

// Methods a reviewer uses on a session's diff. The daemon answers them: a
// comment is an annotation, not a change to the checkout.
const (
	// MethodCommentList returns a session's comments.
	MethodCommentList = "session.comment.list"
	// MethodCommentAdd stores one comment and returns it.
	MethodCommentAdd = "session.comment.add"
	// MethodCommentRemove deletes one comment.
	MethodCommentRemove = "session.comment.remove"
)

// CommentAddRequest is one note a reviewer leaves on a diff line.
type CommentAddRequest struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	// Side is "new" for an added or unchanged line, "old" for a deleted one.
	Side string `json:"side"`
	Text string `json:"text"`
}

// CommentRemoveRequest names the comment to delete.
type CommentRemoveRequest struct {
	ID string `json:"id"`
}

// CommentListResponse carries a session's comments, oldest first.
type CommentListResponse struct {
	Comments []comments.Comment `json:"comments"`
}

// Methods a review ends with. The daemon answers them for the same reason it
// answers the diff: they write to the checkout, not to the agent's conversation.
//
// Each one is an explicit request from a person. Nothing here runs on its own
// and nothing force-pushes: a rejected push is a decision left to the user.
const (
	// MethodCommit stages every change and commits it.
	MethodCommit = "session.commit"
	// MethodPush sends the current branch to origin.
	MethodPush = "session.push"
	// MethodPullRequest opens a pull request for the current branch with gh.
	MethodPullRequest = "session.pr"
)

// CommitRequest names the message one commit should carry. An empty message is
// refused rather than invented.
type CommitRequest struct {
	Message string `json:"message"`
}

// PullRequestRequest names what the pull request says.
type PullRequestRequest struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// Base is the branch to merge into. Empty lets gh use the repository's
	// default, which is what a caller who did not think about it wants.
	Base string `json:"base,omitempty"`
}

// WriteResponse is what a commit, a push or a pull request produced. Summary is
// one line fit for a log or a toast; Ref names the handle (a commit id, a
// branch, a pull request URL) when the action has one.
type WriteResponse struct {
	Summary string `json:"summary"`
	Ref     string `json:"ref,omitempty"`
	Output  string `json:"output,omitempty"`
}

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
	// History is what the agent produced before this client attached, oldest
	// first. The daemon keeps it, because the host's own buffer is bounded and
	// dies with the process: a window that opens a long turn should still see
	// how it got to where it is.
	History []UpdateMessage `json:"history,omitempty"`
	// HistoryMore is true when older updates exist and were left out, so the
	// window does not present its first line as the session's first line.
	HistoryMore bool `json:"history_more,omitempty"`
	// Surface is the controls the agent exposes, empty for a terminal session
	// or an agent that announced none. It rides the attach reply so the first
	// screen has its selectors before any notification arrives.
	Surface []Control `json:"surface,omitempty"`
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
	Options []PermissionOption `json:"options"`
}

// PermissionOption is one choice, carrying the ACP kind so a client reads what
// the choice does rather than guessing from its id.
type PermissionOption struct {
	// ID is the value an answer returns.
	ID string `json:"id"`
	// Kind is the ACP kind, such as "allow_once" or "reject_once".
	Kind string `json:"kind"`
	// Name is the label the agent gave the choice for a person to read. It is
	// empty when the agent offered none, and a client then falls back to ID —
	// which is the protocol's spelling, not something to show if a better word
	// exists.
	Name string `json:"name,omitempty"`
}

// PermissionReply is the client's answer to a question.
type PermissionReply struct {
	// ID echoes the question.
	ID string `json:"id"`
	// OptionID is one of the offered options; empty cancels the tool call.
	OptionID string `json:"option_id,omitempty"`
}

// MethodHistory returns the agent updates a session produced before the caller
// started watching. The daemon answers it from its transcript, so a window that
// opens a long turn shows what the agent did instead of an empty log.
const MethodHistory = "session.history"

// HistoryRequest selects the part of a transcript the caller wants.
type HistoryRequest struct {
	// After restricts the answer to updates past this sequence, which is what a
	// reconnecting client sends. Zero asks for the tail of the whole log.
	After uint64 `json:"after,omitempty"`
	// Tail bounds the answer by count. Zero lets the daemon choose.
	Tail int `json:"tail,omitempty"`
}

// HistoryResponse carries the updates a caller missed, oldest first.
type HistoryResponse struct {
	Updates []UpdateMessage `json:"updates"`
	// More is true when older updates exist and were left out, so a window does
	// not present the first line it has as the session's first line.
	More bool `json:"more,omitempty"`
}

// KindUser is the update the host records for the person's own message. The
// agent reports only its own side of the conversation, so a log built from its
// updates alone leaves out the message every turn is an answer to.
const KindUser = "user"

// UpdateMessage is one agent update as the daemon and the GUI receive it.
type UpdateMessage struct {
	// Seq places the update in the session's log. The host leaves it zero; the
	// daemon fills it in as it stores the update, which is what lets a client
	// tell an update it already has from one it does not.
	Seq uint64 `json:"seq,omitempty"`
	// HostSeq is the number the host gave this update as it produced it. A host
	// replays its buffer to a daemon that reconnects, so the daemon needs a way
	// to recognise an update its log already holds; without one, a restart would
	// store and show the same work twice.
	HostSeq uint64 `json:"host_seq,omitempty"`
	// Kind is "message", "thought", "tool", "plan", "user" or "other".
	Kind string `json:"kind"`
	// Text is the message or thought text.
	Text string `json:"text,omitempty"`
	// ToolCallID, ToolTitle and ToolStatus describe a tool event.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolTitle  string `json:"tool_title,omitempty"`
	ToolStatus string `json:"tool_status,omitempty"`
	// Continued marks a piece of an update that was split to fit one frame. A
	// client joins it to the line before instead of starting a new one: the kind
	// and the text alone cannot tell one message in pieces from a second message
	// of the same kind, and a second prompt must not run into the first.
	Continued bool `json:"cont,omitempty"`
	// SessionID names the session the update arrived under. A child session
	// sends its own id, so an empty value is the session the client attached to.
	SessionID string `json:"session_id,omitempty"`
}

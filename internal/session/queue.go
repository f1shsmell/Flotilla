package session

// Messages line up behind a running turn. ACP's core has no mid-turn delivery:
// what a second prompt means mid-turn is the agent's choice, so a message typed
// while the agent works waits for the turn to end. The daemon holds the queue,
// not the window: a closed window must not lose what was typed into it, and a
// host must never be prompted twice for one message.
const (
	// MethodQueue lists the messages waiting for the running turn to end.
	MethodQueue = "session.queue"
	// MethodQueueAdd appends one message to the end of the queue.
	MethodQueueAdd = "session.queue.add"
	// MethodQueueEdit replaces one queued message's text.
	MethodQueueEdit = "session.queue.edit"
	// MethodQueueRemove deletes one queued message.
	MethodQueueRemove = "session.queue.remove"
	// MethodQueueMove changes one message's place in the queue.
	MethodQueueMove = "session.queue.move"
)

// Outcomes a queued message's delivery can fail with. They travel as
// identities, not sentences: the window owns the words it shows, as it does for
// a stop reason.
const (
	// DeliveryHostUnreachable reports that the session's host went away before
	// the message could start its turn.
	DeliveryHostUnreachable = "host_unreachable"
	// DeliveryPromptFailed reports that the host refused the turn.
	DeliveryPromptFailed = "prompt_failed"
)

// QueuedMessage is one message waiting for its turn. A failed delivery keeps
// it queued with the reason, so nothing the user typed is lost.
type QueuedMessage struct {
	// ID names the message for an edit, a move or a removal.
	ID string `json:"id"`
	// Text is the message exactly as the user wrote it.
	Text string `json:"text"`
	// Error names why the last delivery failed, as a Delivery constant.
	Error string `json:"error,omitempty"`
}

// QueueResponse is the queue as it stands after the call. Every mutation
// answers with it, so a window never renders a second guess at the order.
type QueueResponse struct {
	Messages []QueuedMessage `json:"messages"`
}

// QueueAddRequest carries one message to the end of the queue.
type QueueAddRequest struct {
	Text string `json:"text"`
}

// QueueEditRequest replaces one message's text.
type QueueEditRequest struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// QueueRemoveRequest names the message to drop.
type QueueRemoveRequest struct {
	ID string `json:"id"`
}

// QueueMoveRequest changes one message's place.
type QueueMoveRequest struct {
	ID string `json:"id"`
	// To is the message's zero-based index in the queue after the move.
	To int `json:"to"`
}

package session

// MethodSteer hands one message to the turn a session is running. It is offered
// only by an agent that advertised steering; the daemon queues otherwise.
const MethodSteer = "session.steer"

// SteerRequest is one message to inject into the running turn.
type SteerRequest struct {
	Text string `json:"text"`
}

// SteerResponse reports whether the running turn took the message. A false is
// not a failure: it means no turn was running, and the message belongs in the
// queue.
type SteerResponse struct {
	Steered bool `json:"steered"`
}

// The routes a message can take while a turn is running. Steered means the
// agent took it mid-turn; queued means it waits for the next turn instead.
const (
	SteerSteered = "steered"
	SteerQueued  = "queued"
)

// SteerDelivery is the daemon's answer to session.steer: the route the message
// took, and the queue it joined when it did not reach the turn.
type SteerDelivery struct {
	Route    string          `json:"route"`
	Messages []QueuedMessage `json:"messages,omitempty"`
}

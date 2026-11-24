package session

// MethodReclaim asks a host whether its agent family may be closed as idle. A
// host that allows closes itself as it answers, so the reply may be lost to the
// same close: a dead pipe here is a yes.
const MethodReclaim = "session.reclaim"

// MethodProtect pins or unpins a session against idle reclamation. The daemon
// owns the set: it outlives every host it protects.
const MethodProtect = "session.protect"

// ReclaimRequest asks one host whether its family has been quiet long enough.
type ReclaimRequest struct {
	// IdleForMs is the minimum quiet period. Zero or negative asks for a family
	// with nothing running, however recent its last sign of life.
	IdleForMs int64 `json:"idle_for_ms"`
}

// ReclaimResponse is a host's answer. Allow means the host is closing itself;
// Reason names the veto otherwise.
type ReclaimResponse struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// Why a host keeps its family alive. Codes, not sentences: a reader renders them.
const (
	// ReclaimBusy is a turn in flight.
	ReclaimBusy = "busy"
	// ReclaimWaiting is a session blocked on a person.
	ReclaimWaiting = "waiting"
	// ReclaimChildWorking is a child session still working or waiting.
	ReclaimChildWorking = "child_working"
	// ReclaimActive is family activity inside the quiet period.
	ReclaimActive = "active"
)

// ProtectRequest pins one session on the wire. A call bound to a session may
// omit the id: the connection already names it.
type ProtectRequest struct {
	ID        ID   `json:"id,omitempty"`
	Protected bool `json:"protected"`
}

package session

import "time"

// MethodArchive reads a session this daemon no longer serves. It is the one way
// to see a finished run: nothing is spawned, nothing is attached and no prompt
// can be sent, so a reader cannot mistake the answer for a live session.
const MethodArchive = "session.archive"

// ArchivedEntry is one session that is over. The fields this build never
// recorded stay empty rather than being filled from a guess: a log written
// before the daemon kept metadata has an id and nothing else.
type ArchivedEntry struct {
	ID ID `json:"id"`
	// Mode is "pty" or "acp", empty for a session this build did not watch
	// start.
	Mode Mode `json:"mode,omitempty"`
	// Command is the agent that ran, as it was started.
	Command string `json:"command,omitempty"`
	// Dir is where it ran, which is what lets a person tell two runs of the
	// same agent apart.
	Dir string `json:"dir,omitempty"`
	// Started and Ended bound the run. Ended is the moment this daemon stopped
	// serving the session, which is when it observed the end — not a claim
	// about when the agent's last process exited. It is zero when nothing
	// watched: a daemon that was down, or a host that died while no daemon was
	// there. A zero time reads as unknown, never as "now".
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
}

// ArchiveRequest names the finished session to read.
type ArchiveRequest struct {
	ID ID `json:"id"`
}

// ArchiveResponse is one finished session and the log it left behind. The
// answer is read-only by construction: there is no field a caller could set to
// make this session run again.
type ArchiveResponse struct {
	Session ArchivedEntry `json:"session"`
	// Updates are the stored agent updates, oldest first.
	Updates []UpdateMessage `json:"updates"`
	// More is true when older updates exist and were left out, so the reader
	// does not present the first line it has as the run's first line.
	More bool `json:"more,omitempty"`
}

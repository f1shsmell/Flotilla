package session

import "time"

// A finished turn leaves a snapshot of the session's worktree in git, and a
// rollback rewrites the working tree from one of them. The snapshots are refs,
// so they live with the repository and need no store of the daemon's own.
const (
	// MethodCheckpointList lists a session's snapshots, oldest first.
	MethodCheckpointList = "session.checkpoint.list"
	// MethodCheckpointRestore rolls the working tree back to one snapshot.
	MethodCheckpointRestore = "session.checkpoint.restore"
)

// Checkpoint is one finished turn's snapshot.
type Checkpoint struct {
	// Turn counts the session's snapshots from 1 in the order they were taken.
	Turn int `json:"turn"`
	// Commit is the snapshot's commit; its tree is the working tree it saw.
	Commit string `json:"commit"`
	// At is when the snapshot was taken.
	At time.Time `json:"at"`
}

// CheckpointListResponse is every snapshot a session has, oldest first.
type CheckpointListResponse struct {
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// CheckpointRestoreRequest names the turn to roll the working tree back to.
type CheckpointRestoreRequest struct {
	Turn int `json:"turn"`
}

package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newScheduleID returns an id for a schedule nothing else will collide with.
// The shape mirrors the task ids the orchestrator makes, so a reader can tell
// what an id names by its prefix alone.
func newScheduleID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate a schedule id: %w", err)
	}
	return "sch-" + hex.EncodeToString(raw[:]), nil
}

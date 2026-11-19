package daemon

import (
	"slices"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/session"
)

// TestSpawnArgsCarryThePermissionWait pins the flag chain: a wait the daemon
// was configured with must reach the host, because the host is the process
// that actually waits on the question.
func TestSpawnArgsCarryThePermissionWait(t *testing.T) {
	args, err := spawnArgs(SpawnRequest{
		Session:        "sess-wait",
		Mode:           session.ModeACP,
		Command:        "agent",
		PermissionWait: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("spawnArgs: %v", err)
	}

	i := slices.Index(args, "--permission-wait")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("args %v carry no --permission-wait", args)
	}
	if args[i+1] != "1m30s" {
		t.Fatalf("permission wait = %q, want 1m30s", args[i+1])
	}
}

// TestSpawnArgsOmitAnUnboundedWait pins the other direction: the default waits
// forever, and a flag that names zero would read as a configured deadline on
// an older host. Leaving it out is what keeps the default the default.
func TestSpawnArgsOmitAnUnboundedWait(t *testing.T) {
	args, err := spawnArgs(SpawnRequest{
		Session: "sess-forever",
		Mode:    session.ModeACP,
		Command: "agent",
	})
	if err != nil {
		t.Fatalf("spawnArgs: %v", err)
	}
	if slices.Contains(args, "--permission-wait") {
		t.Fatalf("args %v name a wait the caller never configured", args)
	}
}

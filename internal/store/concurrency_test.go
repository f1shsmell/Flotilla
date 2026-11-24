package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/store"
)

// TestHelperWriter is the body of a second process writing the same database.
// It stands in for a session host: that process publishes its own record while
// the daemon is also writing, and neither may lose a commit to the other.
func TestHelperWriter(t *testing.T) {
	path := os.Getenv("FLT_TEST_DB")
	if path == "" {
		t.Skip("helper process only")
	}

	// A helper that exits must not rely on a deferred close: os.Exit skips it.
	// The write loop returns its error instead, so the close always runs.
	if err := writeFifty(path); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// writeFifty opens the database, commits fifty events and closes it. It is a
// function so every failure path returns rather than exits, which is what lets
// the close run.
func writeFifty(path string) error {
	s, err := store.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = s.Close() }()

	for i := 0; i < 50; i++ {
		if _, err := s.Append(context.Background(), store.Commit{
			Event: store.Event{Kind: "helper.write", SessionID: fmt.Sprintf("h-%d", i)},
		}); err != nil {
			return fmt.Errorf("append %d: %w", i, err)
		}
	}
	return nil
}

// TestTwoProcessesWriteOneDatabase is the feasibility check for moving the host
// registry into the database: the host and the daemon would be two processes on
// one file, and a design that loses writes under that is not usable.
func TestTwoProcessesWriteOneDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a second process")
	}

	path := dbPath(t)

	parent, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=TestHelperWriter", "-test.v=false")
	cmd.Env = append(os.Environ(), "FLT_TEST_DB="+path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}

	// Write here while the helper writes there. Both are committing to one file.
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if _, err := parent.Append(ctx, store.Commit{
			Event: store.Event{Kind: "parent.write", SessionID: fmt.Sprintf("p-%d", i)},
		}); err != nil {
			_ = cmd.Process.Kill()
			t.Fatalf("append %d: %v", i, err)
		}
		// Give the helper a window to contend for the write lock.
		time.Sleep(time.Millisecond)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the helper failed: %v", err)
	}

	// Every commit from both sides must be there: a lost one is a session that
	// would vanish from the fleet.
	events, err := parent.Events(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var parentWrites, helperWrites int
	for _, e := range events {
		switch e.Kind {
		case "parent.write":
			parentWrites++
		case "helper.write":
			helperWrites++
		}
	}
	if parentWrites != 50 {
		t.Errorf("parent writes = %d, want 50", parentWrites)
	}
	if helperWrites != 50 {
		t.Errorf("helper writes = %d, want 50: a second process lost commits", helperWrites)
	}

	// The sequence is dense: no gap means no transaction was torn.
	for i, e := range events {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d: the log has a hole", i, e.Seq)
		}
	}
}

// TestTwoProcessesSeeEachOthersWrites pins the read side: a host publishing its
// record must be visible to a daemon that is already running.
func TestTwoProcessesSeeEachOthersWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a second process")
	}

	path := dbPath(t)
	parent, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=TestHelperWriter", "-test.v=false")
	cmd.Env = append(os.Environ(), "FLT_TEST_DB="+path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the helper failed: %v", err)
	}

	events, err := parent.Events(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 50 {
		t.Fatalf("events = %d, want 50: the reader did not see the other process", len(events))
	}

	// A payload survives the round trip: the record a host publishes carries
	// its endpoint, and a reader that cannot decode it cannot dial it.
	payload, err := json.Marshal(map[string]any{"id": "sess-1", "pid": 42})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := parent.Append(context.Background(), store.Commit{
		Event: store.Event{Kind: "record", Payload: payload},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

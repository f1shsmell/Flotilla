package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/gitops"
	"github.com/f1shsmell/flotilla/internal/gittest"
)

// readFile reads a file the test expects to exist, without the line ending the
// machine's git config chooses on checkout.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

// TestCaptureSnapshotsTheWorkingTreeWithoutTouchingTheBranch pins the two
// boundaries of a checkpoint: the snapshot holds what the turn left in the
// working tree, ignored files included in nothing, and HEAD, the branch and the
// user's own index are where they were. A snapshot is a read, not a commit.
func TestCaptureSnapshotsTheWorkingTreeWithoutTouchingTheBranch(t *testing.T) {
	dir := gittest.Init(t)
	head := gittest.Run(t, dir, "rev-parse", "HEAD")

	gittest.Write(t, dir, "readme.md", "changed by the turn\n")
	gittest.Write(t, dir, "notes.txt", "a new file\n")
	gittest.Write(t, dir, ".env", "SECRET=2\n")

	cp, created, err := gitops.Capture(context.Background(), dir, "sess-one")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !created || cp.Turn != 1 {
		t.Fatalf("capture = %+v, created %v; want the first turn", cp, created)
	}

	if got := gittest.Run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("a capture moved HEAD from %s to %s", head, got)
	}

	// The working tree stays dirty, exactly as before the capture: the user's
	// uncommitted review is not the snapshot's business to stage.
	status := gittest.Run(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "readme.md") || !strings.Contains(status, "notes.txt") {
		t.Fatalf("the working tree after a capture reads %q", status)
	}

	if got := gittest.Run(t, dir, "show", cp.Commit+":readme.md"); got != "changed by the turn" {
		t.Fatalf("the checkpoint holds readme.md as %q", got)
	}
	if got := gittest.Run(t, dir, "show", cp.Commit+":notes.txt"); got != "a new file" {
		t.Fatalf("the checkpoint holds notes.txt as %q", got)
	}

	// An ignored file is never part of a snapshot: .env stays the user's secret.
	names := gittest.Run(t, dir, "ls-tree", "-r", "--name-only", cp.Commit)
	if strings.Contains(names, ".env") {
		t.Fatalf("the checkpoint captured an ignored file: %s", names)
	}
}

// TestCaptureReusesTheSnapshotWhenTheTreeIsUnchanged makes an ordinal mean a
// state, not an event: two finished turns that leave the same tree are one
// checkpoint, so the list a user reads has no duplicates.
func TestCaptureReusesTheSnapshotWhenTheTreeIsUnchanged(t *testing.T) {
	dir := gittest.Init(t)

	first, created, err := gitops.Capture(context.Background(), dir, "sess-one")
	if err != nil || !created {
		t.Fatalf("first capture = %+v, created %v, err %v", first, created, err)
	}

	again, created, err := gitops.Capture(context.Background(), dir, "sess-one")
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if created {
		t.Fatalf("an unchanged tree made a second checkpoint: %+v", again)
	}
	if again.Turn != first.Turn || again.Commit != first.Commit {
		t.Fatalf("the reused checkpoint reads %+v, want %+v", again, first)
	}

	list, err := gitops.Checkpoints(context.Background(), dir, "sess-one")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("the session has %d checkpoint(s), want 1", len(list))
	}
}

// TestCheckpointsListTurnsInOrder keeps the list a user rolls back with in the
// order the turns ran, and keeps one session's snapshots out of another's in a
// shared checkout.
func TestCheckpointsListTurnsInOrder(t *testing.T) {
	dir := gittest.Init(t)
	ctx := context.Background()

	gittest.Write(t, dir, "readme.md", "turn one\n")
	if _, _, err := gitops.Capture(ctx, dir, "sess-one"); err != nil {
		t.Fatalf("capture turn one: %v", err)
	}
	gittest.Write(t, dir, "readme.md", "turn two\n")
	if _, _, err := gitops.Capture(ctx, dir, "sess-one"); err != nil {
		t.Fatalf("capture turn two: %v", err)
	}
	gittest.Write(t, dir, "readme.md", "turn three\n")
	if _, _, err := gitops.Capture(ctx, dir, "sess-one"); err != nil {
		t.Fatalf("capture turn three: %v", err)
	}

	list, err := gitops.Checkpoints(ctx, dir, "sess-one")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("the session has %d checkpoint(s), want 3", len(list))
	}
	for i, cp := range list {
		if cp.Turn != i+1 {
			t.Fatalf("checkpoint %d is turn %d", i, cp.Turn)
		}
		if cp.Commit == "" {
			t.Fatalf("checkpoint %d has no commit", i)
		}
		if cp.At.IsZero() {
			t.Fatalf("checkpoint %d has no time", i)
		}
	}

	other, err := gitops.Checkpoints(ctx, dir, "sess-two")
	if err != nil {
		t.Fatalf("list the other session: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("another session sees %d checkpoint(s) in a shared checkout", len(other))
	}
}

// TestRestoreRollsTheWorkingTreeBackToATurn is the feature's one acceptance: one
// call puts every file back the way the snapshot has it. The files the turn
// touched come back, a file the checkpoint never held is removed, an ignored
// file survives, and HEAD does not move.
func TestRestoreRollsTheWorkingTreeBackToATurn(t *testing.T) {
	dir := gittest.Init(t)
	ctx := context.Background()

	gittest.Write(t, dir, "readme.md", "first turn\n")
	gittest.Write(t, dir, "added.txt", "first turn\n")
	gittest.Write(t, dir, "gone.txt", "first turn\n")
	first, _, err := gitops.Capture(ctx, dir, "sess-one")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	head := gittest.Run(t, dir, "rev-parse", "HEAD")

	// The work that came after the snapshot: a staged change, a changed file the
	// checkpoint holds untracked, a deletion, and a file it never saw.
	gittest.Write(t, dir, "readme.md", "second turn\n")
	gittest.Run(t, dir, "add", "readme.md")
	gittest.Write(t, dir, "added.txt", "second turn\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatalf("remove gone.txt: %v", err)
	}
	gittest.Write(t, dir, "fresh.txt", "second turn\n")

	res, err := gitops.Restore(ctx, dir, "sess-one", first.Turn)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Ref != first.Commit {
		t.Fatalf("the restore reports %q, want %q", res.Ref, first.Commit)
	}

	if got := readFile(t, dir, "readme.md"); got != "first turn\n" {
		t.Fatalf("readme.md after the restore reads %q", got)
	}
	if got := readFile(t, dir, "added.txt"); got != "first turn\n" {
		t.Fatalf("added.txt after the restore reads %q", got)
	}
	if got := readFile(t, dir, "gone.txt"); got != "first turn\n" {
		t.Fatalf("gone.txt after the restore reads %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file the checkpoint never held survived the restore: %v", err)
	}
	if got := readFile(t, dir, ".env"); got != "SECRET=1\n" {
		t.Fatalf("an ignored file after the restore reads %q", got)
	}
	if got := gittest.Run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("a restore moved HEAD from %s to %s", head, got)
	}
}

// TestRestoreRefusesATurnTheSessionNeverHad reports a miss as a miss, so the
// window can tell the user the checkpoint is gone instead of showing a failure
// of git it cannot explain.
func TestRestoreRefusesATurnTheSessionNeverHad(t *testing.T) {
	dir := gittest.Init(t)

	_, err := gitops.Restore(context.Background(), dir, "sess-one", 7)
	if !errors.Is(err, gitops.ErrNoCheckpoint) {
		t.Fatalf("a restore of a missing turn failed with %v, want ErrNoCheckpoint", err)
	}
}

// TestCheckpointsWorkInARepositoryWithoutCommits keeps the first turn of a fresh
// repository snapshotable: there is no HEAD to hang a commit on, and waiting for
// the user's first commit would lose precisely the turn they may want back.
func TestCheckpointsWorkInARepositoryWithoutCommits(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	gittest.Run(t, dir, "init", "-b", "main")
	gittest.Run(t, dir, "config", "user.name", "test")
	gittest.Run(t, dir, "config", "user.email", "test@example.com")
	gittest.Write(t, dir, "readme.md", "before any commit\n")

	cp, created, err := gitops.Capture(ctx, dir, "sess-new")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !created || cp.Turn != 1 {
		t.Fatalf("capture = %+v, created %v; want the first turn", cp, created)
	}

	gittest.Write(t, dir, "readme.md", "changed without a commit\n")
	if _, err := gitops.Restore(ctx, dir, "sess-new", cp.Turn); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, dir, "readme.md"); got != "before any commit\n" {
		t.Fatalf("readme.md after the restore reads %q", got)
	}
}

// TestCheckpointsWorkFromASubdirectorySession covers the session that starts in
// one directory of a repository: the snapshot and the rollback belong to the
// checkout, so one call from the subdirectory covers the whole tree.
func TestCheckpointsWorkFromASubdirectorySession(t *testing.T) {
	dir := gittest.Init(t)
	ctx := context.Background()

	sub := filepath.Join(dir, "pkg")
	gittest.Write(t, dir, "pkg/file.txt", "in the subdirectory\n")
	cp, _, err := gitops.Capture(ctx, sub, "sess-sub")
	if err != nil {
		t.Fatalf("capture from a subdirectory: %v", err)
	}

	gittest.Write(t, dir, "readme.md", "changed at the root\n")
	gittest.Write(t, dir, "pkg/file.txt", "changed in the subdirectory\n")
	if _, err := gitops.Restore(ctx, sub, "sess-sub", cp.Turn); err != nil {
		t.Fatalf("restore from a subdirectory: %v", err)
	}
	if got := readFile(t, dir, "readme.md"); got != "hello\n" {
		t.Fatalf("readme.md after the restore reads %q", got)
	}
	if got := readFile(t, dir, "pkg/file.txt"); got != "in the subdirectory\n" {
		t.Fatalf("pkg/file.txt after the restore reads %q", got)
	}
}

// TestCheckpointsReportADirectoryThatIsNotARepository keeps a session outside
// git honest: there is nothing to snapshot, and the caller hears why.
func TestCheckpointsReportADirectoryThatIsNotARepository(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	if _, _, err := gitops.Capture(ctx, dir, "sess-plain"); !errors.Is(err, gitops.ErrNotARepository) {
		t.Fatalf("capture in a plain directory failed with %v, want ErrNotARepository", err)
	}
	if _, err := gitops.Checkpoints(ctx, dir, "sess-plain"); !errors.Is(err, gitops.ErrNotARepository) {
		t.Fatalf("list in a plain directory failed with %v, want ErrNotARepository", err)
	}
	if _, err := gitops.Restore(ctx, dir, "sess-plain", 1); !errors.Is(err, gitops.ErrNotARepository) {
		t.Fatalf("restore in a plain directory failed with %v, want ErrNotARepository", err)
	}
}

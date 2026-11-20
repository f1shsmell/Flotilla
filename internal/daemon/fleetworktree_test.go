package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/f1shsmell/flotilla/internal/gittest"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// buildSessionHost compiles the host the daemon spawns, so the check runs the
// shipped process rather than a library stand-in.
func buildSessionHost(t *testing.T) string {
	t.Helper()

	target := filepath.Join(t.TempDir(), "flt-session-host")
	if filepath.Separator == '\\' {
		target += ".exe"
	}

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, "./cmd/flt-session-host")
	cmd.Dir = repoRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the session host: %v: %s", err, output)
	}
	return target
}

// TestThreeWorktreesRunThreeTasksWithoutCrossing is the W5-6 acceptance check:
// three sessions start in three worktrees of one repository, run their turns at
// the same time, and each stream carries only its own task.
func TestThreeWorktreesRunThreeTasksWithoutCrossing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs real host processes")
	}

	repo := gittest.Init(t)
	// The spawned host resolves its state directory from FLT_HOME, so the test
	// points it at the same home the daemon serves; without this the host
	// publishes into the machine's real state directory.
	home := t.TempDir()
	t.Setenv("FLT_HOME", home)
	stateDir := filepath.Join(home, "sessions")
	const token = "shared"
	// The host reads the connection token from the home the daemon serves,
	// the file the shipped daemon's entry point writes at startup.
	if err := ipc.WriteTokenFile(filepath.Join(home, "token"), token); err != nil {
		t.Fatalf("write the connection token: %v", err)
	}

	runDaemonWith(t, Config{
		Channel:    "test",
		Token:      token,
		StateDir:   stateDir,
		HostBinary: buildSessionHost(t),
	})
	agent := buildFakeAgent(t)
	control := dialDaemon(t, stateDir, token, nil, nil)

	tasks := []struct{ id, branch, marker string }{
		{"sess-wt1", "feat/task1", "task-one"},
		{"sess-wt2", "feat/task2", "task-two"},
		{"sess-wt3", "feat/task3", "task-three"},
	}

	var (
		mu        sync.Mutex
		texts     = make([][]string, len(tasks))
		asked     = make([]int, len(tasks))
		replyErrs []error
	)

	conns := make([]*ipc.Conn, len(tasks))
	dirs := make([]string, len(tasks))

	for i, task := range tasks {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		raw, err := control.Call(ctx, MethodSessionStart, StartRequest{
			ID:       session.ID(task.id),
			Mode:     session.ModeACP,
			Command:  agent,
			Worktree: &WorktreeRequest{Repo: repo, Branch: task.branch},
		})
		cancel()
		if err != nil {
			t.Fatalf("start %s: %v", task.id, err)
		}
		var resp StartResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode the start of %s: %v", task.id, err)
		}
		dirs[i] = resp.Dir

		// A host outlives its daemon by design, so the test takes it down
		// itself; a failure further down must not leave it running.
		id := session.ID(task.id)
		t.Cleanup(func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), waitFor)
			defer stopCancel()
			_, _ = control.Call(stopCtx, session.MethodStop, session.StopRequest{ID: id})
		})

		conns[i] = dialDaemon(t, stateDir, token, func(_ context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
			switch method {
			case session.MethodUpdate:
				var msg session.UpdateMessage
				if err := json.Unmarshal(params, &msg); err == nil {
					mu.Lock()
					texts[i] = append(texts[i], msg.Text)
					mu.Unlock()
				}
			case session.MethodPermission:
				var q session.PermissionQuestion
				if err := json.Unmarshal(params, &q); err == nil {
					mu.Lock()
					asked[i]++
					mu.Unlock()

					callCtx, callCancel := context.WithTimeout(context.Background(), waitFor)
					defer callCancel()
					if _, err := peer.Call(callCtx, session.MethodPermissionReply, session.PermissionReply{
						ID:       q.ID,
						OptionID: "allow",
					}); err != nil {
						// Recorded, not logged: a reply can fail after the test
						// has finished, and logging then panics the runner.
						mu.Lock()
						replyErrs = append(replyErrs, fmt.Errorf("answer %s's permission question: %w", id, err))
						mu.Unlock()
					}
				}
			}
			return struct{}{}, nil
		}, nil)

		attachCtx, attachCancel := context.WithTimeout(context.Background(), waitFor)
		_, err = conns[i].Call(attachCtx, session.MethodAttach, session.AttachRequest{ID: id})
		attachCancel()
		if err != nil {
			t.Fatalf("attach %s: %v", task.id, err)
		}
	}

	for i, task := range tasks {
		want := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-"+strings.ReplaceAll(task.branch, "/", "-"))
		if dirs[i] != want {
			t.Fatalf("%s runs in %q, want its own worktree %q", task.id, dirs[i], want)
		}
		if got := gittest.Run(t, dirs[i], "branch", "--show-current"); got != task.branch {
			t.Fatalf("worktree %s is on branch %q, want %q", dirs[i], got, task.branch)
		}
		if _, err := os.Stat(filepath.Join(dirs[i], ".env")); err != nil {
			t.Fatalf("worktree %s is missing its included .env: %v", dirs[i], err)
		}
	}
	if got := gittest.Run(t, repo, "branch", "--show-current"); got != "main" {
		t.Fatalf("the main checkout moved to %q: a worktree session touched it", got)
	}

	stops := make([]string, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), waitFor)
			defer cancel()
			raw, err := conns[i].Call(ctx, session.MethodPrompt, session.PromptRequest{Text: task.marker})
			if err != nil {
				t.Errorf("prompt %s: %v", task.id, err)
				return
			}
			var turn session.PromptResponse
			if err := json.Unmarshal(raw, &turn); err != nil {
				t.Errorf("decode %s's turn: %v", task.id, err)
				return
			}
			stops[i] = turn.StopReason
		}()
	}
	wg.Wait()

	func() {
		mu.Lock()
		defer mu.Unlock()
		for _, err := range replyErrs {
			t.Fatal(err)
		}
		for i, task := range tasks {
			if stops[i] != "end_turn" {
				t.Fatalf("%s stopped as %q, want end_turn", task.id, stops[i])
			}
			if asked[i] != 1 {
				t.Fatalf("%s asked %d permission questions, want the agent's one", task.id, asked[i])
			}
			joined := strings.Join(texts[i], "\n")
			if !strings.Contains(joined, "working on: "+task.marker) {
				t.Fatalf("%s never carried its own task; saw %q", task.id, joined)
			}
			for j, other := range tasks {
				if i != j && strings.Contains(joined, other.marker) {
					t.Fatalf("%s's stream carried %s's task: %q", task.id, other.id, joined)
				}
			}
		}
	}()

	for _, task := range tasks {
		waitForState(t, stateDir, token, session.ID(task.id), session.StateDone)
	}
}

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/comments"
	"github.com/f1shsmell/flotilla/internal/gittest"
	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// buildFakeAgent compiles the stand-in ACP agent, so a relay test drives a real
// subprocess over real stdio instead of a stub in this package.
func buildFakeAgent(t *testing.T) string {
	t.Helper()

	root := repoRoot(t)
	target := filepath.Join(t.TempDir(), "flt-fake-acp-agent")
	if filepath.Separator == '\\' {
		target += ".exe"
	}

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", target, "./cmd/flt-fake-acp-agent")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the fake agent: %v: %s", err, output)
	}
	return target
}

// startACPHost takes an ACP session in this process, the way a session host does.
func startACPHost(t *testing.T, dir, id, token string) *host.Host {
	t.Helper()

	binary := buildFakeAgent(t)
	h, err := host.Start(context.Background(), host.Config{
		ID:       session.ID(id),
		Mode:     session.ModeACP,
		Token:    token,
		StateDir: dir,
		Command:  binary,
		Dir:      dir,
		Policy:   acp.Supervised,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the ACP host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	if _, err := waitRecord(t, session.NewRegistry(dir), session.ID(id)); err != nil {
		t.Fatalf("the ACP host never published a record: %v", err)
	}
	return h
}

// runDaemon starts a daemon that listens, and waits until it accepts a client.
//
// Cleanup waits for Run to return before the next test starts. The pipe name is
// shared by every test in this package (there is one "test" channel), so a
// listener that is still closing makes the next test's Listen fail — and its
// dial then finds a pipe that is about to disappear.
func runDaemon(t *testing.T, dir, token string) *Daemon {
	t.Helper()

	return runDaemonWith(t, Config{Channel: "test", Token: token, StateDir: dir})
}

// runDaemonWith is runDaemon for a test that also needs its own config
// directory, which is where the agent adapters and switches live.
func runDaemonWith(t *testing.T, cfg Config) *Daemon {
	t.Helper()

	cfg.Log = discardLogger()
	d := New(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = d.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(waitFor):
			t.Errorf("the daemon on %q did not stop", d.Addr().String())
		}
	})

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		nc, err := ipc.DialConn(ctx, d.Addr(), cfg.StateDir)
		if err == nil {
			_ = nc.Close()
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the daemon never started listening")
	return nil
}

// dialDaemon connects one client to the daemon under test.
func dialDaemon(t *testing.T, dir, token string, handler ipc.Handler, onData ipc.DataSink) *ipc.Conn {
	t.Helper()
	return dialDaemonAs(t, dir, token, "", handler, onData)
}

// dialDaemonAs connects as one named client. A client name is stable across the
// connections one window opens, which is what lets the daemon answer a
// per-window question: pass the same name for a window's control connection and
// its session connection.
func dialDaemonAs(t *testing.T, dir, token, client string, handler ipc.Handler, onData ipc.DataSink) *ipc.Conn {
	t.Helper()
	return dialDaemonWith(t, dir, token, client, nil, handler, onData)
}

// dialDaemonWith connects as a client that names a specific capability set.
// Nil means this build's own set, which is what a current window sends; an
// explicitly empty slice is an older build that named nothing, and is how a
// trim is tested.
func dialDaemonWith(t *testing.T, dir, token, client string, caps []ipc.Capability, handler ipc.Handler, onData ipc.DataSink) *ipc.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	conn, err := ipc.Dial(ctx, ipc.Address{Channel: "test", Name: "daemon"}, dir, ipc.Config{
		Token:        token,
		Role:         "gui",
		Client:       client,
		Capabilities: caps,
		Handler:      handler,
		OnData:       onData,
	})
	if err != nil {
		t.Fatalf("dial the daemon: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// TestAttachDrivesATurnThroughTheDaemon is the slice's reason to exist: a client
// that only knows the daemon's pipe runs a whole ACP turn, including the
// permission question the host would otherwise have asked a direct client.
func TestAttachDrivesATurnThroughTheDaemon(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-relay", token)
	runDaemon(t, dir, token)

	var (
		mu      sync.Mutex
		updates []string
		asked   []string
	)

	client := dialDaemon(t, dir, token, func(_ context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
		switch method {
		case session.MethodUpdate:
			var msg session.UpdateMessage
			if err := json.Unmarshal(params, &msg); err == nil {
				mu.Lock()
				updates = append(updates, msg.Kind)
				mu.Unlock()
			}
		case session.MethodPermission:
			var q session.PermissionQuestion
			if err := json.Unmarshal(params, &q); err == nil {
				mu.Lock()
				asked = append(asked, q.Title)
				mu.Unlock()

				callCtx, cancel := context.WithTimeout(context.Background(), waitFor)
				defer cancel()
				if _, err := peer.Call(callCtx, session.MethodPermissionReply, session.PermissionReply{
					ID:       q.ID,
					OptionID: "allow",
				}); err != nil {
					t.Errorf("answer the permission question: %v", err)
				}
			}
		}
		return struct{}{}, nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-relay"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	var attached session.AttachResponse
	if err := json.Unmarshal(raw, &attached); err != nil {
		t.Fatalf("decode the attach response: %v", err)
	}
	if attached.Info.Mode != string(session.ModeACP) {
		t.Fatalf("attached to a %q session, want acp", attached.Info.Mode)
	}

	raw, err = client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("prompt through the daemon: %v", err)
	}
	var turn session.PromptResponse
	if err := json.Unmarshal(raw, &turn); err != nil {
		t.Fatalf("decode the turn: %v", err)
	}
	if turn.StopReason != "end_turn" {
		t.Fatalf("turn stopped as %q, want end_turn (updates: %v)", turn.StopReason, updates)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "edit config.json" {
		t.Fatalf("permission questions = %v, want the agent's one question", asked)
	}
	for _, kind := range []string{"message", "thought", "tool"} {
		if !contains(updates, kind) {
			t.Fatalf("updates = %v, want a %q update among them", updates, kind)
		}
	}
}

// TestTwoClientsSeeTheSameStream pins the fan-out: the host has one client, the
// daemon, so a second window must not steal the session from the first.
func TestTwoClientsSeeTheSameStream(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-shared", token)
	runDaemon(t, dir, token)

	collect := func() (*ipc.Conn, func() []string) {
		var (
			mu   sync.Mutex
			seen []string
		)
		conn := dialDaemon(t, dir, token, func(_ context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
			switch method {
			case session.MethodUpdate:
				var msg session.UpdateMessage
				if err := json.Unmarshal(params, &msg); err == nil {
					mu.Lock()
					seen = append(seen, msg.Kind)
					mu.Unlock()
				}
			case session.MethodPermission:
				var q session.PermissionQuestion
				if err := json.Unmarshal(params, &q); err == nil {
					callCtx, cancel := context.WithTimeout(context.Background(), waitFor)
					defer cancel()
					_, _ = peer.Call(callCtx, session.MethodPermissionReply, session.PermissionReply{ID: q.ID, OptionID: "allow"})
				}
			}
			return struct{}{}, nil
		}, nil)
		return conn, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), seen...)
		}
	}

	first, firstSeen := collect()
	second, secondSeen := collect()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	for name, conn := range map[string]*ipc.Conn{"first": first, "second": second} {
		if _, err := conn.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-shared"}); err != nil {
			t.Fatalf("%s client attach: %v", name, err)
		}
	}

	if _, err := first.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	for name, seen := range map[string]func() []string{"first": firstSeen, "second": secondSeen} {
		if got := seen(); !contains(got, "message") {
			t.Fatalf("the %s client saw %v, want the stream both clients share", name, got)
		}
	}
	if !first.Alive() || !second.Alive() {
		t.Fatal("attaching the second client dropped the first")
	}
}

// TestPTYBytesCrossTheDaemon pins the data plane: input from a client reaches
// the agent and the agent's output reaches every attached client.
func TestPTYBytesCrossTheDaemon(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-pty", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-pty"); err != nil {
		t.Fatalf("the PTY host never published a record: %v", err)
	}
	runDaemon(t, dir, token)

	output := make(chan string, 64)
	client := dialDaemon(t, dir, token, nil, func(_ *ipc.Conn, stream uint32, payload []byte) {
		if stream == session.StreamOutput {
			select {
			case output <- string(payload):
			default:
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-pty"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := client.SendData(session.StreamInput, []byte("hello relay\r\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}

	var seen string
	deadline := time.After(waitFor)
	for {
		select {
		case chunk := <-output:
			seen += chunk
			if strings.Contains(seen, "echo: hello relay") {
				return
			}
		case <-deadline:
			t.Fatalf("the agent never echoed through the daemon; output so far: %q", seen)
		}
	}
}

// TestResizeCrossesTheDaemon keeps the terminal honest about its size: the
// window's own dimensions have to reach the PTY through the relay.
func TestResizeCrossesTheDaemon(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-size", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-size"); err != nil {
		t.Fatalf("the PTY host never published a record: %v", err)
	}
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-size"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := client.Call(ctx, session.MethodResize, session.ResizeRequest{Cols: 132, Rows: 43}); err != nil {
		t.Fatalf("resize through the daemon: %v", err)
	}

	raw, err := client.Call(ctx, session.MethodInfo, struct{}{})
	if err != nil {
		t.Fatalf("info through the daemon: %v", err)
	}
	var info session.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Cols != 132 || info.Rows != 43 {
		t.Fatalf("info reports %dx%d, want 132x43", info.Cols, info.Rows)
	}
}

// TestASessionEndsWhenItsHostDies pins the honest failure: the client holding a
// session learns it is over instead of waiting on a stream that cannot come.
func TestASessionEndsWhenItsHostDies(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	h := startACPHost(t, dir, "sess-doomed", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-doomed"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("close the host: %v", err)
	}

	select {
	case <-client.Done():
	case <-time.After(waitFor):
		t.Fatal("the attached client was never told the session ended")
	}
}

// TestAttachRefusesAnUnknownSession keeps a typo from binding a connection to
// nothing.
func TestAttachRefusesAnUnknownSession(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemon(t, dir, token)
	client := dialDaemon(t, dir, token, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "nope"}); err == nil {
		t.Fatal("attach accepted a session the daemon does not serve")
	}
}

// TestAWindowSeesTheScreenFromBeforeItWatched pins the bug this replay exists
// for: the daemon attaches to a host when the session starts, before any window
// exists, so the agent's first output reaches the daemon with nobody to forward
// it to. A window that attaches later must still be shown that screen.
func TestAWindowSeesTheScreenFromBeforeItWatched(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-early", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-early"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	// A daemon that is already running attaches as it starts, the same way the
	// session start path does. No window exists yet.
	d := runDaemon(t, dir, token)
	defer d.Detach()

	att := d.lookup("sess-early")
	if att == nil {
		t.Fatal("the daemon did not attach to the session")
	}

	// Output produced while the daemon is the only client: this is the state
	// every session reaches before its first window opens.
	if err := att.Conn.SendData(session.StreamInput, []byte("echo produced-before-the-window\r\n")); err != nil {
		t.Fatalf("send the marker: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// The window attaches now. What the daemon already lost must come back.
	var (
		mu   sync.Mutex
		seen strings.Builder
	)
	conn := dialDaemon(t, dir, token, nil, func(_ *ipc.Conn, stream uint32, payload []byte) {
		if stream == session.StreamOutput {
			mu.Lock()
			seen.Write(payload)
			mu.Unlock()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, err := conn.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-early"}); err != nil {
		t.Fatalf("attach the window: %v", err)
	}

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := seen.String()
		mu.Unlock()
		if strings.Contains(got, "produced-before-the-window") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("the window never received the screen the session already had; saw %q", seen.String())
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// TestTheDaemonTakesBackToBackClients pins the accept loop: a client dialling
// while the previous handshake is being served must still find the endpoint,
// not be told it does not exist.
func TestTheDaemonTakesBackToBackClients(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	runDaemon(t, dir, token)

	for i := 0; i < 25; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		conn, err := ipc.Dial(ctx, ipc.Address{Channel: "test", Name: "daemon"}, dir, ipc.Config{
			Token: token,
			Role:  "cli",
		})
		cancel()
		if err != nil {
			t.Fatalf("dial %d of 25: %v", i+1, err)
		}
		conn.Close()
	}
}

// TestStartRunsTheSessionInAWorktree pins the daemon's half of the slice: a
// request for a worktree becomes the directory the agent runs in.
func TestStartRunsTheSessionInAWorktree(t *testing.T) {
	repo := gittest.Init(t)

	dir, included, skipped, err := worktreeFor(context.Background(), StartRequest{
		ID:       "sess-wt",
		Worktree: &WorktreeRequest{Repo: repo, Branch: "feat/session"},
	})
	if err != nil {
		t.Fatalf("worktreeFor: %v", err)
	}

	if dir != filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-feat-session") {
		t.Fatalf("session dir = %q, want the worktree beside the repository", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		t.Fatalf("the worktree is missing its included .env: %v", err)
	}
	if len(included) != 1 || len(skipped) != 0 {
		t.Fatalf("included = %v, skipped = %v; want the one .env and nothing skipped", included, skipped)
	}
}

// TestStartWithoutAWorktreeKeepsTheCallersDirectory keeps the simple case.
func TestStartWithoutAWorktreeKeepsTheCallersDirectory(t *testing.T) {
	dir, included, skipped, err := worktreeFor(context.Background(), StartRequest{
		ID:  "sess-plain",
		Dir: "/tmp/wherever",
	})
	if err != nil {
		t.Fatalf("worktreeFor: %v", err)
	}
	if dir != "/tmp/wherever" || included != nil || skipped != nil {
		t.Fatalf("worktreeFor = (%q, %v, %v), want the caller's directory untouched", dir, included, skipped)
	}
}

// TestTheDaemonRemembersWhereASessionStarted pins the turn scope's input: the
// commit is taken when the session starts, before the agent touches anything.
func TestTheDaemonRemembersWhereASessionStarted(t *testing.T) {
	d := New(Config{Channel: "test", Token: "shared", StateDir: t.TempDir(), Log: discardLogger()}, nil)

	if d.baseline("sess-a") != "" {
		t.Fatal("a session the daemon never started has a baseline")
	}

	d.remember("sess-a", "abc123")
	if got := d.baseline("sess-a"); got != "abc123" {
		t.Fatalf("baseline = %q, want abc123", got)
	}

	d.remember("sess-b", "")
	if d.baseline("sess-b") != "" {
		t.Fatal("an empty baseline was recorded")
	}
}

// startHostIn takes a session whose agent runs in workDir, so a diff has a
// repository to read.
func startHostIn(t *testing.T, stateDir, id, token, workDir string) {
	t.Helper()

	command, args := helperCommand(t)
	h, err := host.Start(context.Background(), host.Config{
		ID:       session.ID(id),
		Token:    token,
		StateDir: stateDir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
		Dir:      workDir,
	}, session.NewRegistry(stateDir), discardLogger())
	if err != nil {
		t.Fatalf("start the host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	if _, err := waitRecord(t, session.NewRegistry(stateDir), session.ID(id)); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}
}

// TestDiffCrossesTheDaemon pins the review path: a client asks the daemon what
// changed in the session's checkout and gets the files and one patch back.
func TestDiffCrossesTheDaemon(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)

	startHostIn(t, stateDir, "sess-diff", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-diff"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// The agent edits a file; the review has to see it.
	if err := os.WriteFile(filepath.Join(repo, "readme.md"), []byte("hello\nworld\n"), 0o600); err != nil {
		t.Fatalf("edit the file: %v", err)
	}

	raw, err := client.Call(ctx, session.MethodDiff, session.DiffRequest{Scope: "unstaged"})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	var listed session.DiffResponse
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode the diff: %v", err)
	}
	if len(listed.Files) != 1 || listed.Files[0].Path != "readme.md" {
		t.Fatalf("files = %+v, want the edited readme", listed.Files)
	}

	raw, err = client.Call(ctx, session.MethodDiff, session.DiffRequest{Scope: "unstaged", Path: "readme.md"})
	if err != nil {
		t.Fatalf("diff of one file: %v", err)
	}
	var patched session.DiffResponse
	if err := json.Unmarshal(raw, &patched); err != nil {
		t.Fatalf("decode the patch: %v", err)
	}
	if !strings.Contains(patched.Patch, "+world") {
		t.Fatalf("patch does not carry the edit:/n%s", patched.Patch)
	}

	// A session the daemon did not start has no recorded baseline, so the turn
	// scope asks the caller for the commit it began at.
	baseline := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	raw, err = client.Call(ctx, session.MethodDiff, session.DiffRequest{Scope: "turn", Base: baseline})
	if err != nil {
		t.Fatalf("turn diff: %v", err)
	}
	var turn session.DiffResponse
	if err := json.Unmarshal(raw, &turn); err != nil {
		t.Fatalf("decode the turn diff: %v", err)
	}
	if len(turn.Files) != 1 || turn.Files[0].Path != "readme.md" {
		t.Fatalf("turn files = %+v, want the edit since the session started", turn.Files)
	}
}

// TestCommentsCrossTheDaemon pins the review loop: a note lands on a diff line,
// is read back, and is deleted, all through the daemon that owns the checkout.
func TestCommentsCrossTheDaemon(t *testing.T) {
	stateDir := t.TempDir()
	const token = "shared"
	repo := gittest.Init(t)

	startHostIn(t, stateDir, "sess-notes", token, repo)
	runDaemon(t, stateDir, token)

	client := dialDaemon(t, stateDir, token, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-notes"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	raw, err := client.Call(ctx, session.MethodCommentAdd, session.CommentAddRequest{
		Path: "readme.md",
		Line: 2,
		Side: "new",
		Text: "this needs a test",
	})
	if err != nil {
		t.Fatalf("add a comment: %v", err)
	}
	var added comments.Comment
	if err := json.Unmarshal(raw, &added); err != nil {
		t.Fatalf("decode the comment: %v", err)
	}
	if added.ID == "" || added.Author != "user" {
		t.Fatalf("comment = %+v, want an id and an author", added)
	}

	raw, err = client.Call(ctx, session.MethodCommentList, struct{}{})
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	var listed session.CommentListResponse
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode the list: %v", err)
	}
	if len(listed.Comments) != 1 || listed.Comments[0].Text != "this needs a test" {
		t.Fatalf("comments = %+v, want the one note", listed.Comments)
	}

	if _, err := client.Call(ctx, session.MethodCommentRemove, session.CommentRemoveRequest{ID: added.ID}); err != nil {
		t.Fatalf("remove the comment: %v", err)
	}

	raw, err = client.Call(ctx, session.MethodCommentList, struct{}{})
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode the list: %v", err)
	}
	if len(listed.Comments) != 0 {
		t.Fatalf("comments after remove = %+v, want none", listed.Comments)
	}

	// A note with no text is refused rather than stored as a blank line.
	if _, err := client.Call(ctx, session.MethodCommentAdd, session.CommentAddRequest{Path: "readme.md", Line: 1, Side: "new"}); err == nil {
		t.Fatal("an empty comment was accepted")
	}
}

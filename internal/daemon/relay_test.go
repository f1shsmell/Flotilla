package daemon

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
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
func runDaemon(t *testing.T, dir, token string) *Daemon {
	t.Helper()

	d := New(Config{Channel: "test", Token: token, StateDir: dir, Log: discardLogger()}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = d.Run(ctx) }()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		nc, err := ipc.DialConn(ctx, d.Addr(), dir)
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

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)

	conn, err := ipc.Dial(ctx, ipc.Address{Channel: "test", Name: "daemon"}, dir, ipc.Config{
		Token:   token,
		Role:    "gui",
		Handler: handler,
		OnData:  onData,
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

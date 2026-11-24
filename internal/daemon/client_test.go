package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// inProcessSpawner starts session hosts in this process, for a test that needs
// session.start without a detached child. It returns the fake agent's path so
// the test can name it in the start request.
func inProcessSpawner(t *testing.T, token string, policy acp.Policy) (Spawner, string) {
	t.Helper()
	binary := buildFakeAgent(t)

	spawn := func(ctx context.Context, req SpawnRequest) (ipc.Address, error) {
		h, err := host.Start(ctx, host.Config{
			ID:       req.Session,
			Mode:     req.Mode,
			Token:    token,
			StateDir: req.StateDir,
			Command:  req.Command,
			Args:     req.Args,
			Dir:      req.Dir,
			Channel:  req.Channel,
			Parent:   req.Parent,
			Policy:   policy,
		}, session.NewRegistry(req.StateDir), discardLogger())
		if err != nil {
			return ipc.Address{}, err
		}

		serveCtx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() {
			cancel()
			_ = h.Close()
		})
		go func() { _ = h.Serve(serveCtx) }()

		if _, err := waitRecord(t, session.NewRegistry(req.StateDir), req.Session); err != nil {
			return ipc.Address{}, err
		}
		return ipc.Address{Channel: string(req.Session), Name: "host"}, nil
	}
	return spawn, binary
}

// TestStatusNamesTheSessionThatStartedAWorker is the lineage gate: a worker an
// agent asked for carries its parent, and the scoped view built from the report
// is exactly the root and that worker.
func TestStatusNamesTheSessionThatStartedAWorker(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	spawn, binary := inProcessSpawner(t, token, acp.Auto)
	runDaemonSpawning(t, Config{Channel: "test", Token: token, StateDir: dir}, spawn)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	client, err := Connect(ctx, "test", token, dir)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	if _, err := client.StartSession(ctx, StartRequest{
		ID: "sess-root", Mode: session.ModeACP, Command: binary, Dir: dir,
	}); err != nil {
		t.Fatalf("start the root session: %v", err)
	}
	if _, err := client.StartSession(ctx, StartRequest{
		ID: "sess-worker", Mode: session.ModeACP, Command: binary, Dir: dir,
		Parent: "sess-root",
	}); err != nil {
		t.Fatalf("start the worker session: %v", err)
	}

	st, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("read the status: %v", err)
	}

	parents := make(map[session.ID]session.ID, len(st.Sessions))
	for _, s := range st.Sessions {
		parents[s.ID] = s.Parent
	}
	if got := parents["sess-worker"]; got != "sess-root" {
		t.Errorf("worker parent = %q, want %q: %+v", got, "sess-root", st.Sessions)
	}
	if got, ok := parents["sess-root"]; !ok || got != "" {
		t.Errorf("root parent = %q (listed=%t), want none", got, ok)
	}

	scoped := Scoped(st, "sess-root")
	if len(scoped.Sessions) != 2 {
		t.Errorf("scoped fleet = %+v, want the root and its worker", scoped.Sessions)
	}

	// The record is what a daemon that starts later reads, so the lineage has
	// to be there rather than only in this process's memory.
	rec, err := session.NewRegistry(dir).Read("sess-worker")
	if err != nil {
		t.Fatalf("read the worker's record: %v", err)
	}
	if rec.Parent != "sess-root" {
		t.Errorf("worker record parent = %q, want sess-root", rec.Parent)
	}
}

// TestStartHandsTheHostTheFleetToolBinary pins the last seam of the injection:
// the daemon derives the flt beside its session host and asks the host to
// register it, which is what gives the agent any tools at all.
func TestStartHandsTheHostTheFleetToolBinary(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	spawn, binary := inProcessSpawner(t, token, acp.Auto)
	toolBinary := filepath.Join(filepath.Dir(binary), "flt.exe")
	if err := os.WriteFile(toolBinary, []byte("not run by this test"), 0o755); err != nil {
		t.Fatalf("place a flt beside the fake agent: %v", err)
	}

	var mu sync.Mutex
	var requests []SpawnRequest
	recording := func(ctx context.Context, req SpawnRequest) (ipc.Address, error) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		return spawn(ctx, req)
	}
	runDaemonSpawning(t, Config{Channel: "test", Token: token, StateDir: dir, HostBinary: binary}, recording)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	client, err := Connect(ctx, "test", token, dir)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	if _, err := client.StartSession(ctx, StartRequest{
		ID: "sess-tools", Mode: session.ModeACP, Command: binary, Dir: dir,
	}); err != nil {
		t.Fatalf("start a session: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("spawned %d hosts, want one", len(requests))
	}
	if got, want := requests[0].MCPCommand, fleetBinary(binary); got != toolBinary || got != want {
		t.Errorf("spawn MCP command = %q, want %q (fleetBinary said %q)", got, toolBinary, want)
	}
}

// startPolicyHost takes an ACP session at one policy, in this process.
func startPolicyHost(t *testing.T, dir, id, token string, policy acp.Policy) {
	t.Helper()

	h, err := host.Start(context.Background(), host.Config{
		ID:       session.ID(id),
		Mode:     session.ModeACP,
		Token:    token,
		StateDir: dir,
		Command:  buildFakeAgent(t),
		Dir:      dir,
		Policy:   policy,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the ACP host: %v", err)
	}

	serveCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = h.Close()
	})
	go func() { _ = h.Serve(serveCtx) }()

	if _, err := waitRecord(t, session.NewRegistry(dir), session.ID(id)); err != nil {
		t.Fatalf("the ACP host never published a record: %v", err)
	}
}

// TestPromptSessionReadsTheWholeTurn pins the prompt path: the tool's client
// attaches, collects the messages the turn produced, and ends with the agent's
// own stop reason. In a supervised session the unattended question is refused
// instead of waiting for a person who is not there.
func TestPromptSessionReadsTheWholeTurn(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startPolicyHost(t, dir, "sess-supervised", token, acp.Supervised)
	startPolicyHost(t, dir, "sess-auto", token, acp.Auto)
	runDaemon(t, dir, token)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	client, err := Connect(ctx, "test", token, dir)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	auto, err := client.PromptSession(ctx, "sess-auto", "hello")
	if err != nil {
		t.Fatalf("prompt the auto session: %v", err)
	}
	if auto.StopReason != "end_turn" {
		t.Errorf("auto stop reason = %q, want end_turn", auto.StopReason)
	}
	if !strings.Contains(auto.Answer, "working on: hello") {
		t.Errorf("auto answer = %q, want the agent's message", auto.Answer)
	}
	if auto.Declined != 0 {
		t.Errorf("auto session declined %d questions, want none", auto.Declined)
	}
}

// TestPromptSessionRefusesAnUnattendedQuestion is the supervised half: the tool
// caller cannot see a permission card, so the tool cancels the tool call and
// the turn ends cancelled rather than hanging on a person.
func TestPromptSessionRefusesAnUnattendedQuestion(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startPolicyHost(t, dir, "sess-supervised", token, acp.Supervised)
	runDaemon(t, dir, token)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	client, err := Connect(ctx, "test", token, dir)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	res, err := client.PromptSession(ctx, "sess-supervised", "hello")
	if err != nil {
		t.Fatalf("prompt the supervised session: %v", err)
	}
	if res.Declined != 1 {
		t.Errorf("declined = %d, want one refused question", res.Declined)
	}
	if res.StopReason != "cancelled" {
		t.Errorf("stop reason = %q, want cancelled", res.StopReason)
	}
	if !strings.Contains(res.Answer, "working on: hello") || !strings.Contains(res.Answer, "stopped: no permission") {
		t.Errorf("answer = %q, want the turn's messages in order", res.Answer)
	}
}

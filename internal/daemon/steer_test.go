package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// startSteeringHost takes an ACP session whose agent advertises steering and
// holds its turn open for one mid-turn message.
func startSteeringHost(t *testing.T, dir, id, token string) {
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
		Env:      append(os.Environ(), "FLT_FAKE_STEER=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the steering host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	if _, err := waitRecord(t, session.NewRegistry(dir), session.ID(id)); err != nil {
		t.Fatalf("the steering host never published a record: %v", err)
	}
}

// waitDaemonText waits for one update whose text contains want.
func waitDaemonText(t *testing.T, texts <-chan string, want string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	for {
		select {
		case text := <-texts:
			if strings.Contains(text, want) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("no update ever said %q", want)
		}
	}
}

// TestSteerReachesTheRunningTurnThroughTheDaemon is the slice's working case: a
// window sends a message mid-turn and the daemon hands it to the turn instead
// of the queue.
func TestSteerReachesTheRunningTurnThroughTheDaemon(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startSteeringHost(t, dir, "sess-steer", token)
	runDaemon(t, dir, token)

	texts := make(chan string, 64)
	client := dialDaemon(t, dir, token, func(_ context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
		switch method {
		case session.MethodUpdate:
			var msg session.UpdateMessage
			if json.Unmarshal(params, &msg) == nil && msg.Kind == "message" {
				texts <- msg.Text
			}
		case session.MethodPermission:
			var q session.PermissionQuestion
			if json.Unmarshal(params, &q) == nil {
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

	control := dialDaemon(t, dir, token, nil, nil)
	status := fetchStatus(t, ctx, control)
	if len(status.Sessions) != 1 || !status.Sessions[0].Steer {
		t.Fatalf("status = %+v, want the session listed as steerable", status.Sessions)
	}

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-steer"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	prompted := make(chan error, 1)
	go func() {
		_, err := client.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "sail"})
		prompted <- err
	}()

	waitDaemonText(t, texts, "working on: sail")

	raw, err := client.Call(ctx, session.MethodSteer, session.SteerRequest{Text: "turn left"})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	var delivery session.SteerDelivery
	if err := json.Unmarshal(raw, &delivery); err != nil {
		t.Fatalf("decode the steer answer: %v", err)
	}
	if delivery.Route != session.SteerSteered {
		t.Fatalf("route = %q, want %q", delivery.Route, session.SteerSteered)
	}

	waitDaemonText(t, texts, "steered: turn left")
	if err := <-prompted; err != nil {
		t.Fatalf("the turn failed: %v", err)
	}

	raw, err = client.Call(ctx, session.MethodQueue, struct{}{})
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	var queue session.QueueResponse
	if err := json.Unmarshal(raw, &queue); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	if len(queue.Messages) != 0 {
		t.Fatalf("queue = %+v, want nothing: the message reached the turn", queue.Messages)
	}
}

// TestASteerWithNoRunningTurnIsQueued pins the fallback: the turn ended between
// the window's decision and the call, so the daemon keeps the message for the
// next turn rather than dropping it.
func TestASteerWithNoRunningTurnIsQueued(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startSteeringHost(t, dir, "sess-steer-idle", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-steer-idle"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	raw, err := client.Call(ctx, session.MethodSteer, session.SteerRequest{Text: "later"})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	var delivery session.SteerDelivery
	if err := json.Unmarshal(raw, &delivery); err != nil {
		t.Fatalf("decode the steer answer: %v", err)
	}
	if delivery.Route != session.SteerQueued {
		t.Fatalf("route = %q, want %q", delivery.Route, session.SteerQueued)
	}
	if len(delivery.Messages) != 1 || delivery.Messages[0].Text != "later" {
		t.Fatalf("queued = %+v, want the message the call carried", delivery.Messages)
	}
}

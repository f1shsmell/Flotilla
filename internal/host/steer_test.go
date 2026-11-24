package host

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// waitUpdate waits for one update whose text contains want.
func waitUpdate(t *testing.T, updates <-chan session.UpdateMessage, want string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	for {
		select {
		case msg := <-updates:
			if strings.Contains(msg.Text, want) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("no update ever said %q", want)
		}
	}
}

// TestSteerReachesTheRunningTurn is the working case through the host's own
// door: the message lands in the turn that is running and the agent answers it
// inside that turn.
func TestSteerReachesTheRunningTurn(t *testing.T) {
	h, dir := startACPHostEnv(t, acp.Auto, 0, append(os.Environ(), "FLT_FAKE_STEER=1"))
	if !h.info().Steer {
		t.Fatal("the host does not report the steering its agent advertised")
	}

	updates := make(chan session.UpdateMessage, 64)
	conn := dialHost(t, dir, func(msg session.UpdateMessage) { updates <- msg }, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	prompted := make(chan error, 1)
	go func() {
		_, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "sail"})
		prompted <- err
	}()

	waitUpdate(t, updates, "working on: sail")

	raw, err := conn.Call(ctx, session.MethodSteer, session.SteerRequest{Text: "turn left"})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	var resp session.SteerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if !resp.Steered {
		t.Fatal("the running turn did not take the message")
	}

	waitUpdate(t, updates, "steered: turn left")
	if err := <-prompted; err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
}

// TestSteerWithNoRunningTurnSaysSo pins the refusal a caller uses to fall back:
// no turn is running, so the answer is a false rather than an error.
func TestSteerWithNoRunningTurnSaysSo(t *testing.T) {
	_, dir := startACPHostEnv(t, acp.Auto, 0, append(os.Environ(), "FLT_FAKE_STEER=1"))
	conn := dialHost(t, dir, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodSteer, session.SteerRequest{Text: "later"})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	var resp session.SteerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if resp.Steered {
		t.Fatal("a steer was accepted with no turn running")
	}
}

// TestSteerIsRefusedByAnAgentThatCannotSteer pins the capability gate on the
// host side: even a working session is not asked when the agent advertised no
// steering extension.
func TestSteerIsRefusedByAnAgentThatCannotSteer(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)
	if h.info().Steer {
		t.Fatal("the host reports steering for an agent that advertised none")
	}

	h.mu.Lock()
	h.state = session.StateWorking
	h.mu.Unlock()

	conn := dialHost(t, dir, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodSteer, session.SteerRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	var resp session.SteerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if resp.Steered {
		t.Fatal("an agent that cannot steer took a message")
	}
}

// TestSteerOnATerminalIsWrongMode pins the boundary: a terminal is steered by
// typing into it, which is not this method.
func TestSteerOnATerminalIsWrongMode(t *testing.T) {
	_, conn, _ := startInjectHost(t, "sess-pty-steer", adapters.Adapter{})

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	_, err := conn.Call(ctx, session.MethodSteer, session.SteerRequest{Text: "hello"})
	if got := ipc.CodeOf(err); got != ipc.CodeWrongMode {
		t.Fatalf("steer on a terminal: code %d (err %v), want %d", got, err, ipc.CodeWrongMode)
	}
}

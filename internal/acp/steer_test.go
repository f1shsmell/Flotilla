package acp

import (
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// openSteeringFake opens the fake variant that advertises steering and holds
// its turn open for one mid-turn message.
func openSteeringFake(t *testing.T) (*Session, <-chan string) {
	t.Helper()

	binary := fakeAgentBinary(t)
	workspace := t.TempDir()
	texts := make(chan string, 32)

	sess, err := Open(t.Context(), Launch{
		Command: binary,
		Dir:     workspace,
		Env:     append(os.Environ(), "FLT_FAKE_STEER=1"),
	}, Config{
		Workspace: workspace,
		Policy:    Auto,
		OnUpdate: func(u Update) {
			if u.Kind == UpdateMessage {
				texts <- u.Text
			}
		},
	})
	if err != nil {
		t.Fatalf("open a steering session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, texts
}

// TestSteerIsReadFromTheAgentsAdvertisement pins the capability gate: the fake
// advertised steering in its _meta, so the session must report it.
func TestSteerIsReadFromTheAgentsAdvertisement(t *testing.T) {
	sess, _ := openSteeringFake(t)
	if !sess.SteerSupported() {
		t.Fatal("the fake advertised steering but the session reports none")
	}

	plain, _, _ := openFake(t, Auto, nil)
	if plain.SteerSupported() {
		t.Fatal("a session whose agent advertised nothing reports steering")
	}
}

// TestSteerReachesTheRunningTurn is the working case: the message arrives while
// the turn waits, and the agent's answer carries it.
func TestSteerReachesTheRunningTurn(t *testing.T) {
	sess, texts := openSteeringFake(t)

	type result struct {
		resp sdk.PromptResponse
		err  error
	}
	prompted := make(chan result, 1)
	go func() {
		resp, err := sess.Prompt(t.Context(), sdk.TextBlock("sail"))
		prompted <- result{resp: resp, err: err}
	}()

	// The fake holds the turn after this update; steering before it arrives
	// would race the turn's start, not the thing under test.
	waitText(t, texts, "working on: sail")

	delivered, err := sess.Steer(t.Context(), "turn left")
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if !delivered {
		t.Fatal("the running turn did not take the message")
	}

	got := <-prompted
	if got.err != nil {
		t.Fatalf("the turn failed: %v", got.err)
	}
	if got.resp.StopReason != sdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", got.resp.StopReason)
	}
	waitText(t, texts, "steered: turn left")
}

// TestSteerWithoutATurnSaysSo pins the idle opt-in: with no turn running the
// adapter answers "promptRequired" instead of starting a turn of its own, and
// the client reports the message as not taken so its caller can queue it. A
// client that stopped sending the _meta would get "startedNewTurn" here.
func TestSteerWithoutATurnSaysSo(t *testing.T) {
	sess, _ := openSteeringFake(t)

	delivered, err := sess.Steer(t.Context(), "too early")
	if err != nil {
		t.Fatalf("steer with no turn: %v", err)
	}
	if delivered {
		t.Fatal("a steer with no running turn was reported as taken")
	}
}

// waitText waits for one update whose text contains want.
func waitText(t *testing.T, texts <-chan string, want string) {
	t.Helper()

	for {
		select {
		case text := <-texts:
			if strings.Contains(text, want) {
				return
			}
		case <-time.After(waitFor):
			t.Fatalf("no update ever said %q", want)
		}
	}
}

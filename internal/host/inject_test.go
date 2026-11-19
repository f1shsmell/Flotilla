package host

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/adapters"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// startInjectHost starts a PTY host around the echo agent and returns a dialled
// client plus the channel its terminal output arrives on.
func startInjectHost(t *testing.T, id session.ID, adapter adapters.Adapter) (*Host, *ipc.Conn, <-chan string) {
	t.Helper()

	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       id,
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
		Adapter:  adapter,
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	rec, err := waitForRecord(t, session.NewRegistry(dir), id)
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	output := make(chan string, 64)
	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		OnData: func(_ *ipc.Conn, stream uint32, payload []byte) {
			if stream == session.StreamOutput {
				output <- string(payload)
			}
		},
	})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	return h, conn, output
}

// callPrompt sends one structured prompt and decodes the reply.
func callPrompt(t *testing.T, conn *ipc.Conn, text string) session.PromptResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: text})
	if err != nil {
		t.Fatalf("prompt the terminal session: %v", err)
	}
	var resp session.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the prompt response: %v", err)
	}
	return resp
}

// waitForOutput collects terminal chunks until every want appears, and returns
// everything seen on the way so a caller can also assert what did not arrive.
// One shared accumulation: adjacent frames can coalesce into a single chunk, so
// separate waits would lose a want that travelled with an earlier one.
func waitForOutput(t *testing.T, output <-chan string, wants ...string) string {
	t.Helper()

	deadline := time.After(waitFor)
	var seen strings.Builder
	for {
		select {
		case chunk := <-output:
			seen.WriteString(chunk)
			s := seen.String()
			all := true
			for _, want := range wants {
				all = all && strings.Contains(s, want)
			}
			if all {
				return s
			}
		case <-deadline:
			t.Fatalf("the agent never printed %v; output so far: %q", wants, seen.String())
		}
	}
}

// TestAPromptReachesATerminalAgent pins the injection path: a structured prompt
// on a PTY session is typed into the terminal and submitted, and the reply says
// injected rather than claiming a turn this build cannot observe. The adapter
// declares a pattern that never matched, so the gate is the state, not the
// presence of a definition.
func TestAPromptReachesATerminalAgent(t *testing.T) {
	h, conn, output := startInjectHost(t, "sess-inject", adapters.Adapter{
		Blocked: []string{"do you trust"},
	})

	resp := callPrompt(t, conn, "hello inject")
	if resp.StopReason != "injected" {
		t.Fatalf("stop reason = %q, want injected: a terminal turn has no observable end", resp.StopReason)
	}

	waitForOutput(t, output, "echo: hello inject")

	if got := h.CurrentState(); got == session.StateDone || got == session.StateWorking {
		t.Fatalf("after an injection the session reports %q; a terminal's work cannot be observed", got)
	}
}

// TestABlockedTerminalSessionRefusesAPrompt pins the gate: typing a prompt into
// an approval dialog answers a question the user never saw. The refusal carries
// its own wire identity, and the payload never reaches the terminal.
func TestABlockedTerminalSessionRefusesAPrompt(t *testing.T) {
	_, conn, output := startInjectHost(t, "sess-gated", adapters.Adapter{
		Blocked: []string{"do you trust"},
	})

	if err := conn.SendData(session.StreamInput, []byte("Do you trust this folder?\r\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}
	// The echo of the trust line proves the read pump is live before the
	// absence assertion below: the payload would have travelled the same pipe.
	seen := waitForOutput(t, output, "echo: Do you trust this folder?")

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	_, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "secret payload"})
	if err == nil {
		t.Fatal("a prompt was injected into a blocked session")
	}
	if code := ipc.CodeOf(err); code != ipc.CodeAgentBlocked {
		t.Fatalf("refusal code = %d, want CodeAgentBlocked (%d)", code, ipc.CodeAgentBlocked)
	}

	if strings.Contains(seen, "secret payload") {
		t.Fatal("the refused payload reached the terminal anyway")
	}
	for {
		select {
		case chunk := <-output:
			if strings.Contains(chunk, "secret payload") {
				t.Fatal("the refused payload reached the terminal anyway")
			}
		default:
			return
		}
	}
}

// TestLinePasteDeliversEveryLine pins the paced paste: a Line-mode adapter
// splits the prompt and waits for the agent to answer each line before sending
// the next. If the wait is never fed, the second line never leaves and this
// test hangs on the missing echo rather than passing by luck.
func TestLinePasteDeliversEveryLine(t *testing.T) {
	_, conn, output := startInjectHost(t, "sess-line", adapters.Adapter{
		Paste: adapters.Line,
	})

	resp := callPrompt(t, conn, "one\ntwo\nthree")
	if resp.StopReason != "injected" {
		t.Fatalf("stop reason = %q, want injected", resp.StopReason)
	}

	waitForOutput(t, output, "echo: one", "echo: two", "echo: three")
}

// TestASecondInjectionIsRefusedWhileOneRuns pins the concurrency gate: two
// pastes interleaving in one terminal would shred both. The lock is held here
// directly so the refusal is deterministic rather than a race the test hopes
// to win.
func TestASecondInjectionIsRefusedWhileOneRuns(t *testing.T) {
	h, conn, _ := startInjectHost(t, "sess-busy", adapters.Adapter{})

	h.injectMu.Lock()
	defer h.injectMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	_, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: "second"})
	if err == nil {
		t.Fatal("a second injection was accepted while one held the terminal")
	}
	if code := ipc.CodeOf(err); code != ipc.CodeAlreadyRunning {
		t.Fatalf("refusal code = %d, want CodeAlreadyRunning (%d)", code, ipc.CodeAlreadyRunning)
	}
}

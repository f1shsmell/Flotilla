package host

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// killAgent kills the host's current agent process, the same observable event
// as a crash: the process is gone and nobody asked the host to close.
func killAgent(t *testing.T, h *Host) int {
	t.Helper()

	pid := h.acpAgent().PID()
	if pid == 0 {
		t.Fatal("the fake agent has no process id")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find the agent process %d: %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the agent: %v", err)
	}
	return pid
}

// waitResumed polls the surface until the fake's resume marker appears. The
// marker only comes from a session/resume answer, so it proves the host
// reopened the conversation instead of creating a new one.
func waitResumed(t *testing.T, conn *ipc.Conn) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		for _, control := range fetchSurface(t, conn) {
			if control.ID == "resumed" && control.Current == "true" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the host never resumed the conversation after its agent died")
}

// drainUpdates collects what is on the channel until it goes quiet.
func drainUpdates(updates <-chan session.UpdateMessage) []session.UpdateMessage {
	var out []session.UpdateMessage
	for {
		select {
		case u := <-updates:
			out = append(out, u)
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

// promptTurn runs one turn and fails the test unless it ends normally.
func promptTurn(t *testing.T, conn *ipc.Conn, text string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: text})
	if err != nil {
		t.Fatalf("prompt %q: %v", text, err)
	}
	var resp session.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the prompt response: %v", err)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop reason for %q = %q, want end_turn", text, resp.StopReason)
	}
}

// TestACPHostResumesAfterAgentCrash pins the self-heal: an agent that advertised
// resume is relaunched inside the same host when it dies, the conversation
// continues under its saved id, and the update numbering carries on so a
// daemon's transcript still recognises what it already stored.
func TestACPHostResumesAfterAgentCrash(t *testing.T) {
	h, dir := startACPHost(t, acp.Auto)
	conn, surfaces, updates := dialSurface(t, dir)

	promptTurn(t, conn, "before the crash")
	before := drainUpdates(updates)
	if len(before) == 0 {
		t.Fatal("the first turn produced no updates")
	}
	lastSeq := before[len(before)-1].HostSeq

	killAgent(t, h)
	waitResumed(t, conn)

	select {
	case <-h.Done():
		t.Fatal("the host closed instead of resuming its agent")
	default:
	}
	// The interrupted turn never finished, so done would claim work that did
	// not happen. Unknown is what this build knows.
	if got := h.CurrentState(); got == session.StateDone {
		t.Fatalf("a crashed turn left the session reporting %q", got)
	}

	// The attached client is told the surface moved: the resumed agent's
	// controls replace the dead one's without a reconnect.
	for marked := false; !marked; {
		select {
		case resp := <-surfaces:
			for _, control := range resp.Controls {
				if control.ID == "resumed" && control.Current == "true" {
					marked = true
				}
			}
		case <-time.After(waitFor):
			t.Fatal("the resume never pushed a surface notification with the marker")
		}
	}

	promptTurn(t, conn, "after the crash")
	after := drainUpdates(updates)
	if len(after) == 0 {
		t.Fatal("the resumed turn produced no updates")
	}
	if after[0].HostSeq <= lastSeq {
		t.Fatalf("the resumed turn numbered from %d, want past %d", after[0].HostSeq, lastSeq)
	}
}

// TestACPHostStopsResumingAfterACrashLoop pins the cap: an agent that keeps
// dying is relaunched only a bounded number of times, and past that the host
// closes as it did before resume existed, instead of spawning forever.
func TestACPHostStopsResumingAfterACrashLoop(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	for i := 0; i < maxResumes+1; i++ {
		pid := killAgent(t, h)
		if i == maxResumes {
			break
		}
		deadline := time.Now().Add(waitFor)
		for h.acpAgent().PID() == pid {
			if time.Now().After(deadline) {
				t.Fatalf("resume %d never produced a new agent process", i+1)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("the host kept resuming past the crash cap")
	}
}

// TestResumeCapForgetsCrashesOutsideTheWindow pins the pruning: a long-lived
// session is not punished for crashes the window has already moved past.
func TestResumeCapForgetsCrashesOutsideTheWindow(t *testing.T) {
	h, _ := startACPHost(t, acp.Auto)

	old := time.Now().Add(-resumeWindow - time.Minute)
	h.acp.mu.Lock()
	h.acp.resumes = []time.Time{old, old, old}
	h.acp.mu.Unlock()

	if !h.resumeAllowed() {
		t.Fatal("crashes outside the window still counted against the cap")
	}
}

// TestACPHostClosesWhenResumeIsRefused pins the failure path: an agent that
// advertised resume but refuses the reopen takes its session down, rather than
// leaving a host whose every turn fails at the transport.
func TestACPHostClosesWhenResumeIsRefused(t *testing.T) {
	h, _ := startACPHostEnv(t, acp.Auto, 0, append(os.Environ(), "FLT_FAKE_RESUME_FAIL=1"))

	killAgent(t, h)

	select {
	case <-h.Done():
	case <-time.After(waitFor):
		t.Fatal("the host kept serving after the resume was refused")
	}
}

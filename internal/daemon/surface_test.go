package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// TestAttachCarriesTheSurface pins the first screen: a window that attaches
// learns the agent's controls in the attach reply itself, so the selectors are
// filled before any notification arrives.
func TestAttachCarriesTheSurface(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-surface", token)
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-surface"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	var attached session.AttachResponse
	if err := json.Unmarshal(raw, &attached); err != nil {
		t.Fatalf("decode the attach response: %v", err)
	}

	if len(attached.Surface) == 0 {
		t.Fatal("the attach reply carries no controls for an agent that announced them")
	}
	if attached.Surface[0].ID != "mode" || attached.Surface[0].Current != "code" {
		t.Fatalf("the first control = %+v, want the current mode", attached.Surface[0])
	}
}

// TestSurfaceSetTravelsTheRelay pins the write path through the daemon: the
// call reaches the host, and the agent's answer comes back to the attached
// window as a surface notification.
func TestSurfaceSetTravelsTheRelay(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	startACPHost(t, dir, "sess-set", token)
	runDaemon(t, dir, token)

	surfaces := make(chan session.SurfaceResponse, 8)
	client := dialDaemon(t, dir, token, func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
		if method == session.MethodSurfaceUpdate {
			var resp session.SurfaceResponse
			if err := json.Unmarshal(params, &resp); err == nil {
				surfaces <- resp
			}
		}
		return struct{}{}, nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	if _, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-set"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := client.Call(ctx, session.MethodSurfaceSet, session.SurfaceSetRequest{ID: "mode", Value: "plan"}); err != nil {
		t.Fatalf("set the mode through the daemon: %v", err)
	}

	select {
	case resp := <-surfaces:
		if len(resp.Controls) == 0 || resp.Controls[0].Current != "plan" {
			t.Fatalf("the surface event = %+v, want mode plan", resp.Controls)
		}
	case <-time.After(waitFor):
		t.Fatal("the attached window never heard about the mode change")
	}

	raw, err := client.Call(ctx, session.MethodSurface, struct{}{})
	if err != nil {
		t.Fatalf("read the surface through the daemon: %v", err)
	}
	var resp session.SurfaceResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the surface: %v", err)
	}
	if len(resp.Controls) == 0 || resp.Controls[0].Current != "plan" {
		t.Fatalf("a fresh read = %+v, want mode plan", resp.Controls)
	}
}

// TestAttachToAPTYCarriesNoSurface pins the tolerant path: a terminal session
// has no controls, and the attach still succeeds with an empty surface rather
// than failing on a call the host does not serve.
func TestAttachToAPTYCarriesNoSurface(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	newHost(t, dir, "sess-pty-surface", token)
	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-pty-surface"); err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}
	runDaemon(t, dir, token)

	client := dialDaemon(t, dir, token, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	raw, err := client.Call(ctx, session.MethodAttach, session.AttachRequest{ID: "sess-pty-surface"})
	if err != nil {
		t.Fatalf("attach to a terminal session: %v", err)
	}
	var attached session.AttachResponse
	if err := json.Unmarshal(raw, &attached); err != nil {
		t.Fatalf("decode the attach response: %v", err)
	}
	if len(attached.Surface) != 0 {
		t.Fatalf("a terminal session reported controls: %+v", attached.Surface)
	}
}

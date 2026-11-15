package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// testTimeout bounds every wait in this file. A test that needs a real timeout
// to pass is broken; this only stops a hung test from hanging the run.
const testTimeout = 10 * time.Second

// servePair returns a served side and a dialled side on one real endpoint.
func servePair(t *testing.T, server, client Config) (*Conn, *Conn) {
	t.Helper()

	l, addr, dir := listenTest(t)

	servedc := make(chan struct {
		conn *Conn
		err  error
	}, 1)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			servedc <- struct {
				conn *Conn
				err  error
			}{nil, err}
			return
		}
		c, err := Serve(nc, server)
		servedc <- struct {
			conn *Conn
			err  error
		}{c, err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	clientSide, err := Dial(ctx, addr, dir, client)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(clientSide.Close)

	res := <-servedc
	if res.err != nil {
		t.Fatalf("Serve: %v", res.err)
	}
	t.Cleanup(res.conn.Close)

	return res.conn, clientSide
}

// writeHandshake writes one handshake frame and flushes it. It reports through
// t.Errorf because callers can run it in a goroutine.
func writeHandshake(t *testing.T, nc net.Conn, h Handshake) {
	t.Helper()

	payload, err := encodeHandshake(h)
	if err != nil {
		t.Errorf("encodeHandshake: %v", err)
		return
	}

	w := NewWriter(nc)
	if err := w.WriteControl(payload); err != nil {
		t.Errorf("WriteControl handshake: %v", err)
		return
	}
	w.Close()
	w.Wait()
}

// dialPeer connects a bare peer to addr and returns its side of the pipe. The
// dial runs in a goroutine because on Windows a pipe write blocks until the
// peer reads, so both sides have to be live at once.
func dialPeer(t *testing.T, addr Address, dir string) net.Conn {
	t.Helper()

	peerc := make(chan net.Conn, 1)
	go func() {
		nc, err := DialConn(context.Background(), addr, dir)
		if err != nil {
			t.Errorf("DialConn: %v", err)
			close(peerc)
			return
		}
		peerc <- nc
	}()

	nc, ok := <-peerc
	if !ok {
		t.Fatal("the peer never connected")
	}
	t.Cleanup(func() { _ = nc.Close() })
	return nc
}

// serveRejectedPeer dials as a bare handshake writer and gives the accepted
// side to Serve, so the rejection the server decided is the thing under test.
func serveRejectedPeer(t *testing.T, hello Handshake, serverCfg Config) error {
	t.Helper()

	l, addr, dir := listenTest(t)
	acceptc := asyncAccept(l)

	peerConn := dialPeer(t, addr, dir)
	accepted := finishAccept(t, acceptc)

	go writeHandshake(t, peerConn, hello)
	// A real peer reads the answer; draining it here keeps the server's final
	// flush from waiting on a reader that never comes.
	go func() {
		_, _ = NewReader(peerConn).ReadFrame()
	}()

	_, err := Serve(accepted, serverCfg)
	if err == nil {
		t.Fatal("Serve accepted a peer it should have rejected")
	}
	return err
}

func TestServeRejectsAWrongToken(t *testing.T) {
	err := serveRejectedPeer(t,
		Handshake{Version: ProtocolVersion, Token: "wrong", Role: "gui"},
		Config{Token: "right", Role: "daemon"},
	)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Serve with a wrong token = %v, want ErrUnauthorized", err)
	}
}

func TestServeRejectsAProtocolMismatch(t *testing.T) {
	err := serveRejectedPeer(t,
		Handshake{Version: ProtocolVersion + 1, Token: "shared", Role: "gui"},
		Config{Token: "shared", Role: "daemon"},
	)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Serve with a mismatched version = %v, want ErrProtocol", err)
	}
}

func TestServeRejectsAPeerThatPresentsNoToken(t *testing.T) {
	err := serveRejectedPeer(t,
		Handshake{Version: ProtocolVersion, Role: "gui"},
		Config{Token: "shared", Role: "daemon"},
	)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Serve without a token = %v, want ErrUnauthorized", err)
	}
}

func TestServeRefusesToRunWithoutAToken(t *testing.T) {
	err := serveRejectedPeer(t,
		Handshake{Version: ProtocolVersion, Token: "anything", Role: "gui"},
		Config{Role: "daemon"},
	)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Serve without a configured token = %v, want ErrUnauthorized", err)
	}
}

// TestServeTellsARejectedPeerWhy pins the rejection reason onto the wire: a
// peer that is dropped without a word cannot tell a wrong token from a crash.
func TestServeTellsARejectedPeerWhy(t *testing.T) {
	l, addr, dir := listenTest(t)
	acceptc := asyncAccept(l)

	peerConn := dialPeer(t, addr, dir)
	accepted := finishAccept(t, acceptc)

	go writeHandshake(t, peerConn, Handshake{Version: ProtocolVersion, Token: "wrong", Role: "gui"})

	reasonc := make(chan string, 1)
	go func() {
		f, err := NewReader(peerConn).ReadFrame()
		if err != nil {
			reasonc <- "read: " + err.Error()
			return
		}
		hello, err := decodeHandshake(f.Payload)
		if err != nil {
			reasonc <- "decode: " + err.Error()
			return
		}
		reasonc <- hello.Error
	}()

	if _, err := Serve(accepted, Config{Token: "right", Role: "daemon"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Serve with a wrong token = %v, want ErrUnauthorized", err)
	}

	if reason := <-reasonc; !strings.Contains(reason, "token mismatch") {
		t.Fatalf("the rejected peer was told %q, want the token mismatch", reason)
	}
}

// TestServeDropsAPeerThatNeverSendsAHandshake pins the accept loop against a
// client that connects and then says nothing: without a deadline it holds the
// loop, and the process stops accepting everyone else.
func TestServeDropsAPeerThatNeverSendsAHandshake(t *testing.T) {
	l, addr, dir := listenTest(t)
	acceptc := asyncAccept(l)

	_ = dialPeer(t, addr, dir)
	accepted := finishAccept(t, acceptc)

	errc := make(chan error, 1)
	go func() {
		_, err := Serve(accepted, Config{
			Token:            "shared",
			Role:             "daemon",
			HandshakeTimeout: 100 * time.Millisecond,
		})
		errc <- err
	}()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Serve accepted a peer that never sent a handshake")
		}
	case <-time.After(testTimeout):
		t.Fatal("Serve waited on a silent peer instead of giving up")
	}
}

func TestCallRoundTrips(t *testing.T) {
	served, client := servePair(t,
		Config{Token: "shared", Role: "daemon", Handler: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			var in struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, err
			}
			return map[string]any{"method": method, "doubled": in.N * 2}, nil
		}},
		Config{Token: "shared", Role: "cli"},
	)

	if served.Peer() != "cli" {
		t.Fatalf("served side sees peer %q, want cli", served.Peer())
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	raw, err := client.Call(ctx, "ping", map[string]int{"n": 21})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	var out struct {
		Method  string `json:"method"`
		Doubled int    `json:"doubled"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.Method != "ping" || out.Doubled != 42 {
		t.Fatalf("result = %+v, want ping doubled to 42", out)
	}
}

func TestCallSurfacesAHandlerError(t *testing.T) {
	_, client := servePair(t,
		Config{Token: "shared", Role: "daemon", Handler: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return nil, errors.New("no such session")
		}},
		Config{Token: "shared", Role: "cli"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := client.Call(ctx, "session.attach", nil)
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("Call = %v, want a *CallError", err)
	}
	if callErr.Code != codeMethodFailed {
		t.Fatalf("CallError.Code = %d, want %d", callErr.Code, codeMethodFailed)
	}
	if callErr.Message != "no such session" {
		t.Fatalf("CallError.Message = %q, want the handler's reason", callErr.Message)
	}
}

func TestCallFailsWhenThePeerHasNoHandler(t *testing.T) {
	_, client := servePair(t,
		Config{Token: "shared", Role: "daemon"},
		Config{Token: "shared", Role: "cli"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := client.Call(ctx, "session.attach", nil)
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("Call to a peer without a handler = %v, want a *CallError", err)
	}
	if callErr.Message != ErrNoHandler.Error() {
		t.Fatalf("CallError.Message = %q, want %q", callErr.Message, ErrNoHandler.Error())
	}
}

func TestNotificationReachesTheHandler(t *testing.T) {
	seen := make(chan string, 1)

	_, client := servePair(t,
		Config{Token: "shared", Role: "host", Handler: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			seen <- method
			return nil, nil
		}},
		Config{Token: "shared", Role: "daemon"},
	)

	if err := client.Notify("session.output", map[string]string{"chunk": "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	select {
	case got := <-seen:
		if got != "session.output" {
			t.Fatalf("handler saw %q, want session.output", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("the notification never reached the handler")
	}
}

func TestDataPlaneReachesTheSink(t *testing.T) {
	received := make(chan string, 1)

	_, client := servePair(t,
		Config{Token: "shared", Role: "host", OnData: func(stream uint32, payload []byte) {
			if stream != 7 {
				return
			}
			received <- string(payload)
		}},
		Config{Token: "shared", Role: "daemon"},
	)

	if err := client.SendData(7, []byte("pty bytes")); err != nil {
		t.Fatalf("SendData: %v", err)
	}

	select {
	case got := <-received:
		if got != "pty bytes" {
			t.Fatalf("sink received %q, want %q", got, "pty bytes")
		}
	case <-time.After(testTimeout):
		t.Fatal("the data plane never delivered the payload")
	}
}

func TestAFailedCallLeavesTheConnectionUsable(t *testing.T) {
	calls := 0
	_, client := servePair(t,
		Config{Token: "shared", Role: "daemon", Handler: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("first call fails")
			}
			return map[string]bool{"ok": true}, nil
		}},
		Config{Token: "shared", Role: "cli"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, err := client.Call(ctx, "first", nil); err == nil {
		t.Fatal("the first call should have failed")
	}
	if _, err := client.Call(ctx, "second", nil); err != nil {
		t.Fatalf("the second call failed after one error: %v", err)
	}
}

// TestAnOversizedReplyFailsInsteadOfHanging pins the frame limit against a
// handler whose reply cannot fit one frame: the caller must learn why, because
// a silent drop is indistinguishable from a slow peer.
func TestAnOversizedReplyFailsInsteadOfHanging(t *testing.T) {
	_, client := servePair(t,
		Config{Token: "shared", Role: "daemon", Handler: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return strings.Repeat("x", MaxFrameSize), nil
		}},
		Config{Token: "shared", Role: "cli"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, err := client.Call(ctx, "big.reply", nil); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("call with an oversized reply = %v, want ErrResponseTooLarge", err)
	}
}

func TestCloseUnblocksAPendingCall(t *testing.T) {
	_, client := servePair(t,
		Config{Token: "shared", Role: "daemon", Handler: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		Config{Token: "shared", Role: "cli"},
	)

	done := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "never-answered", nil)
		done <- err
	}()

	client.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("pending call returned %v, want ErrConnClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not unblock the pending call")
	}
}

func TestCallRespectsItsDeadline(t *testing.T) {
	_, client := servePair(t,
		Config{Token: "shared", Role: "daemon", Handler: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		Config{Token: "shared", Role: "cli"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := client.Call(ctx, "slow", nil); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Call past its deadline = %v, want ErrTimeout", err)
	}
}

func TestSessionNameCrossesTheHandshake(t *testing.T) {
	served, _ := servePair(t,
		Config{Token: "shared", Role: "daemon"},
		Config{Token: "shared", Role: "host", Session: "sess-42"},
	)

	if served.Session() != "sess-42" {
		t.Fatalf("served side sees session %q, want sess-42", served.Session())
	}
}

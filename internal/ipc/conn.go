package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Handler serves one inbound method call. Returning an error sends the peer a
// failure reply; a notification has no reply channel, so its error is dropped.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// DataSink receives terminal bytes for one stream.
type DataSink func(stream uint32, payload []byte)

// Config is what either side needs to start a connection.
type Config struct {
	// Token is the shared secret, read from the token file by the caller.
	Token string
	// Role names this side for the peer's logs: "daemon", "host", "cli", "gui".
	Role string
	// Session names the session this connection is about, when it has one.
	Session string
	// Handler serves inbound calls; nil rejects them with ErrNoHandler, which
	// is what a data-only peer wants.
	Handler Handler
	// OnData receives inbound terminal bytes; nil drops them.
	OnData DataSink
	// HandshakeTimeout bounds the wait for the peer's handshake. Zero uses the
	// default: a peer that connects and then says nothing must not hold an
	// accept loop.
	HandshakeTimeout time.Duration
}

// handshakeTimeout is the default bound on the wait for a peer's handshake.
const handshakeTimeout = 10 * time.Second

func (c Config) handshakeTimeout() time.Duration {
	if c.HandshakeTimeout > 0 {
		return c.HandshakeTimeout
	}
	return handshakeTimeout
}

// Conn is one authenticated connection carrying both planes.
type Conn struct {
	rwc     net.Conn
	r       *Reader
	w       *Writer
	token   string
	role    string
	peer    string
	session string
	handler Handler
	onData  DataSink

	nextID  atomic.Int64
	pendMu  sync.Mutex
	pending map[int64]chan rpcMessage

	closed       chan struct{}
	shutdownOnce sync.Once
	cause        atomic.Pointer[error]
}

// Serve accepts an inbound connection: it reads the peer's handshake, verifies
// the token, answers with its own, then serves both planes.
func Serve(nc net.Conn, cfg Config) (*Conn, error) {
	c := newConn(nc, cfg)

	if err := nc.SetReadDeadline(time.Now().Add(cfg.handshakeTimeout())); err != nil {
		c.Close()
		return nil, fmt.Errorf("set the handshake deadline: %w", err)
	}

	hello, err := c.readHandshake()
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := c.authorize(hello); err != nil {
		// Say why before closing: a silent drop is undiagnosable across a pipe.
		_ = c.sendHandshake(Handshake{Version: ProtocolVersion, Role: c.role, Error: err.Error()})
		c.closeAfterFlush(flushTimeout)
		return nil, err
	}
	if err := c.sendHandshake(Handshake{Version: ProtocolVersion, Role: c.role, Session: c.session}); err != nil {
		c.Close()
		return nil, err
	}
	if err := nc.SetReadDeadline(time.Time{}); err != nil {
		c.Close()
		return nil, fmt.Errorf("clear the handshake deadline: %w", err)
	}

	c.peer = hello.Role
	c.session = hello.Session
	go c.readLoop()
	return c, nil
}

// Dial connects outward: it sends its handshake, reads the answer, and fails
// unless the peer accepted the token.
func Dial(ctx context.Context, addr Address, dir string, cfg Config) (*Conn, error) {
	nc, err := dial(ctx, addr, dir)
	if err != nil {
		return nil, err
	}

	c := newConn(nc, cfg)
	if err := nc.SetReadDeadline(time.Now().Add(cfg.handshakeTimeout())); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("set the handshake deadline: %w", err)
	}
	if err := c.sendHandshake(Handshake{
		Version: ProtocolVersion,
		Token:   cfg.Token,
		Role:    cfg.Role,
		Session: cfg.Session,
	}); err != nil {
		_ = nc.Close()
		return nil, err
	}

	hello, err := c.readHandshake()
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	if hello.Error != "" {
		_ = nc.Close()
		return nil, fmt.Errorf("%s rejected the connection: %s: %w", addr, hello.Error, ErrUnauthorized)
	}
	if hello.Version != ProtocolVersion {
		_ = nc.Close()
		return nil, fmt.Errorf("peer speaks protocol %d, this build speaks %d: %w", hello.Version, ProtocolVersion, ErrProtocol)
	}
	if err := nc.SetReadDeadline(time.Time{}); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("clear the handshake deadline: %w", err)
	}

	c.peer = hello.Role
	go c.readLoop()
	return c, nil
}

func newConn(nc net.Conn, cfg Config) *Conn {
	return &Conn{
		rwc:     nc,
		r:       NewReader(nc),
		w:       NewWriter(nc),
		token:   cfg.Token,
		role:    cfg.Role,
		session: cfg.Session,
		handler: cfg.Handler,
		onData:  cfg.OnData,
		pending: make(map[int64]chan rpcMessage),
		closed:  make(chan struct{}),
	}
}

// Role reports this side's role.
func (c *Conn) Role() string { return c.role }

// Peer reports the role the peer announced, or "" before its handshake.
func (c *Conn) Peer() string { return c.peer }

// Session reports the session this connection named, if any.
func (c *Conn) Session() string { return c.session }

// Done closes when the connection has stopped.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// Err reports why the connection stopped, or ErrConnClosed after a clean Close.
func (c *Conn) Err() error {
	if p := c.cause.Load(); p != nil {
		return *p
	}
	return ErrConnClosed
}

// Alive reports whether the connection is still carrying traffic. A peer that
// went away is not an error to report, so this answers the question a status
// view actually asks.
func (c *Conn) Alive() bool {
	select {
	case <-c.closed:
		return false
	default:
		return true
	}
}

// Close stops the connection. Queued frames are dropped: a closed connection
// has nobody left to read them.
func (c *Conn) Close() { c.shutdown(nil) }

// flushTimeout bounds how long a final message may hold a closing connection
// open. A peer that stopped reading must not block the caller.
const flushTimeout = time.Second

// closeAfterFlush lets the frames already queued reach the peer before the
// transport goes away, which is what makes a rejection reason readable.
func (c *Conn) closeAfterFlush(timeout time.Duration) {
	c.w.Close()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-c.w.Done():
	case <-timer.C:
	}
	c.shutdown(nil)
}

func (c *Conn) shutdown(cause error) {
	c.shutdownOnce.Do(func() {
		if cause != nil {
			c.cause.Store(&cause)
		}
		c.w.Close()
		_ = c.rwc.Close()

		// Waiters watch closed, so their channels stay open: closing one here
		// would race a reply already on its way from handleControl.
		c.pendMu.Lock()
		c.pending = make(map[int64]chan rpcMessage)
		c.pendMu.Unlock()

		close(c.closed)
	})
}

// Call sends a request and waits for its response, or for ctx to expire.
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	resp, err := c.roundTrip(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *Conn) roundTrip(ctx context.Context, method string, params any) (rpcMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcMessage, 1)

	c.pendMu.Lock()
	select {
	case <-c.closed:
		c.pendMu.Unlock()
		return rpcMessage{}, fmt.Errorf("call %q: %w", method, c.Err())
	default:
	}
	c.pending[id] = ch
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	payload, err := encodeRequest(id, method, params)
	if err != nil {
		return rpcMessage{}, err
	}
	if err := c.w.WriteControl(payload); err != nil {
		return rpcMessage{}, fmt.Errorf("call %q: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return rpcMessage{}, fmt.Errorf("call %q: %w", method, errors.Join(ErrTimeout, ctx.Err()))
	case <-c.closed:
		return rpcMessage{}, fmt.Errorf("call %q: %w", method, c.Err())
	case resp := <-ch:
		if resp.Error != nil {
			return rpcMessage{}, &CallError{Method: method, Code: resp.Error.Code, Message: resp.Error.Message}
		}
		return resp, nil
	}
}

// Notify sends a request that expects no response.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params for %q: %w", method, err)
	}
	payload, err := json.Marshal(rpcMessage{Version: "2.0", Method: method, Params: raw})
	if err != nil {
		return fmt.Errorf("encode notification %q: %w", method, err)
	}
	return c.w.WriteControl(payload)
}

// SendData writes terminal bytes for one stream, blocking while the data queue
// is full so the PTY reader feels the backpressure.
func (c *Conn) SendData(stream uint32, payload []byte) error {
	return c.w.WriteData(stream, payload)
}

func (c *Conn) readLoop() {
	for {
		f, err := c.r.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.shutdown(nil)
				return
			}
			c.shutdown(fmt.Errorf("read frame: %w", err))
			return
		}

		switch f.Kind {
		case KindData:
			if c.onData != nil {
				c.onData(f.Stream, f.Payload)
			}
		case KindControl:
			if !c.handleControl(f.Payload) {
				return
			}
		}
	}
}

// handleControl returns false when the connection must stop.
func (c *Conn) handleControl(payload []byte) bool {
	var msg rpcMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		// Not JSON means the peer is broken, not that one call failed: there is
		// no id to answer.
		c.shutdown(fmt.Errorf("decode control frame: %w", ErrProtocol))
		return false
	}

	switch {
	case msg.ID != nil && msg.Method == "":
		c.pendMu.Lock()
		ch, ok := c.pending[*msg.ID]
		c.pendMu.Unlock()
		if ok {
			ch <- msg
		}
		return true
	case msg.Method == "":
		c.shutdown(fmt.Errorf("control frame with neither id nor method: %w", ErrProtocol))
		return false
	default:
		go c.dispatch(msg)
		return true
	}
}

// dispatch runs one inbound call and answers it.
func (c *Conn) dispatch(msg rpcMessage) {
	if c.handler == nil {
		if msg.ID != nil {
			_ = c.replyError(msg.ID, codeMethodFailed, ErrNoHandler.Error())
		}
		return
	}

	result, err := c.handler(context.Background(), msg.Method, msg.Params)
	if msg.ID == nil {
		return
	}
	if err != nil {
		_ = c.replyError(msg.ID, codeMethodFailed, err.Error())
		return
	}

	raw, err := json.Marshal(result)
	if err != nil {
		_ = c.replyError(msg.ID, codeMethodFailed, fmt.Sprintf("encode result: %v", err))
		return
	}
	payload, err := json.Marshal(rpcMessage{Version: "2.0", ID: msg.ID, Result: raw})
	if err != nil {
		_ = c.replyError(msg.ID, codeMethodFailed, fmt.Sprintf("encode response: %v", err))
		return
	}
	if len(payload) > MaxFrameSize {
		// The writer would refuse this frame and drop it, leaving the caller to
		// wait for a timeout it cannot tell from a slow peer.
		_ = c.replyError(msg.ID, codeResponseTooLarge, ErrResponseTooLarge.Error())
		return
	}
	_ = c.w.WriteControl(payload)
}

func (c *Conn) replyError(id *int64, code int, message string) error {
	payload, err := json.Marshal(rpcMessage{
		Version: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	})
	if err != nil {
		return fmt.Errorf("encode error reply: %w", err)
	}
	return c.w.WriteControl(payload)
}

func (c *Conn) readHandshake() (Handshake, error) {
	f, err := c.r.ReadFrame()
	if err != nil {
		return Handshake{}, fmt.Errorf("read handshake: %w", err)
	}
	if f.Kind != KindControl {
		return Handshake{}, fmt.Errorf("first frame is not a handshake: %w", ErrProtocol)
	}
	return decodeHandshake(f.Payload)
}

func (c *Conn) sendHandshake(h Handshake) error {
	payload, err := encodeHandshake(h)
	if err != nil {
		return err
	}
	return c.w.WriteControl(payload)
}

// authorize rejects a handshake whose version or token does not match. The
// token check is constant-time and runs before any method is served.
func (c *Conn) authorize(hello Handshake) error {
	if hello.Version != ProtocolVersion {
		return fmt.Errorf("peer speaks protocol %d, this build speaks %d: %w", hello.Version, ProtocolVersion, ErrProtocol)
	}
	if c.token == "" {
		return fmt.Errorf("no token configured: %w", ErrUnauthorized)
	}
	if !EqualToken(hello.Token, c.token) {
		return fmt.Errorf("token mismatch: %w", ErrUnauthorized)
	}
	return nil
}

// CallError is a failure the peer reported for one call. It carries the peer's
// code so a caller branches on identity, not on message text.
type CallError struct {
	Method  string
	Code    int
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("%s failed (%d): %s", e.Method, e.Code, e.Message)
}

// Unwrap maps the peer's code onto the sentinel a caller branches on, so a
// failure is identified by what it was, not by the peer's wording.
func (e *CallError) Unwrap() error {
	switch e.Code {
	case codeResponseTooLarge:
		return ErrResponseTooLarge
	default:
		return nil
	}
}

const (
	// codeMethodFailed is the JSON-RPC server-error code used for a handler
	// that returned an error; the reason travels in the message.
	codeMethodFailed = -32000
	// codeResponseTooLarge reports a reply the wire cannot carry in one frame.
	codeResponseTooLarge = -32001
)

type rpcMessage struct {
	Version string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func encodeRequest(id int64, method string, params any) ([]byte, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode params for %q: %w", method, err)
	}
	payload, err := json.Marshal(rpcMessage{Version: "2.0", ID: &id, Method: method, Params: raw})
	if err != nil {
		return nil, fmt.Errorf("encode request %q: %w", method, err)
	}
	return payload, nil
}

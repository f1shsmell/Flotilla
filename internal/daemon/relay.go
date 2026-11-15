package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// ErrUnknownSession reports an attach for a session this daemon does not serve.
var ErrUnknownSession = errors.New("daemon: no such session")

// HostHooks are what the daemon does with traffic from one host connection: the
// host only ever sends, so a call from it is a notification and its data frames
// are agent output.
type HostHooks struct {
	Handler ipc.Handler
	OnData  ipc.DataSink
}

// relay binds client connections to sessions. One connection carries one
// session: the daemon is the only client a host has, so it fans the host's
// stream out to however many clients attached, and forwards their calls back.
type relay struct {
	mu    sync.Mutex
	bound map[*ipc.Conn]session.ID
	subs  map[session.ID]map[*ipc.Conn]struct{}
}

func newRelay() *relay {
	return &relay{
		bound: make(map[*ipc.Conn]session.ID),
		subs:  make(map[session.ID]map[*ipc.Conn]struct{}),
	}
}

// bind makes peer the client of id, replacing any session it was bound to.
func (r *relay) bind(peer *ipc.Conn, id session.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if previous, ok := r.bound[peer]; ok && previous != id {
		r.removeLocked(previous, peer)
	}
	r.bound[peer] = id
	if r.subs[id] == nil {
		r.subs[id] = make(map[*ipc.Conn]struct{})
	}
	r.subs[id][peer] = struct{}{}
}

// sessionOf reports the session peer is bound to.
func (r *relay) sessionOf(peer *ipc.Conn) (session.ID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, ok := r.bound[peer]
	return id, ok
}

// forget drops a closed client from both maps.
func (r *relay) forget(peer *ipc.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, ok := r.bound[peer]
	if !ok {
		return
	}
	r.removeLocked(id, peer)
}

func (r *relay) removeLocked(id session.ID, peer *ipc.Conn) {
	delete(r.bound, peer)
	if subs := r.subs[id]; subs != nil {
		delete(subs, peer)
		if len(subs) == 0 {
			delete(r.subs, id)
		}
	}
}

// subscribers snapshots the clients attached to id.
func (r *relay) subscribers(id session.ID) []*ipc.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()

	subs := r.subs[id]
	out := make([]*ipc.Conn, 0, len(subs))
	for peer := range subs {
		out = append(out, peer)
	}
	return out
}

// drop removes every client of id and returns them, so the caller can tell each
// one that the session is over.
func (r *relay) drop(id session.ID) []*ipc.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()

	subs := r.subs[id]
	out := make([]*ipc.Conn, 0, len(subs))
	for peer := range subs {
		delete(r.bound, peer)
		out = append(out, peer)
	}
	delete(r.subs, id)
	return out
}

// attachClient binds one client connection to a session. Everything the session
// produces after this point arrives on that connection, and every call it makes
// is forwarded to the host.
func (d *Daemon) attachClient(peer *ipc.Conn, params json.RawMessage) (any, error) {
	var req session.AttachRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodAttach, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: empty session id: %w", session.MethodAttach, session.ErrNoRecord)
	}

	att := d.lookup(req.ID)
	if att == nil {
		return nil, fmt.Errorf("attach to session %q: %w", req.ID, ErrUnknownSession)
	}
	if !att.Conn.Alive() {
		return nil, fmt.Errorf("attach to session %q: the host is unreachable: %w", req.ID, ErrUnknownSession)
	}

	d.relay.bind(peer, req.ID)
	go func() {
		<-peer.Done()
		d.relay.forget(peer)
	}()

	d.cfg.Log.Info("client attached to a session", "session", req.ID, "peer", peer.Role())

	return session.AttachResponse{ID: req.ID, Info: att.Info}, nil
}

// relayCall forwards one call from an attached client to its host. The host's
// reply is handed back untouched, so the client decodes the same shape it would
// have got by dialling the host itself.
func (d *Daemon) relayCall(ctx context.Context, id session.ID, method string, params json.RawMessage) (any, error) {
	switch method {
	case session.MethodPrompt, session.MethodCancel, session.MethodPermissionReply:
	default:
		return nil, ipc.ErrNoHandler
	}

	att := d.lookup(id)
	if att == nil || !att.Conn.Alive() {
		return nil, fmt.Errorf("%s on session %q: %w", method, id, ErrUnknownSession)
	}

	raw, err := att.Conn.Call(ctx, method, params)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", method, id, err)
	}
	return raw, nil
}

// clientData forwards terminal input from an attached client to its host.
func (d *Daemon) clientData(peer *ipc.Conn, stream uint32, payload []byte) {
	id, ok := d.relay.sessionOf(peer)
	if !ok {
		return
	}
	att := d.lookup(id)
	if att == nil {
		return
	}
	// A failed send means the host is gone; the subscriber learns from its own
	// connection closing, so there is nothing to report here.
	_ = att.Conn.SendData(stream, payload)
}

// hostHooks build the callbacks for one host connection: its notifications go to
// every attached client, and its terminal bytes go to them the same way.
func (d *Daemon) hostHooks(id session.ID) HostHooks {
	return HostHooks{
		Handler: func(_ context.Context, _ *ipc.Conn, method string, params json.RawMessage) (any, error) {
			for _, peer := range d.relay.subscribers(id) {
				if err := peer.Notify(method, params); err != nil {
					d.cfg.Log.Debug("forward a session notification failed", "session", id, "err", err.Error())
				}
			}
			return struct{}{}, nil
		},
		OnData: func(_ *ipc.Conn, stream uint32, payload []byte) {
			for _, peer := range d.relay.subscribers(id) {
				_ = peer.SendData(stream, payload)
			}
		},
	}
}

// watch ends every client of att when its host connection stops, so a window
// holding a dead session learns instead of waiting on a stream that cannot come.
func (d *Daemon) watch(att Attachment) {
	go func() {
		<-att.Conn.Done()
		d.dropSession(att.Record.ID)
	}()
}

// dropSession tells every client of a session that it is over: the host died, so
// the connection they were holding has nothing behind it.
func (d *Daemon) dropSession(id session.ID) {
	for _, peer := range d.relay.drop(id) {
		peer.Close()
	}
}

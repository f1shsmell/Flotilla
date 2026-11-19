package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/f1shsmell/flotilla/internal/comments"
	"github.com/f1shsmell/flotilla/internal/diff"
	"github.com/f1shsmell/flotilla/internal/gitops"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// ErrUnknownSession reports an attach for a session this daemon does not serve.
var ErrUnknownSession = ipc.Class(errors.New("daemon: no such session"), ipc.CodeUnknownSession)

// ErrHostUnreachable reports a session whose host stopped answering. The work
// is there and the process behind it is not, which is a different cause from a
// session that never existed even though the caller cannot proceed either way.
var ErrHostUnreachable = ipc.Class(errors.New("daemon: the host is unreachable"), ipc.CodeHostUnreachable)

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
	// queues holds the per-client ordering gate. It is created at bind and
	// dropped when the client goes.
	queues map[*ipc.Conn]*peerQueue
	// seen records which clients have looked at a session's finished turn. It
	// lives here because this is the only layer that knows how many clients
	// there are: a host has one client and cannot answer the question, and one
	// window clearing its own badge must not clear another's. A client is named
	// in its handshake, so the control connection that polls the fleet and the
	// session connection that reports seen are the same client.
	seen map[session.ID]map[string]struct{}
	// present is the set of windows the daemon has seen, read and written under
	// mu. It is what bounds the per-client seen table: a window's name never
	// returns, so without a bound a long-lived daemon would keep one entry per
	// launch forever.
	present map[string]struct{}
	// seenPath names the file the seen table is kept in; empty means memory
	// only. The writer goroutine owns the file, waking on seenWake and taking
	// its final snapshot when seenStop closes.
	seenPath string
	seenWake chan struct{}
	seenStop chan struct{}
	seenDone chan struct{}
	seenOnce sync.Once
	// onWriteError receives a frame that could not be delivered while it was
	// queued. It is a field rather than a logger so a test can build a relay
	// with nothing behind it.
	onWriteError func(error)
}

// newRelay builds a relay whose queued-write failures go to onWriteError. The
// daemon passes its own logger; a test passes nothing and gets a silent relay.
func newRelay(onWriteError func(error)) *relay {
	return &relay{
		bound:        make(map[*ipc.Conn]session.ID),
		subs:         make(map[session.ID]map[*ipc.Conn]struct{}),
		queues:       make(map[*ipc.Conn]*peerQueue),
		seen:         make(map[session.ID]map[string]struct{}),
		present:      make(map[string]struct{}),
		onWriteError: onWriteError,
	}
}

// bind makes peer the client of id, replacing any session it was bound to, and
// holds what the session produces until the caller releases the queue.
//
// The hold happens before the relay lock is released and the write path never
// runs under that lock, so no frame can slip through between the bind and the
// hold.
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

	q := r.queues[peer]
	if q == nil {
		q = newPeerQueue(r.reportWrite)
		r.queues[peer] = q
	}
	// hold takes the queue's own lock, which enqueue never holds across a
	// write — so this cannot block on a client that stopped reading.
	q.hold()
}

// reportWrite logs a frame that could not be delivered while it was queued.
// The client's own connection closing is what it acts on; this is the record a
// postmortem needs.
func (r *relay) reportWrite(err error) {
	if r.onWriteError != nil {
		r.onWriteError(err)
	}
}

// release lets a bound client's held frames through. It is what runs once the
// attach reply is on the wire.
func (r *relay) release(peer *ipc.Conn) {
	r.mu.Lock()
	q := r.queues[peer]
	r.mu.Unlock()

	if q != nil {
		q.release()
	}
}

// deliver hands one frame to a client in order, and reports what the write
// said. A client with no queue left — one that departed between the subscriber
// snapshot and this call — is written to directly; the transport drops the frame
// on a closed connection, which is the same end the queue would have reached.
func (r *relay) deliver(peer *ipc.Conn, write func() error) error {
	r.mu.Lock()
	q := r.queues[peer]
	r.mu.Unlock()

	if q == nil {
		return write()
	}
	return q.enqueue(write)
}

// sessionOf reports the session peer is bound to.
func (r *relay) sessionOf(peer *ipc.Conn) (session.ID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, ok := r.bound[peer]
	return id, ok
}

// forget drops a closed client from every map. Its queue goes too: a frame that
// arrives for a departed client is written to a closed connection and dropped
// by the transport, which is why nothing waits on it.
func (r *relay) forget(peer *ipc.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.queues, peer)
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
		delete(r.queues, peer)
		out = append(out, peer)
	}
	delete(r.subs, id)
	return out
}

// attachClient binds one client connection to a session. Everything the session
// produces after this point arrives on that connection, and every call it makes
// is forwarded to the host.
func (d *Daemon) attachClient(ctx context.Context, peer *ipc.Conn, params json.RawMessage) (any, error) {
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
		return nil, fmt.Errorf("attach to session %q: %w", req.ID, ErrHostUnreachable)
	}

	d.relay.bind(peer, req.ID)
	go func() {
		<-peer.Done()
		d.relay.forget(peer)
	}()

	// A host writes its scrollback to whoever is attached when the output
	// arrives, and its only client is this daemon. The daemon attached at
	// start time, before any window existed, so those bytes went to the daemon
	// with nobody to forward them to. Asking the host for a replay now is what
	// makes the first window see the screen the session already had.
	//
	// The release runs after this call's reply, through the gate: the connection
	// holds every frame of the session until then, so no live update can overtake
	// the history the renderer folds first.
	ipc.OnReply(ctx, func() { d.relay.release(peer) })
	// The replay request runs after that, and on its own goroutine: it is a
	// blocking call bounded at five seconds, and a host that stopped answering
	// must not hold this connection's next frames, nor the call that asked for
	// the attach.
	ipc.OnReply(ctx, func() { go d.requestReplay(req.ID, att.Conn) })

	// The agent's own log is the daemon's, not the host's: this is what shows a
	// window the work that happened before it opened, and it survives the host.
	history, more, err := d.readHistory(req.ID, session.HistoryRequest{})
	if err != nil {
		// A transcript that cannot be read is worth a warning, not a refusal:
		// the live stream still works, and the window starts with what arrives.
		d.cfg.Log.Warn("cannot read a session transcript", "session", req.ID, "err", err.Error())
	}

	d.cfg.Log.Info("client attached to a session", "session", req.ID, "peer", peer.Peer())

	return session.AttachResponse{
		ID:          req.ID,
		Info:        att.Info,
		History:     history,
		HistoryMore: more,
		Surface:     d.readSurface(req.ID, att.Conn),
	}, nil
}

// requestReplay asks a host to resend what it is holding for a client that has
// just started watching. The host answers on its own connection, so the bytes
// arrive as ordinary data frames and reach every subscriber like live output.
func (d *Daemon) requestReplay(id session.ID, conn *ipc.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()

	if _, err := conn.Call(ctx, session.MethodReplay, struct{}{}); err != nil {
		// A session with nothing buffered, or one whose host is going away, is
		// not a failure the user needs to see: the screen simply starts empty.
		d.cfg.Log.Debug("replay request failed", "session", id, "err", err.Error())
	}
}

// relayCall forwards one call from an attached client to its host. The host's
// reply is handed back untouched, so the client decodes the same shape it would
// have got by dialling the host itself.
func (d *Daemon) relayCall(ctx context.Context, peer *ipc.Conn, id session.ID, method string, params json.RawMessage) (any, error) {
	// The diff and its notes belong to the checkout, not to the agent: the daemon
	// answers them itself instead of forwarding to a host with no repository.
	switch method {
	case session.MethodSeen:
		// Seen belongs to the window that looked, and this is the only layer
		// that can tell one window from another. Forwarding it to the host
		// would clear every window's badge, and would turn a PTY session the
		// host cannot classify from unknown into idle.
		d.relay.markSeen(id, peer.Client())
		return struct{}{}, nil
	case session.MethodDiff:
		return d.readDiff(ctx, id, params)
	case session.MethodCommentList:
		return d.listComments(id)
	case session.MethodCommentAdd:
		return d.addComment(id, params)
	case session.MethodCommentRemove:
		return d.removeComment(id, params)
	case session.MethodCommit:
		return d.commit(ctx, id, params)
	case session.MethodPush:
		return d.push(ctx, id)
	case session.MethodPullRequest:
		return d.pullRequest(ctx, id, params)
	case session.MethodHistory:
		// The transcript is the daemon's own record of the session, not the
		// agent's current state: the host it came from may already be gone.
		return d.history(id, params)
	}

	switch method {
	case session.MethodInfo, session.MethodPrompt, session.MethodCancel,
		session.MethodPermissionReply, session.MethodResize, session.MethodStop,
		session.MethodSurface, session.MethodSurfaceSet:
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

// readDiff answers a diff request for one session's checkout. The turn scope
// falls back to the commit the daemon saw when the session started.
func (d *Daemon) readDiff(ctx context.Context, id session.ID, params json.RawMessage) (any, error) {
	var req session.DiffRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodDiff, err)
	}

	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodDiff, id, ErrUnknownSession)
	}

	request := diff.Request{
		Repo:  att.Info.Dir,
		Scope: diff.Scope(req.Scope),
		Base:  req.Base,
	}
	if request.Base == "" && request.Scope == diff.Turn {
		request.Base = d.baseline(id)
	}

	if req.Path != "" {
		patch, err := diff.Patch(ctx, request, req.Path)
		if err != nil {
			return nil, fmt.Errorf("%s on session %q: %w", session.MethodDiff, id, err)
		}
		return session.DiffResponse{Patch: patch}, nil
	}

	files, err := diff.Files(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodDiff, id, err)
	}
	return session.DiffResponse{Files: files}, nil
}

// listComments returns a session's notes, oldest first.
func (d *Daemon) listComments(id session.ID) (any, error) {
	list, err := d.comments.List(string(id))
	if err != nil {
		return nil, fmt.Errorf("list the comments of session %q: %w", id, err)
	}
	return session.CommentListResponse{Comments: list}, nil
}

// addComment stores one note on a diff line.
func (d *Daemon) addComment(id session.ID, params json.RawMessage) (any, error) {
	var req session.CommentAddRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodCommentAdd, err)
	}

	added, err := d.comments.Add(string(id), comments.Comment{
		Path: req.Path,
		Line: req.Line,
		Side: comments.Side(req.Side),
		Text: req.Text,
	})
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCommentAdd, id, err)
	}
	return added, nil
}

// removeComment deletes one note.
func (d *Daemon) removeComment(id session.ID, params json.RawMessage) (any, error) {
	var req session.CommentRemoveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodCommentRemove, err)
	}

	if err := d.comments.Remove(string(id), req.ID); err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCommentRemove, id, err)
	}
	return struct{}{}, nil
}

// commit stages every change in the session's checkout and commits it.
//
// The message comes from the caller: this daemon never invents a subject, for
// the same reason it does not invent a diff summary — the person who presses
// the button has to have read what goes into history.
func (d *Daemon) commit(ctx context.Context, id session.ID, params json.RawMessage) (any, error) {
	var req session.CommitRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodCommit, err)
	}

	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCommit, id, ErrUnknownSession)
	}

	res, err := gitops.Commit(ctx, att.Info.Dir, req.Message)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodCommit, id, err)
	}
	d.cfg.Log.Info("session committed", "session", id, "ref", res.Ref)
	return session.WriteResponse{Summary: res.Summary, Ref: res.Ref, Output: res.Output}, nil
}

// push sends the session's branch to origin, through the user's own credentials.
func (d *Daemon) push(ctx context.Context, id session.ID) (any, error) {
	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodPush, id, ErrUnknownSession)
	}

	res, err := gitops.Push(ctx, att.Info.Dir)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodPush, id, err)
	}
	d.cfg.Log.Info("session pushed", "session", id, "ref", res.Ref)
	return session.WriteResponse{Summary: res.Summary, Ref: res.Ref, Output: res.Output}, nil
}

// pullRequest opens a pull request for the session's branch with gh.
func (d *Daemon) pullRequest(ctx context.Context, id session.ID, params json.RawMessage) (any, error) {
	var req session.PullRequestRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", session.MethodPullRequest, err)
	}

	att := d.lookup(id)
	if att == nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodPullRequest, id, ErrUnknownSession)
	}

	res, err := gitops.PullRequest(ctx, att.Info.Dir, req.Title, req.Body, req.Base)
	if err != nil {
		return nil, fmt.Errorf("%s on session %q: %w", session.MethodPullRequest, id, err)
	}
	d.cfg.Log.Info("session opened a pull request", "session", id, "ref", res.Ref)
	return session.WriteResponse{Summary: res.Summary, Ref: res.Ref, Output: res.Output}, nil
}

// baseline reports the commit a session started at, if the daemon watched it
// start. A session reattached after a restart has none, and the turn scope then
// asks the caller for one.
func (d *Daemon) baseline(id session.ID) string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.baselines[id]
}

// remember records the commit a session started at.
func (d *Daemon) remember(id session.ID, base string) {
	if base == "" {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.baselines[id] = base
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
			// A state change is also the daemon's own business: status reports
			// read it, so it is recorded before being fanned out. A daemon that
			// only forwarded it would answer "unknown" for a session the host
			// had already classified.
			if method == session.MethodState {
				var update session.StateUpdate
				if err := json.Unmarshal(params, &update); err == nil {
					d.rememberState(id, update.State)
					// A turn that just finished is unread in every window:
					// the badge belongs to this turn, not to one read earlier.
					// Any other state leaves the flags alone, because a
					// transition back to done means new work arrived.
					if update.State == session.StateDone {
						d.relay.forgetSeen(id)
					}
				}
			}

			// An agent update is stored before it is forwarded, so a window that
			// attaches later can be told what it missed. What goes out carries
			// the sequence the store gave it, which is how a client discards an
			// update it already has from its attach history. A replay of work the
			// log has already dropped is forwarded to nobody: it would put a line
			// from before the window's whole log at the end of it.
			forwarded := params
			if method == session.MethodUpdate {
				var keep bool
				forwarded, keep = d.recordUpdate(id, params)
				if !keep {
					return struct{}{}, nil
				}
			}

			for _, peer := range d.relay.subscribers(id) {
				err := d.relay.deliver(peer, func() error { return peer.Notify(method, forwarded) })
				if err != nil {
					d.cfg.Log.Debug("forward a session notification failed", "session", id, "err", err.Error())
				}
			}
			return struct{}{}, nil
		},
		OnData: func(_ *ipc.Conn, stream uint32, payload []byte) {
			for _, peer := range d.relay.subscribers(id) {
				// A failed send reaches the client's own connection close, which
				// is the event the daemon acts on; a logged line per failed byte
				// would be a flood of its own.
				_ = d.relay.deliver(peer, func() error { return peer.SendData(stream, payload) })
			}
		},
	}
}

// rememberState records the lifecycle a host reported, so a status poll answers
// with what the host last said rather than with the state it had at attach.
func (d *Daemon) rememberState(id session.ID, state session.State) {
	if !state.Valid() {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.fleet.Attachments {
		if d.fleet.Attachments[i].Record.ID == id {
			d.fleet.Attachments[i].Info.State = state
			return
		}
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

// dropSession forgets a session whose host connection stopped and tells every
// client it is over: the connection they were holding has nothing behind it. A
// status report must not keep listing a host that is gone, so the attachment
// goes too.
//
// Nothing here reads the record to decide: a host publishes before it listens
// and removes its record after it closes the pipes, so at the moment this runs
// both orders are possible. The record's own state is not what makes the session
// gone — the connection is. A host that is merely wedged is still attached and
// never reaches this path; one whose record survives a crash is pruned by the
// next scan, which has the pid to check.
func (d *Daemon) dropSession(id session.ID) {
	d.end(id, false)

	for _, peer := range d.relay.drop(id) {
		peer.Close()
	}
}

// end forgets a session and removes its record. An explicit stop takes the
// record unconditionally: the user asked for the session to be gone, and the
// host may still be alive while it winds down. A host that went on its own
// keeps its record unless nothing stands behind its pid, because a merely
// wedged host deserves another attempt on the next scan.
func (d *Daemon) end(id session.ID, explicit bool) {
	d.forget(id)
	// The handle a finished session wrote through goes; the file it wrote stays,
	// because "the agent is gone" is not "what the agent did did not happen".
	d.releaseTranscript(id)

	// An explicit stop does not read the record first. On Windows a read that
	// races the host's own removal fails with a sharing violation rather than
	// ENOENT, and treating any read error as "nothing to do" would leave the
	// record behind — the session the user killed would come back on the next
	// daemon start. Removing a file that is already gone is fine; removing one
	// that is still there is the whole point.
	if explicit {
		d.prune(id, "stopped")
		return
	}

	rec, err := d.reg.Read(id)
	if err != nil {
		return
	}
	if !processAlive(rec.PID) {
		d.prune(id, "dead")
	}
}

// prune removes a session record, retrying a removal that loses a race with the
// host. The host deletes its own record as it shuts down, so the two can meet;
// a few short retries turn that into one successful delete instead of a warning
// and a record nobody will look at again.
func (d *Daemon) prune(id session.ID, why string) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = d.reg.Unpublish(id); err == nil {
			d.cfg.Log.Info("pruned a "+why+" host record", "session", id)
			return
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}

	d.cfg.Log.Warn("cannot prune a "+why+" host record", "session", id, "err", err.Error())
}

// forget removes a session from the fleet this daemon reports.
func (d *Daemon) forget(id session.ID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// A new slice, not a compaction in place: lookup hands out pointers into
	// this slice, and overwriting an element would silently retarget one.
	kept := make([]Attachment, 0, len(d.fleet.Attachments))
	for _, att := range d.fleet.Attachments {
		if att.Record.ID != id {
			kept = append(kept, att)
		}
	}
	d.fleet.Attachments = kept
	delete(d.baselines, id)
	d.relay.forgetSeen(id)
}

// stopTimeout bounds the request that asks a host to end its session. A host
// that is going away closes the pipe quickly; one that does not answer is
// treated as gone anyway, so the wait must be short enough not to look hung.
const stopTimeout = 5 * time.Second

// replayTimeout bounds the request for scrollback. A large buffer is a few
// hundred kilobytes; a host that cannot answer that quickly has nothing worth
// waiting for.
const replayTimeout = 5 * time.Second

// stopSession ends a session on request. It asks the host to stop, then drops
// the session whatever the host answered: a host that cannot take the call is
// one that is not there, and the user asked for it to be gone.
func (d *Daemon) stopSession(ctx context.Context, params json.RawMessage) (any, error) {
	var req session.StopRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode %s: %w", MethodSessionStop, err)
	}
	if req.ID == "" {
		return nil, fmt.Errorf("%s: empty session id: %w", MethodSessionStop, session.ErrNoRecord)
	}

	att := d.lookup(req.ID)
	if att == nil {
		// Not serving it is not an error for a caller that wants it gone.
		return struct{}{}, nil
	}
	// forget rebuilds the fleet slice, so the connection is read out first.
	conn := att.Conn

	// A host closes its connection as it stops, so this call usually fails with
	// the pipe gone. That is the intended result, not a reason to keep going.
	// The bound matters: a wedged host that never answers must not keep the
	// caller from clearing the row, and the caller's own context may be the
	// background one with no deadline of its own.
	stopCtx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	_, _ = conn.Call(stopCtx, session.MethodStop, struct{}{})

	// The user asked for this session to be gone, so its record goes even if the
	// host has not finished exiting yet.
	d.end(req.ID, true)

	for _, peer := range d.relay.drop(req.ID) {
		peer.Close()
	}
	conn.Close()

	d.cfg.Log.Info("session stopped", "session", req.ID)
	return struct{}{}, nil
}

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// bufferedUpdates bounds how many agent updates a detached ACP session keeps for
// its next client. Past that the oldest go. This is the first of two layers: the
// host keeps the last few for a client that is only briefly away, and the daemon
// stores every update for a window that opens later, or for one that arrives
// after this process is gone.
const bufferedUpdates = 256

// maxResumes and resumeWindow bound the crash loop: an agent that keeps dying
// is relaunched only this many times inside the window, and past that the host
// closes instead of spawning forever.
const (
	maxResumes   = 3
	resumeWindow = 10 * time.Minute
)

// errWrongMode reports a call that does not fit this session's mode.
var errWrongMode = ipc.Class(errors.New("host: wrong session mode"), ipc.CodeWrongMode)

// errBadSize reports a resize that names no terminal size.
var errBadSize = ipc.Class(errors.New("host: a terminal needs positive columns and rows"), ipc.CodeBadRequest)

// pendingQuestion is one ask a client has not answered, kept so a client that
// attaches later is handed the card it missed. sent names the connection the
// ask went to, which is what keeps a reattach from sending it twice.
type pendingQuestion struct {
	answer chan acp.PermissionAnswer
	ask    session.PermissionQuestion
	sent   *ipc.Conn
}

// acpSession is the ACP half of a host: the agent conversation plus the pending
// permission questions the client has not answered yet.
type acpSession struct {
	sess *acp.Session

	mu      sync.Mutex
	pending map[string]*pendingQuestion
	nextID  int
	// gen increments on relaunch, so a waiter from the old agent cannot report
	// state for the new one.
	gen    uint64
	buffer []session.UpdateMessage
	// nextSeq numbers every update this host produces. A host hands its buffer
	// to a daemon that reconnects, and this is the number that lets that daemon
	// tell a replay of work it already stored from new work.
	nextSeq uint64
	// resumes are the relaunch times the crash cap counts, oldest first.
	resumes []time.Time
	// answer collects the turn's message text so the prompt reply carries it.
	answer    strings.Builder
	answering bool
	// answerCut records that the turn said more than one reply frame carries.
	answerCut bool
}

// acpAgent reads the current agent session under the lock a relaunch takes:
// a resume swaps the pointer while prompts and surface reads are using it.
func (h *Host) acpAgent() *acp.Session {
	h.acp.mu.Lock()
	defer h.acp.mu.Unlock()
	return h.acp.sess
}

// acpLaunch describes this host's agent process, resuming a saved conversation
// when an id is given.
func (h *Host) acpLaunch(resumeID string) acp.Launch {
	return acp.Launch{
		Command:    h.cfg.Command,
		Args:       h.cfg.Args,
		Dir:        h.cfg.Dir,
		Env:        withEnv(agentEnv(h.baseEnv(), h.cfg.Adapter.Env), h.scopeEnv()),
		ResumeID:   resumeID,
		MCPServers: h.mcpServers(),
	}
}

// mcpServers is the fleet tool server every ACP session of this host gets. The
// env entries pin it to this session and this state root, because an agent need
// not pass its own environment to a server it starts.
func (h *Host) mcpServers() []sdk.McpServer {
	if h.cfg.MCPCommand == "" {
		return nil
	}

	env := []sdk.EnvVariable{
		{Name: "FLT_SESSION_ID", Value: string(h.cfg.ID)},
		{Name: "FLT_POLICY", Value: h.cfg.Policy.String()},
	}
	if h.cfg.Channel != "" {
		env = append(env, sdk.EnvVariable{Name: "FLT_CHANNEL", Value: h.cfg.Channel})
	}
	if root := os.Getenv("FLT_HOME"); root != "" {
		env = append(env, sdk.EnvVariable{Name: "FLT_HOME", Value: root})
	}

	return []sdk.McpServer{{Stdio: &sdk.McpServerStdio{
		Name:    "flotilla",
		Command: h.cfg.MCPCommand,
		Args:    []string{"mcp", "serve"},
		Env:     env,
	}}}
}

// lookPath is the resolver agentEnv uses for bare executable names; a variable
// so tests can pin the resolution without a real PATH.
var lookPath = exec.LookPath

// agentEnv lays the adapter's declared variables over the host's environment.
// A value that names an executable on PATH resolves to its absolute path: an
// adapter cannot know where a machine keeps its binaries, and a consumer that
// does not search PATH would fail on a bare name. A nil environment inherits
// this process's, which is what the child would have read anyway.
func agentEnv(environ []string, declared map[string]string) []string {
	if len(declared) == 0 {
		return environ
	}
	env := environ
	if env == nil {
		env = os.Environ()
	}
	for key, value := range declared {
		if !strings.ContainsAny(value, `\/`) {
			if resolved, err := lookPath(value); err == nil {
				value = resolved
			}
		}
		env = append(env, key+"="+value)
	}
	return env
}

// acpConfig is the client behaviour every launch of this host's agent gets.
func (h *Host) acpConfig() acp.Config {
	return acp.Config{
		Workspace:    h.cfg.Dir,
		Policy:       h.cfg.Policy,
		Asker:        h.askClient,
		MaxReadBytes: ipc.MaxFrameSize,
		OnUpdate:     h.forwardUpdate,
		OnSurface:    h.surfaceChanged,
	}
}

// startACP opens an ACP conversation for this host. Policy zero is Supervised,
// so a caller that names none gets the cautious one.
func (h *Host) startACP(ctx context.Context) error {
	sess, err := acp.Open(ctx, h.acpLaunch(""), h.acpConfig())
	if err != nil {
		return fmt.Errorf("open the acp connection: %w", err)
	}

	h.acp = &acpSession{
		sess:    sess,
		pending: make(map[string]*pendingQuestion),
		// A session id can be reused after its first host is gone, and the
		// transcript is kept. Numbering from one again would make the new
		// run's first update look like a replay of the old run's.
		nextSeq: h.cfg.SeqBase,
	}
	return nil
}

// resumeAllowed counts one relaunch against the crash cap. Times outside the
// window fall away, so a long-lived session is not punished for old crashes.
func (h *Host) resumeAllowed() bool {
	h.acp.mu.Lock()
	defer h.acp.mu.Unlock()

	cutoff := time.Now().Add(-resumeWindow)
	kept := make([]time.Time, 0, len(h.acp.resumes))
	for _, at := range h.acp.resumes {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	h.acp.resumes = kept
	if len(kept) >= maxResumes {
		return false
	}
	kept = append(kept, time.Now())
	h.acp.resumes = kept
	return true
}

// resumeACP relaunches the agent inside this host and reopens the saved
// conversation. The buffer and the numbering stay: a daemon's transcript must
// still recognise what the dead agent already produced. An in-flight prompt is
// never resent — its call already failed when the pipe died.
func (h *Host) resumeACP(ctx context.Context, id sdk.SessionId) error {
	sess, err := acp.Open(ctx, h.acpLaunch(string(id)), h.acpConfig())
	if err != nil {
		return fmt.Errorf("relaunch the agent: %w", err)
	}

	h.acp.mu.Lock()
	// A stop that arrived during the relaunch owns the host. Close stores
	// closing before it reads the session under this lock, so the new agent is
	// either swapped in for Close to find or closed here — never orphaned.
	if h.closing.Load() {
		h.acp.mu.Unlock()
		_ = sess.Close()
		return fmt.Errorf("host %q is closing", h.cfg.ID)
	}
	h.acp.sess = sess
	// The old questions belonged to the dead connection; their waiters already
	// unwound with it, and a stale answer must not reach the new agent.
	h.acp.pending = make(map[string]*pendingQuestion)
	h.acp.gen++
	h.acp.mu.Unlock()

	h.surfaceChanged(acp.SurfaceChange{})
	return nil
}

// answerBudget bounds the answer one prompt reply carries. The reply is a
// single control frame capped at ipc.MaxFrameSize, and JSON escaping can cost
// several bytes per byte, so the answer leaves ample room for the envelope.
const answerBudget = updateTextBudget

// appendCapped appends text while it fits budget, cutting on a rune boundary.
// It reports whether the whole text fit; false means the turn said more.
func appendCapped(b *strings.Builder, text string, budget int) bool {
	room := budget - b.Len()
	if room <= 0 {
		return false
	}
	if len(text) <= room {
		b.WriteString(text)
		return true
	}

	cut := room
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	b.WriteString(text[:cut])
	return false
}

// isTurnSession reports whether an update belongs to this host's own
// conversation rather than a child session the agent announced. The ACP client
// stamps every update with the id it arrived under, so the comparison is
// against the id the agent assigned this session.
func (h *Host) isTurnSession(id string) bool {
	if id == "" {
		return true
	}
	sess := h.acpAgent()
	return sess != nil && id == string(sess.ID())
}

// forwardUpdate pushes one agent update to the attached client. With no client
// it is buffered, so a reattach shows what happened while nobody listened. An
// update too large for one frame is split: a streaming client already renders
// pieces of one message, so dropping it would lose what splitting keeps.
//
// Every piece carries the number this host gave it, because a daemon that
// reconnects is handed the buffer again and must recognise what it already has.
func (h *Host) forwardUpdate(u acp.Update) {
	h.touch()
	if u.Kind == acp.UpdateSubagent && u.SubagentID != "" && u.SubagentState != "" {
		h.mu.Lock()
		h.family[u.SubagentID] = u.SubagentState
		h.mu.Unlock()
	}
	// The turn's own messages are also the turn's answer. A message from a
	// child session the agent announced is not this turn speaking.
	if u.Kind == session.KindMessage && h.isTurnSession(u.SessionID) {
		h.acp.mu.Lock()
		if h.acp.answering && !appendCapped(&h.acp.answer, u.Text, answerBudget) {
			h.acp.answerCut = true
		}
		h.acp.mu.Unlock()
	}

	msg := session.UpdateMessage{
		Kind:                u.Kind,
		Text:                u.Text,
		ToolCallID:          u.ToolCallID,
		ToolTitle:           u.ToolTitle,
		ToolStatus:          u.ToolStatus,
		SessionID:           u.SessionID,
		SubagentID:          u.SubagentID,
		SubagentTitle:       u.SubagentTitle,
		SubagentDescription: u.SubagentDescription,
		SubagentState:       u.SubagentState,
	}

	for _, part := range chunkUpdate(msg, updateTextBudget) {
		h.deliverUpdate(part)
	}
}

// deliverUpdate pushes one update to the client, or buffers it when nobody is
// listening.
func (h *Host) deliverUpdate(msg session.UpdateMessage) {
	msg.HostSeq = h.nextUpdateSeq()

	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		h.acp.mu.Lock()
		if len(h.acp.buffer) >= bufferedUpdates {
			h.acp.buffer = h.acp.buffer[1:]
		}
		h.acp.buffer = append(h.acp.buffer, msg)
		h.acp.mu.Unlock()
		return
	}

	if err := conn.Notify(session.MethodUpdate, msg); err != nil {
		h.log.Debug("forward an update failed", "session", h.cfg.ID, "err", err.Error())
	}
}

// nextUpdateSeq hands out the next number for an update. It is one counter for
// the whole session rather than one for the buffer: a number has to keep its
// meaning after the update it belongs to has been trimmed away.
func (h *Host) nextUpdateSeq() uint64 {
	h.acp.mu.Lock()
	defer h.acp.mu.Unlock()

	h.acp.nextSeq++
	return h.acp.nextSeq
}

// updateTextBudget bounds the text one update carries. A frame holds
// ipc.MaxFrameSize bytes, JSON escaping can cost six bytes per byte, and the
// envelope needs room of its own.
const updateTextBudget = ipc.MaxFrameSize / 8

// chunkUpdate splits an update whose text does not fit the budget into
// consecutive updates of the same kind, cutting on rune boundaries so a split
// never breaks a character. Every piece after the first is marked as a
// continuation, so a client joins the pieces of one message and still starts a
// new line for a second message of the same kind.
func chunkUpdate(msg session.UpdateMessage, budget int) []session.UpdateMessage {
	if budget <= 0 || len(msg.Text) <= budget {
		return []session.UpdateMessage{msg}
	}

	parts := make([]session.UpdateMessage, 0, len(msg.Text)/budget+1)
	text := msg.Text

	for len(text) > budget {
		cut := budget
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			cut = budget
		}

		part := msg
		part.Text = text[:cut]
		part.Continued = len(parts) > 0
		parts = append(parts, part)
		text = text[cut:]
	}

	part := msg
	part.Text = text
	part.Continued = len(parts) > 0
	return append(parts, part)
}

// askClient sends a permission question and waits for the answer. A client
// that disconnects becomes a refusal; an unanswered question waits forever
// unless the host was given an explicit PermissionWait.
func (h *Host) askClient(ctx context.Context, req acp.PermissionRequest) (acp.PermissionAnswer, error) {
	options := make([]session.PermissionOption, 0, len(req.Options))
	for _, opt := range req.Options {
		// The agent's own label travels with the choice: it is the only party
		// that knows what "allow_once" means for this tool call, and a client
		// that received only the id would have to invent a word or show one.
		options = append(options, session.PermissionOption{
			ID:   opt.ID,
			Kind: opt.Kind,
			Name: opt.Name,
		})
	}

	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	// Nobody is listening: the agent hears a refusal at once instead of waiting
	// on a card no window can see.
	if conn == nil {
		return acp.PermissionAnswer{}, nil
	}

	h.acp.mu.Lock()
	h.acp.nextID++
	id := fmt.Sprintf("perm-%d", h.acp.nextID)
	pq := &pendingQuestion{
		answer: make(chan acp.PermissionAnswer, 1),
		ask: session.PermissionQuestion{
			ID:      id,
			Title:   req.Title,
			Kind:    req.Kind,
			Options: options,
		},
		sent: conn,
	}
	h.acp.pending[id] = pq
	gen := h.acp.gen
	h.acp.mu.Unlock()

	// Every exit removes this ask; only the exits that let the agent move on
	// report working, and then only when no other card is still waiting. An
	// agent can ask twice for parallel tool calls, and one answer is not the
	// turn resuming while the second question waits.
	resume := false
	defer func() {
		if h.questionDone(gen, id) && resume {
			h.setState(session.StateWorking)
		}
	}()

	// The agent is waiting on the user. This is the exact case `blocked` names:
	// an approval or question UI the session cannot get past on its own. Unlike
	// a PTY session, ACP tells us rather than leaving us to recognise a dialog.
	h.setState(session.StateBlocked)

	if err := conn.Notify(session.MethodPermission, pq.ask); err != nil {
		// A client that attached meanwhile was handed the ask by the replay, so
		// this failure is not a refusal: the card is visible and still waits.
		h.acp.mu.Lock()
		delivered := pq.sent != conn
		h.acp.mu.Unlock()
		if !delivered {
			resume = true
			return acp.PermissionAnswer{}, nil
		}
	}

	timer := h.cfg.permissionTimer()
	if timer != nil {
		defer timer.Stop()
	}
	var timeout <-chan time.Time
	if timer != nil {
		timeout = timer.C
	}

	select {
	case a := <-pq.answer:
		// The connection may have died with the answer in flight: a cancelled
		// context means the turn is gone, and working would claim it resumed.
		if ctx.Err() != nil {
			return acp.PermissionAnswer{}, nil
		}
		// Answered: the turn resumes, so the session is working again.
		resume = true
		return a, nil
	case <-ctx.Done():
		return acp.PermissionAnswer{}, nil
	case <-timeout:
		h.log.Warn("permission unanswered", "session", h.cfg.ID, "title", req.Title)
		if ctx.Err() != nil {
			return acp.PermissionAnswer{}, nil
		}
		// The refusal reaches the agent and the turn moves on, so the session
		// is no longer waiting on anyone.
		resume = true
		return acp.PermissionAnswer{}, nil
	case <-h.donec:
		return acp.PermissionAnswer{}, nil
	}
}

// questionDone removes one ask and reports whether any question still waits. A
// waiter from an agent that was relaunched reports false without touching the
// new agent's questions.
func (h *Host) questionDone(gen uint64, id string) bool {
	h.acp.mu.Lock()
	defer h.acp.mu.Unlock()

	if h.acp.gen != gen {
		return false
	}
	delete(h.acp.pending, id)
	return len(h.acp.pending) == 0
}

// permissionTimer arms the configured wait, or returns nil for forever. A nil
// timer's channel is nil, which a select never chooses: that is what makes the
// default wait unbounded without a special case in the select itself.
func (c Config) permissionTimer() *time.Timer {
	if c.PermissionWait <= 0 {
		return nil
	}
	return time.NewTimer(c.PermissionWait)
}

// answerPermission resolves a pending question. An unknown id is a stale answer
// from a client that reconnected, so it is not an error.
func (h *Host) answerPermission(reply session.PermissionReply) {
	if h.acp == nil {
		return
	}

	h.acp.mu.Lock()
	pq, ok := h.acp.pending[reply.ID]
	h.acp.mu.Unlock()

	if !ok {
		return
	}
	select {
	case pq.answer <- acp.PermissionAnswer{OptionID: reply.OptionID}:
	default:
	}
}

// replayQuestions hands a client that just attached every card still waiting.
// The agent asked before this connection existed, and its turn stays blocked
// until one of them is answered.
func (h *Host) replayQuestions(conn *ipc.Conn) {
	if h.acp == nil {
		return
	}

	h.acp.mu.Lock()
	asks := make([]session.PermissionQuestion, 0, len(h.acp.pending))
	for _, pq := range h.acp.pending {
		if pq.sent == conn {
			continue
		}
		pq.sent = conn
		asks = append(asks, pq.ask)
	}
	h.acp.mu.Unlock()

	for _, ask := range asks {
		if err := conn.Notify(session.MethodPermission, ask); err != nil {
			return
		}
	}
}

// replayBuffered sends the updates a detached client missed.
func (h *Host) replayBuffered(conn *ipc.Conn) {
	if h.acp == nil {
		return
	}

	h.acp.mu.Lock()
	buffered := h.acp.buffer
	h.acp.buffer = nil
	h.acp.mu.Unlock()

	for _, msg := range buffered {
		if err := conn.Notify(session.MethodUpdate, msg); err != nil {
			return
		}
	}
}

// promptACP runs one turn.
func (h *Host) promptACP(ctx context.Context, req session.PromptRequest) (session.PromptResponse, error) {
	if h.acp == nil {
		return session.PromptResponse{}, fmt.Errorf("session %q is not an ACP session: %w", h.cfg.ID, errWrongMode)
	}

	// The agent reports only its own side of the conversation, so the question
	// is recorded here, before the turn: a client that opens the session later
	// would otherwise read the reply as an answer to nothing. It goes through
	// the same path as an agent update, which is what numbers it, buffers it for
	// a client that is away, and hands it to the daemon's log.
	h.forwardUpdate(acp.Update{Kind: session.KindUser, Text: req.Text})

	// The turn is now running. An ACP session knows this exactly: the call is
	// synchronous and returns when the agent stops.
	h.setState(session.StateWorking)

	// The ACP connection handles every update it received before a response
	// before the call returns, so the collector is complete when Prompt does.
	h.acp.mu.Lock()
	h.acp.answer.Reset()
	h.acp.answerCut = false
	h.acp.answering = true
	h.acp.mu.Unlock()

	resp, err := h.acpAgent().Prompt(ctx, sdk.TextBlock(req.Text))

	h.acp.mu.Lock()
	answer := h.acp.answer.String()
	truncated := h.acp.answerCut
	h.acp.answering = false
	h.acp.answer.Reset()
	h.acp.answerCut = false
	h.acp.mu.Unlock()

	if err != nil {
		// A failed turn is not "done": the work did not happen, and a caller
		// that reads done would think there is something to look at. Unknown
		// says what this build knows.
		h.setState(session.StateUnknown)
		return session.PromptResponse{}, fmt.Errorf("run the turn: %w", err)
	}

	// The agent stopped. Whether the user has seen the result is the client's
	// question, so the host reports done and the client marks it seen.
	h.setState(session.StateDone)
	return session.PromptResponse{
		StopReason:      string(resp.StopReason),
		Answer:          answer,
		AnswerTruncated: truncated,
	}, nil
}

// cancelACP stops the running turn.
func (h *Host) cancelACP(ctx context.Context) error {
	if h.acp == nil {
		return fmt.Errorf("session %q is not an ACP session: %w", h.cfg.ID, errWrongMode)
	}
	if err := h.acpAgent().Cancel(ctx); err != nil {
		return fmt.Errorf("cancel the turn: %w", err)
	}
	return nil
}

func (h *Host) closeACP() []error {
	if h.acp == nil {
		return nil
	}
	if err := h.acpAgent().Close(); err != nil {
		return []error{fmt.Errorf("close the acp session: %w", err)}
	}
	return nil
}

// decodeParams decodes one call's parameters. Empty parameters are allowed: a
// method with no arguments is a normal method.
func decodeParams[T any](params json.RawMessage) (T, error) {
	var out T
	if len(params) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(params, &out); err != nil {
		return out, fmt.Errorf("decode parameters: %w", err)
	}
	return out, nil
}

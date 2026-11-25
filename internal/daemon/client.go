package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/schedule"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Client is a CLI or GUI's handle on a running daemon. It holds one connection
// and reuses it; a daemon restart means a new Client.
type Client struct {
	conn *ipc.Conn
	// channel, token and stateDir are kept so a prompt can open a connection of
	// its own: the daemon binds a connection to one session, and the CLI's
	// control connection has no session to be bound to.
	channel  string
	token    string
	stateDir string
}

// Connect dials the daemon serving channel, using the token the daemon wrote.
func Connect(ctx context.Context, channel, token, stateDir string) (*Client, error) {
	addr := ipc.Address{Channel: channel, Name: "daemon"}

	conn, err := ipc.Dial(ctx, addr, stateDir, ipc.Config{Token: token, Role: "cli"})
	if err != nil {
		return nil, fmt.Errorf("connect to the daemon: %w", err)
	}
	return &Client{conn: conn, channel: channel, token: token, stateDir: stateDir}, nil
}

// Close releases the connection. The daemon keeps running.
func (c *Client) Close() { c.conn.Close() }

// Alive reports whether the connection is still up.
func (c *Client) Alive() bool { return c.conn.Alive() }

// Status reports the fleet the daemon serves.
func (c *Client) Status(ctx context.Context) (Status, error) {
	raw, err := c.conn.Call(ctx, MethodStatus, struct{}{})
	if err != nil {
		return Status{}, fmt.Errorf("ask the daemon for its status: %w", err)
	}

	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return Status{}, fmt.Errorf("decode daemon status: %w", err)
	}
	return st, nil
}

// Agents lists the agents this machine can offer, with their switches.
func (c *Client) Agents(ctx context.Context) (agent.Listing, error) {
	raw, err := c.conn.Call(ctx, MethodAgents, struct{}{})
	if err != nil {
		return agent.Listing{}, fmt.Errorf("ask the daemon for its agents: %w", err)
	}

	var list agent.Listing
	if err := json.Unmarshal(raw, &list); err != nil {
		return agent.Listing{}, fmt.Errorf("decode the agent listing: %w", err)
	}
	return list, nil
}

// StartSession asks the daemon to take a session.
func (c *Client) StartSession(ctx context.Context, req StartRequest) (StartResponse, error) {
	raw, err := c.conn.Call(ctx, MethodSessionStart, req)
	if err != nil {
		return StartResponse{}, fmt.Errorf("ask the daemon to start a session: %w", err)
	}

	var resp StartResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return StartResponse{}, fmt.Errorf("decode session start: %w", err)
	}
	return resp, nil
}

// StopSession asks the daemon to end a session. Stopping one that is already
// gone succeeds: the caller asked for it to be gone.
func (c *Client) StopSession(ctx context.Context, id session.ID) error {
	req := session.StopRequest{ID: id}
	if _, err := c.conn.Call(ctx, MethodSessionStop, req); err != nil {
		return fmt.Errorf("ask the daemon to stop session %q: %w", id, err)
	}
	return nil
}

// PromptResult is one prompt's outcome: why the turn stopped and what the agent
// said while it ran.
type PromptResult struct {
	StopReason string `json:"stop_reason"`
	// Answer is the agent's message text for the turn, without thoughts or tools.
	Answer string `json:"answer,omitempty"`
	// AnswerTruncated says the answer was cut to fit one frame.
	AnswerTruncated bool `json:"answer_truncated,omitempty"`
	// Declined counts the tool-permission questions refused during the turn
	// because no person was attached to answer them.
	Declined int `json:"declined,omitempty"`
}

// PromptSession sends one message to a session and waits for the turn to end.
// It opens a connection of its own and binds it to the session, because the
// daemon relays a session's calls and questions only to a bound connection; the
// connection is closed when the turn is over.
func (c *Client) PromptSession(ctx context.Context, id session.ID, text string) (PromptResult, error) {
	if c.channel == "" {
		return PromptResult{}, fmt.Errorf("prompt session %q: this client has no daemon link", id)
	}

	var (
		mu       sync.Mutex
		declined int
	)
	conn, err := ipc.Dial(ctx, ipc.Address{Channel: c.channel, Name: "daemon"}, c.stateDir, ipc.Config{
		Token: c.token,
		Role:  "cli",
		Handler: func(ctx context.Context, peer *ipc.Conn, method string, params json.RawMessage) (any, error) {
			if method != session.MethodPermission {
				return nil, ipc.ErrNoHandler
			}
			var q session.PermissionQuestion
			if err := json.Unmarshal(params, &q); err != nil {
				return nil, fmt.Errorf("decode the permission question: %w", err)
			}
			// Nobody is attached to read this card: cancel the tool call rather
			// than leave the turn waiting on a person who is not there. The
			// host cannot finish the turn until this answer arrives, so the
			// count is settled before the prompt reply is.
			_, _ = peer.Call(ctx, session.MethodPermissionReply, session.PermissionReply{ID: q.ID})
			mu.Lock()
			declined++
			mu.Unlock()
			return struct{}{}, nil
		},
	})
	if err != nil {
		return PromptResult{}, fmt.Errorf("open a session connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Call(ctx, session.MethodAttach, session.AttachRequest{ID: id}); err != nil {
		return PromptResult{}, fmt.Errorf("attach to session %q: %w", id, err)
	}

	raw, err := conn.Call(ctx, session.MethodPrompt, session.PromptRequest{Text: text})
	if err != nil {
		return PromptResult{}, fmt.Errorf("prompt session %q: %w", id, err)
	}
	var resp session.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return PromptResult{}, fmt.Errorf("decode the prompt answer: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()
	return PromptResult{
		StopReason:      resp.StopReason,
		Answer:          resp.Answer,
		AnswerTruncated: resp.AnswerTruncated,
		Declined:        declined,
	}, nil
}

// Schedules lists the daemon's timed jobs.
func (c *Client) Schedules(ctx context.Context) ([]schedule.Schedule, error) {
	raw, err := c.conn.Call(ctx, MethodScheduleList, struct{}{})
	if err != nil {
		return nil, fmt.Errorf("ask the daemon for its schedules: %w", err)
	}
	var resp ScheduleListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode the schedules: %w", err)
	}
	return resp.Schedules, nil
}

// SetSchedule creates or edits one schedule.
func (c *Client) SetSchedule(ctx context.Context, req ScheduleSetRequest) (schedule.Schedule, error) {
	raw, err := c.conn.Call(ctx, MethodScheduleSet, req)
	if err != nil {
		return schedule.Schedule{}, fmt.Errorf("set a schedule: %w", err)
	}
	var applied schedule.Schedule
	if err := json.Unmarshal(raw, &applied); err != nil {
		return schedule.Schedule{}, fmt.Errorf("decode the schedule: %w", err)
	}
	return applied, nil
}

// RemoveSchedule deletes one schedule.
func (c *Client) RemoveSchedule(ctx context.Context, id string) error {
	if _, err := c.conn.Call(ctx, MethodScheduleRemove, ScheduleRemoveRequest{ID: id}); err != nil {
		return fmt.Errorf("remove schedule %q: %w", id, err)
	}
	return nil
}

// TriggerSchedule runs one schedule now. The same request id twice runs it once.
func (c *Client) TriggerSchedule(ctx context.Context, id, requestID string) (ScheduleTriggerResponse, error) {
	raw, err := c.conn.Call(ctx, MethodScheduleTrigger, ScheduleTriggerRequest{ID: id, RequestID: requestID})
	if err != nil {
		return ScheduleTriggerResponse{}, fmt.Errorf("trigger schedule %q: %w", id, err)
	}
	var resp ScheduleTriggerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ScheduleTriggerResponse{}, fmt.Errorf("decode the trigger answer: %w", err)
	}
	return resp, nil
}

package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Client is a CLI or GUI's handle on a running daemon. It holds one connection
// and reuses it; a daemon restart means a new Client.
type Client struct {
	conn *ipc.Conn
}

// Connect dials the daemon serving channel, using the token the daemon wrote.
func Connect(ctx context.Context, channel, token, stateDir string) (*Client, error) {
	addr := ipc.Address{Channel: channel, Name: "daemon"}

	conn, err := ipc.Dial(ctx, addr, stateDir, ipc.Config{Token: token, Role: "cli"})
	if err != nil {
		return nil, fmt.Errorf("connect to the daemon: %w", err)
	}
	return &Client{conn: conn}, nil
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

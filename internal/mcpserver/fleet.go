package mcpserver

import (
	"context"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/daemon"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Dialer reaches the daemon on demand. Every tool call opens its own
// connection: the server outlives daemon restarts, and the token file is the
// only name that stays valid across one.
type Dialer struct {
	Channel   string
	TokenPath string
	StateDir  string
}

func (d Dialer) connect(ctx context.Context) (*daemon.Client, error) {
	token, err := ipc.ReadTokenFile(d.TokenPath)
	if err != nil {
		return nil, fmt.Errorf("read the connection token: %w", err)
	}
	client, err := daemon.Connect(ctx, d.Channel, token, d.StateDir)
	if err != nil {
		return nil, fmt.Errorf("the %s daemon is not running: %w", d.Channel, err)
	}
	return client, nil
}

// Status reports the fleet the daemon serves.
func (d Dialer) Status(ctx context.Context) (daemon.Status, error) {
	client, err := d.connect(ctx)
	if err != nil {
		return daemon.Status{}, err
	}
	defer client.Close()
	st, err := client.Status(ctx)
	if err != nil {
		return daemon.Status{}, fmt.Errorf("read the fleet: %w", err)
	}
	return st, nil
}

// Agents lists the agents this machine offers.
func (d Dialer) Agents(ctx context.Context) (agent.Listing, error) {
	client, err := d.connect(ctx)
	if err != nil {
		return agent.Listing{}, err
	}
	defer client.Close()
	list, err := client.Agents(ctx)
	if err != nil {
		return agent.Listing{}, fmt.Errorf("list the agents: %w", err)
	}
	return list, nil
}

// StartSession asks the daemon for a session.
func (d Dialer) StartSession(ctx context.Context, req daemon.StartRequest) (daemon.StartResponse, error) {
	client, err := d.connect(ctx)
	if err != nil {
		return daemon.StartResponse{}, err
	}
	defer client.Close()
	resp, err := client.StartSession(ctx, req)
	if err != nil {
		return daemon.StartResponse{}, fmt.Errorf("start session %q: %w", req.ID, err)
	}
	return resp, nil
}

// StopSession asks the daemon to end a session.
func (d Dialer) StopSession(ctx context.Context, id session.ID) error {
	client, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.StopSession(ctx, id); err != nil {
		return fmt.Errorf("stop session %q: %w", id, err)
	}
	return nil
}

// PromptSession runs one turn on a session and waits for it to end.
func (d Dialer) PromptSession(ctx context.Context, id session.ID, text string) (daemon.PromptResult, error) {
	client, err := d.connect(ctx)
	if err != nil {
		return daemon.PromptResult{}, err
	}
	defer client.Close()
	res, err := client.PromptSession(ctx, id, text)
	if err != nil {
		return daemon.PromptResult{}, fmt.Errorf("prompt session %q: %w", id, err)
	}
	return res, nil
}

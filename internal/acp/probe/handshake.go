package probe

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
)

// Mode is one mode an agent offers, as it announced it. Current marks the one
// the agent reported as active; exactly one mode carries it when the agent sent
// a mode state at all.
type Mode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// Choice is one selectable value of a configuration option. Group is the header
// the agent put it under, and is empty for a flat list.
type Choice struct {
	Value string `json:"value"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
}

// ConfigOption is one configuration option an agent announced, flattened out of
// the SDK's union so a report can be read without knowing the union.
type ConfigOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type is "select" or "boolean", the two shapes ACP defines.
	Type    string   `json:"type"`
	Current string   `json:"current"`
	Choices []Choice `json:"choices,omitempty"`
}

// HandshakeResult is what the agent announced when its session was opened,
// and nothing else: no prompt was sent, so no turn ran and no token was spent.
type HandshakeResult struct {
	// Command is the agent that answered.
	Command string `json:"command"`
	// SessionID is the id the agent assigned.
	SessionID string `json:"session_id"`
	// Resume is whether the agent advertised session/resume.
	Resume bool `json:"resume"`
	// Modes is what the agent offers, empty when it announced no modes.
	Modes []Mode `json:"modes,omitempty"`
	// Options is what the agent announced as configuration, empty when it
	// announced none.
	Options []ConfigOption `json:"options,omitempty"`
	// Updates counts what the agent streamed while the session was open. A
	// handshake that sent a prompt would move this off zero, because every
	// ACP agent answers a turn with updates. It is the evidence that the probe
	// spent nothing, rather than a promise that it did not.
	Updates int `json:"updates"`
	// ElapsedMS is how long opening the session took, in milliseconds. It is a
	// number rather than a duration so the JSON report this feeds reads the same
	// to a person and to a script.
	ElapsedMS int64 `json:"elapsed_ms"`
}

// Handshake opens one session and reads what the agent announced, then closes it
// without sending a prompt. This is the zero-token probe: an agent that needs a
// login can still be measured, because nothing reaches its provider.
//
// It is the same opening acp.Open performs for a real session, so a handshake
// that passes here cannot fail on a different code path when a host runs it.
func Handshake(ctx context.Context, agent Agent, workspace string) (HandshakeResult, error) {
	binary, err := agent.Resolve()
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("probe %s: %w", agent.Command, err)
	}

	started := time.Now()
	// OnUpdate runs on the client's reader goroutine, which is still running
	// when this function reads the count. An atomic is what keeps that from
	// being a race on the only case that matters: an agent that pushes an update
	// before the first prompt.
	var updates atomic.Int64
	session, err := acp.Open(ctx, acp.Launch{
		Command: binary,
		Args:    agent.Args,
		Dir:     workspace,
	}, acp.Config{
		Workspace: workspace,
		// An agent that answers with updates is an agent a turn reached, which
		// would mean this probe spent tokens after all. Counting them is what
		// turns "sends no prompt" into something a test can assert.
		OnUpdate: func(acp.Update) { updates.Add(1) },
	})
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("probe %s: %w", agent.Command, err)
	}
	defer func() { _ = session.Close() }()

	return HandshakeResult{
		Command:   agent.Command,
		SessionID: string(session.ID()),
		Resume:    session.SupportsResume(),
		Modes:     projectModes(session.Modes()),
		Options:   projectOptions(session.ConfigOptions()),
		Updates:   int(updates.Load()),
		ElapsedMS: time.Since(started).Milliseconds(),
	}, nil
}

// projectModes flattens a mode state into what a report shows.
func projectModes(state *sdk.SessionModeState) []Mode {
	if state == nil {
		return nil
	}

	out := make([]Mode, 0, len(state.AvailableModes))
	for _, mode := range state.AvailableModes {
		out = append(out, Mode{
			ID:      string(mode.Id),
			Name:    mode.Name,
			Current: mode.Id == state.CurrentModeId,
		})
	}
	return out
}

// projectOptions flattens the configuration options into their two shapes. An
// option of neither shape is dropped rather than guessed at: the union has two
// variants, and a third would be one this build cannot describe.
func projectOptions(options []sdk.SessionConfigOption) []ConfigOption {
	out := make([]ConfigOption, 0, len(options))
	for _, option := range options {
		switch {
		case option.Select != nil:
			out = append(out, ConfigOption{
				ID:      string(option.Select.Id),
				Name:    option.Select.Name,
				Type:    "select",
				Current: string(option.Select.CurrentValue),
				Choices: projectChoices(option.Select.Options),
			})
		case option.Boolean != nil:
			out = append(out, ConfigOption{
				ID:      string(option.Boolean.Id),
				Name:    option.Boolean.Name,
				Type:    "boolean",
				Current: strconv.FormatBool(option.Boolean.CurrentValue),
			})
		}
	}
	return out
}

// projectChoices flattens a select's options, flat or grouped, into one list.
func projectChoices(options sdk.SessionConfigSelectOptions) []Choice {
	var out []Choice
	if options.Ungrouped != nil {
		for _, choice := range *options.Ungrouped {
			out = append(out, Choice{Value: string(choice.Value), Name: choice.Name})
		}
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			for _, choice := range group.Options {
				out = append(out, Choice{Value: string(choice.Value), Name: choice.Name, Group: group.Name})
			}
		}
	}
	return out
}

// Report renders the summary a maintainer pastes into a PR: one line that says
// what the agent is, whether a session survives its host, and how much it
// announced. The detail goes in Lines.
func (r HandshakeResult) Report() string {
	return fmt.Sprintf("%s: session=%s resume=%t modes=%d options=%d elapsed=%dms",
		r.Command, r.SessionID, r.Resume, len(r.Modes), len(r.Options),
		r.ElapsedMS)
}

// Lines renders the announcement in full, one fact per line, so a maintainer can
// read what a support-matrix row is claiming without a JSON viewer.
func (r HandshakeResult) Lines() []string {
	lines := []string{r.Report()}

	for _, mode := range r.Modes {
		current := ""
		if mode.Current {
			current = " (current)"
		}
		lines = append(lines, fmt.Sprintf("  mode %s = %s%s", mode.ID, mode.Name, current))
	}

	for _, option := range r.Options {
		lines = append(lines, fmt.Sprintf("  option %s = %s [%s] current=%s",
			option.ID, option.Name, option.Type, option.Current))
		for _, choice := range option.Choices {
			label := choice.Name
			if choice.Group != "" {
				label = choice.Group + " / " + choice.Name
			}
			lines = append(lines, fmt.Sprintf("    %s = %s", choice.Value, label))
		}
	}

	return lines
}

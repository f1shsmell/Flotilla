// Package probe runs one real ACP conversation against a configured agent, so a
// maintainer can confirm a vendor CLI works before a session host depends on it.
//
// It is the opposite of the fake in internal/acp/acptest: the fake proves the
// client logic, the probe proves this build speaks a specific agent's dialect.
// The probe costs real tokens, so nothing in the test suite calls it.
package probe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/agent"
	"github.com/f1shsmell/flotilla/internal/session"
)

// DefaultPrompt is what the probe sends. It asks for one short line so a probe
// costs as little as a turn can.
const DefaultPrompt = "Reply with exactly: flotilla-ok"

// Result is what one probe observed.
type Result struct {
	// Command is the agent that answered.
	Command string
	// SessionID is the id the agent assigned.
	SessionID string
	// Resume is whether the agent advertised session/resume, which decides
	// whether an ACP session can survive its host.
	Resume bool
	// StopReason is why the turn ended.
	StopReason string
	// Messages and Thoughts count the streamed updates of each kind.
	Messages int
	Thoughts int
	Tools    int
	// Reply is the agent's text, joined for a one-line report.
	Reply string
	// Elapsed is how long the turn took.
	Elapsed time.Duration
}

// Agent is one ACP agent this build knows how to start.
type Agent struct {
	// Name is the short name a user types.
	Name string
	// Command and Args launch it in ACP mode.
	Command string
	Args    []string
	// NeedsCredential names the environment variable or login the agent needs,
	// so a probe can say what is missing instead of timing out.
	NeedsCredential string
}

// Known lists the ACP agents this build has a name for, projected from
// agent.Builtins() so the picker and `flt probe` cannot drift apart. The list is
// a convenience, not a capability claim: what a host can do depends on the
// agent's own dialect, which the probe measures and docs/support-matrix.md
// records.
func Known() []Agent {
	presets := agent.Builtins()

	out := make([]Agent, 0, len(presets))
	for _, preset := range presets {
		// A probe drives ACP only; a preset for another protocol has no turn
		// for it to run.
		if preset.Mode != session.ModeACP {
			continue
		}
		out = append(out, Agent{
			Name:            preset.ID,
			Command:         preset.Command,
			Args:            preset.Args,
			NeedsCredential: preset.Needs,
		})
	}
	return out
}

// Names lists the agents a user can name on the command line.
func Names() []string {
	known := Known()
	names := make([]string, 0, len(known))
	for _, a := range known {
		names = append(names, a.Name)
	}
	return names
}

// Lookup finds a known agent by name.
func Lookup(name string) (Agent, error) {
	for _, a := range Known() {
		if a.Name == name {
			return a, nil
		}
	}
	return Agent{}, fmt.Errorf("unknown agent %q: known names are %s", name, strings.Join(Names(), ", "))
}

// Available reports whether the agent's command can be run: beside this build
// when the binary ships with it, otherwise on PATH.
//
// The bundled lookup is not optional. The catalogue offers an agent that ships
// beside the daemon, and a probe that only asked PATH would accept the name and
// then fail on every install — the one case the name was added for.
func (a Agent) Available() error {
	if _, ok := agent.Bundled(a.Command); ok {
		return nil
	}
	if _, err := exec.LookPath(a.Command); err != nil {
		if a.NeedsCredential == "" {
			return fmt.Errorf("%s is not on PATH: %w", a.Command, err)
		}
		return fmt.Errorf("%s is not on PATH (%s): %w", a.Command, a.NeedsCredential, err)
	}
	return nil
}

// Run opens one session with the agent, sends one prompt, and reports what came
// back. It never runs in the test suite: a real turn costs money.
func Run(ctx context.Context, agent Agent, workspace, prompt string) (Result, error) {
	if prompt == "" {
		prompt = DefaultPrompt
	}

	res := Result{Command: agent.Command}

	started := time.Now()
	session, err := acp.Open(ctx, acp.Launch{
		Command: agent.Command,
		Args:    agent.Args,
		Dir:     workspace,
	}, acp.Config{
		Workspace: workspace,
		Policy:    acp.Auto,
		OnUpdate: func(u acp.Update) {
			switch u.Kind {
			case acp.UpdateMessage:
				res.Messages++
				res.Reply += u.Text
			case acp.UpdateThought:
				res.Thoughts++
			case acp.UpdateTool:
				res.Tools++
			}
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("probe %s: %w", agent.Command, err)
	}
	defer func() { _ = session.Close() }()

	res.SessionID = string(session.ID())

	response, err := session.Prompt(ctx, sdk.TextBlock(prompt))
	if err != nil {
		return Result{}, fmt.Errorf("probe %s: %w", agent.Command, err)
	}

	res.StopReason = string(response.StopReason)
	res.Elapsed = time.Since(started)
	res.Resume = session.SupportsResume()

	return res, nil
}

// Report renders a result as the one line a maintainer wants to paste into a PR.
func (r Result) Report() string {
	return fmt.Sprintf("%s: stop=%s resume=%t messages=%d thoughts=%d tools=%d elapsed=%s reply=%q",
		r.Command, r.StopReason, r.Resume, r.Messages, r.Thoughts, r.Tools, r.Elapsed.Round(time.Millisecond),
		strings.TrimSpace(r.Reply))
}

// ErrNoCredential reports a probe that cannot run because the agent's own login
// is missing. It is the honest failure: this build never holds a vendor token.
var ErrNoCredential = errors.New("probe: the agent is not logged in")

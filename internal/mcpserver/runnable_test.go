package mcpserver

import (
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/agent"
)

// TestRunnableSaysWhyAnAgentCannotStart pins the reason a start is refused: the
// tier the daemon computed is projected, not re-derived from the install flag.
func TestRunnableSaysWhyAnAgentCannotStart(t *testing.T) {
	cases := []struct {
		name  string
		entry agent.Entry
		want  string
	}{
		{"the ACP entry point is missing", agent.Entry{Command: "claude-agent-acp", Tier: agent.TierInstalledNotACP}, "installed, but"},
		{"only the config directory is there", agent.Entry{Command: "claude-agent-acp", Tier: agent.TierSuspected}, "only its configuration"},
		{"nothing at all", agent.Entry{Command: "claude-agent-acp", Tier: agent.TierMissing}, "not installed"},
		{"an older daemon sends no tier", agent.Entry{Command: "claude-agent-acp"}, "not installed"},
		{"disabled", agent.Entry{Installed: true, Enabled: false, Tier: agent.TierAvailable}, "disabled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runnable(c.entry, "claude")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("runnable = %v, want it to say %q", err, c.want)
			}
		})
	}
}

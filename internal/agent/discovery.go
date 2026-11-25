package agent

import (
	"os"
	"path/filepath"
)

// Tier is what this machine shows about an agent: the discovery list's answer
// to whether it is here and whether this build can speak to it.
type Tier string

const (
	// TierAvailable means the command this build starts resolves on this
	// machine, so it can be launched once the switches allow it. Whether the
	// agent then speaks ACP is the probe's question, not this list's.
	TierAvailable Tier = "available"
	// TierInstalledNotACP means the vendor CLI is there but the ACP entry point
	// this build starts is not. The CLI alone does not speak ACP; without the
	// bridge the agent is installed and unreachable.
	TierInstalledNotACP Tier = "installed-but-not-acp"
	// TierSuspected means no command resolved, but the agent's own config
	// directory exists. That proves an install happened, nothing more.
	TierSuspected Tier = "suspected"
	// TierMissing means nothing on this machine matches the agent.
	TierMissing Tier = "missing"
)

// Discovery is how a preset is recognised on a machine beyond its start
// command: the vendor CLI it adapts and the config directory an install leaves
// behind. A handshake probe, not this, is what proves an agent speaks ACP.
// It reads the real user home even under FLT_HOME: an installed agent is a
// fact of the machine, not of one state root.
type Discovery struct {
	// Command is the vendor CLI behind a preset that starts a bridge.
	Command string `json:"command"`
	// Config is the agent's config dir under the user's home, forward slashes.
	Config string `json:"config"`
}

// grade names the tier from this machine's answers, strongest signal first: the
// resolved start command, then the vendor CLI, then the config directory.
func (c *Catalog) grade(installed bool, d Discovery) Tier {
	switch {
	case installed:
		return TierAvailable
	case c.onPath(d.Command):
		return TierInstalledNotACP
	case c.configDirExists(d.Config):
		return TierSuspected
	}
	return TierMissing
}

// onPath reports whether a bare command resolves on PATH. A vendor CLI is the
// user's own install, so it is never looked for beside the executable.
func (c *Catalog) onPath(command string) bool {
	if command == "" {
		return false
	}
	_, err := c.lookPath(command)
	return err == nil
}

// configDirExists reports whether the agent's config directory exists under the
// user's home. The directory proves an install happened; it proves nothing
// about whether the agent can run.
func (c *Catalog) configDirExists(config string) bool {
	if config == "" {
		return false
	}
	home, err := c.home()
	if err != nil || home == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(home, filepath.FromSlash(config)))
	return err == nil && info.IsDir()
}

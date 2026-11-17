// Command flt-replay-agent is the ACP agent that answers from a recorded
// scenario. It is a build of internal/acp/replay, so the preset the catalogue
// ships and the agent a session spawns cannot drift apart.
//
// A scenario path may be given as the first argument; with none it serves the
// builtin one, which is why the preset works with nothing prepared.
package main

import (
	"os"

	"github.com/f1shsmell/flotilla/internal/acp/replay"
)

func main() {
	os.Exit(replay.Main(os.Args[1:]))
}

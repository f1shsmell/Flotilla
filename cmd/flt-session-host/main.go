// Command flt-session-host holds one session's agent handles — the ConPTY or
// the ACP stdio pair — so a daemon restart never costs a live session.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/f1shsmell/flotilla/internal/version"
)

func main() {
	fs := flag.NewFlagSet("flt-session-host", flag.ContinueOnError)
	sessionID := fs.String("session", "", "session id this host holds")
	showVersion := fs.Bool("version", false, "print the build version and exit")

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	if *sessionID == "" {
		fmt.Fprintln(os.Stderr, "flt-session-host: --session is required")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "flt-session-host: no session engine yet for %q\n", *sessionID)
	os.Exit(1)
}

// Command flt is the Flotilla CLI: the same service layer the GUI and daemon
// expose, reachable from a terminal and from agents.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/f1shsmell/flotilla/internal/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "flt:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("flt", flag.ContinueOnError)
	fs.SetOutput(out)
	showVersion := fs.Bool("version", false, "print the build version and exit")
	fs.Usage = func() { _, _ = io.WriteString(out, usage) }

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}

	if *showVersion {
		return print(out, version.String())
	}

	switch fs.Arg(0) {
	case "version":
		return print(out, version.String())
	case "":
		fs.Usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
}

const usage = "usage: flt <command>\n\ncommands:\n  version  print the build version\n"

func print(out io.Writer, line string) error {
	if _, err := io.WriteString(out, line+"\n"); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

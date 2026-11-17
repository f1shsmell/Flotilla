// Command flt-contract renders the Go wire types into the TypeScript module the
// GUI reads, and checks that module for drift.
//
//	flt-contract -out gui/electron/contract.ts          # write it
//	flt-contract -out gui/electron/contract.ts -check   # fail if stale
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/f1shsmell/flotilla/internal/wirecontract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "flt-contract:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("flt-contract", flag.ContinueOnError)
	out := fs.String("out", "", "contract file to write (empty prints to stdout)")
	check := fs.Bool("check", false, "report drift instead of writing")
	root := fs.String("root", ".", "module root")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if *check && *out == "" {
		return fmt.Errorf("-check needs -out: there is nothing to compare")
	}

	if *check {
		_, same, err := wirecontract.Check(*root, *out)
		if err != nil {
			return fmt.Errorf("check the contract: %w", err)
		}
		if same {
			return nil
		}
		return fmt.Errorf("%s is stale: run 'go run ./cmd/flt-contract -out %s'", *out, *out)
	}

	if *out == "" {
		if err := wirecontract.Write(*root, os.Stdout); err != nil {
			return fmt.Errorf("write the contract: %w", err)
		}
		return nil
	}

	contract, err := wirecontract.Build(*root)
	if err != nil {
		return fmt.Errorf("build the contract: %w", err)
	}
	if err := os.WriteFile(*out, wirecontract.Render(contract), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	return nil
}

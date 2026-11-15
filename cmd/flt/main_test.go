package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/version"
)

func TestRunPrintsTheBuildVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out bytes.Buffer

		if err := run(args, &out); err != nil {
			t.Fatalf("run(%q): %v", args, err)
		}
		if got, want := strings.TrimSpace(out.String()), version.String(); got != want {
			t.Fatalf("run(%q) printed %q, want %q", args, got, want)
		}
	}
}

func TestRunRejectsAnUnknownCommand(t *testing.T) {
	var out bytes.Buffer

	err := run([]string{"frobnicate"}, &out)
	if err == nil {
		t.Fatal("run accepted an unknown command")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("error %q does not name the rejected command", err)
	}
}

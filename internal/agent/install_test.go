package agent

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// TestAnAdapterCanSayHowItsAgentIsInstalled pins the adapter file's install
// field: it is the registry's distribution shape, so one recipe format serves
// both, and an adapter is the place a user adds one for an agent this build
// does not ship.
func TestAnAdapterCanSayHowItsAgentIsInstalled(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "x.json",
		`{"id":"x","command":"x","install":{"npx":{"package":"agent@1.0.0","args":["--acp"]}}}`)

	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	want := adapters.Install{Npx: &adapters.Package{Package: "agent@1.0.0", Args: []string{"--acp"}}}
	if got := entryFor(t, c.List(), "x").Install; !reflect.DeepEqual(got, want) {
		t.Fatalf("install = %+v, want %+v", got, want)
	}
}

// TestAnAdapterInstallIsValidated keeps a floating package spec from becoming a
// recipe that fetches whatever the publisher shipped that day.
func TestAnAdapterInstallIsValidated(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "y.json", `{"id":"y","command":"y","install":{"npx":{"package":"agent"}}}`)

	c := New(dir)
	c.selfDir = t.TempDir()
	c.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	problems := strings.Join(c.List().Problems, "\n")
	if !strings.Contains(problems, "exact version") {
		t.Fatalf("problems = %q, want the install refusal", problems)
	}
}

// TestAnAdapterKeepsTheInstallItDoesNotRestate pins the field-by-field merge:
// restating the package runner must not drop a binary the builtin listed.
func TestAnAdapterKeepsTheInstallItDoesNotRestate(t *testing.T) {
	binary := map[string]adapters.Binary{
		"linux-x86_64": {Archive: "https://example.com/a.tar.gz", Cmd: "./a"},
	}
	base := Preset{Install: adapters.Install{
		Npx:    &adapters.Package{Package: "base@1.0.0"},
		Binary: binary,
	}}
	override := Preset{Install: adapters.Install{
		Npx: &adapters.Package{Package: "fork@2.0.0"},
	}}

	got := inherit(base, override).Install
	if got.Npx.Package != "fork@2.0.0" {
		t.Fatalf("npx = %+v, want the adapter's", got.Npx)
	}
	if !reflect.DeepEqual(got.Binary, binary) {
		t.Fatalf("binary = %+v, want the builtin's %+v", got.Binary, binary)
	}
}

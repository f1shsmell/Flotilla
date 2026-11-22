package home

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// parityCase is one row of the shared golden in testdata/home-paths.json, the
// one definition of where state lives per platform and channel. The TypeScript
// side (gui/test/home.test.ts) asserts every row; this side asserts the rows
// whose platform matches the running GOOS, so the two resolvers cannot drift
// apart in silence.
type parityCase struct {
	Platform string `json:"platform"`
	Channel  string `json:"channel"`
	Env      string `json:"env"`
	Base     string `json:"base"`
	Suffix   string `json:"suffix"`
}

func TestGoldenMatchesThisPlatform(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "home-paths.json"))
	if err != nil {
		t.Fatalf("read the shared golden: %v", err)
	}
	var golden struct {
		Cases []parityCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse the shared golden: %v", err)
	}

	asserted := 0
	for _, tc := range golden.Cases {
		// Node names Windows "win32"; Go names it "windows". The golden uses
		// the Node value because the TS side switches on it verbatim.
		goPlatform := tc.Platform
		if goPlatform == "win32" {
			goPlatform = "windows"
		}
		if goPlatform != runtime.GOOS {
			continue
		}
		asserted++
		t.Run(tc.Channel, func(t *testing.T) {
			// The bases this GOOS does not read still get values, so a branch
			// change cannot silently start reading an unrelated variable.
			t.Setenv("LOCALAPPDATA", `C:\parity-base`)
			t.Setenv("XDG_STATE_HOME", "/xdg-parity")
			t.Setenv("FLT_HOME", "")
			if tc.Env == "set" {
				t.Setenv(EnvHome, `C:\tmp\flt-parity`)
			}

			got, err := Dir(tc.Channel)
			if err != nil {
				t.Fatalf("Dir(%q): %v", tc.Channel, err)
			}

			if tc.Env == "set" {
				if got != `C:\tmp\flt-parity` {
					t.Errorf("FLT_HOME override lost: Dir = %q", got)
				}
				return
			}
			if !strings.HasSuffix(filepath.ToSlash(got), "/"+tc.Suffix) {
				t.Errorf("Dir(%q) = %q, golden suffix is %q", tc.Channel, got, tc.Suffix)
			}
			// The base the row names must be the one the result came from: an
			// XDG row reading LOCALAPPDATA (or the reverse) is branch drift.
			switch tc.Base {
			case "LOCALAPPDATA":
				if !strings.HasPrefix(got, `C:\parity-base`) {
					t.Errorf("Dir(%q) ignored LOCALAPPDATA: %q", tc.Channel, got)
				}
			case "XDG_STATE_HOME":
				if !strings.HasPrefix(got, "/xdg-parity") {
					t.Errorf("Dir(%q) ignored XDG_STATE_HOME: %q", tc.Channel, got)
				}
			}
		})
	}
	if asserted == 0 {
		t.Fatalf("the golden carries no row for %s: add one", runtime.GOOS)
	}
}

// TestResolveDerivesTheSubdirectories keeps the Paths derivation (sessions,
// token) honest for the platform it runs on; the golden pins only the root.
func TestResolveDerivesTheSubdirectories(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	paths, err := Resolve("stable")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if paths.Sessions != filepath.Join(paths.Root, "sessions") {
		t.Errorf("Sessions = %q", paths.Sessions)
	}
	if paths.Token != filepath.Join(paths.Root, "token") {
		t.Errorf("Token = %q", paths.Token)
	}
}

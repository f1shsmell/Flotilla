package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/gitops"
	"github.com/f1shsmell/flotilla/internal/gittest"
)

// fakeGh puts a gh on PATH that prints what the real one prints, so the pull
// request path can be checked without a network, a login, or a real repository
// on GitHub. t.Setenv restores PATH when the test ends.
func fakeGh(t *testing.T, output string) {
	t.Helper()

	dir := t.TempDir()
	script := "@echo off\r\necho " + output + "\r\n"

	// Windows resolves a command through PATHEXT, so the shim has to carry the
	// extension it will be found by. Elsewhere a plain executable file works.
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(dir, "gh.cmd"), []byte(script), 0o700); err != nil {
			t.Fatalf("write the gh shim: %v", err)
		}
		t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	} else {
		body := "#!/bin/sh\necho \"" + output + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0o700); err != nil {
			t.Fatalf("write the gh shim: %v", err)
		}
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestPullRequestReportsTheAddressGhPrinted pins the whole call: gh runs, its
// URL becomes the handle, and the summary names it. Without this the pull
// request path has never been executed at all.
func TestPullRequestReportsTheAddressGhPrinted(t *testing.T) {
	fakeGh(t, "https://github.com/owner/name/pull/7")
	repo := gittest.Init(t)

	res, err := gitops.PullRequest(context.Background(), repo, "a title", "a body", "main")
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if res.Ref != "https://github.com/owner/name/pull/7" {
		t.Fatalf("ref = %q, want the URL gh printed", res.Ref)
	}
	if !strings.Contains(res.Summary, res.Ref) {
		t.Fatalf("summary %q does not name the pull request", res.Summary)
	}
}

// TestPullRequestWithoutAnAddressIsHonest pins the fallback: gh output with no
// URL yields a summary and no ref, rather than a link the caller invented.
func TestPullRequestWithoutAnAddressIsHonest(t *testing.T) {
	fakeGh(t, "something went sideways")
	repo := gittest.Init(t)

	res, err := gitops.PullRequest(context.Background(), repo, "a title", "", "")
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if res.Ref != "" {
		t.Fatalf("ref = %q, want none", res.Ref)
	}
	if !strings.Contains(res.Summary, "pull request") {
		t.Fatalf("summary = %q, want it to name what happened", res.Summary)
	}
}

// TestPullRequestRepeatsGhOnFailure pins the error path: a gh that refuses must
// produce an error carrying gh's own words, because that is where the reason is.
func TestPullRequestRepeatsGhOnFailure(t *testing.T) {
	repo := gittest.Init(t)

	// A shim that prints an error must also exit non-zero, or the call looks
	// successful. That is the one thing fakeGh cannot express, which is why this
	// test builds its own.
	dir := t.TempDir()
	body := "@echo off\r\necho error: no commits between main and main 1>&2\r\nexit /b 1\r\n"
	name := "gh.cmd"
	if runtime.GOOS != "windows" {
		body = "#!/bin/sh\necho 'error: no commits between main and main' >&2\nexit 1\n"
		name = "gh"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
		t.Fatalf("write the failing gh shim: %v", err)
	}
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := gitops.PullRequest(context.Background(), repo, "a title", "", "main")
	if err == nil {
		t.Fatal("PullRequest reported success for a gh that failed")
	}
	if !strings.Contains(err.Error(), "no commits between") {
		t.Fatalf("error %q does not carry gh's explanation", err.Error())
	}
}

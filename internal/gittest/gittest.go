// Package gittest builds real repositories for tests that run git for real,
// which is the only way to check code that shells out to it.
package gittest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Init makes a repository with one commit and returns its path. The repository
// ignores .env and asks for it back through .worktreeinclude, so a test can see
// both halves of that contract.
func Init(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	Run(t, dir, "init", "-b", "main")

	Write(t, dir, "readme.md", "hello\n")
	Write(t, dir, ".gitignore", ".env\n")
	Write(t, dir, ".worktreeinclude", ".env\n")

	Run(t, dir, "add", ".")
	Commit(t, dir, "init")

	// Written after the commit, the way a real ignored secret is.
	Write(t, dir, ".env", "SECRET=1\n")
	return dir
}

// Write puts a file in the repository.
func Write(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// Commit stages everything and commits it with an identity of its own, so a
// test never depends on the machine's git configuration.
func Commit(t *testing.T, dir, message string) {
	t.Helper()

	Run(t, dir, "add", "-A")
	Run(t, dir, "commit", "-m", message)
}

// Run executes git in dir and returns its trimmed output.
func Run(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

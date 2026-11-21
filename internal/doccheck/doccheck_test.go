package doccheck

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDocumentedDirectoriesExist is the gate: a document in this repository may
// only name a top-level directory that is really there.
func TestDocumentedDirectoriesExist(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}

	missing, err := Check(root)
	if err != nil {
		t.Fatalf("check the docs: %v", err)
	}

	for _, one := range missing {
		t.Errorf("%s:%d names %s, which is not a directory in this repository", one.File, one.Line, one.Path)
	}
}

// TestCheckNamesTheFileAndLine pins the report itself: a gate that failed with
// "something is wrong" would leave the reader to find it.
func TestCheckNamesTheFileAndLine(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "docs"))
	mkdir(t, filepath.Join(root, "real"))

	write(t, filepath.Join(root, "README.md"), "ok: `real/` and `docs/`\nbroken: `nowhere/`\n")
	write(t, filepath.Join(root, "docs", "guide.md"), "also `vanished/`\n")

	missing, err := Check(root)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	want := []PathRef{
		{File: "README.md", Line: 2, Path: "nowhere/"},
		{File: "docs/guide.md", Line: 1, Path: "vanished/"},
	}
	if len(missing) != len(want) {
		t.Fatalf("Check reported %+v, want %+v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("Check reported %+v, want %+v", missing, want)
		}
	}
}

// TestDeeperPathsAreNotClaims covers the one thing skipped: a deeper path names
// a subtree this check does not resolve, so it is not a claim about the root.
func TestDeeperPathsAreNotClaims(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "README.md"), "`gui/release/` `gui/src/`\n")

	missing, err := Check(root)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("Check reported %+v, want the deeper paths skipped", missing)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("make %s: %v", filepath.ToSlash(path), err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.ToSlash(path), err)
	}
}

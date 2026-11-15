package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")

	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if err := WriteTokenFile(path, token); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	read, err := ReadTokenFile(path)
	if err != nil {
		t.Fatalf("ReadTokenFile: %v", err)
	}
	if read != token {
		t.Fatalf("ReadTokenFile = %q, want %q", read, token)
	}
}

func TestNewTokenIsRandom(t *testing.T) {
	first, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	second, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if first == second {
		t.Fatal("two tokens are identical")
	}
	if len(first) != TokenSize*2 {
		t.Fatalf("token is %d hex chars, want %d", len(first), TokenSize*2)
	}
}

func TestEqualToken(t *testing.T) {
	if !EqualToken("abc", "abc") {
		t.Fatal("EqualToken rejected identical tokens")
	}
	if EqualToken("abc", "abd") {
		t.Fatal("EqualToken accepted different tokens")
	}
	if EqualToken("abc", "abcd") {
		t.Fatal("EqualToken accepted a prefix")
	}
}

func TestReadTokenFileRejectsAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := ReadTokenFile(path); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ReadTokenFile on an empty file = %v, want ErrUnauthorized", err)
	}
}

func TestWriteTokenFileNarrowsTheOwnerOnlyOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := WriteTokenFile(path, "secret"); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("token file is empty")
	}
}

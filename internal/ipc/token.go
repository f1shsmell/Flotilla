package ipc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenSize is the number of random bytes behind a token; the stored form is
// its hex encoding.
const TokenSize = 32

// NewToken returns a fresh token in its stored (hex) form.
func NewToken() (string, error) {
	b := make([]byte, TokenSize)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// EqualToken compares two tokens without leaking their common prefix length.
func EqualToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// WriteTokenFile writes token to path, readable only by its owner.
func WriteTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open token file %q: %w", path, err)
	}

	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write token file %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close token file %q: %w", path, err)
	}

	// The file is created before the DACL is narrowed, so restrict it after:
	// a crash between the two leaves a token nobody else has read.
	return restrictToOwner(path)
}

// ReadTokenFile returns the token stored at path.
func ReadTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty: %w", path, ErrUnauthorized)
	}
	return token, nil
}

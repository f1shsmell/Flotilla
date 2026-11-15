// Package home resolves every on-disk location the binaries use.
//
// Setting FLT_HOME redirects all of them under one directory and skips every
// fallback into the real home, so a source build never shares state with an
// installed one.
package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// EnvHome names the environment variable that overrides the state root.
const EnvHome = "FLT_HOME"

// Paths are the directories and files a running Flotilla owns on disk.
type Paths struct {
	// Root holds everything below it and is what FLT_HOME overrides.
	Root string
	// Config, Logs, Sessions and Cache are created on demand by their owner.
	Config   string
	Logs     string
	Sessions string
	Cache    string
	// Token is the file holding the IPC connection token, ACL-restricted to
	// the current user.
	Token string
}

// Resolve returns the state paths for this channel, creating the root.
func Resolve(channel string) (Paths, error) {
	root, err := Dir(channel)
	if err != nil {
		return Paths{}, err
	}

	p := Paths{
		Root:     root,
		Config:   filepath.Join(root, "config"),
		Logs:     filepath.Join(root, "logs"),
		Sessions: filepath.Join(root, "sessions"),
		Cache:    filepath.Join(root, "cache"),
		Token:    filepath.Join(root, "token"),
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return Paths{}, fmt.Errorf("create state root %q: %w", root, err)
	}

	return p, nil
}

// Dir returns the state root: FLT_HOME when set, otherwise the platform default
// for the channel.
func Dir(channel string) (string, error) {
	if v := os.Getenv(EnvHome); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("resolve %s=%q: %w", EnvHome, v, err)
		}
		return abs, nil
	}
	return defaultDir(channel)
}

func defaultDir(channel string) (string, error) {
	name := "Flotilla"
	if channel == "preview" {
		name = "FlotillaPreview"
	}

	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA is unset and FLT_HOME is not set")
		}
		return filepath.Join(base, name), nil
	case "darwin":
		base, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(base, "Library", "Application Support", name), nil
	default:
		if base := os.Getenv("XDG_STATE_HOME"); base != "" {
			return filepath.Join(base, name), nil
		}
		base, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(base, ".local", "state", name), nil
	}
}

package adapters

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Install says how an agent is fetched onto a machine: a package runner pinned
// to an exact version, or a binary archive per platform. It is the adapter
// file's "install" field, and the official ACP Registry's "distribution" uses
// the same shape, so registry data maps in unchanged rather than growing a
// second catalog format.
type Install struct {
	Npx    *Package          `json:"npx,omitempty"`
	Uvx    *Package          `json:"uvx,omitempty"`
	Binary map[string]Binary `json:"binary,omitempty"`
}

// Package is one package-runner recipe: the package spec, the arguments that
// put it in ACP mode, and any environment it needs.
type Package struct {
	Package string            `json:"package"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Binary is one platform's archive: where to download it, the command inside
// the extracted directory, any environment it needs, and the hash the download
// must match when the publisher provides one.
type Binary struct {
	Archive string            `json:"archive"`
	Cmd     string            `json:"cmd"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	SHA256  string            `json:"sha256,omitempty"`
}

// exactVersion accepts the version tail of a package spec: three numbered parts
// with an optional semver suffix. The suffix charset is closed so npm range
// syntax such as `1.3.0-0 || 2.0.0` cannot pose as a pin.
var exactVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Empty reports whether the definition declares no way to install the agent.
func (i Install) Empty() bool {
	return i.Npx == nil && i.Uvx == nil && len(i.Binary) == 0
}

// Validate refuses a recipe this build could not describe honestly. An unpinned
// package spec is refused on purpose: it would fetch whatever the publisher
// shipped that day, which is exactly what the pin exists to prevent.
func (i Install) Validate() error {
	if i.Npx != nil {
		if err := i.Npx.validate("npx", "@"); err != nil {
			return err
		}
	}
	if i.Uvx != nil {
		if err := i.Uvx.validate("uvx", "==", "@"); err != nil {
			return err
		}
	}
	for platform, binary := range i.Binary {
		if err := binary.validate(platform); err != nil {
			return err
		}
	}
	return nil
}

func (p *Package) validate(runner string, separators ...string) error {
	spec := strings.TrimSpace(p.Package)
	if spec == "" {
		return fmt.Errorf("%s package is empty: %w", runner, ErrInvalid)
	}
	at, width := lastSeparator(spec, separators)
	if at <= 0 || !exactVersion.MatchString(spec[at+width:]) {
		return fmt.Errorf("%s package %q is not pinned to an exact version: %w", runner, spec, ErrInvalid)
	}
	if err := checkPackageName(spec[:at]); err != nil {
		return fmt.Errorf("%s package %q: %w: %w", runner, spec, err, ErrInvalid)
	}
	return nil
}

// lastSeparator finds the rightmost separator: that is where the version tail
// starts, since a scoped npm name carries an '@' of its own.
func lastSeparator(spec string, separators []string) (int, int) {
	at, width := -1, 0
	for _, separator := range separators {
		if i := strings.LastIndex(spec, separator); i > at {
			at, width = i, len(separator)
		}
	}
	return at, width
}

// checkPackageName refuses everything before the pin that is not a package
// name: a leading '-' is an option to the runner, and ':' or a path is a URL or
// a local file dressed as a recipe.
func checkPackageName(name string) error {
	switch {
	case strings.HasPrefix(name, "-"):
		return errors.New("a package name cannot start with '-'")
	case strings.ContainsAny(name, `:\`), strings.IndexFunc(name, unicode.IsSpace) >= 0:
		return errors.New("a package name cannot contain ':', '\\' or whitespace")
	case strings.HasPrefix(name, "."), strings.HasPrefix(name, "/"):
		return errors.New("a package name cannot start with '.' or '/'")
	case strings.Contains(name, "/") && (!strings.HasPrefix(name, "@") || strings.Count(name, "/") != 1):
		return errors.New("only a scoped package name may contain '/'")
	}
	return nil
}

func (b Binary) validate(platform string) error {
	if strings.TrimSpace(platform) == "" {
		return fmt.Errorf("a binary install names an empty platform: %w", ErrInvalid)
	}
	archive, err := url.Parse(strings.TrimSpace(b.Archive))
	if err != nil || archive.Scheme != "https" || archive.Host == "" {
		return fmt.Errorf("binary install for %s needs an https archive, got %q: %w",
			platform, b.Archive, ErrInvalid)
	}
	if strings.TrimSpace(b.Cmd) == "" {
		return fmt.Errorf("binary install for %s names no command: %w", platform, ErrInvalid)
	}
	if b.SHA256 != "" && !sha256Hex.MatchString(b.SHA256) {
		return fmt.Errorf("binary install for %s has a sha256 that is not 64 hex characters: %w",
			platform, ErrInvalid)
	}
	return nil
}

// Normalize trims the values a recipe stores, so the recipe kept is the one
// Validate read.
func (i *Install) Normalize() {
	if i.Npx != nil {
		i.Npx.Package = strings.TrimSpace(i.Npx.Package)
	}
	if i.Uvx != nil {
		i.Uvx.Package = strings.TrimSpace(i.Uvx.Package)
	}
	for platform, binary := range i.Binary {
		binary.Archive = strings.TrimSpace(binary.Archive)
		binary.Cmd = strings.TrimSpace(binary.Cmd)
		binary.SHA256 = strings.TrimSpace(binary.SHA256)
		i.Binary[platform] = binary
	}
}

// Runner names the package registry a pin belongs to.
type Runner string

const (
	// RunnerNPM is the npm registry, which npx recipes fetch from.
	RunnerNPM Runner = "npm"
	// RunnerPyPI is the Python package index, which uvx recipes fetch from.
	RunnerPyPI Runner = "pypi"
)

// Pin is one package a recipe pins to an exact version: the runner that would
// fetch it, and the name and version without the separator syntax.
type Pin struct {
	Runner  Runner
	Package string
	Version string
}

// Pins lists what a recipe pins, for a caller that asks each registry what it
// publishes. A recipe this build would refuse pins nothing: its specs are not
// trusted enough to turn into requests.
func (i Install) Pins() []Pin {
	if i.Validate() != nil {
		return nil
	}

	pins := make([]Pin, 0, 2)
	for _, entry := range []struct {
		runner     Runner
		pkg        *Package
		separators []string
	}{
		{RunnerNPM, i.Npx, []string{"@"}},
		{RunnerPyPI, i.Uvx, []string{"==", "@"}},
	} {
		if entry.pkg == nil {
			continue
		}
		spec := strings.TrimSpace(entry.pkg.Package)
		at, width := lastSeparator(spec, entry.separators)
		pins = append(pins, Pin{
			Runner:  entry.runner,
			Package: spec[:at],
			Version: spec[at+width:],
		})
	}
	return pins
}

// Summary renders the recipe as the one line a terminal shows: which runner a
// package goes through, or how many platforms have a binary.
func (i Install) Summary() string {
	parts := make([]string, 0, 3)
	if i.Npx != nil {
		parts = append(parts, "npx "+i.Npx.Package)
	}
	if i.Uvx != nil {
		parts = append(parts, "uvx "+i.Uvx.Package)
	}
	if len(i.Binary) > 0 {
		word := "platforms"
		if len(i.Binary) == 1 {
			word = "platform"
		}
		parts = append(parts, fmt.Sprintf("binary (%d %s)", len(i.Binary), word))
	}
	return strings.Join(parts, " + ")
}

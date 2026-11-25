package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// archiveLimit bounds one download: a platform archive is tens of megabytes,
// and a reader that keeps reading a hostile or broken stream would fill the
// disk before the hash ever gets a chance to refuse it.
const archiveLimit = 512 << 20

var (
	// ErrNoArchive reports a recipe that names no archive, so nothing is fetched.
	ErrNoArchive = errors.New("agent: the recipe has no binary archive")
	// ErrUnverified reports an archive the publisher gave no hash for. Its bytes
	// cannot be checked, so they are not fetched without the user's consent.
	ErrUnverified = errors.New("agent: the publisher provides no sha256")
	// ErrHash reports a download that did not match the hash the recipe carries.
	ErrHash = errors.New("agent: the archive does not match its sha256")
	// ErrArchive reports bytes this build will not treat as an archive: an
	// unknown format, a broken stream, or a path that would escape its target.
	ErrArchive = errors.New("agent: the archive cannot be unpacked")
	// ErrNotNew reports a target directory that already holds something. An
	// install never unpacks over files it did not download itself.
	ErrNotNew = errors.New("agent: the install directory is not empty")
	// ErrInsecure reports a download that left https. A release asset that
	// redirects to plain http is refused, not followed.
	ErrInsecure = errors.New("agent: the download left https")
)

// Options is what a user said to one plan.
type Options struct {
	// AllowUnverified lets an archive with no sha256 through. The caller has
	// asked the user and been told the publisher provides no hash; nothing here
	// can prove the bytes, and the install reports them as unverified.
	AllowUnverified bool
}

// BinariesFor picks the archive for one platform, in the registry's own naming:
// windows-x86_64, darwin-aarch64, linux-x86_64-musl and so on.
func BinariesFor(binaries map[string]adapters.Binary, goos, goarch string) (adapters.Binary, bool) {
	binary, ok := binaries[Platform(goos, goarch)]
	return binary, ok
}

// Platform is the registry key of one machine. The registry spells amd64 as
// x86_64 and arm64 as aarch64; the libc suffix is not detectable, so a musl-only
// entry is not matched.
func Platform(goos, goarch string) string {
	arch := goarch
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return goos + "-" + arch
}

// InstallArchive fetches one platform's archive into dst, verifies it against
// the hash the recipe carries, and unpacks it. dst must be a new, empty
// directory: a stale file from a previous attempt is exactly what an archive is
// not allowed to stand behind. The returned path is the absolute command the
// recipe names, inside dst.
func InstallArchive(ctx context.Context, client *http.Client, recipe adapters.Binary, dst string, opts Options) (string, error) {
	if strings.TrimSpace(recipe.Archive) == "" {
		return "", fmt.Errorf("%w", ErrNoArchive)
	}
	if strings.TrimSpace(recipe.SHA256) == "" && !opts.AllowUnverified {
		return "", fmt.Errorf("%w, and an unhashed download is not run", ErrUnverified)
	}
	if err := prepare(dst); err != nil {
		return "", err
	}

	payload, err := fetch(ctx, client, strings.TrimSpace(recipe.Archive))
	if err != nil {
		return "", err
	}
	if sum := recipe.SHA256; strings.TrimSpace(sum) != "" {
		digest := sha256.Sum256(payload)
		got := hex.EncodeToString(digest[:])
		if !strings.EqualFold(got, strings.TrimSpace(sum)) {
			return "", fmt.Errorf("%w: got %s, the recipe names %s", ErrHash, got, strings.ToLower(sum))
		}
	}

	command, err := inside(dst, recipe.Cmd)
	if err != nil {
		return "", err
	}
	unpacked, err := unpack(payload, recipe.Archive, dst)
	if err != nil {
		return "", err
	}
	if !unpacked {
		// The publisher ships a bare executable, as muse-code and sigit do: the
		// recipe's cmd names the file the bytes become. A document served with a
		// 200 is a proxy's answer, not a program.
		if looksLikeDocument(payload) {
			return "", fmt.Errorf("%w: %s answered with a document, not an archive or a program",
				ErrArchive, path.Base(strings.TrimSpace(recipe.Archive)))
		}
		if err := writeFile(command, bytes.NewReader(payload), 0o700); err != nil {
			return "", err
		}
		return command, nil
	}
	info, err := os.Stat(command)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("%w: the archive has no command at %q", ErrArchive, recipe.Cmd)
	}
	return command, nil
}

// prepare makes dst, or refuses one that already holds files. An empty
// directory is what the caller's staging area gives; anything else is a target
// an archive could overwrite.
func prepare(dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("read %s: %w", dst, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrNotNew, dst)
	}
	return nil
}

// fetch downloads one archive into memory. The answer is bounded rather than
// streamed to disk: the hash has to be checked before any bytes are unpacked.
func fetch(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request for %s: %w", address, err)
	}
	req.Header.Set("User-Agent", "flt-install")

	resp, err := guarded(client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", address, resp.Status)
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, archiveLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", address, err)
	}
	if len(payload) > archiveLimit {
		return nil, fmt.Errorf("%s is larger than %d bytes", address, archiveLimit)
	}
	return payload, nil
}

// guarded returns a client that refuses a redirect off https. The recipe's
// archive is validated as https when the adapter is read; a redirect is the
// one hop validation cannot see, and a release asset that bounces to plain http
// would hand back bytes an on-path attacker chose.
func guarded(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copied := *client
	previous := client.CheckRedirect
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("%w: redirected to %s", ErrInsecure, req.URL.Redacted())
		}
		if previous != nil {
			return previous(req, via)
		}
		// A nil policy is the client's own ten-hop cap; replacing it means
		// restating the cap rather than losing it.
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &copied
}

// unpack extracts one archive into dst and reports whether the bytes were an
// archive at all. The format is read from the bytes, not the name: registry
// links are release assets whose content type is not reliable, and a download
// that is not the archive its name promises must fail rather than be written
// out as a program.
func unpack(payload []byte, address, dst string) (bool, error) {
	switch {
	case bytes.HasPrefix(payload, []byte("PK\x03\x04")), bytes.HasPrefix(payload, []byte("PK\x05\x06")):
		return true, unpackZip(payload, dst)
	case bytes.HasPrefix(payload, []byte("\x1f\x8b")):
		return true, unpackTar(payload, dst, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) })
	case bytes.HasPrefix(payload, []byte("BZh")):
		return true, unpackTar(payload, dst, func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil })
	}
	if namedArchive(address) {
		return false, fmt.Errorf("%w: %s is not the archive its name promises", ErrArchive, path.Base(address))
	}
	return false, nil
}

// looksLikeDocument reports a payload that opens an HTML or XML document: a
// download that is a page cannot be run, whatever the url called it.
func looksLikeDocument(payload []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(payload))
	for _, prefix := range [][]byte{[]byte("<!doctype"), []byte("<html"), []byte("<?xml")} {
		if bytes.HasPrefix(head, prefix) {
			return true
		}
	}
	return false
}

// namedArchive reports whether the url's path claims to be an archive.
func namedArchive(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	name := strings.ToLower(parsed.Path)
	for _, suffix := range []string{".zip", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func unpackZip(payload []byte, dst string) error {
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}
	for _, entry := range reader.File {
		target, err := inside(dst, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("%w: the zip has a %s entry at %q", ErrArchive, entry.Mode().Type(), entry.Name)
		}
		source, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrArchive, err)
		}
		err = writeFile(target, source, entry.Mode().Perm())
		_ = source.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func unpackTar(payload []byte, dst string, decompress func(io.Reader) (io.Reader, error)) error {
	stream, err := decompress(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}
	reader := tar.NewReader(stream)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrArchive, err)
		}
		target, err := inside(dst, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := writeFile(target, reader, os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: the tar has a type %d entry at %q", ErrArchive, header.Typeflag, header.Name)
		}
	}
}

// inside resolves one archive entry's name under dst, refusing anything that
// would land outside it: an absolute name, a drive letter, a `..` segment, an
// alternate data stream, or a name Windows would rewrite onto its neighbour.
func inside(dst, name string) (string, error) {
	cleaned := path.Clean(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	if strings.HasPrefix(cleaned, "/") || (len(cleaned) >= 2 && cleaned[1] == ':') {
		return "", fmt.Errorf("%w: %q is an absolute path", ErrArchive, name)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q escapes the archive", ErrArchive, name)
	}
	for _, part := range strings.Split(cleaned, "/") {
		if strings.Contains(part, ":") {
			return "", fmt.Errorf("%w: %q names an alternate data stream", ErrArchive, name)
		}
		// Win32 drops trailing dots and spaces, so ".. " would still leave.
		if strings.TrimRight(part, ". ") != part {
			return "", fmt.Errorf("%w: %q has a segment Windows would rewrite", ErrArchive, name)
		}
	}
	target := filepath.Join(dst, filepath.FromSlash(cleaned))
	if !isWithin(dst, target) {
		return "", fmt.Errorf("%w: %q escapes the archive", ErrArchive, name)
	}
	return target, nil
}

// isWithin reports whether target is dst itself or below it.
func isWithin(dst, target string) bool {
	rel, err := filepath.Rel(dst, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// writeFile writes one file, creating the directories it needs. The mode keeps
// the owner's bits and drops everyone else's: an extracted agent runs as this
// user, never as the group.
func writeFile(target string, source io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	perm := mode & 0o700
	if perm == 0 {
		perm = 0o600
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s appears more than once", ErrArchive, filepath.Base(target))
		}
		return fmt.Errorf("create %s: %w", target, err)
	}
	if _, err := io.Copy(file, source); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

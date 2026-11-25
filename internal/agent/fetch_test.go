package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// zipBytes builds one zip archive.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip entry %q: %v", name, err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("zip body %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("finish the zip: %v", err)
	}
	return buf.Bytes()
}

// zipProgram builds one zip archive whose single entry carries the executable
// bit, as a release asset for a program does.
func zipProgram(t *testing.T, name, body string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("zip entry %q: %v", name, err)
	}
	if _, err := entry.Write([]byte(body)); err != nil {
		t.Fatalf("zip body %q: %v", name, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("finish the zip: %v", err)
	}
	return buf.Bytes()
}

// tarGzBytes builds one tar.gz archive whose files are executable.
func tarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("tar header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("finish the tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("finish the gzip: %v", err)
	}
	return buf.Bytes()
}

func sum(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// serveArchive answers one payload, or one status, on every path.
func serveArchive(t *testing.T, status int, payload []byte) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestAnArchiveLandsVerified is the happy path: the bytes match the hash the
// recipe carries, and the command the recipe names is unpacked inside the
// directory the caller chose.
func TestAnArchiveLandsVerified(t *testing.T) {
	payload := zipBytes(t, map[string]string{"bin/agent.exe": "binary"})
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/acme-1.0.0.zip", Cmd: "./bin/agent.exe", SHA256: sum(payload)}
	dst := filepath.Join(t.TempDir(), "agent")

	command, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if want := filepath.Join(dst, "bin", "agent.exe"); command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
	body, err := os.ReadFile(command)
	if err != nil || string(body) != "binary" {
		t.Fatalf("the command holds %q (%v)", body, err)
	}
}

// TestABadHashLeavesNothingUnpacked: a download that does not match the recipe
// must not extract a single file, let alone run one.
func TestABadHashLeavesNothingUnpacked(t *testing.T) {
	payload := zipBytes(t, map[string]string{"agent.exe": "evil"})
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: "./agent.exe", SHA256: strings.Repeat("0", 64)}
	dst := filepath.Join(t.TempDir(), "agent")

	_, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
	if !errors.Is(err, ErrHash) {
		t.Fatalf("install with a bad hash = %v, want ErrHash", err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("a refused archive left %v behind", entries)
	}
}

// TestAnUnhashedArchiveIsNotFetched is the policy without consent: only bytes
// the publisher named a hash for are trusted enough to unpack and run.
func TestAnUnhashedArchiveIsNotFetched(t *testing.T) {
	fetched := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fetched = true
	}))
	t.Cleanup(server.Close)

	recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: "./agent.exe"}
	_, err := InstallArchive(context.Background(), server.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
	if !errors.Is(err, ErrUnverified) {
		t.Fatalf("install without a hash = %v, want ErrUnverified", err)
	}
	if fetched {
		t.Fatal("an unhashed archive was fetched anyway")
	}
}

// TestTheUsersConsentLetsAnUnhashedArchiveThrough: a publisher that provides no
// hash can still be installed once the user has been told so.
func TestTheUsersConsentLetsAnUnhashedArchiveThrough(t *testing.T) {
	payload := zipBytes(t, map[string]string{"agent.exe": "binary"})
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: "./agent.exe"}
	dst := filepath.Join(t.TempDir(), "agent")

	if _, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{AllowUnverified: true}); err != nil {
		t.Fatalf("install with consent: %v", err)
	}
}

// TestAnArchiveCannotEscapeItsDirectory covers the extraction boundary: every
// entry that would land outside the download directory refuses the whole
// archive, and leaves nothing behind.
func TestAnArchiveCannotEscapeItsDirectory(t *testing.T) {
	for _, name := range []string{
		"../escape.exe",
		"bin/../../escape.exe",
		"C:/escape.exe",
		"bin/.. /escape.exe",
		`..\escape.exe`,
		"x.exe:stream",
	} {
		t.Run(name, func(t *testing.T) {
			payload := zipBytes(t, map[string]string{name: "escape"})
			server := serveArchive(t, http.StatusOK, payload)
			recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: "./escape.exe", SHA256: sum(payload)}
			dst := filepath.Join(t.TempDir(), "agent")

			_, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
			if !errors.Is(err, ErrArchive) {
				t.Fatalf("install of an escaping archive = %v, want ErrArchive", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "escape.exe")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the escaping entry was written")
			}
		})
	}
}

// TestATarGzLandsVerified covers the second archive format the registry uses.
func TestATarGzLandsVerified(t *testing.T) {
	payload := tarGzBytes(t, map[string]string{"goose-package/goose.exe": "binary"})
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/goose.tar.gz", Cmd: "./goose-package/goose.exe", SHA256: sum(payload)}
	dst := filepath.Join(t.TempDir(), "agent")

	command, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if filepath.Base(command) != "goose.exe" {
		t.Fatalf("command = %q, want the tar's entry", command)
	}
}

// TestATarBz2LandsVerified covers goose's format. The standard library reads
// bzip2 but does not write it, so the archive is a real stream kept as hex.
func TestATarBz2LandsVerified(t *testing.T) {
	payload, err := hex.DecodeString("425a6839314159265359bff4f3ca00006efb80c980000440004b8000" +
		"0872a19e20080820005446a351a34d340c698a092281919064d01f6f01d083dbd08467d4a04f48e48" +
		"10c0c4dc4ec23670ee06373e8f40a8ed645566283ece9500117e2ee48a70a1217fe9e7940")
	if err != nil {
		t.Fatalf("decode the fixture: %v", err)
	}
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/goose.tar.bz2", Cmd: "./goose", SHA256: sum(payload)}
	dst := filepath.Join(t.TempDir(), "agent")

	if _, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{}); err != nil {
		t.Fatalf("install: %v", err)
	}
}

// TestABareExecutableBecomesTheCommand covers muse-code and sigit, whose
// archives are none at all: the recipe's cmd names the file the bytes become.
func TestABareExecutableBecomesTheCommand(t *testing.T) {
	payload := []byte("MZ binary")
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/muse.exe", Cmd: "./muse.exe", SHA256: sum(payload)}
	dst := filepath.Join(t.TempDir(), "agent")

	command, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if body, err := os.ReadFile(command); err != nil || string(body) != "MZ binary" {
		t.Fatalf("the command holds %q (%v)", body, err)
	}
}

// TestABareExecutableMustBeNamedAsOne keeps a non-archive download from being
// written out under a name that claims an archive.
func TestABareExecutableMustBeNamedAsOne(t *testing.T) {
	server := serveArchive(t, http.StatusOK, []byte("not a zip"))
	recipe := adapters.Binary{Archive: server.URL + "/agent.zip", Cmd: "./agent.exe", SHA256: sum([]byte("not a zip"))}

	_, err := InstallArchive(context.Background(), server.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
	if !errors.Is(err, ErrArchive) {
		t.Fatalf("install of a mislabelled archive = %v, want ErrArchive", err)
	}
}

// TestADocumentIsNotAProgram covers a proxy or cdn that answers a release asset
// with a page and a 200: a document is written out as nothing.
func TestADocumentIsNotAProgram(t *testing.T) {
	for _, body := range []string{"<!DOCTYPE html><h1>Blocked</h1>", "  <html><body>Sign in</body></html>", `<?xml version="1.0"?><Error/>`} {
		server := serveArchive(t, http.StatusOK, []byte(body))
		recipe := adapters.Binary{Archive: server.URL + "/download?file=agent.exe", Cmd: "./agent.exe", SHA256: sum([]byte(body))}
		dst := filepath.Join(t.TempDir(), "agent")

		_, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
		if !errors.Is(err, ErrArchive) {
			t.Fatalf("install of %q = %v, want ErrArchive", body, err)
		}
		if entries, _ := os.ReadDir(dst); len(entries) != 0 {
			t.Fatalf("a document left %v", entries)
		}
	}
}

// TestAnEntryNamedTwiceIsAnArchiveError keeps the duplicate guard in the
// extraction's own error class: a caller classifying failures sees it as the
// malformed archive it is.
func TestAnEntryNamedTwiceIsAnArchiveError(t *testing.T) {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for range 2 {
		entry, err := writer.Create("bin.exe")
		if err != nil {
			t.Fatalf("zip entry: %v", err)
		}
		if _, err := entry.Write([]byte("binary")); err != nil {
			t.Fatalf("zip body: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("finish the zip: %v", err)
	}

	server := serveArchive(t, http.StatusOK, buf.Bytes())
	recipe := adapters.Binary{Archive: server.URL + "/bin.zip", Cmd: "./bin.exe", SHA256: sum(buf.Bytes())}
	_, err := InstallArchive(context.Background(), server.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
	if !errors.Is(err, ErrArchive) {
		t.Fatalf("install of a zip with a repeated entry = %v, want ErrArchive", err)
	}
}

// TestTheCommandMustBeInTheArchive prevents a recipe from naming a file the
// archive did not bring: a command that is not there is a broken install.
func TestTheCommandMustBeInTheArchive(t *testing.T) {
	payload := tarGzBytes(t, map[string]string{"agent": "binary"})
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/x.tar.gz", Cmd: "./no-such", SHA256: sum(payload)}

	_, err := InstallArchive(context.Background(), server.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
	if !errors.Is(err, ErrArchive) {
		t.Fatalf("install with a missing command = %v, want ErrArchive", err)
	}
}

// TestACommandOutsideTheArchiveIsRefused keeps a recipe from naming a system
// binary to run after the download: the command has to be the archive's own.
func TestACommandOutsideTheArchiveIsRefused(t *testing.T) {
	payload := zipBytes(t, map[string]string{"agent.exe": "binary"})
	server := serveArchive(t, http.StatusOK, payload)

	for _, cmd := range []string{"../escape.exe", `C:\Windows\System32\cmd.exe`} {
		recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: cmd, SHA256: sum(payload)}
		_, err := InstallArchive(context.Background(), server.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
		if !errors.Is(err, ErrArchive) {
			t.Fatalf("install with cmd %q = %v, want ErrArchive", cmd, err)
		}
	}
}

// TestAFailedFetchIsReported keeps a non-200 away from the extractor.
func TestAFailedFetchIsReported(t *testing.T) {
	server := serveArchive(t, http.StatusNotFound, nil)
	recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: "./x.exe", SHA256: strings.Repeat("a", 64)}

	_, err := InstallArchive(context.Background(), server.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("install of a missing archive = %v", err)
	}
}

// TestARedirectOffHttpsIsRefused covers the hop the recipe's validation cannot
// see: an https link that bounces to plain http must not hand back bytes.
func TestARedirectOffHttpsIsRefused(t *testing.T) {
	payload := zipBytes(t, map[string]string{"agent.exe": "binary"})
	plain := serveArchive(t, http.StatusOK, payload)

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/x.zip", http.StatusFound)
	}))
	t.Cleanup(secure.Close)

	recipe := adapters.Binary{Archive: secure.URL + "/x.zip", Cmd: "./agent.exe", SHA256: sum(payload)}
	_, err := InstallArchive(context.Background(), secure.Client(), recipe, filepath.Join(t.TempDir(), "agent"), Options{})
	if !errors.Is(err, ErrInsecure) {
		t.Fatalf("install through a redirect off https = %v, want ErrInsecure", err)
	}
}

// TestATargetWithFilesIsRefused keeps an install from unpacking over anything:
// a leftover file is not the publisher's, and the archive cannot stand behind it.
func TestATargetWithFilesIsRefused(t *testing.T) {
	payload := zipBytes(t, map[string]string{"agent.exe": "binary"})
	server := serveArchive(t, http.StatusOK, payload)
	recipe := adapters.Binary{Archive: server.URL + "/x.zip", Cmd: "./agent.exe", SHA256: sum(payload)}
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatalf("plant the leftover: %v", err)
	}

	_, err := InstallArchive(context.Background(), server.Client(), recipe, dst, Options{})
	if !errors.Is(err, ErrNotNew) {
		t.Fatalf("install into a used directory = %v, want ErrNotNew", err)
	}
}

// TestThePlatformKeyIsTheRegistrysOwn pins the spelling recipes are keyed by.
func TestThePlatformKeyIsTheRegistrysOwn(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"windows", "amd64", "windows-x86_64"},
		{"windows", "arm64", "windows-aarch64"},
		{"darwin", "arm64", "darwin-aarch64"},
		{"linux", "amd64", "linux-x86_64"},
	} {
		if got := Platform(c.goos, c.goarch); got != c.want {
			t.Fatalf("Platform(%s, %s) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
		binary, ok := BinariesFor(map[string]adapters.Binary{c.want: {Archive: "https://example.com/x.zip"}}, c.goos, c.goarch)
		if !ok || binary.Archive == "" {
			t.Fatalf("no archive found for %s/%s", c.goos, c.goarch)
		}
	}
}

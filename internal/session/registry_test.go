package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/f1shsmell/flotilla/internal/ipc"
)

func recordFor(id ID) Record {
	return Record{
		ID:       id,
		Endpoint: ipc.Address{Channel: string(id), Name: "host"},
		PID:      4242,
	}
}

func TestRegistryPublishAndRead(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	if err := reg.Publish(recordFor("sess-a")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rec, err := reg.Read("sess-a")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.ID != "sess-a" || rec.Endpoint.Name != "host" || rec.PID != 4242 {
		t.Fatalf("Read returned %+v, want the published record", rec)
	}
}

func TestRegistryPublishReplacesThePreviousRecord(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	first := recordFor("sess-a")
	if err := reg.Publish(first); err != nil {
		t.Fatalf("Publish first: %v", err)
	}

	second := recordFor("sess-a")
	second.PID = 99
	if err := reg.Publish(second); err != nil {
		t.Fatalf("Publish second: %v", err)
	}

	records, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("List returned %d records, want 1: publishing must replace, not append", len(records))
	}
	if records[0].PID != 99 {
		t.Fatalf("record PID = %d, want the newer 99", records[0].PID)
	}
}

func TestRegistryUnpublish(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	if err := reg.Publish(recordFor("sess-a")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := reg.Unpublish("sess-a"); err != nil {
		t.Fatalf("Unpublish: %v", err)
	}
	if _, err := reg.Read("sess-a"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("Read after Unpublish = %v, want ErrNoRecord", err)
	}

	// Shutdown must not fail on bookkeeping that was never done.
	if err := reg.Unpublish("never-published"); err != nil {
		t.Fatalf("Unpublish of an unknown session: %v", err)
	}
}

func TestRegistryListSkipsAMalformedRecord(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	if err := reg.Publish(recordFor("sess-a")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write a malformed record: %v", err)
	}

	records, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 || records[0].ID != "sess-a" {
		t.Fatalf("List returned %+v, want only the readable record", records)
	}
}

func TestRegistryListOnAMissingDirectoryIsEmpty(t *testing.T) {
	records, err := NewRegistry(filepath.Join(t.TempDir(), "absent")).List()
	if err != nil {
		t.Fatalf("List on a missing directory: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("List returned %d records, want none", len(records))
	}
}

func TestRegistryReadOfAnUnknownSessionIsErrNoRecord(t *testing.T) {
	if _, err := NewRegistry(t.TempDir()).Read("absent"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("Read of an unknown session = %v, want ErrNoRecord", err)
	}
}

func TestRegistryKeepsAHostileIDInsideItsDirectory(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	if err := reg.Publish(recordFor(ID("../escape"))); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the record produced %d files, want exactly one inside the directory", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape.json")); err == nil {
		t.Fatal("a session id escaped the registry directory")
	}
}

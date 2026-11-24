package store_test

import (
	"context"
	"testing"

	"github.com/f1shsmell/flotilla/internal/store"
)

// TestProtectedSessionsRoundTrip pins the pin's only job: it is a set, so a
// repeated pin is not a second entry and an unpin removes it for good.
func TestProtectedSessionsRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.SetProtected(ctx, "sess-keep", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := s.SetProtected(ctx, "sess-keep", true); err != nil {
		t.Fatalf("pin again: %v", err)
	}
	if err := s.SetProtected(ctx, "sess-other", true); err != nil {
		t.Fatalf("pin a second session: %v", err)
	}

	ids, err := s.ProtectedIDs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 || ids[0] != "sess-keep" || ids[1] != "sess-other" {
		t.Fatalf("pins = %v, want the two sessions in order", ids)
	}

	if err := s.SetProtected(ctx, "sess-keep", false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	ids, err = s.ProtectedIDs(ctx)
	if err != nil {
		t.Fatalf("list after unpin: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sess-other" {
		t.Fatalf("pins after unpin = %v, want only sess-other", ids)
	}
}

// TestProtectionSurvivesAReopen pins the reason it is stored at all: a pin is
// the user's decision about a session, and a daemon restart must not undo it.
func TestProtectionSurvivesAReopen(t *testing.T) {
	path := dbPath(t)
	ctx := context.Background()

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.SetProtected(ctx, "sess-pin", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	ids, err := second.ProtectedIDs(ctx)
	if err != nil {
		t.Fatalf("list after reopen: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sess-pin" {
		t.Fatalf("pins after reopen = %v, want sess-pin", ids)
	}
}

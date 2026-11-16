package comments_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/f1shsmell/flotilla/internal/comments"
)

func TestAddListAndRemove(t *testing.T) {
	dir := t.TempDir()
	store := comments.NewStore(dir)

	added, err := store.Add("sess-a", comments.Comment{
		Path: "readme.md",
		Line: 3,
		Side: comments.NewSide,
		Text: "this needs a test",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.ID == "" || added.At.IsZero() || added.Author != "user" {
		t.Fatalf("added = %+v, want an id, a time and an author", added)
	}

	// A second store reads what the first wrote, which is what a restart sees.
	list, err := comments.NewStore(dir).List("sess-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != added.ID || list[0].Text != "this needs a test" {
		t.Fatalf("list = %+v, want the one comment", list)
	}

	if err := store.Remove("sess-a", added.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	empty, err := store.List("sess-a")
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("list after remove = %+v, want none", empty)
	}
}

func TestListOfAnUnknownSessionIsEmpty(t *testing.T) {
	list, err := comments.NewStore(t.TempDir()).List("never-seen")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want none", list)
	}
}

func TestRemoveOfAnUnknownCommentIsRefused(t *testing.T) {
	store := comments.NewStore(t.TempDir())
	store.Add("sess-a", comments.Comment{Path: "a.txt", Line: 1, Side: comments.NewSide, Text: "hi"})

	if err := store.Remove("sess-a", "nope"); !errors.Is(err, comments.ErrNoComment) {
		t.Fatalf("Remove of an unknown id = %v, want ErrNoComment", err)
	}
}

// TestRefusesACommentThatCannotBeMeant pins the validation: an empty note, a
// path out of the repository, a line that does not exist, a side nobody draws.
func TestRefusesACommentThatCannotBeMeant(t *testing.T) {
	store := comments.NewStore(t.TempDir())

	cases := map[string]comments.Comment{
		"empty text":   {Path: "a.txt", Line: 1, Side: comments.NewSide, Text: "   "},
		"empty path":   {Line: 1, Side: comments.NewSide, Text: "hi"},
		"absolute":     {Path: "/etc/passwd", Line: 1, Side: comments.NewSide, Text: "hi"},
		"escaping":     {Path: "../outside.txt", Line: 1, Side: comments.NewSide, Text: "hi"},
		"zero line":    {Path: "a.txt", Line: 0, Side: comments.NewSide, Text: "hi"},
		"unknown side": {Path: "a.txt", Line: 1, Side: comments.Side("middle"), Text: "hi"},
	}

	for name, c := range cases {
		if _, err := store.Add("sess-a", c); !errors.Is(err, comments.ErrBadComment) {
			t.Fatalf("%s: Add = %v, want ErrBadComment", name, err)
		}
	}
}

// TestSessionsDoNotShareAFile pins the isolation: one session's notes are not
// another's, whatever the ids look like.
func TestSessionsDoNotShareAFile(t *testing.T) {
	dir := t.TempDir()
	store := comments.NewStore(dir)

	store.Add("sess-a", comments.Comment{Path: "a.txt", Line: 1, Side: comments.NewSide, Text: "a"})
	store.Add("sess-b", comments.Comment{Path: "b.txt", Line: 2, Side: comments.NewSide, Text: "b"})

	a, err := store.List("sess-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(a) != 1 || a[0].Text != "a" {
		t.Fatalf("sess-a = %+v, want only its own comment", a)
	}

	if _, err := filepath.Abs(filepath.Join(dir, "sess-a.json")); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

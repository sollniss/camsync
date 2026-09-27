package camsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalUndo(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) string {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a := write("Inbox/a.jpg")
	ax := write("Inbox/a.jpg.xmp")
	b := write("Inbox/b.jpg")
	deep := filepath.Join(root, "Trips", "2024", "Rügen")

	if _, _, err := LastJournal(root); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("empty journal: %v", err)
	}
	// Two plans, the second creating a folder two levels deep.
	first, err := ApplyPlan(Plan{Root: root, Moves: []Move{{Path: a, Sidecar: ax, To: filepath.Join(root, "Inbox", "x")}}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteJournal(root, first, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	a2 := filepath.Join(root, "Inbox", "x", "a.jpg")
	second, err := ApplyPlan(Plan{Root: root, Moves: []Move{{Path: a2, Sidecar: a2 + ".xmp", To: deep}, {Path: b, To: deep}}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Created) != 3 {
		t.Errorf("created = %v", second.Created)
	}
	if path, err := WriteJournal(root, second, time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)); err != nil || path == "" {
		t.Fatal(path, err)
	}
	if path, err := WriteJournal(root, &ApplyResult{DryRun: true, Moved: 1}, time.Now()); path != "" || err != nil {
		t.Errorf("dry run recorded: %q %v", path, err)
	}

	// Something in the way of b: the undo moves a back, keeps b's move for
	// later and leaves the folders that are not empty.
	write("Inbox/b.jpg")
	j, path, err := LastJournal(root)
	if err != nil || len(j.Moves) != 2 {
		t.Fatalf("last = %+v %v", j, err)
	}
	res, err := Undo(j, path, false, nil)
	if err != nil || res.Moved != 1 || res.Failed != 1 {
		t.Fatalf("undo = %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Inbox", "x", "a.jpg.xmp")); err != nil {
		t.Error("sidecar not moved back:", err)
	}
	if _, err := os.Stat(deep); err != nil {
		t.Error("a folder that still holds a photo was removed")
	}
	j, path, err = LastJournal(root)
	if err != nil || len(j.Moves) != 1 || j.Moves[0].Path != b {
		t.Fatalf("left = %+v %v", j, err)
	}
	// With the way clear, the rest goes back and the created folders go.
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if res, err := Undo(j, path, false, nil); err != nil || res.Moved != 1 {
		t.Fatalf("undo rest = %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Trips")); !os.IsNotExist(err) {
		t.Errorf("created folders left: %v", err)
	}
	// The plan before is next.
	j, path, err = LastJournal(root)
	if err != nil || j.Moves[0].NewPath != a2 {
		t.Fatalf("earlier = %+v %v", j, err)
	}
	if _, err := Undo(j, path, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, ax} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	if _, _, err := LastJournal(root); !errors.Is(err, ErrNothingToUndo) {
		t.Errorf("after undoing everything: %v", err)
	}
}

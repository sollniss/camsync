package camsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPlan(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) string {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a := write("Inbox/a.jpg", "a")
	ax := write("Inbox/a.jpg.xmp", "ax")
	b := write("Inbox/b.jpg", "b")
	c := write("Inbox/c.jpg", "c")
	write("Trip/c.jpg", "taken")
	trip := filepath.Join(root, "Trip")
	fresh := filepath.Join(root, "2024-06-10 - 2024-06-12")

	plan := Plan{Root: root, Moves: []Move{
		{Path: a, Sidecar: ax, To: trip},
		{Path: b, To: fresh},
		{Path: c, To: trip}, // destination exists
		{Path: filepath.Join(root, "Inbox", "missing.jpg"), To: trip},
	}}
	var seen []MoveResult
	res, err := ApplyPlan(plan, true, func(r MoveResult) { seen = append(seen, r) })
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Moved != 2 || res.Failed != 2 || len(seen) != 4 || len(res.Created) != 1 || res.Created[0] != fresh {
		t.Errorf("dry run = %+v", res)
	}
	if _, err := os.Stat(a); err != nil {
		t.Error("dry run moved a file")
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Error("dry run created a directory")
	}

	res, err = ApplyPlan(plan, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 2 || res.Failed != 2 {
		t.Errorf("apply = %+v", res)
	}
	for _, path := range []string{filepath.Join(trip, "a.jpg"), filepath.Join(trip, "a.jpg.xmp"), filepath.Join(fresh, "b.jpg"), c} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	for _, path := range []string{a, ax, b} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there: %v", path, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(trip, "c.jpg")); string(data) != "taken" {
		t.Error("an existing file was overwritten")
	}
	if r := res.Results[2]; r.Err == nil || r.NewPath != filepath.Join(trip, "c.jpg") {
		t.Errorf("collision = %+v", r)
	}

	// Moving back reports "already there" for a file that is not going anywhere.
	res, _ = ApplyPlan(Plan{Root: root, Moves: []Move{{Path: filepath.Join(trip, "a.jpg"), To: trip}}}, false, nil)
	if res.Failed != 1 || res.Results[0].Err.Error() != "already there" {
		t.Errorf("same dir = %+v", res.Results[0])
	}
}

func TestPlanValidate(t *testing.T) {
	root := t.TempDir()
	in := filepath.Join(root, "a.jpg")
	cases := map[string]Plan{
		"no root":      {Moves: []Move{{Path: in, To: root}}},
		"outside":      {Root: root, Moves: []Move{{Path: filepath.Join(root, "..", "a.jpg"), To: root}}},
		"dest outside": {Root: root, Moves: []Move{{Path: in, To: filepath.Dir(root)}}},
		"twice":        {Root: root, Moves: []Move{{Path: in, To: root}, {Path: in, To: root}}},
		"empty":        {Root: root, Moves: []Move{{Path: "", To: root}}},
	}
	for name, plan := range cases {
		if err := plan.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Plan{Root: root, Moves: []Move{{Path: in, Sidecar: in + ".xmp", To: filepath.Join(root, "sub")}}}).Validate(); err != nil {
		t.Error(err)
	}
}

package camsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// JournalDir is where applied plans are recorded, relative to the library
// root. Scans skip it like every hidden directory.
const JournalDir = ".camsync/journal"

// ErrNothingToUndo is returned by Undo when no applied plan is left.
var ErrNothingToUndo = errors.New("nothing to undo")

// Journal records the moves of one applied plan so that they can be undone.
type Journal struct {
	Time    time.Time     `json:"time"`
	Root    string        `json:"root"`
	Moves   []JournalMove `json:"moves"`
	Created []string      `json:"created"` // directories the plan created
}

// JournalMove is one move that succeeded: where the photo and its sidecar
// were and where they are now.
type JournalMove struct {
	Path       string `json:"path"`
	Sidecar    string `json:"sidecar,omitempty"`
	NewPath    string `json:"newPath"`
	NewSidecar string `json:"newSidecar,omitempty"`
}

// WriteJournal records the moves of res that succeeded in the journal of
// root and returns the file written. Dry runs and applies that moved
// nothing are not recorded; the path is then empty.
func WriteJournal(root string, res *ApplyResult, now time.Time) (string, error) {
	if res.DryRun || res.Moved == 0 {
		return "", nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	j := Journal{Time: now, Root: root, Created: []string{}}
	for _, r := range res.Results {
		if r.Err != nil {
			continue
		}
		m := JournalMove{Path: r.Path, Sidecar: r.Sidecar, NewPath: r.NewPath, NewSidecar: r.NewSidecar}
		for _, p := range []*string{&m.Path, &m.Sidecar, &m.NewPath, &m.NewSidecar} {
			if *p != "" {
				if *p, err = filepath.Abs(*p); err != nil {
					return "", err
				}
			}
		}
		j.Moves = append(j.Moves, m)
	}
	for _, dir := range res.Created {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		j.Created = append(j.Created, abs)
	}
	dir := filepath.Join(root, filepath.FromSlash(JournalDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, now.UTC().Format("20060102T150405.000000000Z")+".json")
	return path, writeJournal(path, &j)
}

func writeJournal(path string, j *Journal) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// LastJournal returns the most recent applied plan of root that has not been
// undone, with the file it is recorded in, or ErrNothingToUndo.
func LastJournal(root string) (*Journal, string, error) {
	dir := filepath.Join(root, filepath.FromSlash(JournalDir))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNothingToUndo
	}
	if err != nil {
		return nil, "", err
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && strings.HasSuffix(n, ".json") && !strings.HasSuffix(n, ".undone.json") {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, "", ErrNothingToUndo
	}
	slices.Sort(names)
	path := filepath.Join(dir, names[len(names)-1])
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	return &j, path, nil
}

// Undo moves the photos of the journal at path back where they were and
// removes the directories the plan created once they are empty. Moves that
// succeed are dropped from the journal; when none is left the journal is
// marked as undone, so that the next Undo reaches the plan applied before.
// With dryRun nothing is touched.
func Undo(j *Journal, path string, dryRun bool, progress func(MoveResult)) (*ApplyResult, error) {
	plan := Plan{Root: j.Root, Moves: make([]Move, 0, len(j.Moves))}
	for _, m := range j.Moves {
		plan.Moves = append(plan.Moves, Move{Path: m.NewPath, Sidecar: m.NewSidecar, To: filepath.Dir(m.Path)})
	}
	res, err := ApplyPlan(plan, dryRun, progress)
	if err != nil || dryRun {
		return res, err
	}
	// Deepest first, so that parents are empty by the time they come up.
	created := slices.Clone(j.Created)
	slices.SortFunc(created, func(a, b string) int { return len(b) - len(a) })
	for _, dir := range created {
		_ = os.Remove(dir) // fails, as it should, when something else is in there
	}
	var left []JournalMove
	for i, r := range res.Results {
		if r.Err != nil {
			left = append(left, j.Moves[i])
		}
	}
	if len(left) == 0 {
		return res, os.Rename(path, strings.TrimSuffix(path, ".json")+".undone.json")
	}
	rest := *j
	rest.Moves = left
	return res, writeJournal(path, &rest)
}

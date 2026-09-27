package camsync

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Move is one photo, with its sidecar, going to another directory.
type Move struct {
	Path    string `json:"path"`
	Sidecar string `json:"sidecar,omitempty"`
	To      string `json:"to"` // destination directory
}

// Plan is a list of moves within a library, the outcome of reviewing the
// suggestions of Organize. It is what the UI hands to the command line and
// what --plan writes.
type Plan struct {
	Root  string `json:"root"`
	Moves []Move `json:"moves"`
}

// Validate checks that every move stays inside the root and names no file
// twice.
func (p Plan) Validate() error {
	if p.Root == "" {
		return errors.New("plan has no root")
	}
	root, err := filepath.Abs(p.Root)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range p.Moves {
		if m.Path == "" || m.To == "" {
			return errors.New("move without path or destination")
		}
		for _, path := range []string{m.Path, m.Sidecar, m.To} {
			if path == "" {
				continue
			}
			if err := within(root, path); err != nil {
				return err
			}
		}
		if seen[m.Path] {
			return fmt.Errorf("%s is moved twice", m.Path)
		}
		seen[m.Path] = true
	}
	return nil
}

// within reports an error when path is not under root.
func within(root, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside the library %s", path, root)
	}
	return nil
}

// MoveResult is the outcome of one move.
type MoveResult struct {
	Move
	NewPath    string // where the photo is now
	NewSidecar string
	Err        error
}

// ApplyResult is the outcome of ApplyPlan.
type ApplyResult struct {
	Moved   int
	Failed  int
	Created []string // directories created
	Results []MoveResult
	DryRun  bool
}

// ApplyPlan carries out the moves. Photos are renamed, never copied over
// an existing file, and the sidecar follows its photo. A move whose
// destination exists fails and the photo stays put. Directories are
// created as needed. With dryRun nothing is touched. Progress, if set, is
// called after every move.
func ApplyPlan(plan Plan, dryRun bool, progress func(MoveResult)) (*ApplyResult, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	res := &ApplyResult{DryRun: dryRun}
	created := map[string]bool{}
	for _, m := range plan.Moves {
		r := MoveResult{Move: m, NewPath: filepath.Join(m.To, filepath.Base(m.Path))}
		if m.Sidecar != "" {
			r.NewSidecar = filepath.Join(m.To, filepath.Base(m.Sidecar))
		}
		r.Err = applyMove(m, r, dryRun, created)
		if r.Err != nil {
			res.Failed++
		} else {
			res.Moved++
		}
		res.Results = append(res.Results, r)
		if progress != nil {
			progress(r)
		}
	}
	for dir := range created {
		res.Created = append(res.Created, dir)
	}
	slices.Sort(res.Created)
	return res, nil
}

func applyMove(m Move, r MoveResult, dryRun bool, created map[string]bool) error {
	if _, err := os.Stat(m.Path); err != nil {
		return err
	}
	if filepath.Clean(filepath.Dir(m.Path)) == filepath.Clean(m.To) {
		return errors.New("already there")
	}
	for _, target := range []string{r.NewPath, r.NewSidecar} {
		if target == "" {
			continue
		}
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("%s exists", target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	missing, err := missingDirs(m.To)
	if err != nil {
		return err
	}
	for _, dir := range missing {
		created[dir] = true
	}
	if len(missing) > 0 && !dryRun {
		if err := os.MkdirAll(m.To, 0o755); err != nil {
			return err
		}
	}
	if dryRun {
		return nil
	}
	if err := moveFile(m.Path, r.NewPath); err != nil {
		return err
	}
	if m.Sidecar == "" {
		return nil
	}
	if err := moveFile(m.Sidecar, r.NewSidecar); err != nil {
		// Keep photo and sidecar together: bring the photo back.
		if back := moveFile(r.NewPath, m.Path); back != nil {
			return errors.Join(fmt.Errorf("sidecar: %w", err), fmt.Errorf("moving the photo back: %w", back))
		}
		return fmt.Errorf("sidecar: %w", err)
	}
	return nil
}

// missingDirs returns dir and those of its parents that do not exist yet,
// which is what MkdirAll would create.
func missingDirs(dir string) ([]string, error) {
	var missing []string
	for {
		_, err := os.Stat(dir)
		if err == nil {
			return missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return missing, nil
		}
		dir = parent
	}
}

// moveFile renames src to dst, copying and deleting when the two are on
// different file systems.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !isCrossDevice(linkErr.Err) {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}

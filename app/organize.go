package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/sollniss/camsync"
)

// OrganizeQuery asks how a photo library is organised: which events its
// photos form, what each folder holds and what could be moved where. The
// whole tree under Dir is scanned whatever Recursive says.
type OrganizeQuery struct {
	Scope
	Options camsync.LibraryOptions
}

// Validate reports whether the query can be executed. A failure is a
// *ValidationError.
func (q OrganizeQuery) Validate() error { return invalid(q.check()) }

func (q OrganizeQuery) check() error {
	if err := q.validate(); err != nil {
		return err
	}
	_, err := camsync.ParseLayout(string(q.Options.Layout))
	return err
}

// OrganizeResult is the answer to an OrganizeQuery: the library as seen
// with the query's options, plus everything the scan found so that the
// options can be changed without scanning again (see With).
type OrganizeResult struct {
	*camsync.Library
	Options camsync.LibraryOptions
	Photos  []*camsync.Photo // every photo scanned, sorted by path
	Ignored int              // regular files that are neither photos nor sidecars
}

// With plans the same photos again with other options.
func (r *OrganizeResult) With(opts camsync.LibraryOptions) *OrganizeResult {
	opts.Inboxes = absInboxes(r.Root, opts.Inboxes)
	return &OrganizeResult{Library: camsync.Organize(r.Root, r.Photos, opts), Options: opts, Photos: r.Photos, Ignored: r.Ignored}
}

// absInboxes resolves inbox folders given relative to the root.
func absInboxes(root string, inboxes []string) []string {
	out := make([]string, 0, len(inboxes))
	for _, in := range inboxes {
		if !filepath.IsAbs(in) {
			in = filepath.Join(root, in)
		}
		out = append(out, filepath.Clean(in))
	}
	return out
}

// OrganizeHandler executes OrganizeQuery.
type OrganizeHandler struct{}

// Handle scans the whole tree and plans the library. Nothing is moved.
func (OrganizeHandler) Handle(ctx context.Context, q OrganizeQuery) (*OrganizeResult, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope := q.Scope
	scope.Recursive = true
	res, err := scope.scan(ctx)
	if err != nil {
		return nil, err
	}
	opts := q.Options
	opts.Inboxes = absInboxes(scope.Dir, opts.Inboxes)
	return &OrganizeResult{
		Library: camsync.Organize(scope.Dir, res.Photos, opts),
		Options: opts, Photos: res.Photos, Ignored: res.Ignored,
	}, nil
}

// ApplyCommand carries out the moves of a plan, as written by organize or
// assembled from accepted suggestions.
type ApplyCommand struct {
	Plan   camsync.Plan
	DryRun bool
}

// Validate reports whether the command can be executed. A failure is a
// *ValidationError.
func (c ApplyCommand) Validate() error { return invalid(c.Plan.Validate()) }

// ApplyResult is the outcome of an ApplyCommand.
type ApplyResult struct {
	*camsync.ApplyResult
	// Journal is the file the moves were recorded in for UndoCommand; empty
	// for dry runs and when nothing moved.
	Journal string
	// JournalErr says why the moves could not be recorded. The moves
	// themselves have happened.
	JournalErr error
}

// ApplyHandler executes ApplyCommand. Progress, if set, is called after
// every move.
type ApplyHandler struct {
	Progress func(camsync.MoveResult)
}

// Handle moves the photos with their sidecars and records the moves in the
// library's journal so that UndoCommand can reverse them. A move that names
// no sidecar takes the one found next to the photo, so plans written by hand
// or by the UI need not know about them. Moves that fail are reported in the
// result; the error is only non-nil when the plan itself is invalid.
func (h ApplyHandler) Handle(ctx context.Context, cmd ApplyCommand) (*ApplyResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan := cmd.Plan
	plan.Moves = make([]camsync.Move, len(cmd.Plan.Moves))
	for i, m := range cmd.Plan.Moves {
		if m.Sidecar == "" {
			m.Sidecar = camsync.FindSidecar(m.Path)
		}
		plan.Moves[i] = m
	}
	res, err := camsync.ApplyPlan(plan, cmd.DryRun, h.Progress)
	if err != nil {
		return nil, err
	}
	out := &ApplyResult{ApplyResult: res}
	out.Journal, out.JournalErr = camsync.WriteJournal(plan.Root, res, time.Now())
	return out, nil
}

// UndoCommand moves the photos of the plan last applied to a library back
// where they were. Repeated, it walks back through earlier plans.
type UndoCommand struct {
	Root   string
	DryRun bool
}

// Validate reports whether the command can be executed. A failure is a
// *ValidationError.
func (c UndoCommand) Validate() error { return invalid(Scope{Dir: c.Root}.validate()) }

// UndoResult is the outcome of an UndoCommand.
type UndoResult struct {
	*camsync.ApplyResult
	Applied time.Time // when the undone plan was applied
}

// UndoHandler executes UndoCommand. Progress, if set, is called after every
// move.
type UndoHandler struct {
	Progress func(camsync.MoveResult)
}

// Handle undoes the last plan applied to the library, or returns
// camsync.ErrNothingToUndo. Moves that fail stay in the journal to be
// retried.
func (h UndoHandler) Handle(ctx context.Context, cmd UndoCommand) (*UndoResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j, path, err := camsync.LastJournal(cmd.Root)
	if err != nil {
		return nil, err
	}
	res, err := camsync.Undo(j, path, cmd.DryRun, h.Progress)
	if res == nil {
		return nil, err
	}
	return &UndoResult{ApplyResult: res, Applied: j.Time}, err
}

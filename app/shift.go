package app

import (
	"context"
	"errors"
	"time"

	"github.com/sollniss/camsync"
)

// ShiftCommand moves the timestamps of one camera's photos.
type ShiftCommand struct {
	Scope
	Camera string        // as printed by list: exact name, row number or unique substring
	By     time.Duration // wall-clock amount to add; may be zero when Zone is set
	Zone   string        // UTC offset to stamp on the written dates, any form ParseZone accepts
	DryRun bool
}

// Validate reports whether the command can be executed. A failure is a
// *ValidationError.
func (c ShiftCommand) Validate() error { return invalid(c.check()) }

func (c ShiftCommand) check() error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.Camera == "" {
		return errors.New("camera is required")
	}
	if c.By == 0 && c.Zone == "" {
		return errors.New("a shift amount or a zone is required")
	}
	if c.Zone != "" {
		if _, err := camsync.ParseZone(c.Zone); err != nil {
			return err
		}
	}
	return nil
}

// ResetCommand restores the timestamps recorded in the image files.
type ResetCommand struct {
	Scope
	Camera string
	DryRun bool
}

// Validate reports whether the command can be executed. A failure is a
// *ValidationError.
func (c ResetCommand) Validate() error { return invalid(c.check()) }

func (c ResetCommand) check() error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.Camera == "" {
		return errors.New("camera is required")
	}
	return nil
}

// Action is what happened to one photo.
type Action string

// The actions apply reports for a photo.
const (
	ActionCreate Action = "create" // a new sidecar was written
	ActionUpdate Action = "update" // an existing sidecar was rewritten
	ActionSkip   Action = "skip"   // nothing to do; Reason explains
	ActionFail   Action = "fail"   // the sidecar could not be written; Reason explains
)

// Entry is the outcome for one photo.
type Entry struct {
	Path    string
	Action  Action
	Before  *camsync.Stamp // effective timestamp before the change
	After   *camsync.Stamp // timestamp written; nil when skipped or failed
	Sidecar string         // sidecar written, or that would be written
	Reason  string         // why the photo was skipped or failed
}

// ChangeResult is the outcome of a ShiftCommand or ResetCommand.
type ChangeResult struct {
	Camera  string // resolved camera name
	By      time.Duration
	Zone    string // normalised zone, "" if none was requested
	DryRun  bool
	Entries []Entry
	Written int // sidecars written (created + updated)
	Created int
	Skipped int
	Failed  int
}

// ShiftHandler executes ShiftCommand. Progress, if set, is called after
// every photo.
type ShiftHandler struct {
	Progress func(Entry)
}

// Handle plans and writes the shift for every photo of the camera. Photos
// that cannot be shifted are skipped and reported, not treated as errors;
// check ChangeResult.Failed for write failures. The error is only non-nil
// when nothing could be done at all or the context was cancelled, in which
// case the partial result is returned alongside it.
func (h ShiftHandler) Handle(ctx context.Context, cmd ShiftCommand) (*ChangeResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	shift := camsync.Shift{By: cmd.By}
	if cmd.Zone != "" {
		shift.Zone, _ = camsync.ParseZone(cmd.Zone) // validated above
	}
	plan := func(p *camsync.Photo) (*camsync.Change, error) { return camsync.PlanShift(p, shift) }
	res, err := apply(ctx, cmd.Scope, cmd.Camera, cmd.DryRun, plan, h.Progress)
	if res != nil {
		res.By, res.Zone = cmd.By, shift.Zone
	}
	return res, err
}

// ResetHandler executes ResetCommand. See ShiftHandler for the semantics.
type ResetHandler struct {
	Progress func(Entry)
}

// Handle restores the file timestamps for every photo of the camera.
func (h ResetHandler) Handle(ctx context.Context, cmd ResetCommand) (*ChangeResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	return apply(ctx, cmd.Scope, cmd.Camera, cmd.DryRun, camsync.PlanReset, h.Progress)
}

func apply(
	ctx context.Context,
	scope Scope,
	camera string,
	dryRun bool,
	plan func(*camsync.Photo) (*camsync.Change, error),
	progress func(Entry),
) (*ChangeResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := scope.scan(ctx)
	if err != nil {
		return nil, err
	}
	g, err := camsync.SelectGroup(camsync.GroupPhotos(res.Photos), camera)
	if err != nil {
		return nil, invalid(err) // the camera value did not select one group
	}
	out := &ChangeResult{Camera: g.Camera, DryRun: dryRun}
	for _, p := range g.Photos {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		e := Entry{Path: p.Path, Before: p.Original, Sidecar: p.SidecarTarget}
		ch, err := plan(p)
		if err != nil {
			e.Action, e.Reason = ActionSkip, err.Error()
			out.Skipped++
		} else if created, err := ch.Write(dryRun); err != nil {
			e.Action, e.Reason = ActionFail, err.Error()
			out.Failed++
		} else {
			e.Action = ActionUpdate
			if created {
				e.Action = ActionCreate
				out.Created++
			}
			after := ch.NewOriginal
			e.After = &after
			out.Written++
		}
		out.Entries = append(out.Entries, e)
		if progress != nil {
			progress(e)
		}
	}
	return out, nil
}

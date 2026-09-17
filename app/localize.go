package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sollniss/camsync"
)

// LocalizeCommand rewrites timestamps in the local zone of the place each
// photo was taken, keeping the moment.
type LocalizeCommand struct {
	Scope
	ClockSettings
	// Camera, if set, limits the command to one camera, selected as in list.
	// The other cameras still provide evidence.
	Camera string
	// Zone, if set, is the local zone used where no phone photo tells it, in
	// any form ParseZone accepts.
	Zone   string
	DryRun bool
}

// Validate reports whether the command can be executed. A failure is a
// *ValidationError.
func (c LocalizeCommand) Validate() error { return invalid(c.check()) }

func (c LocalizeCommand) check() error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.Zone != "" {
		if _, err := camsync.ParseZone(c.Zone); err != nil {
			return err
		}
	}
	return c.ClockSettings.check()
}

// LocalizedCamera is the outcome for one camera.
type LocalizedCamera struct {
	ChangeResult
	ClockZone  camsync.ClockZone
	LocalZones []string // zones written, in ascending order
	DayChanges int      // photos whose date changes
}

// LocalizeResult is the outcome of a LocalizeCommand.
type LocalizeResult struct {
	Cameras   []LocalizedCamera
	DryRun    bool
	Evidence  int // photos that tell the local zone
	Written   int
	Created   int
	Skipped   int
	Unchanged int // photos already in local time, counted in Skipped
	Failed    int
}

// LocalizeHandler executes LocalizeCommand. Progress, if set, is called
// after every photo.
type LocalizeHandler struct {
	Progress func(camera string, e Entry)
}

// Handle estimates the clocks, then plans and writes the local timestamps
// for every photo of the selected cameras. Photos that cannot be placed are
// skipped and reported. The error is only non-nil when nothing could be done
// or the context was cancelled, in which case the partial result is returned
// alongside it.
func (h LocalizeHandler) Handle(ctx context.Context, cmd LocalizeCommand) (*LocalizeResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := cmd.scan(ctx)
	if err != nil {
		return nil, err
	}
	groups := camsync.GroupPhotos(res.Photos)
	opts, err := cmd.options(groups)
	if err != nil {
		return nil, err
	}
	selected := groups
	if cmd.Camera != "" {
		g, err := camsync.SelectGroup(groups, cmd.Camera)
		if err != nil {
			return nil, invalid(err)
		}
		selected = []*camsync.Group{g}
	}
	var lopts camsync.LocalizeOptions
	if cmd.Zone != "" {
		z, _ := camsync.ParseZoneOffset(cmd.Zone) // validated
		lopts.Zone = &z
	}

	clocks := camsync.EstimateClocks(groups, opts)
	out := &LocalizeResult{DryRun: cmd.DryRun, Evidence: clocks.Timeline.Len()}
	for _, g := range selected {
		est := clocks.Estimate(g)
		cam := LocalizedCamera{ChangeResult: ChangeResult{Camera: g.Camera, DryRun: cmd.DryRun}, ClockZone: est.ClockZone}
		var progress func(Entry)
		if h.Progress != nil {
			progress = func(e Entry) { h.Progress(g.Camera, e) }
		}
		plan := func(p *camsync.Photo) (*camsync.Change, error) {
			ch, err := clocks.PlanLocalize(est, p, lopts)
			if errors.Is(err, camsync.ErrUnchanged) {
				out.Unchanged++
			}
			return ch, err
		}
		err := cam.run(ctx, g.Photos, plan, progress)
		for _, e := range cam.Entries {
			if e.After == nil {
				continue
			}
			if e.Before != nil && e.Before.T.Format(time.DateOnly) != e.After.T.Format(time.DateOnly) {
				cam.DayChanges++
			}
			if !slices.Contains(cam.LocalZones, e.After.TZ) {
				cam.LocalZones = append(cam.LocalZones, e.After.TZ)
			}
		}
		slices.Sort(cam.LocalZones)
		out.Cameras = append(out.Cameras, cam)
		out.Written += cam.Written
		out.Created += cam.Created
		out.Skipped += cam.Skipped
		out.Failed += cam.Failed
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

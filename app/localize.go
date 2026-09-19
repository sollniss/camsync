package app

import (
	"context"
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

// LocalizeResult is the outcome of a LocalizeCommand. Unchanged counts the
// photos already in local time.
type LocalizeResult struct {
	Totals
	Cameras  []LocalizedCamera
	DryRun   bool
	Evidence int // photos that tell the local zone
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
	clocks, cameras, err := estimate(ctx, cmd.Scope, cmd.ClockSettings, cmd.Camera)
	if err != nil {
		return nil, err
	}
	var lopts camsync.LocalizeOptions
	if cmd.Zone != "" {
		z, _ := camsync.ParseZoneOffset(cmd.Zone) // validated
		lopts.Zone = &z
	}
	out := &LocalizeResult{DryRun: cmd.DryRun, Evidence: clocks.Timeline.Len()}
	err = out.runCameras(ctx, cameras, cmd.DryRun,
		func(e *camsync.ClockEstimate, p *camsync.Photo) (*camsync.Change, error) {
			return clocks.PlanLocalize(e, p, lopts)
		},
		h.Progress,
		func(e *camsync.ClockEstimate, res ChangeResult) {
			cam := LocalizedCamera{ChangeResult: res, ClockZone: e.ClockZone}
			for _, entry := range res.Entries {
				if entry.After == nil {
					continue
				}
				if entry.Before != nil && entry.Before.T.Format(time.DateOnly) != entry.After.T.Format(time.DateOnly) {
					cam.DayChanges++
				}
				if !slices.Contains(cam.LocalZones, entry.After.TZ) {
					cam.LocalZones = append(cam.LocalZones, entry.After.TZ)
				}
			}
			slices.Sort(cam.LocalZones)
			out.Cameras = append(out.Cameras, cam)
		})
	return out, err
}

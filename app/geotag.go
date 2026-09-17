package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sollniss/camsync"
)

// GeotagCommand writes estimated positions for photos without one, taken
// from photos with a measured position close in time.
type GeotagCommand struct {
	Scope
	ClockSettings
	// Camera, if set, limits the command to one camera, selected as in list.
	// The other cameras still provide evidence.
	Camera string
	// Track sets how far the evidence reaches; zero values select the
	// defaults.
	Track  camsync.TrackOptions
	DryRun bool
}

// Validate reports whether the command can be executed. A failure is a
// *ValidationError.
func (c GeotagCommand) Validate() error { return invalid(c.check()) }

func (c GeotagCommand) check() error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.Track.MaxGap < 0 || c.Track.MaxBridge < 0 || c.Track.BridgeDistance < 0 {
		return errors.New("track settings must not be negative")
	}
	return c.ClockSettings.check()
}

// GeotaggedCamera is the outcome for one camera.
type GeotaggedCamera struct {
	ChangeResult
	ClockZone    camsync.ClockZone
	Located      int           // photos that already have a measured position
	Interpolated int           // positions written between two located photos
	MedianGap    time.Duration // median reach of the positions written
	MaxGap       time.Duration // largest reach of the positions written
}

// GeotagResult is the outcome of a GeotagCommand.
type GeotagResult struct {
	Cameras   []GeotaggedCamera
	DryRun    bool
	Evidence  int // located photos the positions come from
	Written   int
	Created   int
	Skipped   int
	Unchanged int // photos whose estimate is already written, counted in Skipped
	Located   int // photos with a measured position, counted in Skipped
	Failed    int
}

// GeotagHandler executes GeotagCommand. Progress, if set, is called after
// every photo.
type GeotagHandler struct {
	Progress func(camera string, e Entry)
}

// Handle estimates the clocks, builds the track of located photos and
// writes a position for every photo of the selected cameras that the track
// reaches. See LocalizeHandler.Handle for the error semantics.
func (h GeotagHandler) Handle(ctx context.Context, cmd GeotagCommand) (*GeotagResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	clocks, selected, err := estimate(ctx, cmd.Scope, cmd.ClockSettings, cmd.Camera)
	if err != nil {
		return nil, err
	}
	track := clocks.Track()
	out := &GeotagResult{DryRun: cmd.DryRun, Evidence: track.Len()}
	for _, g := range selected {
		est := clocks.Estimate(g)
		cam := GeotaggedCamera{ChangeResult: ChangeResult{Camera: g.Camera, DryRun: cmd.DryRun}, ClockZone: est.ClockZone}
		var progress func(Entry)
		if h.Progress != nil {
			progress = func(e Entry) { h.Progress(g.Camera, e) }
		}
		plan := func(p *camsync.Photo) (*camsync.Change, error) {
			ch, err := clocks.PlanGeotag(est, p, track, cmd.Track)
			switch {
			case errors.Is(err, camsync.ErrUnchanged):
				out.Unchanged++
			case errors.Is(err, camsync.ErrHasPosition):
				cam.Located++
			}
			return ch, err
		}
		err := cam.run(ctx, g.Photos, plan, progress)
		var gaps []time.Duration
		for _, e := range cam.Entries {
			if e.Fix == nil {
				continue
			}
			gaps = append(gaps, e.Fix.Gap)
			if e.Fix.Interpolated {
				cam.Interpolated++
			}
		}
		if len(gaps) > 0 {
			slices.Sort(gaps)
			cam.MedianGap, cam.MaxGap = gaps[len(gaps)/2], gaps[len(gaps)-1]
		}
		out.Cameras = append(out.Cameras, cam)
		out.Written += cam.Written
		out.Created += cam.Created
		out.Skipped += cam.Skipped
		out.Located += cam.Located
		out.Failed += cam.Failed
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

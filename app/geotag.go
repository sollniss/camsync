package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sollniss/camsync"
)

// GeotagCommand writes estimated positions for photos without one, taken
// from photos with a measured position close in time and from GPS tracks.
type GeotagCommand struct {
	Scope
	ClockSettings
	// Camera, if set, limits the command to one camera, selected as in list.
	// The other cameras still provide evidence.
	Camera string
	// Tracks are GPS track files (GPX) whose points serve as evidence
	// alongside the located photos.
	Tracks []string
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
	if c.Track.MaxGap < 0 || c.Track.MaxBridge < 0 || c.Track.BridgeDistance < 0 || c.Track.MaxDistance < 0 {
		return errors.New("track settings must not be negative")
	}
	for _, t := range c.Tracks {
		if t == "" {
			return errors.New("track file name is empty")
		}
	}
	return c.ClockSettings.check()
}

// GeotaggedCamera is the outcome for one camera.
type GeotaggedCamera struct {
	ChangeResult
	ClockZone    camsync.ClockZone
	Located      int           // photos that already have a measured position
	Interpolated int           // positions written between two observations
	MedianGap    time.Duration // median reach of the positions written
	MaxGap       time.Duration // largest reach of the positions written
	MaxDistance  float64       // metres: the furthest apart two observations an interpolation lies between are
}

// GeotagResult is the outcome of a GeotagCommand. Unchanged counts the
// photos whose estimate is already written.
type GeotagResult struct {
	Totals
	Cameras     []GeotaggedCamera
	DryRun      bool
	Evidence    int // located photos the positions come from
	TrackPoints int // points of the GPS tracks given
	Located     int // photos with a measured position, counted in Skipped
}

// GeotagHandler executes GeotagCommand. Progress, if set, is called after
// every photo.
type GeotagHandler struct {
	Progress func(camera string, e Entry)
}

// Handle reads the GPS tracks, estimates the clocks, builds the track of
// located photos and writes a position for every photo of the selected
// cameras that the evidence reaches. See LocalizeHandler.Handle for the
// error semantics.
func (h GeotagHandler) Handle(ctx context.Context, cmd GeotagCommand) (*GeotagResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	var gpx [][]camsync.PositionObservation
	for _, f := range cmd.Tracks {
		paths, err := camsync.ReadGPX(f)
		if err != nil {
			return nil, err
		}
		gpx = append(gpx, paths...)
	}
	clocks, cameras, err := estimate(ctx, cmd.Scope, cmd.ClockSettings, cmd.Camera)
	if err != nil {
		return nil, err
	}
	track := clocks.Track()
	out := &GeotagResult{DryRun: cmd.DryRun, Evidence: track.Len()}
	for _, p := range gpx {
		track.Add(p)
	}
	out.TrackPoints = track.Len() - out.Evidence
	err = out.runCameras(ctx, cameras, cmd.DryRun,
		func(e *camsync.ClockEstimate, p *camsync.Photo) (*camsync.Change, error) {
			return clocks.PlanGeotag(e, p, track, cmd.Track)
		},
		h.Progress,
		func(e *camsync.ClockEstimate, res ChangeResult) {
			cam := GeotaggedCamera{ChangeResult: res, ClockZone: e.ClockZone}
			var gaps []time.Duration
			for _, entry := range res.Entries {
				if errors.Is(entry.Err, camsync.ErrHasPosition) {
					cam.Located++
				}
				if entry.Fix == nil {
					continue
				}
				gaps = append(gaps, entry.Fix.Gap)
				if entry.Fix.Interpolated {
					cam.Interpolated++
					cam.MaxDistance = max(cam.MaxDistance, entry.Fix.Distance)
				}
			}
			if len(gaps) > 0 {
				slices.Sort(gaps)
				cam.MedianGap, cam.MaxGap = gaps[len(gaps)/2], gaps[len(gaps)-1]
			}
			out.Located += cam.Located
			out.Cameras = append(out.Cameras, cam)
		})
	return out, err
}

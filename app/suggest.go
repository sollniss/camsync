package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sollniss/camsync"
)

// ClockSettings are the inputs of the clock estimation shared by
// SuggestQuery, LocalizeCommand and GeotagCommand.
type ClockSettings struct {
	// References are the trusted cameras, as printed by list: exact name, row
	// number or unique substring. When empty, phones and cameras whose
	// timestamps agree with their GPS times are used.
	References []string
	// ClockZones set the zone a camera's clock runs on, as "CAMERA=ZONE" with
	// the camera selected like References and any zone ParseZone accepts.
	ClockZones []string
	// MaxOffset is the largest clock error searched; zero selects
	// camsync.DefaultMaxOffset.
	MaxOffset time.Duration
	// MaxZoneGap is how far an observation of the local zone reaches; zero
	// selects camsync.DefaultMaxZoneGap.
	MaxZoneGap time.Duration
}

func (s ClockSettings) check() error {
	if s.MaxOffset < 0 || s.MaxZoneGap < 0 {
		return errors.New("durations must not be negative")
	}
	for _, cz := range s.ClockZones {
		if _, _, err := splitClockZone(cz); err != nil {
			return err
		}
	}
	return nil
}

// splitClockZone parses "CAMERA=ZONE". Camera names may contain "=", zones
// never do, so the last one separates them.
func splitClockZone(v string) (camera string, zone time.Duration, err error) {
	i := strings.LastIndexByte(v, '=')
	if i <= 0 {
		return "", 0, fmt.Errorf("clock zone %q must have the form CAMERA=ZONE", v)
	}
	zone, err = camsync.ParseZoneOffset(v[i+1:])
	return v[:i], zone, err
}

// options resolves the settings against the scanned groups.
func (s ClockSettings) options(groups []*camsync.Group) (camsync.ClockOptions, error) {
	opts := camsync.ClockOptions{MaxOffset: s.MaxOffset, MaxZoneGap: s.MaxZoneGap}
	for _, r := range s.References {
		g, err := camsync.SelectGroup(groups, r)
		if err != nil {
			return opts, invalid(err)
		}
		opts.References = append(opts.References, g)
	}
	for _, cz := range s.ClockZones {
		camera, zone, _ := splitClockZone(cz) // validated
		g, err := camsync.SelectGroup(groups, camera)
		if err != nil {
			return opts, invalid(err)
		}
		if opts.ClockZones == nil {
			opts.ClockZones = map[*camsync.Group]time.Duration{}
		}
		opts.ClockZones[g] = zone
	}
	return opts, nil
}

// estimate scans the scope and estimates every camera's clock. It returns
// the estimates of the cameras selected by camera, all of them in order when
// it is empty; the other cameras still provide the evidence.
func estimate(ctx context.Context, scope Scope, settings ClockSettings, camera string) (*camsync.Clocks, []*camsync.ClockEstimate, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	res, err := scope.scan(ctx)
	if err != nil {
		return nil, nil, err
	}
	groups := camsync.GroupPhotos(res.Photos)
	opts, err := settings.options(groups)
	if err != nil {
		return nil, nil, err
	}
	var only *camsync.Group
	if camera != "" {
		if only, err = camsync.SelectGroup(groups, camera); err != nil {
			return nil, nil, invalid(err)
		}
	}
	clocks := camsync.EstimateClocks(groups, opts)
	if only != nil {
		return clocks, []*camsync.ClockEstimate{clocks.Estimate(only)}, nil
	}
	selected := make([]*camsync.ClockEstimate, len(clocks.Cameras))
	for i := range clocks.Cameras {
		selected[i] = &clocks.Cameras[i]
	}
	return clocks, selected, nil
}

// SuggestQuery asks how far each camera's clock is off, measured against the
// cameras whose clocks can be trusted.
type SuggestQuery struct {
	Scope
	ClockSettings
}

// Validate reports whether the query can be executed. A failure is a
// *ValidationError.
func (q SuggestQuery) Validate() error { return invalid(q.check()) }

func (q SuggestQuery) check() error {
	if err := q.validate(); err != nil {
		return err
	}
	return q.ClockSettings.check()
}

// Suggestion is the clock estimate for one camera.
type Suggestion struct {
	Index int // 1-based position, as in ListResult
	Name  string
	Files int
	camsync.ClockEstimate
}

// SuggestResult is the answer to a SuggestQuery.
type SuggestResult struct {
	Cameras    []Suggestion
	MaxOffset  time.Duration // the search range used
	WallClocks bool          // no zone information; wall clocks were compared
}

// SuggestHandler executes SuggestQuery.
type SuggestHandler struct{}

// Handle scans the scope and estimates the clock offset of every camera.
func (SuggestHandler) Handle(ctx context.Context, q SuggestQuery) (*SuggestResult, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	clocks, cameras, err := estimate(ctx, q.Scope, q.ClockSettings, "")
	if err != nil {
		return nil, err
	}
	out := &SuggestResult{MaxOffset: clocks.MaxOffset, WallClocks: clocks.WallClocks}
	for i, e := range cameras {
		out.Cameras = append(out.Cameras, Suggestion{Index: i + 1, Name: e.Group.Camera, Files: len(e.Group.Photos), ClockEstimate: *e})
	}
	return out, nil
}

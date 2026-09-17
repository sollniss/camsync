package camsync

import (
	"slices"
	"sort"
	"time"
)

// Defaults of TrackOptions.
const (
	// DefaultMaxPositionGap is how far in time a located photo reaches on its
	// own: a few minutes of walking.
	DefaultMaxPositionGap = 5 * time.Minute
	// DefaultMaxBridge is how far apart in time the located photos before
	// and after may be when they are also close in space: someone who
	// photographed the same square twice in a quarter of an hour was there
	// in between.
	DefaultMaxBridge = 15 * time.Minute
	// DefaultBridgeDistance is how close in space those photos must be.
	DefaultBridgeDistance = 1000 // metres
)

// PositionObservation is a photo with a measured position at a known moment.
type PositionObservation struct {
	Instant time.Time // UTC
	Position
	Photo *Photo
}

// Track is the measured positions of a set of photos, the evidence
// PlanGeotag places other photos with, kept as one path per camera: a
// position between two photos is only plausible along the path of one
// device, not between two people photographing in different places.
// Positions camsync estimated are never part of it.
type Track struct {
	paths [][]PositionObservation // per camera, by instant
}

// Track collects the photos of all cameras that have a measured position.
// A photo's moment is its GPS time when it has one, since the position was
// fixed then, and otherwise the moment its camera's clock says (see
// Clocks.Instant), so run shift for clock errors first.
func (c *Clocks) Track() *Track {
	t := &Track{}
	for i := range c.Cameras {
		e := &c.Cameras[i]
		var path []PositionObservation
		for _, p := range e.Group.Photos {
			if p.Position == nil || p.PositionEstimated {
				continue
			}
			instant, ok := time.Time{}, false
			if p.GPSTime != nil {
				instant, ok = *p.GPSTime, true
			} else if !c.WallClocks {
				instant, ok = c.Instant(e, p)
			}
			if ok {
				path = append(path, PositionObservation{Instant: instant, Position: *p.Position, Photo: p})
			}
		}
		if len(path) > 0 {
			slices.SortStableFunc(path, func(a, b PositionObservation) int { return a.Instant.Compare(b.Instant) })
			t.paths = append(t.paths, path)
		}
	}
	return t
}

// Len returns the number of observations.
func (t *Track) Len() int {
	n := 0
	for _, p := range t.paths {
		n += len(p)
	}
	return n
}

// TrackOptions controls Track.At; zero values select the defaults.
type TrackOptions struct {
	MaxGap         time.Duration
	MaxBridge      time.Duration
	BridgeDistance float64 // metres
}

func (o TrackOptions) withDefaults() TrackOptions {
	if o.MaxGap <= 0 {
		o.MaxGap = DefaultMaxPositionGap
	}
	if o.MaxBridge <= 0 {
		o.MaxBridge = DefaultMaxBridge
	}
	if o.BridgeDistance <= 0 {
		o.BridgeDistance = DefaultBridgeDistance
	}
	return o
}

// Fix is an estimated position.
type Fix struct {
	Position
	// Gap is how far in time the estimate reaches: the larger distance to
	// the observations used.
	Gap time.Duration
	// Interpolated reports that the position lies between two observations;
	// otherwise it is copied from the nearest one.
	Interpolated bool
	From         *Photo // the observation before, or the one copied
	To           *Photo // the observation after, when interpolated
}

// At estimates the position at instant from the path of every camera and
// returns the estimate that reaches least far in time. Along one path, the
// position is interpolated between the observation before and the one after
// when both are within MaxGap, or both within MaxBridge and no more than
// BridgeDistance apart; otherwise the nearest observation within MaxGap is
// copied. It reports false when no path has one.
func (t *Track) At(instant time.Time, opts TrackOptions) (Fix, bool) {
	opts = opts.withDefaults()
	var best Fix
	found := false
	for _, path := range t.paths {
		if f, ok := pathAt(path, instant, opts); ok && (!found || f.Gap < best.Gap) {
			best, found = f, true
		}
	}
	return best, found
}

func pathAt(path []PositionObservation, instant time.Time, opts TrackOptions) (Fix, bool) {
	k := sort.Search(len(path), func(i int) bool { return !path[i].Instant.Before(instant) })
	var prev, next *PositionObservation
	if k > 0 {
		prev = &path[k-1]
	}
	if k < len(path) {
		next = &path[k]
	}
	if next != nil && next.Instant.Equal(instant) {
		return Fix{Position: next.Position, From: next.Photo}, true
	}
	if prev != nil && next != nil {
		dp, dn := instant.Sub(prev.Instant), next.Instant.Sub(instant)
		near := dp <= opts.MaxGap && dn <= opts.MaxGap
		bridge := dp <= opts.MaxBridge && dn <= opts.MaxBridge && prev.DistanceTo(next.Position) <= opts.BridgeDistance
		if near || bridge {
			f := float64(dp) / float64(dp+dn)
			return Fix{Position: prev.Towards(next.Position, f), Gap: max(dp, dn), Interpolated: true, From: prev.Photo, To: next.Photo}, true
		}
	}
	var best *PositionObservation
	gap := opts.MaxGap + 1
	for _, o := range []*PositionObservation{prev, next} {
		if o != nil {
			if d := o.Instant.Sub(instant).Abs(); d < gap {
				best, gap = o, d
			}
		}
	}
	if best == nil {
		return Fix{}, false
	}
	return Fix{Position: best.Position, Gap: gap, From: best.Photo}, true
}

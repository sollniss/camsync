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

// PositionObservation is a measured position at a known moment: a photo
// with GPS data, or a point of a GPS track.
type PositionObservation struct {
	Instant time.Time // UTC
	Position
	Source string // the photo's path, or the track file
}

// Track is the measured positions the evidence PlanGeotag places other
// photos with, kept as one path per device: a position between two
// observations is only plausible along the path of one device, not between
// two people photographing in different places. Positions camsync estimated
// are never part of it.
type Track struct {
	paths [][]PositionObservation // per device, by instant
}

// Track collects the photos of all cameras that have a measured position.
// A photo's moment is its GPS time when it has one, since the position was
// fixed then, and otherwise the moment its camera's clock says (see
// Clocks.Instant), so run shift for clock errors first. GPS tracks are
// added with Track.Add.
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
				path = append(path, PositionObservation{Instant: instant, Position: *p.Position, Source: p.Path})
			}
		}
		t.Add(path)
	}
	return t
}

// Add adds the path of one device: its observations, in any order. Empty
// paths are ignored.
func (t *Track) Add(path []PositionObservation) {
	if len(path) == 0 {
		return
	}
	path = slices.Clone(path)
	slices.SortStableFunc(path, func(a, b PositionObservation) int { return a.Instant.Compare(b.Instant) })
	t.paths = append(t.paths, path)
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
	// MaxDistance, if positive, refuses to interpolate between observations
	// further apart than this many metres; the nearest one within MaxGap is
	// copied instead. Zero sets no limit.
	MaxDistance float64
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
	// Distance is how far apart, in metres, the two observations of an
	// interpolated position are: the room the true position had to stray
	// from the line between them. It is zero for a copied position.
	Distance float64
	From     string // source of the observation before, or the one copied
	To       string // source of the observation after, when interpolated
}

// At estimates the position at instant from the path of every device and
// returns the estimate that reaches least far in time. Along one path, the
// position is interpolated between the observation before and the one after
// when both are within MaxGap, or both within MaxBridge and no more than
// BridgeDistance apart, unless they are more than MaxDistance apart;
// otherwise the nearest observation within MaxGap is copied. It reports
// false when no path has one.
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
		return Fix{Position: next.Position, From: next.Source}, true
	}
	if prev != nil && next != nil {
		dp, dn := instant.Sub(prev.Instant), next.Instant.Sub(instant)
		dist := prev.DistanceTo(next.Position)
		near := dp <= opts.MaxGap && dn <= opts.MaxGap
		bridge := dp <= opts.MaxBridge && dn <= opts.MaxBridge && dist <= opts.BridgeDistance
		if (near || bridge) && (opts.MaxDistance <= 0 || dist <= opts.MaxDistance) {
			f := float64(dp) / float64(dp+dn)
			return Fix{Position: prev.Towards(next.Position, f), Gap: max(dp, dn), Interpolated: true, Distance: dist, From: prev.Source, To: next.Source}, true
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
	return Fix{Position: best.Position, Gap: gap, From: best.Source}, true
}

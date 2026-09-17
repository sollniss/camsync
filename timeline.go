package camsync

import (
	"slices"
	"sort"
	"time"
)

// DefaultMaxZoneGap is how far in time an observation of the local zone
// reaches when none is given. Zones change only when travelling, so a phone
// photo from the same day is good evidence.
const DefaultMaxZoneGap = 24 * time.Hour

// ZoneObservation is one piece of evidence for the local zone: a photo from
// a device that follows the local zone, with the zone it recorded or the one
// its GPS time implies.
type ZoneObservation struct {
	Instant time.Time     // when the photo was taken, in UTC
	Zone    time.Duration // local UTC offset at that moment
	Photo   *Photo
}

// Wall returns the local wall clock of the observation.
func (o ZoneObservation) Wall() time.Time { return o.Instant.Add(o.Zone) }

// Timeline is the evidence of the local zone over the course of a trip,
// collected from devices that follow it wherever they are: phones set clock
// and zone from the network. A camera's zone setting is no such evidence;
// a camera left on its owner's home zone says nothing about where it was.
type Timeline struct {
	byInstant []ZoneObservation
	byWall    []ZoneObservation
}

// NewTimeline collects the observations from photos, which should come from
// devices that follow the local zone. A photo counts when its effective
// timestamp records a zone, or when it has a GPS time: a synced clock differs
// from GPS time by the zone alone, which is rounded to 15 minutes to absorb a
// stale GPS fix.
func NewTimeline(photos []*Photo) *Timeline {
	t := &Timeline{}
	for _, p := range photos {
		if o, ok := observe(p); ok {
			t.byInstant = append(t.byInstant, o)
		}
	}
	slices.SortStableFunc(t.byInstant, func(a, b ZoneObservation) int { return a.Instant.Compare(b.Instant) })
	t.byWall = slices.Clone(t.byInstant)
	slices.SortStableFunc(t.byWall, func(a, b ZoneObservation) int { return a.Wall().Compare(b.Wall()) })
	return t
}

func observe(p *Photo) (ZoneObservation, bool) {
	if p.Original == nil {
		return ZoneObservation{}, false
	}
	if z, ok := p.Original.ZoneOffset(); ok {
		return ZoneObservation{Instant: p.Original.T.Add(-z), Zone: z, Photo: p}, true
	}
	if p.GPSTime != nil {
		z := p.Original.T.Sub(*p.GPSTime).Round(15 * time.Minute)
		if z.Abs() <= 14*time.Hour {
			return ZoneObservation{Instant: p.Original.T.Add(-z), Zone: z, Photo: p}, true
		}
	}
	return ZoneObservation{}, false
}

// Len returns the number of observations.
func (t *Timeline) Len() int { return len(t.byInstant) }

// Zones returns the distinct zones observed, in ascending order.
func (t *Timeline) Zones() []time.Duration {
	var zs []time.Duration
	for _, o := range t.byInstant {
		zs = append(zs, o.Zone)
	}
	slices.Sort(zs)
	return slices.Compact(zs)
}

// ZoneAt returns the observation closest in time to instant, if one is
// within maxGap.
func (t *Timeline) ZoneAt(instant time.Time, maxGap time.Duration) (ZoneObservation, bool) {
	return nearest(t.byInstant, instant, maxGap, ZoneObservation.instant)
}

// ZoneAtWall returns the observation whose local wall clock is closest to
// wall, if one is within maxGap. It places a photo from a device that
// follows the local zone but did not record it.
func (t *Timeline) ZoneAtWall(wall time.Time, maxGap time.Duration) (ZoneObservation, bool) {
	return nearest(t.byWall, wall, maxGap, ZoneObservation.Wall)
}

func (o ZoneObservation) instant() time.Time { return o.Instant }

func nearest(obs []ZoneObservation, at time.Time, maxGap time.Duration, key func(ZoneObservation) time.Time) (ZoneObservation, bool) {
	k := sort.Search(len(obs), func(i int) bool { return !key(obs[i]).Before(at) })
	best, gap := ZoneObservation{}, maxGap+1
	for _, i := range []int{k - 1, k} {
		if i >= 0 && i < len(obs) {
			if d := key(obs[i]).Sub(at).Abs(); d < gap {
				best, gap = obs[i], d
			}
		}
	}
	return best, gap <= maxGap
}

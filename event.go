package camsync

import (
	"math"
	"slices"
	"sort"
	"time"
)

// Defaults of EventOptions.
const (
	// DefaultEventGap is the pause in shooting that separates two events.
	// A night's sleep is shorter, a weekend between two trips is longer.
	DefaultEventGap = 24 * time.Hour
	// DefaultEventJump is how far two consecutive located photos may be
	// apart and still belong to the same event. A day's drive is further.
	DefaultEventJump = 50_000.0
	// DefaultHomeRadius is how far from home a photo counts as at home.
	DefaultHomeRadius = 30_000.0
	// DefaultMinTripSpan is how long an event away from home must last to
	// be a trip rather than an outing: anything that runs into the next day.
	DefaultMinTripSpan = 18 * time.Hour
	// DefaultMinTripPhotos is how many photos an event without any location
	// needs, on top of the span, to be taken for a trip.
	DefaultMinTripPhotos = 10
)

// EventOptions controls DetectEvents.
type EventOptions struct {
	// MaxGap is the pause in shooting that ends an event; zero selects
	// DefaultEventGap.
	MaxGap time.Duration
	// MaxJump is the distance between consecutive located photos that ends
	// an event, in metres; zero selects DefaultEventJump.
	MaxJump float64
	// Home is where the library's owner lives. When nil it is inferred as
	// the place with photos on the most days; see InferHome.
	Home *Position
	// HomeRadius is how far from Home a photo still counts as at home, in
	// metres; zero selects DefaultHomeRadius.
	HomeRadius float64
	// MinTripSpan is how long an event away from home must last to be a
	// trip; zero selects DefaultMinTripSpan.
	MinTripSpan time.Duration
	// MinTripPhotos is how many photos an event without a location needs to
	// be a trip; zero selects DefaultMinTripPhotos.
	MinTripPhotos int
}

func (o EventOptions) withDefaults() EventOptions {
	if o.MaxGap <= 0 {
		o.MaxGap = DefaultEventGap
	}
	if o.MaxJump <= 0 {
		o.MaxJump = DefaultEventJump
	}
	if o.HomeRadius <= 0 {
		o.HomeRadius = DefaultHomeRadius
	}
	if o.MinTripSpan <= 0 {
		o.MinTripSpan = DefaultMinTripSpan
	}
	if o.MinTripPhotos <= 0 {
		o.MinTripPhotos = DefaultMinTripPhotos
	}
	return o
}

// EventKind says what an event is to the library's owner.
type EventKind string

const (
	// EventTrip is an event away from home that runs into the next day, or
	// a large multi-day event whose location is unknown.
	EventTrip EventKind = "trip"
	// EventOuting is a shorter event away from home, or a small event whose
	// location is unknown: a day out, a few snaps.
	EventOuting EventKind = "outing"
	// EventHome is an event at home.
	EventHome EventKind = "home"
)

// Event is a run of photos taken at about the same time and place, by any
// number of cameras: a trip, a day out, an afternoon at home. Photos are
// placed by their wall clock, so cameras whose clocks disagree by a few
// hours still share an event, and photos without a date belong to none.
type Event struct {
	Index       int // 1-based position in chronological order
	Photos      []*Photo
	First, Last time.Time // wall clock of the first and last photo
	Cameras     map[string]int
	Located     int      // photos with a position
	Center      Position // mean of the positions; valid when Located > 0
	Radius      float64  // farthest position from Center, in metres
	Away        bool     // most located photos are outside the home radius
	Kind        EventKind
}

// Span is the time between the first and last photo.
func (e *Event) Span() time.Duration { return e.Last.Sub(e.First) }

// Days is the number of calendar days the event touches.
func (e *Event) Days() int {
	first := time.Date(e.First.Year(), e.First.Month(), e.First.Day(), 0, 0, 0, 0, time.UTC)
	last := time.Date(e.Last.Year(), e.Last.Month(), e.Last.Day(), 0, 0, 0, 0, time.UTC)
	return int(last.Sub(first).Hours()/24) + 1
}

// Name renders the event's dates: "2024-05-01" or "2024-05-01 - 2024-05-04".
func (e *Event) Name() string {
	first := e.First.Format(time.DateOnly)
	if last := e.Last.Format(time.DateOnly); last != first {
		return first + " - " + last
	}
	return first
}

// Placed reports whether any photo of the event has a position.
func (e *Event) Placed() bool { return e.Located > 0 }

// InferHome guesses where the owner of the photos lives: the place with
// photos on the most distinct days. Photos taken on holiday come in the
// hundreds but on a few days; those at home come on many. It reports false
// when no photo has a position.
func InferHome(photos []*Photo) (Position, bool) {
	type cell struct {
		days map[string]bool
		sum  Position
		n    int
	}
	cells := map[[2]int]*cell{}
	for _, p := range photos {
		if p.Position == nil || p.Original == nil {
			continue
		}
		key := homeCell(*p.Position)
		c := cells[key]
		if c == nil {
			c = &cell{days: map[string]bool{}}
			cells[key] = c
		}
		c.days[p.Original.T.Format(time.DateOnly)] = true
		c.sum.Lat += p.Position.Lat
		c.sum.Lon += p.Position.Lon
		c.n++
	}
	var best *cell
	var bestKey [2]int
	for key, c := range cells {
		if best == nil || len(c.days) > len(best.days) || len(c.days) == len(best.days) && (c.n > best.n || c.n == best.n && key[0] < bestKey[0]) {
			best, bestKey = c, key
		}
	}
	if best == nil {
		return Position{}, false
	}
	return Position{Lat: best.sum.Lat / float64(best.n), Lon: best.sum.Lon / float64(best.n)}, true
}

// homeCell maps a position to a grid cell of roughly five kilometres.
func homeCell(p Position) [2]int {
	const size = 0.05
	lonSize := size / math.Max(0.1, math.Cos(p.Lat*math.Pi/180))
	return [2]int{int(math.Floor(p.Lat / size)), int(math.Floor(p.Lon / lonSize))}
}

// DetectEvents splits the photos into events. Photos are ordered by wall
// clock and an event ends where the pause until the next photo exceeds
// MaxGap, where the location jumps further than MaxJump, or where the
// photos go from home to away or back. A change of place has to be
// confirmed by the next located photo: a single photo somewhere else, from
// a phone that stayed at home or a stale GPS fix, is ignored. The change
// is placed at the longest pause between the two located photos, which is
// where the travelling was done, so the photos of cameras without GPS taken
// in between fall on the right side. Photos without a date are left out.
// The returned home is the given or inferred one, nil when unknown.
func DetectEvents(photos []*Photo, opts EventOptions) (events []*Event, home *Position) {
	opts = opts.withDefaults()
	home = opts.Home
	if home == nil {
		if h, ok := InferHome(photos); ok {
			home = &h
		}
	}
	atHome := func(p *Photo) bool {
		return home != nil && p.Position != nil && p.Position.DistanceTo(*home) <= opts.HomeRadius
	}
	// samePlace reports whether two located photos belong to one event.
	samePlace := func(a, b *Photo) bool {
		return a.Position.DistanceTo(*b.Position) <= opts.MaxJump && atHome(a) == atHome(b)
	}

	dated := make([]*Photo, 0, len(photos))
	for _, p := range photos {
		if p.Original != nil {
			dated = append(dated, p)
		}
	}
	sort.SliceStable(dated, func(i, j int) bool {
		a, b := dated[i].Original.T, dated[j].Original.T
		if !a.Equal(b) {
			return a.Before(b)
		}
		return dated[i].Path < dated[j].Path
	})
	// nextLocated[i] is the next located photo after i, for confirmation.
	nextLocated := make([]*Photo, len(dated))
	var next *Photo
	for i := len(dated) - 1; i >= 0; i-- {
		nextLocated[i] = next
		if dated[i].Position != nil {
			next = dated[i]
		}
	}

	var runs [][]*Photo
	var cur []*Photo
	var lastLocated *Photo
	for i, p := range dated {
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			split := p.Original.T.Sub(prev.Original.T) > opts.MaxGap
			at := len(cur) // where the new run starts within cur
			if !split && p.Position != nil && lastLocated != nil && !samePlace(p, lastLocated) {
				q := nextLocated[i]
				switch {
				case q != nil && q.Original.T.Sub(p.Original.T) <= opts.MaxGap && samePlace(q, lastLocated):
					// p alone is somewhere else: an outlier, no evidence.
					cur = append(cur, p)
					continue
				default:
					split = true
					at = longestPause(cur, slices.Index(cur, lastLocated), p)
				}
			}
			if split {
				runs = append(runs, cur[:at:at])
				cur = append([]*Photo(nil), cur[at:]...)
				if !slices.Contains(cur, lastLocated) {
					lastLocated = nil // the new event has no position to compare with yet
				}
			}
		}
		cur = append(cur, p)
		if p.Position != nil {
			lastLocated = p
		}
	}
	if len(cur) > 0 {
		runs = append(runs, cur)
	}

	for i, run := range runs {
		e := newEvent(run)
		e.Index = i + 1
		e.classify(home, opts)
		events = append(events, e)
	}
	return events, home
}

func (e *Event) classify(home *Position, opts EventOptions) {
	long := e.Span() >= opts.MinTripSpan
	if e.Located == 0 {
		e.Kind = EventOuting
		if long && len(e.Photos) >= opts.MinTripPhotos {
			e.Kind = EventTrip
		}
		return
	}
	if home == nil {
		e.Away = true
		e.Kind = EventOuting
		if long && len(e.Photos) >= opts.MinTripPhotos {
			e.Kind = EventTrip
		}
		return
	}
	away := 0
	for _, p := range e.Photos {
		if p.Position != nil && p.Position.DistanceTo(*home) > opts.HomeRadius {
			away++
		}
	}
	e.Away = away*2 > e.Located
	switch {
	case !e.Away:
		e.Kind = EventHome
	case long:
		e.Kind = EventTrip
	default:
		e.Kind = EventOuting
	}
}

// longestPause returns where a run should be cut so that the cut falls on
// the longest pause after position from, counting the pause before next,
// the photo that is about to be added: the index of the first photo of the
// new run, len(run) when the pause before next is the longest.
func longestPause(run []*Photo, from int, next *Photo) int {
	at, best := len(run), next.Original.T.Sub(run[len(run)-1].Original.T)
	for i := max(from+1, 1); i < len(run); i++ {
		if gap := run[i].Original.T.Sub(run[i-1].Original.T); gap > best {
			at, best = i, gap
		}
	}
	return at
}
func newEvent(photos []*Photo) *Event {
	e := &Event{Photos: photos, Cameras: map[string]int{}, First: photos[0].Original.T, Last: photos[len(photos)-1].Original.T}
	var lats, lons []float64
	for _, p := range photos {
		e.Cameras[p.Camera]++
		if p.Position != nil {
			e.Located++
			lats, lons = append(lats, p.Position.Lat), append(lons, p.Position.Lon)
		}
	}
	if e.Located == 0 {
		return e
	}
	// The centre is the median position and the radius leaves out the
	// farthest tenth of the photos, so that a lone photo somewhere else, a
	// stale GPS fix or the partner's phone at home, does not stretch the
	// event across the country.
	e.Center = Position{Lat: median(lats), Lon: median(lons)}
	dists := make([]float64, 0, e.Located)
	for _, p := range photos {
		if p.Position != nil {
			dists = append(dists, e.Center.DistanceTo(*p.Position))
		}
	}
	sort.Float64s(dists)
	e.Radius = dists[(len(dists)*9+9)/10-1]
	return e
}

// median returns the middle value of vs, which it sorts.
func median(vs []float64) float64 {
	sort.Float64s(vs)
	n := len(vs)
	if n%2 == 1 {
		return vs[n/2]
	}
	return (vs[n/2-1] + vs[n/2]) / 2
}

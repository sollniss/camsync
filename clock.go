package camsync

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Clock estimation works with trusted reference clocks: phones, whose clocks
// are network-synced, and cameras whose timestamps agree with the GPS time
// recorded in their photos. Every other camera is assumed to be off by a
// constant amount for the whole set of photos, which EstimateClocks finds by
// measuring it against GPS time or by aligning the camera's shooting activity
// with that of the reference cameras: people photograph the same moments, so
// the right shift makes the bursts of both line up.

// Basis says why a camera's clock is trusted or how its offset was found.
type Basis string

// The bases EstimateClocks reports.
const (
	BasisUser     Basis = "user"     // named as a reference by the caller
	BasisPhone    Basis = "phone"    // a phone, whose clock is network-synced
	BasisGPS      Basis = "gps"      // compared with the GPS times in the photos
	BasisActivity Basis = "activity" // shooting activity aligned with the reference cameras
)

// Confidence grades an estimated offset.
type Confidence string

// The confidence levels, best first.
const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

const (
	// DefaultMaxOffset is the largest offset searched when none is given.
	// Much more than half a day makes the previous or next day's activity a
	// plausible match.
	DefaultMaxOffset = 12 * time.Hour
	// GPSTolerance is how far a camera's clock may be from GPS time and still
	// count as a reference.
	GPSTolerance = time.Minute
	// MatchWindow is the distance within which a camera photo counts as
	// matching a reference photo.
	MatchWindow = time.Minute
)

// GPSCheck compares a camera's timestamps with the GPS times in its photos.
type GPSCheck struct {
	Photos int // photos with both a timestamp and a GPS time
	// Error is the median clock error: the camera's wall clock minus the true
	// local time. Shifting by -Error corrects it.
	Error time.Duration
	// Zone is the UTC offset of the local time. When no photo recorded its
	// zone, ZoneInferred is set and Zone is the GPS difference rounded to 15
	// minutes, the granularity of real time zones, so Error is only known
	// within ±7.5 minutes.
	Zone         time.Duration
	ZoneInferred bool
}

// CheckGPS compares the effective timestamps of photos with their GPS times.
// It reports false when no photo has both.
func CheckGPS(photos []*Photo) (GPSCheck, bool) {
	var known, unknown []time.Duration
	zones := map[time.Duration]int{}
	for _, p := range photos {
		if p.Original == nil || p.GPSTime == nil {
			continue
		}
		d := p.Original.T.Sub(*p.GPSTime) // wall clock minus UTC: zone plus error
		if z, ok := p.Original.ZoneOffset(); ok {
			known = append(known, d-z)
			zones[z]++
		} else {
			unknown = append(unknown, d)
		}
	}
	c := GPSCheck{Photos: len(known) + len(unknown)}
	switch {
	case len(known) > 0:
		c.Error = medianDuration(known)
		c.Zone = mostCommonZone(zones).d
	case len(unknown) > 0:
		d := medianDuration(unknown)
		c.Zone = d.Round(15 * time.Minute)
		if c.Zone < -14*time.Hour || c.Zone > 14*time.Hour {
			return GPSCheck{}, false // days off: the zone cannot be told apart from the error
		}
		c.Error, c.ZoneInferred = d-c.Zone, true
	default:
		return GPSCheck{}, false
	}
	return c, true
}

func medianDuration(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// phoneModels maps the lower-cased Make of phone vendors to a pattern for the
// lower-cased Model of their phones; nil means every model is a phone.
// Vendors that also build cameras match their phone models only: Sony's
// Xperia phones, for example, report codes like F3111 or XQ-AT51 rather than
// a name.
var phoneModels = map[string]*regexp.Regexp{
	"apple":          regexp.MustCompile(`^(iphone|ipad)`),
	"google":         regexp.MustCompile(`^pixel`),
	"samsung":        regexp.MustCompile(`^(sm-|gt-|sgh-|sch-|galaxy)`),
	"sony":           regexp.MustCompile(`^(xq-|so-|sov|xperia|[c-j]\d{4}$)`),
	"asus":           regexp.MustCompile(`^(asus_|zenfone)`),
	"microsoft":      regexp.MustCompile(`^lumia`),
	"xiaomi":         nil,
	"redmi":          nil,
	"poco":           nil,
	"oneplus":        nil,
	"huawei":         nil,
	"honor":          nil,
	"oppo":           nil,
	"realme":         nil,
	"vivo":           nil,
	"motorola":       nil,
	"nothing":        nil,
	"fairphone":      nil,
	"hmd global":     nil,
	"nokia":          nil,
	"htc":            nil,
	"lge":            nil,
	"lg electronics": nil,
	"zte":            nil,
	"lenovo":         nil,
	"blackberry":     nil,
	"meizu":          nil,
}

// IsPhone reports whether Make and Model name a phone or tablet.
func IsPhone(mk, model string) bool {
	re, ok := phoneModels[strings.ToLower(strings.TrimSpace(mk))]
	return ok && (re == nil || re.MatchString(strings.ToLower(strings.TrimSpace(model))))
}

// ClockOptions controls EstimateClocks.
type ClockOptions struct {
	// References are the cameras whose clocks are trusted. When empty, phones
	// and cameras whose timestamps agree with GPS time are used.
	References []*Group
	// ClockZones sets the zone a camera's clock runs on, overriding what
	// EstimateClocks would work out.
	ClockZones map[*Group]time.Duration
	// MaxOffset is the largest clock error searched by activity alignment;
	// zero selects DefaultMaxOffset.
	MaxOffset time.Duration
	// MaxZoneGap is how far in time an observation of the local zone reaches;
	// zero selects DefaultMaxZoneGap.
	MaxZoneGap time.Duration
}

// ZoneSource says how a camera's clock zone was determined.
type ZoneSource string

// The sources of a ClockZone.
const (
	ZoneFromUser     ZoneSource = "user"     // set by the caller
	ZoneFromPhotos   ZoneSource = "photos"   // recorded in the camera's photos
	ZoneFromGPS      ZoneSource = "gps"      // the difference between its clock and GPS time
	ZoneFromActivity ZoneSource = "activity" // the offset that aligns it with the references
	ZoneLocal        ZoneSource = "local"    // it follows the local zone, like a phone
)

// ClockZone is the zone a camera's clock runs on. Photos that record a zone
// use their own; this one places the photos that do not.
type ClockZone struct {
	Zone   time.Duration // meaningful when Source is neither "" nor ZoneLocal
	Source ZoneSource    // "" when unknown
}

// Known reports whether the zone is known, local included.
func (z ClockZone) Known() bool { return z.Source != "" }

// String renders the zone as "+09:00", "local" or "unknown".
func (z ClockZone) String() string {
	switch z.Source {
	case "":
		return "unknown"
	case ZoneLocal:
		return "local"
	}
	return FormatZone(z.Zone)
}

// ClockEstimate is the verdict on one camera's clock.
type ClockEstimate struct {
	Group *Group
	// Reference is set for trusted cameras, which the others are aligned to.
	Reference bool
	// Basis says why a reference is trusted or how Offset was found.
	Basis Basis
	// GPS holds the comparison with the photos' GPS times, if they have any.
	GPS *GPSCheck
	// ClockZone is the zone the camera's clock runs on.
	ClockZone ClockZone

	// Found reports that Offset is an estimate. Offset is the amount to add
	// to the camera's timestamps, on top of any shift already applied.
	Found      bool
	Offset     time.Duration
	Matched    int // photos that support the estimate
	Confidence Confidence
	Note       string // why nothing was found
}

// The notes EstimateClocks gives when it cannot estimate a clock. The
// remaining note, "no reference photos within ...", carries the search range.
const (
	NoteNoTimestamps         = "no timestamps"
	NoteNoReference          = "no reference camera"
	NoteReferenceZoneUnknown = "reference photos have no time zone" // set a clock zone for them
)

// Clocks is the result of EstimateClocks: a verdict per camera and the
// evidence of the local zone.
type Clocks struct {
	Cameras  []ClockEstimate // in the order of the groups
	Timeline *Timeline       // local zone observations from phones
	// WallClocks is set when no photo carries any zone information, so
	// timestamps could only be compared as wall clocks.
	WallClocks bool
	// MaxOffset and MaxZoneGap are the settings used, defaults filled in.
	MaxOffset  time.Duration
	MaxZoneGap time.Duration
}

// Estimate returns the estimate for g, or nil.
func (c *Clocks) Estimate(g *Group) *ClockEstimate {
	for i := range c.Cameras {
		if c.Cameras[i].Group == g {
			return &c.Cameras[i]
		}
	}
	return nil
}

// Instant returns when a photo of the camera e was taken, in UTC, using the
// photo's own zone, else the camera's clock zone. Photos of a camera that
// follows the local zone are placed on the timeline by their wall clock. It
// reports false when the zone cannot be told. With WallClocks the wall clock
// is returned as is.
func (c *Clocks) Instant(e *ClockEstimate, p *Photo) (time.Time, bool) {
	if p.Original == nil {
		return time.Time{}, false
	}
	t := p.Original.T
	if c.WallClocks {
		return t, true
	}
	if z, ok := p.Original.ZoneOffset(); ok {
		return t.Add(-z), true
	}
	switch e.ClockZone.Source {
	case "":
		return time.Time{}, false
	case ZoneLocal:
		o, ok := c.Timeline.ZoneAtWall(t, c.MaxZoneGap)
		return t.Add(-o.Zone), ok
	}
	return t.Add(-e.ClockZone.Zone), true
}

// instants returns the instants of the camera's photos that have one.
func (c *Clocks) instants(e *ClockEstimate) []time.Time {
	var ts []time.Time
	for _, p := range e.Group.Photos {
		if t, ok := c.Instant(e, p); ok {
			ts = append(ts, t)
		}
	}
	return ts
}

// EstimateClocks judges the clock of every group, in the order given.
// References are trusted as they are. A camera with GPS times and a known
// zone is measured against GPS time; any other camera is aligned with the
// photos of all references together (see Align). Offsets are measured
// against effective timestamps, so a camera already shifted correctly gets
// an offset near zero.
//
// Photos are compared as moments in time, the way zone-aware readers such as
// Immich order them, so a camera left on its owner's home zone is not off by
// the zone difference. That needs every camera's clock zone (see
// ClockEstimate.ClockZone), which comes, in this order, from
// ClockOptions.ClockZones, from phones following the local zone, from the
// zone most of the camera's photos record, or from its GPS times. A camera
// without any of these is aligned with its wall clock read as UTC; the
// offset found then splits into the clock zone, the nearest whole hour or
// zone seen elsewhere, and the clock error. A reference camera without a
// zone is taken to follow the local zone. When no photo carries any zone
// information at all, wall clocks are compared (see Clocks.WallClocks).
func EstimateClocks(groups []*Group, opts ClockOptions) *Clocks {
	c := &Clocks{Cameras: make([]ClockEstimate, len(groups)), WallClocks: len(opts.ClockZones) == 0, MaxOffset: opts.MaxOffset, MaxZoneGap: opts.MaxZoneGap}
	if c.MaxOffset <= 0 {
		c.MaxOffset = DefaultMaxOffset
	}
	if c.MaxZoneGap <= 0 {
		c.MaxZoneGap = DefaultMaxZoneGap
	}
	var phones []*Photo
	for i, g := range groups {
		e := ClockEstimate{Group: g}
		if gc, ok := CheckGPS(g.Photos); ok {
			e.GPS = &gc
		}
		phone := isPhoneGroup(g)
		switch {
		case len(opts.References) > 0:
			if slices.Contains(opts.References, g) {
				e.Reference, e.Basis = true, BasisUser
			}
		case phone:
			e.Reference, e.Basis = true, BasisPhone
		case e.GPS != nil && e.GPS.Error.Abs() <= GPSTolerance:
			e.Reference, e.Basis = true, BasisGPS
		}
		if phone {
			phones = append(phones, g.Photos...)
		}
		recorded := photosZone(g.Photos)
		if z, ok := opts.ClockZones[g]; ok {
			e.ClockZone = ClockZone{z, ZoneFromUser}
		} else if phone {
			e.ClockZone = ClockZone{Source: ZoneLocal}
		} else if recorded.ok {
			e.ClockZone = ClockZone{recorded.d, ZoneFromPhotos}
		} else if e.GPS != nil {
			e.ClockZone = ClockZone{e.GPS.Zone, ZoneFromGPS}
		}
		if recorded.ok || e.GPS != nil {
			c.WallClocks = false
		}
		c.Cameras[i] = e
	}
	c.Timeline = NewTimeline(phones)
	if c.WallClocks {
		for i := range c.Cameras {
			c.Cameras[i].ClockZone = ClockZone{}
		}
	}

	var ref []time.Time
	for i := range c.Cameras {
		e := &c.Cameras[i]
		if !e.Reference {
			continue
		}
		if !e.ClockZone.Known() && !c.WallClocks {
			e.ClockZone = ClockZone{Source: ZoneLocal}
		}
		ref = append(ref, c.instants(e)...)
	}

	// The cameras are independent of each other from here on: each judgement
	// reads the references and the timeline and writes its own estimate, so
	// the alignment searches, the expensive part, run side by side.
	hasReference := len(ref) > 0 || slices.ContainsFunc(c.Cameras, func(r ClockEstimate) bool { return r.Reference })
	var wg sync.WaitGroup
	for i := range c.Cameras {
		if e := &c.Cameras[i]; !e.Reference {
			wg.Go(func() { c.judge(e, ref, hasReference) })
		}
	}
	wg.Wait()
	return c
}

// judge estimates the clock of the camera e against the reference instants;
// hasReference says whether any camera is a reference, even one whose
// photos gave no instants.
func (c *Clocks) judge(e *ClockEstimate, ref []time.Time, hasReference bool) {
	dated := slices.ContainsFunc(e.Group.Photos, func(p *Photo) bool { return p.Original != nil })
	switch {
	case !dated:
		e.Note = NoteNoTimestamps
	case e.GPS != nil && !e.GPS.ZoneInferred:
		// Measured directly; better than any alignment.
		e.Found, e.Basis, e.Offset, e.Matched = true, BasisGPS, -e.GPS.Error.Round(time.Second), e.GPS.Photos
		e.Confidence = ConfidenceMedium
		if e.GPS.Photos >= 3 {
			e.Confidence = ConfidenceHigh
		}
	case len(ref) == 0 && hasReference:
		e.Note = NoteReferenceZoneUnknown
	case len(ref) == 0:
		e.Note = NoteNoReference
	case e.ClockZone.Known() || c.WallClocks:
		a, ok := Align(ref, c.instants(e), c.MaxOffset)
		if !ok {
			e.Note = "no reference photos within " + FormatSpan(c.MaxOffset)
			break
		}
		e.Found, e.Basis, e.Offset, e.Matched, e.Confidence = true, BasisActivity, a.Offset, a.Matched, a.Confidence
	default:
		// Read the wall clock as UTC: the offset found is the clock
		// zone, negated, plus the clock error.
		var walls []time.Time
		for _, p := range e.Group.Photos {
			if p.Original != nil {
				walls = append(walls, p.Original.T)
			}
		}
		search := max(c.MaxOffset, 14*time.Hour)
		a, ok := Align(ref, walls, search)
		if !ok {
			e.Note = "no reference photos within " + FormatSpan(search)
			break
		}
		z := nearestZone(-a.Offset, c.Timeline.Zones())
		e.ClockZone = ClockZone{z, ZoneFromActivity}
		e.Found, e.Basis, e.Offset, e.Matched, e.Confidence = true, BasisActivity, a.Offset+z, a.Matched, a.Confidence
	}
}

// nearestZone returns the whole hour or one of the known zones nearest to d.
// Clock errors are minutes while zones are hours apart, so an offset of
// +2h03m is the +02:00 zone with a clock three minutes fast.
func nearestZone(d time.Duration, known []time.Duration) time.Duration {
	best := d.Round(time.Hour)
	for _, z := range known {
		if (d - z).Abs() < (d - best).Abs() {
			best = z
		}
	}
	return best
}

// isPhoneGroup reports whether most photos of g come from a phone. Groups
// are keyed by display name, so all photos normally agree.
func isPhoneGroup(g *Group) bool {
	n := 0
	for _, p := range g.Photos {
		if IsPhone(p.Make, p.Model) {
			n++
		}
	}
	return n > 0 && 2*n >= len(g.Photos)
}

// zone is a UTC offset that may be unknown.
type zone struct {
	d  time.Duration
	ok bool
}

// photosZone returns the zone most of the dated photos record.
func photosZone(photos []*Photo) zone {
	counts := map[time.Duration]int{}
	for _, p := range photos {
		if p.Original != nil {
			if z, ok := p.Original.ZoneOffset(); ok {
				counts[z]++
			}
		}
	}
	return mostCommonZone(counts)
}

// mostCommonZone returns the zone with the highest count, the smaller offset
// on ties.
func mostCommonZone(counts map[time.Duration]int) zone {
	var best zone
	top := 0
	for z, n := range counts {
		if n > top || (n == top && z < best.d) {
			best, top = zone{z, true}, n
		}
	}
	return best
}

// Alignment is the offset of one camera's clock found by Align.
type Alignment struct {
	Offset     time.Duration // amount to add to the camera's timestamps
	Matched    int           // camera photos within MatchWindow of a reference photo after the shift
	Confidence Confidence
}

const (
	alignStep  = 30 * time.Second // grid of the coarse search
	alignWidth = 5 * time.Minute  // tolerance of the coarse search
	burstGap   = time.Minute      // photos closer than this form one burst
)

// Align finds the shift of the camera times cam, within ±maxOffset, that
// best lines them up with the reference times ref. It reports false when no
// shift brings any camera photo near a reference photo.
//
// The coarse search scores every shift on a 30 second grid: each camera
// photo earns up to one point, falling off linearly to zero at five minutes
// from the nearest reference photo. A burst of photos shares one point, so
// a single frantic moment does not outweigh a day of ordinary shots. The
// best shift is then refined on the distances to the nearest reference
// photos, where photos of the very same moment form a tight cluster.
//
// Confidence compares the best score with the strongest rival peak, both
// measured above the average score, which is what unrelated photos reach.
func Align(ref, cam []time.Time, maxOffset time.Duration) (Alignment, bool) {
	if len(ref) == 0 || len(cam) == 0 {
		return Alignment{}, false
	}
	base := ref[0]
	secs := func(ts []time.Time) []float64 {
		out := make([]float64, len(ts))
		for i, t := range ts {
			out[i] = t.Sub(base).Seconds()
		}
		slices.Sort(out)
		return out
	}
	r, c := secs(ref), secs(cam)
	w := burstWeights(c, burstGap.Seconds())

	// residual returns the signed distance from x to the nearest reference.
	residual := func(x float64) float64 {
		k := sort.SearchFloat64s(r, x)
		best := math.Inf(1)
		if k < len(r) {
			best = r[k] - x
		}
		if k > 0 && x-r[k-1] < math.Abs(best) {
			best = r[k-1] - x
		}
		return best
	}
	score := func(shift, width float64) float64 {
		s := 0.0
		for i, x := range c {
			if d := math.Abs(residual(x + shift)); d < width {
				s += w[i] * (1 - d/width)
			}
		}
		return s
	}

	step, width, limit := alignStep.Seconds(), alignWidth.Seconds(), maxOffset.Seconds()
	n := int(limit / step)
	scores := make([]float64, 2*n+1)
	best, mean := n, 0.0
	for i := range scores {
		scores[i] = score(float64(i-n)*step, width)
		mean += scores[i]
		if scores[i] > scores[best] || (scores[i] == scores[best] && abs(i-n) < abs(best-n)) {
			best = i
		}
	}
	mean /= float64(len(scores))
	if scores[best] == 0 {
		return Alignment{}, false
	}
	shift := float64(best-n) * step
	for _, win := range []float64{120, 30} {
		shift += refine(c, w, residual, shift, win)
	}
	shift = math.Round(shift)

	a := Alignment{Offset: time.Duration(shift) * time.Second}
	for _, x := range c {
		if math.Abs(residual(x+shift)) <= MatchWindow.Seconds() {
			a.Matched++
		}
	}
	// A rival is another peak that the scores fall well below on the way from
	// the best one, so the shoulders of the best peak do not count: long,
	// busy sessions make shifts of ten or twenty minutes score nearly as well
	// as the right one without being a different explanation.
	lead := scores[best] - mean
	rival := 0.0
	for _, dir := range []int{-1, 1} {
		valley := scores[best]
		for i := best + dir; i >= 0 && i < len(scores); i += dir {
			valley = min(valley, scores[i])
			if valley <= mean+(scores[i]-mean)/2 {
				rival = max(rival, scores[i])
			}
		}
	}
	ratio := max(rival-mean, 0) / lead
	// Calibrated on simulated trips: no unrelated set of photos reached the
	// high band, and the few wrong offsets there were off by minutes.
	switch {
	case lead >= 3 && ratio <= 0.4:
		a.Confidence = ConfidenceHigh
	case lead >= 2 && ratio <= 0.6:
		a.Confidence = ConfidenceMedium
	default:
		a.Confidence = ConfidenceLow
	}
	return a, true
}

// refine returns the correction to shift suggested by the camera photos that
// are within win seconds of a reference photo: the weighted median of the
// residuals around their densest point.
func refine(c, w []float64, residual func(float64) float64, shift, win float64) float64 {
	type res struct{ d, w float64 }
	var rs []res
	for i, x := range c {
		if d := residual(x + shift); math.Abs(d) <= win {
			rs = append(rs, res{d, w[i]})
		}
	}
	if len(rs) == 0 {
		return 0
	}
	band := win / 4
	mode, top := 0.0, -1.0
	for _, a := range rs {
		density := 0.0
		for _, b := range rs {
			if d := math.Abs(a.d - b.d); d < band {
				density += b.w * (1 - d/band)
			}
		}
		if density > top {
			mode, top = a.d, density
		}
	}
	var near []res
	total := 0.0
	for _, a := range rs {
		if math.Abs(a.d-mode) <= band {
			near = append(near, a)
			total += a.w
		}
	}
	slices.SortFunc(near, func(a, b res) int { return cmp.Compare(a.d, b.d) })
	acc := 0.0
	for _, a := range near {
		acc += a.w
		if acc >= total/2 {
			return a.d
		}
	}
	return mode
}

// burstWeights gives each of the sorted times the weight 1/len(burst), where
// a burst is a run of times less than gap seconds apart.
func burstWeights(ts []float64, gap float64) []float64 {
	w := make([]float64, len(ts))
	for start := 0; start < len(ts); {
		end := start + 1
		for end < len(ts) && ts[end]-ts[end-1] < gap {
			end++
		}
		for i := start; i < end; i++ {
			w[i] = 1 / float64(end-start)
		}
		start = end
	}
	return w
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

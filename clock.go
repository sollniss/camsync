package camsync

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
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
	// MaxOffset is the largest offset searched by activity alignment; zero
	// selects DefaultMaxOffset.
	MaxOffset time.Duration
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
	// Zone is the camera's UTC offset ("+02:00"): the one most of its photos
	// record, else the one its GPS times imply, else "".
	Zone string

	// Found reports that Offset is an estimate. Offset is the amount to add
	// to the camera's timestamps, on top of any shift already applied.
	Found      bool
	Offset     time.Duration
	Matched    int // photos that support the estimate
	Confidence Confidence
	Note       string // why nothing was found
}

// zone is a UTC offset that may be unknown.
type zone struct {
	d  time.Duration
	ok bool
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
// the zone difference. A photo without a zone is taken to be in its camera's
// zone (see ClockEstimate.Zone), else in the zone of the references, else in
// the zone of the camera being estimated. Without any zone at all, wall
// clocks are compared.
func EstimateClocks(groups []*Group, opts ClockOptions) []ClockEstimate {
	maxOffset := opts.MaxOffset
	if maxOffset <= 0 {
		maxOffset = DefaultMaxOffset
	}
	out := make([]ClockEstimate, len(groups))
	zones := make([]zone, len(groups))
	refZones := map[time.Duration]int{}
	for i, g := range groups {
		e := ClockEstimate{Group: g}
		if c, ok := CheckGPS(g.Photos); ok {
			e.GPS = &c
		}
		switch {
		case len(opts.References) > 0:
			if slices.Contains(opts.References, g) {
				e.Reference, e.Basis = true, BasisUser
			}
		case isPhoneGroup(g):
			e.Reference, e.Basis = true, BasisPhone
		case e.GPS != nil && e.GPS.Error.Abs() <= GPSTolerance:
			e.Reference, e.Basis = true, BasisGPS
		}
		zones[i] = photosZone(g.Photos)
		if !zones[i].ok && e.GPS != nil {
			zones[i] = zone{e.GPS.Zone, true}
		}
		if zones[i].ok {
			e.Zone = FormatZone(zones[i].d)
			if e.Reference {
				refZones[zones[i].d] += len(g.Photos)
			}
		}
		out[i] = e
	}
	refZone := mostCommonZone(refZones)

	for i := range out {
		e := &out[i]
		if e.Reference {
			continue
		}
		fallback := refZone
		if !fallback.ok {
			fallback = zones[i]
		}
		var ref []time.Time
		for j, r := range out {
			if r.Reference {
				ref = append(ref, times(r.Group.Photos, zones[j], fallback)...)
			}
		}
		cam := times(e.Group.Photos, zones[i], fallback)
		switch {
		case len(cam) == 0:
			e.Note = "no timestamps"
		case e.GPS != nil && !e.GPS.ZoneInferred:
			// Measured directly; better than any alignment.
			e.Found, e.Basis, e.Offset, e.Matched = true, BasisGPS, -e.GPS.Error.Round(time.Second), e.GPS.Photos
			e.Confidence = ConfidenceMedium
			if e.GPS.Photos >= 3 {
				e.Confidence = ConfidenceHigh
			}
		case len(ref) == 0:
			e.Note = "no reference camera"
		default:
			a, ok := Align(ref, cam, maxOffset)
			if !ok {
				e.Note = "no reference photos within " + strings.TrimPrefix(FormatDuration(maxOffset), "+")
				break
			}
			e.Found, e.Basis, e.Offset, e.Matched, e.Confidence = true, BasisActivity, a.Offset, a.Matched, a.Confidence
		}
	}
	return out
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

// times returns the effective timestamps of the dated photos as moments in
// time, using the photo's own zone, else groupZone, else fallback. With no
// zone at all the wall clock is used as is.
func times(photos []*Photo, groupZone, fallback zone) []time.Time {
	var ts []time.Time
	for _, p := range photos {
		if p.Original == nil {
			continue
		}
		t := p.Original.T
		if z, ok := p.Original.ZoneOffset(); ok {
			t = t.Add(-z)
		} else if groupZone.ok {
			t = t.Add(-groupZone.d)
		} else if fallback.ok {
			t = t.Add(-fallback.d)
		}
		ts = append(ts, t)
	}
	return ts
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
	alignStep   = 30 * time.Second // grid of the coarse search
	alignWidth  = 5 * time.Minute  // tolerance of the coarse search
	burstGap    = time.Minute      // photos closer than this form one burst
	peakSpacing = 3 * alignWidth   // minimum distance of a rival peak
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

package camsync

import (
	"errors"
	"fmt"
	"time"
)

// The errors the planners return for photos they cannot place. Errors that
// add detail wrap them, so match with errors.Is.
var (
	// ErrUnchanged reports that a photo needs no change.
	ErrUnchanged = errors.New("nothing to change")
	// ErrClockZoneUnknown reports that the zone a camera's clock runs on is
	// not known, so the moment of the photo cannot be told. Setting the
	// camera's clock zone (ClockOptions.ClockZones) resolves it.
	ErrClockZoneUnknown = errors.New("clock zone unknown")
	// ErrClockZoneGuessed reports that the clock zone was only guessed with
	// low confidence, which is not enough to rewrite the photo.
	ErrClockZoneGuessed = errors.New("clock zone guessed with low confidence")
	// ErrNoLocalZone reports that the local zone at the moment of the photo
	// is not known. LocalizeOptions.Zone resolves it.
	ErrNoLocalZone = errors.New("no local zone known")
)

// unchanged is an ErrUnchanged with a reason.
type unchanged string

func (u unchanged) Error() string        { return string(u) }
func (u unchanged) Is(target error) bool { return target == ErrUnchanged }

// LocalizeOptions controls PlanLocalize.
type LocalizeOptions struct {
	// Zone, if set, is the local zone used where the timeline has no
	// observation within reach.
	Zone *time.Duration
}

// PlanLocalize plans rewriting the timestamps of a photo of camera e in the
// local zone of the place it was taken: the moment stays the same, but the
// wall clock and zone become those of the nearest observation on the
// timeline, or opts.Zone. Zone-aware readers such as Immich then show and
// group the photo by local time rather than by the camera's clock zone or
// the viewer's browser.
//
// Photos already written in their local zone return an error matching
// ErrUnchanged; photos whose moment or local zone cannot be told return an
// error saying why (see ErrClockZoneUnknown and ErrNoLocalZone).
func (c *Clocks) PlanLocalize(e *ClockEstimate, p *Photo, opts LocalizeOptions) (*Change, error) {
	instant, err := c.photoInstant(e, p)
	if err != nil {
		return nil, err
	}
	var local time.Duration
	if o, ok := c.Timeline.ZoneAt(instant, c.MaxZoneGap); ok {
		local = o.Zone
	} else if opts.Zone != nil {
		local = *opts.Zone
	} else {
		return nil, fmt.Errorf("%w within %s", ErrNoLocalZone, FormatSpan(c.MaxZoneGap))
	}

	tz := FormatZone(local)
	orig := *p.Original
	orig.T, orig.TZ = instant.Add(local), tz
	if orig.Equal(*p.Original) {
		return nil, unchanged("already in local time")
	}
	var dig *Stamp
	if p.Digitized != nil {
		d := p.Digitized.Add(orig.T.Sub(p.Original.T))
		d.TZ = tz
		dig = &d
	}
	return newChange(p, orig, dig), nil
}

// photoInstant is Instant for planning a change: it explains why a photo's
// moment cannot be told, and refuses clock zones guessed with low
// confidence.
func (c *Clocks) photoInstant(e *ClockEstimate, p *Photo) (time.Time, error) {
	if p.Err != nil {
		return time.Time{}, p.Err
	}
	if p.Original == nil {
		return time.Time{}, errors.New("no timestamp found")
	}
	if c.WallClocks {
		return time.Time{}, fmt.Errorf("%w: no photo records a time zone", ErrClockZoneUnknown)
	}
	if _, recorded := p.Original.ZoneOffset(); !recorded && e.ClockZone.Source == ZoneFromActivity && e.Confidence == ConfidenceLow {
		return time.Time{}, ErrClockZoneGuessed
	}
	instant, ok := c.Instant(e, p)
	if !ok {
		if e.ClockZone.Source == ZoneLocal {
			return time.Time{}, fmt.Errorf("%w: no phone photo within %s tells the local zone it follows", ErrClockZoneUnknown, FormatSpan(c.MaxZoneGap))
		}
		return time.Time{}, ErrClockZoneUnknown
	}
	return instant, nil
}

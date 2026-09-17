package camsync

import (
	"errors"
	"strings"
	"time"
)

// ErrUnchanged reports that a photo needs no change. Errors saying why, such
// as "already in local time", match it with errors.Is.
var ErrUnchanged = errors.New("nothing to change")

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
// error saying why.
func (c *Clocks) PlanLocalize(e *ClockEstimate, p *Photo, opts LocalizeOptions) (*Change, error) {
	instant, err := c.photoInstant(e, p)
	if err != nil {
		return nil, err
	}
	var local time.Duration
	if o, ok := c.Timeline.ZoneAt(instant, c.maxZoneGap); ok {
		local = o.Zone
	} else if opts.Zone != nil {
		local = *opts.Zone
	} else {
		return nil, errors.New("no local zone known within " + gapText(c.maxZoneGap) + "; set one with --zone")
	}

	tz := FormatZone(local)
	orig := *p.Original
	orig.T, orig.TZ = instant.Add(local), tz
	if orig == *p.Original {
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
		return time.Time{}, errors.New("no photo records a time zone; the moment cannot be told")
	}
	if _, recorded := p.Original.ZoneOffset(); !recorded && e.ClockZone.Source == ZoneFromActivity && e.Confidence == ConfidenceLow {
		return time.Time{}, errors.New("clock zone guessed with low confidence; set one with --clock-zone")
	}
	instant, ok := c.Instant(e, p)
	if !ok {
		if e.ClockZone.Source == ZoneLocal {
			return time.Time{}, errors.New("no local zone known within " + gapText(c.maxZoneGap) + " of its time")
		}
		return time.Time{}, errors.New("clock zone unknown; set one with --clock-zone")
	}
	return instant, nil
}

// gapText renders a duration without sign, "0s" for zero.
func gapText(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	return strings.TrimPrefix(FormatDuration(d.Abs()), "+")
}

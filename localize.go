package camsync

import (
	"errors"
	"strings"
	"time"
)

// ErrUnchanged reports that a photo needs no change.
var ErrUnchanged = errors.New("already in local time")

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
// Photos already written in their local zone return ErrUnchanged; photos
// whose moment or local zone cannot be told return an error saying why.
func (c *Clocks) PlanLocalize(e *ClockEstimate, p *Photo, opts LocalizeOptions) (*Change, error) {
	if p.Err != nil {
		return nil, p.Err
	}
	if p.Original == nil {
		return nil, errors.New("no timestamp found")
	}
	if c.WallClocks {
		return nil, errors.New("no photo records a time zone; the moment cannot be told")
	}
	if _, recorded := p.Original.ZoneOffset(); !recorded && e.ClockZone.Source == ZoneFromActivity && e.Confidence == ConfidenceLow {
		return nil, errors.New("clock zone guessed with low confidence; set one with --clock-zone")
	}
	instant, ok := c.Instant(e, p)
	if !ok {
		if e.ClockZone.Source == ZoneLocal {
			return nil, errors.New("no local zone known within " + gapText(c.maxZoneGap) + " of its time")
		}
		return nil, errors.New("clock zone unknown; set one with --clock-zone")
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
		return nil, ErrUnchanged
	}
	var dig *Stamp
	if p.Digitized != nil {
		d := p.Digitized.Add(orig.T.Sub(p.Original.T))
		d.TZ = tz
		dig = &d
	}
	return newChange(p, orig, dig), nil
}

func gapText(d time.Duration) string { return strings.TrimPrefix(FormatDuration(d), "+") }

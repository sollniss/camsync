package camsync

import (
	"errors"
	"strings"

	"github.com/sollniss/camsync/xmp"
)

// GeotagMethod is the GPSProcessingMethod camsync writes with an estimated
// position, which tells its estimates apart from measured positions.
const GeotagMethod = "camsync"

// geotagFields are the sidecar properties PlanGeotag writes.
var geotagFields = []xmp.Field{xmp.GPSLatitude, xmp.GPSLongitude, xmp.GPSProcessingMethod}

// ErrHasPosition reports that a photo already has a measured position.
var ErrHasPosition = errors.New("has a location")

// PlanGeotag plans writing the position track estimates for a photo of
// camera e (see Track.At) to its sidecar, marked with GeotagMethod. Photos
// with a measured position return ErrHasPosition; a previous estimate is
// replaced when the track now says otherwise and returns an error matching
// ErrUnchanged when it does not.
func (c *Clocks) PlanGeotag(e *ClockEstimate, p *Photo, track *Track, opts TrackOptions) (*Change, error) {
	if p.Position != nil && !p.PositionEstimated {
		return nil, ErrHasPosition
	}
	instant, err := c.photoInstant(e, p)
	if err != nil {
		return nil, err
	}
	fix, ok := track.At(instant, opts)
	if !ok {
		o := opts.withDefaults()
		return nil, errors.New("no located photo within " + FormatSpan(max(o.MaxGap, o.MaxBridge)))
	}
	if p.Position != nil {
		lat, lon := p.Position.XMP()
		if nl, nn := fix.XMP(); lat == nl && lon == nn {
			return nil, unchanged("already geotagged")
		}
	}
	lat, lon := fix.XMP()
	fields := xmp.Fields{xmp.GPSLatitude: lat, xmp.GPSLongitude: lon, xmp.GPSProcessingMethod: GeotagMethod}
	addCameraFields(p, fields)
	return &Change{Photo: p, Fields: fields, Fix: &fix}, nil
}

// String describes the fix for people: "54.4250000, 13.4350000 (between
// IMG_0001.JPG and IMG_0002.JPG, 2m, 130m apart)", or "(along track.gpx,
// 4s, 12m apart)" for a position on a GPS track.
func (f Fix) String() string {
	var b strings.Builder
	b.WriteString(f.Position.String())
	b.WriteString(" (")
	switch {
	case f.Interpolated && f.From == f.To:
		b.WriteString("along ")
		b.WriteString(baseName(f.From))
	case f.Interpolated:
		b.WriteString("between ")
		b.WriteString(baseName(f.From))
		b.WriteString(" and ")
		b.WriteString(baseName(f.To))
	default:
		b.WriteString("from ")
		b.WriteString(baseName(f.From))
	}
	b.WriteString(", ")
	b.WriteString(FormatSpan(f.Gap))
	if f.Interpolated {
		b.WriteString(", ")
		b.WriteString(FormatDistance(f.Distance))
		b.WriteString(" apart")
	}
	b.WriteByte(')')
	return b.String()
}

// baseName returns the last element of a path in either slash convention.
func baseName(path string) string {
	if path == "" {
		return "?"
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

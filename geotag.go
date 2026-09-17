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
		reach := o.MaxGap
		if o.MaxBridge > reach {
			reach = o.MaxBridge
		}
		return nil, errors.New("no located photo within " + gapText(reach))
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
// IMG_0001.JPG and IMG_0002.JPG, 2m)".
func (f Fix) String() string {
	var b strings.Builder
	b.WriteString(f.Position.String())
	b.WriteString(" (")
	if f.Interpolated {
		b.WriteString("between ")
		b.WriteString(photoName(f.From))
		b.WriteString(" and ")
		b.WriteString(photoName(f.To))
	} else {
		b.WriteString("from ")
		b.WriteString(photoName(f.From))
	}
	b.WriteString(", ")
	b.WriteString(gapText(f.Gap))
	b.WriteByte(')')
	return b.String()
}

func photoName(p *Photo) string {
	if p == nil {
		return "?"
	}
	if i := strings.LastIndexAny(p.Path, `/\`); i >= 0 {
		return p.Path[i+1:]
	}
	return p.Path
}

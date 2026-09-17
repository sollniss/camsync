package camsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sollniss/camsync/xmp"
)

// Change is a planned sidecar update for one photo.
type Change struct {
	Photo  *Photo
	Fields xmp.Fields  // what will be written or replaced in the sidecar
	Remove []xmp.Field // what will be removed from an existing sidecar
	// NewOriginal is the timestamp the photo will have afterwards; zero when
	// the change leaves the timestamps alone.
	NewOriginal Stamp
	// Fix is the position written by a geotag change.
	Fix *Fix
}

// Shift describes how to move a photo's timestamps.
type Shift struct {
	// By is the wall-clock amount to add. It may be zero when only Zone is set.
	By time.Duration
	// Zone, if not empty, is the UTC offset ("+02:00", "Z") stamped on the
	// written dates, replacing whatever the source has. Use it for cameras
	// that record no zone at all: readers such as Immich treat a zone-less
	// sidecar date as UTC. Normalise user input with ParseZone.
	Zone string
}

// PlanShift plans moving the photo's timestamps by s, on top of any shift
// already applied. The error explains why the photo has to be skipped.
func PlanShift(p *Photo, s Shift) (*Change, error) {
	if p.Err != nil {
		return nil, p.Err
	}
	if p.Original == nil {
		return nil, errors.New("no timestamp found")
	}
	orig := p.Original.Add(s.By)
	var dig *Stamp
	if p.Digitized != nil {
		x := p.Digitized.Add(s.By)
		dig = &x
	}
	if s.Zone != "" {
		orig.TZ = s.Zone
		if dig != nil {
			dig.TZ = s.Zone
		}
	}
	return newChange(p, orig, dig), nil
}

// PlanReset plans restoring the timestamps recorded in the image file and
// removing a position camsync estimated (see PlanGeotag). Positions from the
// image or written by other tools are kept.
func PlanReset(p *Photo) (*Change, error) {
	if p.Err != nil {
		return nil, p.Err
	}
	if p.Sidecar == "" {
		return nil, errors.New("no sidecar, nothing to reset")
	}
	ch := &Change{Photo: p, Fields: xmp.Fields{}}
	switch {
	case p.FileOriginal != nil:
		ch = newChange(p, *p.FileOriginal, p.FileDigitized)
	case !p.PositionEstimated:
		return nil, errors.New("image file has no timestamp to restore")
	}
	if p.PositionEstimated {
		ch.Remove = geotagFields
	}
	return ch, nil
}

func newChange(p *Photo, orig Stamp, dig *Stamp) *Change {
	fields := xmp.Fields{
		xmp.DateTimeOriginal: orig.XMP(),
		xmp.CreateDate:       orig.XMP(),
		xmp.DateCreated:      orig.XMP(),
	}
	if dig != nil {
		fields[xmp.DateTimeDigitized] = dig.XMP()
	}
	addCameraFields(p, fields)
	return &Change{Photo: p, Fields: fields, NewOriginal: orig}
}

// addCameraFields adds Make and Model for a sidecar that does not exist yet,
// so it names the camera on its own.
func addCameraFields(p *Photo, fields xmp.Fields) {
	if p.Sidecar != "" {
		return
	}
	if p.Make != "" {
		fields[xmp.Make] = p.Make
	}
	if p.Model != "" {
		fields[xmp.Model] = p.Model
	}
}

// Write applies the change to the photo's sidecar, updating an existing
// file in place or creating a new one at Photo.SidecarTarget. With dryRun
// the sidecar content is prepared but nothing is written. The image itself
// is never touched.
//
// An existing sidecar is rewritten from the content read when the photo was
// loaded, so nothing is read again here; the plan and the write see the
// same document.
func (c *Change) Write(dryRun bool) (created bool, err error) {
	p := c.Photo
	var out []byte
	if p.Sidecar != "" {
		data := p.sidecarData
		if data == nil { // Photo not built by LoadPhoto
			d, err := os.ReadFile(p.Sidecar)
			if err != nil {
				return false, err
			}
			data = d
		}
		if out, err = xmp.Update(data, c.Fields, c.Remove); err != nil {
			return false, fmt.Errorf("sidecar %s: %w", p.Sidecar, err)
		}
	} else {
		out = xmp.New(c.Fields)
		created = true
	}
	if dryRun {
		return created, nil
	}
	return created, atomicWrite(p.SidecarTarget, out)
}

// atomicWrite writes data to a temporary file next to path and renames it
// into place, keeping the permissions of an existing file.
func atomicWrite(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	closed := false
	// cleanup closes the file if it is still open and removes it, joining any
	// errors on the way to err.
	cleanup := func(err error) error {
		if !closed {
			closed = true
			if cErr := tmp.Close(); cErr != nil {
				err = errors.Join(err, cErr)
			}
		}
		if rErr := os.Remove(tmpName); rErr != nil {
			err = errors.Join(err, rErr)
		}
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return cleanup(err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return cleanup(err)
	}
	return nil
}

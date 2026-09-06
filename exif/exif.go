// Package exif extracts the camera and timestamp tags from image files.
//
// It understands JPEG, TIFF and the TIFF-based raw formats, PNG, WebP,
// HEIF/HEIC/AVIF, Canon CR3, JPEG XL, Fujifilm RAF and Minolta MRW. Files are
// only ever read. Only the tags listed as Tag* constants are extracted; the
// package is deliberately not a general EXIF library.
package exif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
)

// EXIF/TIFF tag IDs that Tags can hold.
const (
	TagMake                = 0x010F
	TagModel               = 0x0110
	TagDateTime            = 0x0132
	TagExifIFD             = 0x8769
	TagDateTimeOriginal    = 0x9003
	TagDateTimeDigitized   = 0x9004
	TagOffsetTime          = 0x9010
	TagOffsetTimeOriginal  = 0x9011
	TagOffsetTimeDigitized = 0x9012
	TagSubSecTime          = 0x9290
	TagSubSecTimeOriginal  = 0x9291
	TagSubSecTimeDigitized = 0x9292
)

var wantedTags = map[uint16]bool{
	TagMake: true, TagModel: true, TagDateTime: true,
	TagDateTimeOriginal: true, TagDateTimeDigitized: true,
	TagOffsetTime: true, TagOffsetTimeOriginal: true, TagOffsetTimeDigitized: true,
	TagSubSecTime: true, TagSubSecTimeOriginal: true, TagSubSecTimeDigitized: true,
}

// ErrNotFound is returned for well-formed files that carry no EXIF block.
var ErrNotFound = errors.New("exif: no EXIF data found")

const (
	maxIFDEntries = 4096
	maxStringLen  = 1 << 16
	maxRead       = 64 << 20
)

// Tags holds the raw string values of the extracted tags, keyed by tag ID.
// Values are trimmed and NUL-terminated garbage is removed, but they are not
// otherwise interpreted; dates keep the EXIF "2006:01:02 15:04:05" form.
type Tags map[uint16]string

func (t Tags) merge(o Tags) { maps.Copy(t, o) }

// readAt reads exactly n bytes at off.
func readAt(r io.ReaderAt, off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || n > maxRead {
		return nil, fmt.Errorf("read of %d bytes at offset %d out of range", n, off)
	}
	b := make([]byte, n)
	got, err := r.ReadAt(b, off)
	if got == len(b) {
		return b, nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

type tiffReader struct {
	r    io.ReaderAt
	base int64 // position of the TIFF header within r
	size int64 // bytes available from base, or -1 if unknown
	bo   binary.ByteOrder

	// MakerNote location, recorded while reading the Exif IFD.
	makerOff, makerLen int64
}

func (t *tiffReader) bytes(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || (t.size >= 0 && off+n > t.size) {
		return nil, fmt.Errorf("exif: %d bytes at offset %d exceed block size %d", n, off, t.size)
	}
	return readAt(t.r, t.base+off, n)
}

// isTIFFHeader reports whether b starts like a TIFF file, including the
// Olympus ORF ("IIRO"/"MMOR") and Panasonic RW2 ("IIU\0") variants.
func isTIFFHeader(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch string(b[:4]) {
	case "II*\x00", "MM\x00*", "IIRO", "MMOR", "IIU\x00":
		return true
	}
	return false
}

// ParseTIFF reads IFD0 and the Exif sub-IFD of a TIFF structure located at
// base within r. Only the tags in wantedTags are collected.
func ParseTIFF(r io.ReaderAt, base, size int64) (Tags, error) {
	t := &tiffReader{r: r, base: base, size: size}
	hdr, err := t.bytes(0, 8)
	if err != nil {
		return nil, err
	}
	switch string(hdr[:2]) {
	case "II":
		t.bo = binary.LittleEndian
	case "MM":
		t.bo = binary.BigEndian
	default:
		return nil, errors.New("exif: bad TIFF byte order mark")
	}
	switch t.bo.Uint16(hdr[2:4]) {
	case 42, 0x4F52, 0x0055: // classic TIFF, ORF, RW2
	default:
		return nil, errors.New("exif: bad TIFF magic")
	}
	tags := Tags{}
	exifIFD, err := t.readIFD(int64(t.bo.Uint32(hdr[4:8])), tags)
	if err != nil {
		return nil, err
	}
	if exifIFD > 0 {
		// A broken Exif pointer should not throw away IFD0's data.
		if _, err := t.readIFD(exifIFD, tags); err != nil && len(tags) == 0 {
			return nil, err
		}
	}
	if len(tags) == 0 {
		return nil, ErrNotFound
	}
	t.readMakerNoteTimeZone(tags)
	return tags, nil
}

const (
	tagMakerNote      = 0x927C
	canonTagTimeInfo  = 0x0035
	nikonTagWorldTime = 0x0024
)

// readMakerNoteTimeZone fills the OffsetTime* tags from vendor-specific
// maker note records when the standard EXIF 2.31 tags are missing. Cameras
// have recorded their configured time zone in maker notes for a decade
// longer than in EXIF proper, so this is the only zone information most
// files carry. The vendor is chosen by the Make tag.
func (t *tiffReader) readMakerNoteTimeZone(tags Tags) {
	if t.makerOff <= 0 {
		return
	}
	if tags[TagOffsetTimeOriginal] != "" && tags[TagOffsetTime] != "" && tags[TagOffsetTimeDigitized] != "" {
		return
	}
	mk := strings.ToLower(tags[TagMake])
	var minutes int
	var ok bool
	switch {
	case strings.HasPrefix(mk, "canon"):
		minutes, ok = t.canonTimeZone()
	case strings.HasPrefix(mk, "nikon"):
		minutes, ok = t.nikonTimeZone()
	}
	if !ok || minutes < -14*60 || minutes > 14*60 {
		return
	}
	zone := formatOffset(minutes)
	for _, tag := range []uint16{TagOffsetTime, TagOffsetTimeOriginal, TagOffsetTimeDigitized} {
		if tags[tag] == "" {
			tags[tag] = zone
		}
	}
}

// ifdEntries reads the raw 12-byte entries of the IFD at off (an absolute
// offset from the TIFF header) using the given byte order.
func (t *tiffReader) ifdEntries(off int64, bo binary.ByteOrder) ([]byte, error) {
	cb, err := t.bytes(off, 2)
	if err != nil {
		return nil, err
	}
	n := int64(bo.Uint16(cb))
	if n == 0 || n > maxIFDEntries {
		return nil, errors.New("exif: implausible maker note IFD")
	}
	return t.bytes(off+2, n*12)
}

// canonTimeZone reads the TimeInfo record of a Canon MakerNote: four int32
// values of which index 1 is the offset in minutes, DST already included.
//
// The maker note is a plain IFD in the file's byte order. Value offsets are
// normally relative to the TIFF header, but files re-saved by other software
// may have them relative to the maker note itself; the record's size field
// (always 16) tells which.
func (t *tiffReader) canonTimeZone() (int, bool) {
	entries, err := t.ifdEntries(t.makerOff, t.bo)
	if err != nil || int64(len(entries))+2 > t.makerLen {
		return 0, false
	}
	for i := 0; i+12 <= len(entries); i += 12 {
		e := entries[i : i+12]
		tag, typ, count := t.bo.Uint16(e[0:2]), t.bo.Uint16(e[2:4]), t.bo.Uint32(e[4:8])
		if tag != canonTagTimeInfo || (typ != 4 && typ != 9) || count < 4 {
			continue
		}
		valOff := int64(t.bo.Uint32(e[8:12]))
		for _, off := range []int64{valOff, t.makerOff + valOff} {
			b, err := t.bytes(off, 16)
			if err == nil && t.bo.Uint32(b[0:4]) == 16 {
				return int(int32(t.bo.Uint32(b[4:8]))), true
			}
		}
		return 0, false
	}
	return 0, false
}

// nikonTimeZone reads the WorldTime record of a Nikon MakerNote: an int16
// offset in minutes followed by a DST flag. Nikon stores the base zone and
// DST separately, so an hour is added when the flag is set, which is also
// what exiftool-based readers such as Immich do.
//
// Modern maker notes start with "Nikon\0" and a version, followed by an
// embedded TIFF header that sets the byte order and the base for offsets.
// Older ones are a headerless IFD relative to the file's TIFF header.
func (t *tiffReader) nikonTimeZone() (int, bool) {
	hdr, err := t.bytes(t.makerOff, min(18, t.makerLen))
	if err != nil {
		return 0, false
	}
	bo, base, ifd := t.bo, int64(0), t.makerOff
	if string(hdr[:min(6, len(hdr))]) == "Nikon\x00" {
		if len(hdr) < 18 || hdr[6] != 2 { // type 2 (early Coolpix) has no WorldTime
			return 0, false
		}
		switch string(hdr[10:12]) {
		case "II":
			bo = binary.LittleEndian
		case "MM":
			bo = binary.BigEndian
		default:
			return 0, false
		}
		base = t.makerOff + 10
		ifd = base + int64(bo.Uint32(hdr[14:18]))
	}
	entries, err := t.ifdEntries(ifd, bo)
	if err != nil {
		return 0, false
	}
	for i := 0; i+12 <= len(entries); i += 12 {
		e := entries[i : i+12]
		tag, typ, count := bo.Uint16(e[0:2]), bo.Uint16(e[2:4]), bo.Uint32(e[4:8])
		if tag != nikonTagWorldTime || (typ != 7 && typ != 1) || count < 3 {
			continue
		}
		rec := e[8:12]
		if count > 4 {
			if rec, err = t.bytes(base+int64(bo.Uint32(e[8:12])), 4); err != nil {
				return 0, false
			}
		}
		minutes := int(int16(bo.Uint16(rec[0:2])))
		if rec[2] == 1 {
			minutes += 60
		}
		return minutes, true
	}
	return 0, false
}

// formatOffset renders minutes east of UTC as +hh:mm.
func formatOffset(minutes int) string {
	b := make([]byte, 0, 6)
	if minutes < 0 {
		b = append(b, '-')
		minutes = -minutes
	} else {
		b = append(b, '+')
	}
	h, m := minutes/60, minutes%60
	b = append(b, byte('0'+h/10), byte('0'+h%10), ':', byte('0'+m/10), byte('0'+m%10))
	return string(b)
}

func (t *tiffReader) readIFD(off int64, tags Tags) (exifIFD int64, err error) {
	cb, err := t.bytes(off, 2)
	if err != nil {
		return 0, err
	}
	n := int64(t.bo.Uint16(cb))
	if n > maxIFDEntries {
		return 0, fmt.Errorf("exif: implausible IFD with %d entries", n)
	}
	entries, err := t.bytes(off+2, n*12)
	if err != nil {
		return 0, err
	}
	for i := range n {
		e := entries[i*12 : i*12+12]
		tag := t.bo.Uint16(e[0:2])
		typ := t.bo.Uint16(e[2:4])
		count := int64(t.bo.Uint32(e[4:8]))
		val := e[8:12]
		if tag == TagExifIFD {
			if typ == 3 { // SHORT, written by some broken encoders
				exifIFD = int64(t.bo.Uint16(val[:2]))
			} else {
				exifIFD = int64(t.bo.Uint32(val))
			}
			continue
		}
		if tag == tagMakerNote && count > 4 {
			t.makerOff, t.makerLen = int64(t.bo.Uint32(val)), count
			continue
		}
		if !wantedTags[tag] {
			continue
		}
		if typ != 1 && typ != 2 && typ != 7 { // BYTE, ASCII, UNDEFINED
			continue
		}
		if count > maxStringLen {
			continue
		}
		var data []byte
		if count <= 4 {
			data = val[:count]
		} else {
			data, err = t.bytes(int64(t.bo.Uint32(val)), count)
			if err != nil {
				continue
			}
		}
		if s := cleanString(data); s != "" {
			tags[tag] = s
		}
	}
	return exifIFD, nil
}

func cleanString(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.ToValidUTF8(s, ""))
}

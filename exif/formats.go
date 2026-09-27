package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// photoExts lists the file extensions that are considered photos. Detection of
// the actual container happens by content, so misnamed files still work.
var photoExts = map[string]bool{
	// JPEG
	".jpg": true, ".jpeg": true, ".jpe": true, ".jfif": true, ".jif": true,
	// TIFF and TIFF-based raw
	".tif": true, ".tiff": true, ".dng": true, ".cr2": true, ".nef": true, ".nrw": true,
	".arw": true, ".srf": true, ".sr2": true, ".pef": true, ".orf": true, ".rw2": true,
	".rwl": true, ".srw": true, ".3fr": true, ".fff": true, ".erf": true, ".mef": true,
	".mos": true, ".kdc": true, ".dcr": true, ".iiq": true,
	// other raw containers
	".cr3": true, ".raf": true, ".mrw": true,
	// other image containers
	".png": true, ".webp": true, ".heic": true, ".heif": true, ".hif": true,
	".avif": true, ".jxl": true,
}

// IsSupportedFile reports whether the file extension of path belongs to a
// format this package can read.
func IsSupportedFile(path string) bool {
	return photoExts[strings.ToLower(filepath.Ext(path))]
}

// ReadFile opens path read-only and returns the EXIF tags it carries plus a
// short format label such as "JPEG" or "NEF". It returns ErrNotFound when
// the file is a recognised image without EXIF data.
func ReadFile(path string) (Tags, string, error) {
	m, err := ReadFileMeta(path)
	return m.Tags, m.Format, err
}

// Meta is what ReadFileMeta finds in an image.
type Meta struct {
	Tags   Tags
	Format string // short label such as "JPEG" or "NEF"
	// XMP is the XMP packet embedded in the image, or nil. IPTC holds the
	// IPTC datasets listed as IPTC* constants, or is nil. Only JPEG files
	// are searched for either.
	XMP  []byte
	IPTC IPTCTags
}

// IPTCTags holds raw IPTC values keyed by record<<8 | dataset.
type IPTCTags map[uint16]string

// IPTC datasets that IPTCTags can hold.
const (
	IPTCDateCreated         = 2<<8 | 55 // "20060102"
	IPTCTimeCreated         = 2<<8 | 60 // "150405", optionally with "-0700"
	IPTCDigitalCreationDate = 2<<8 | 62
	IPTCDigitalCreationTime = 2<<8 | 63
)

// ReadFileMeta is ReadFile that also returns the embedded XMP packet, where
// some apps record the date while leaving EXIF empty. ErrNotFound means the
// file has no EXIF data; Meta.XMP may still be set.
func ReadFileMeta(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Meta{}, err
	}
	return ReadMeta(f, st.Size(), filepath.Ext(path))
}

// Read detects the container by its magic bytes and extracts the EXIF tags.
// size is the total length of r; ext is the file extension (with or without
// the dot) and is only used to label TIFF-based raw formats.
func Read(r io.ReaderAt, size int64, ext string) (Tags, string, error) {
	m, err := ReadMeta(r, size, ext)
	return m.Tags, m.Format, err
}

// ReadMeta is Read that also returns the embedded XMP packet.
func ReadMeta(f io.ReaderAt, size int64, ext string) (m Meta, err error) {
	head := make([]byte, 16)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]

	ext = strings.ToUpper(strings.TrimPrefix(ext, "."))
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		var j jpegMeta
		j, err = parseJPEG(f, 0, size)
		m.Tags, m.XMP, m.IPTC, m.Format = j.tags, j.xmp, j.iptc, "JPEG"
	case isTIFFHeader(head):
		m.Tags, err = ParseTIFF(f, 0, size)
		if ext == "" {
			ext = "TIFF"
		}
		m.Format = ext
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		m.Tags, err = parsePNG(f, size)
		m.Format = "PNG"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		m.Tags, err = parseWebP(f, size)
		m.Format = "WebP"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		if string(head[8:12]) == "crx " {
			m.Tags, err = parseCR3(f, size)
			m.Format = "CR3"
			break
		}
		m.Tags, err = parseHEIF(f, size)
		if ext != "AVIF" {
			ext = "HEIF"
		}
		m.Format = ext
	case len(head) >= 12 && string(head[4:8]) == "JXL ":
		m.Tags, err = parseJXL(f, size)
		m.Format = "JXL"
	case bytes.HasPrefix(head, []byte("FUJIFILMCCD-RAW")):
		m.Tags, err = parseRAF(f, size)
		m.Format = "RAF"
	case bytes.HasPrefix(head, []byte("\x00MRM")):
		m.Tags, err = parseMRW(f, size)
		m.Format = "MRW"
	default:
		return Meta{Format: ext}, errors.New("unrecognised image format")
	}
	return m, err
}

// parseTIFFMaybePrefixed parses a TIFF block that may start with the
// "Exif\0\0" marker some writers put in front of it.
func parseTIFFMaybePrefixed(r io.ReaderAt, off, size int64) (Tags, error) {
	if size >= 6 {
		if p, err := readAt(r, off, 6); err == nil && string(p) == "Exif\x00\x00" {
			off += 6
			size -= 6
		}
	}
	return ParseTIFF(r, off, size)
}

// xmpHeader starts the APP1 segment holding a JPEG's main XMP packet.
// ExtendedXMP segments have another header and are not read.
const xmpHeader = "http://ns.adobe.com/xap/1.0/\x00"

// photoshopHeader starts the APP13 segment holding Photoshop image
// resources, among them the IPTC datasets.
const photoshopHeader = "Photoshop 3.0\x00"

// jpegMeta is what parseJPEG finds.
type jpegMeta struct {
	tags Tags
	xmp  []byte
	iptc IPTCTags
}

// parseJPEG walks the marker segments looking for the APP1 Exif segment,
// the APP1 XMP packet and the APP13 IPTC datasets. It returns ErrNotFound,
// with whatever else it found, when there is no Exif segment.
func parseJPEG(r io.ReaderAt, start, end int64) (m jpegMeta, err error) {
	defer func() {
		if m.tags == nil && err == nil {
			err = ErrNotFound
		}
	}()
	pos := start + 2
	for pos+4 <= end && (m.tags == nil || m.xmp == nil || m.iptc == nil) {
		b, err := readAt(r, pos, 4)
		if err != nil {
			return m, err
		}
		if b[0] != 0xFF {
			return m, fmt.Errorf("jpeg: corrupt marker at offset %d", pos)
		}
		marker := b[1]
		switch {
		case marker == 0xFF: // fill byte
			pos++
			continue
		case marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // no payload
			pos += 2
			continue
		case marker == 0xD9 || marker == 0xDA: // EOI / SOS: metadata segments are over
			return m, nil
		}
		segLen := int64(binary.BigEndian.Uint16(b[2:4]))
		if segLen < 2 {
			return m, fmt.Errorf("jpeg: corrupt segment length at offset %d", pos)
		}
		data, size := pos+4, segLen-2
		switch marker {
		case 0xE1:
			p, err := readAt(r, data, min(size, int64(len(xmpHeader))))
			switch {
			case err != nil:
			case m.tags == nil && strings.HasPrefix(string(p), "Exif\x00\x00"):
				// A block of blanked values is no Exif data, but the
				// segments after it may still hold the date.
				if m.tags, err = ParseTIFF(r, data+6, size-6); err != nil && !errors.Is(err, ErrNotFound) {
					return m, err
				}
			case m.xmp == nil && string(p) == xmpHeader:
				n := int64(len(xmpHeader))
				if p, err := readAt(r, data+n, size-n); err == nil {
					m.xmp = bytes.TrimRight(p, "\x00") // Apple terminates the packet with a NUL
				}
			}
		case 0xED:
			if p, err := readAt(r, data, size); err == nil && m.iptc == nil && strings.HasPrefix(string(p), photoshopHeader) {
				m.iptc = parsePhotoshopIPTC(p[len(photoshopHeader):])
			}
		}
		pos += 2 + segLen
	}
	return m, nil
}

// parsePhotoshopIPTC finds the IPTC-NAA resource (ID 0x0404) among
// Photoshop image resources and extracts its datasets. It returns nil when
// there is none.
func parsePhotoshopIPTC(b []byte) IPTCTags {
	for len(b) >= 8 && string(b[:4]) == "8BIM" {
		id := binary.BigEndian.Uint16(b[4:6])
		nameLen := 1 + int(b[6]) // Pascal string, padded to an even length
		nameLen += nameLen & 1
		if len(b) < 6+nameLen+4 {
			return nil
		}
		size := int(binary.BigEndian.Uint32(b[6+nameLen:]))
		b = b[6+nameLen+4:]
		if size > len(b) {
			return nil
		}
		if id == 0x0404 {
			return parseIPTC(b[:size])
		}
		b = b[min(size+size&1, len(b)):]
	}
	return nil
}

// parseIPTC extracts the IPTC* datasets from an IPTC-NAA block. Of repeated
// datasets the first wins.
func parseIPTC(b []byte) IPTCTags {
	tags := IPTCTags{}
	for len(b) >= 5 && b[0] == 0x1C {
		key := uint16(b[1])<<8 | uint16(b[2])
		n := int(binary.BigEndian.Uint16(b[3:5]))
		if n&0x8000 != 0 || 5+n > len(b) { // extended datasets are never dates
			break
		}
		switch key {
		case IPTCDateCreated, IPTCTimeCreated, IPTCDigitalCreationDate, IPTCDigitalCreationTime:
			if _, dup := tags[key]; !dup {
				tags[key] = cleanString(b[5 : 5+n])
			}
		}
		b = b[5+n:]
	}
	return tags
}

func parsePNG(r io.ReaderAt, size int64) (Tags, error) {
	pos := int64(8)
	for pos+8 <= size {
		h, err := readAt(r, pos, 8)
		if err != nil {
			return nil, err
		}
		length := int64(binary.BigEndian.Uint32(h[:4]))
		typ := string(h[4:8])
		if typ == "eXIf" {
			return parseTIFFMaybePrefixed(r, pos+8, length)
		}
		if typ == "IEND" {
			break
		}
		pos += 12 + length
	}
	return nil, ErrNotFound
}

func parseWebP(r io.ReaderAt, size int64) (Tags, error) {
	pos := int64(12)
	for pos+8 <= size {
		h, err := readAt(r, pos, 8)
		if err != nil {
			return nil, err
		}
		length := int64(binary.LittleEndian.Uint32(h[4:8]))
		if string(h[:4]) == "EXIF" {
			return parseTIFFMaybePrefixed(r, pos+8, length)
		}
		pos += 8 + length + length&1
	}
	return nil, ErrNotFound
}

type box struct {
	typ        string
	start      int64 // box start
	data       int64 // payload start (after header and uuid, if any)
	end        int64
	uuid       []byte
	headerSize int64
}

func readBoxes(r io.ReaderAt, start, end int64) ([]box, error) {
	var boxes []box
	pos := start
	for pos+8 <= end && len(boxes) < 10000 {
		h, err := readAt(r, pos, 8)
		if err != nil {
			return boxes, err
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		hdr := int64(8)
		switch size {
		case 1:
			l, err := readAt(r, pos+8, 8)
			if err != nil {
				return boxes, err
			}
			size = int64(binary.BigEndian.Uint64(l))
			hdr = 16
		case 0:
			size = end - pos
		}
		if size < hdr || pos+size > end {
			break // truncated box; use what we have
		}
		b := box{typ: string(h[4:8]), start: pos, data: pos + hdr, end: pos + size, headerSize: hdr}
		if b.typ == "uuid" && size >= hdr+16 {
			b.uuid, _ = readAt(r, pos+hdr, 16)
			b.data += 16
		}
		boxes = append(boxes, b)
		pos += size
	}
	return boxes, nil
}

func findBox(boxes []box, typ string) *box {
	for i := range boxes {
		if boxes[i].typ == typ {
			return &boxes[i]
		}
	}
	return nil
}

// cursor reads big-endian integers from a byte slice without panicking.
type cursor struct {
	b   []byte
	p   int
	bad bool
}

func (c *cursor) n(size int) uint64 {
	if size == 0 {
		return 0
	}
	if c.p+size > len(c.b) {
		c.bad = true
		c.p = len(c.b)
		return 0
	}
	var v uint64
	for i := range size {
		v = v<<8 | uint64(c.b[c.p+i])
	}
	c.p += size
	return v
}

func (c *cursor) u8() uint64  { return c.n(1) }
func (c *cursor) u16() uint64 { return c.n(2) }
func (c *cursor) u32() uint64 { return c.n(4) }

// parseExifItem handles the Exif item payload of HEIF and JXL: a 4-byte
// big-endian offset to the TIFF header, optionally followed by "Exif\0\0".
func parseExifItem(buf []byte) (Tags, error) {
	if len(buf) < 8 {
		return nil, ErrNotFound
	}
	off := int64(binary.BigEndian.Uint32(buf)) + 4
	if off >= int64(len(buf)) {
		return nil, ErrNotFound
	}
	return parseTIFFMaybePrefixed(bytes.NewReader(buf), off, int64(len(buf))-off)
}

func parseHEIF(r io.ReaderAt, size int64) (Tags, error) {
	top, err := readBoxes(r, 0, size)
	if err != nil {
		return nil, err
	}
	meta := findBox(top, "meta")
	if meta == nil {
		return nil, ErrNotFound
	}
	kids, err := readBoxes(r, meta.data+4, meta.end) // meta is a FullBox
	if err != nil {
		return nil, err
	}
	iinf, iloc := findBox(kids, "iinf"), findBox(kids, "iloc")
	if iinf == nil || iloc == nil {
		return nil, ErrNotFound
	}

	// iinf: find the item ID of type "Exif".
	hdr, err := readAt(r, iinf.data, 4)
	if err != nil {
		return nil, err
	}
	entriesStart := iinf.data + 6 // version 0: u16 entry count
	if hdr[0] >= 1 {
		entriesStart = iinf.data + 8
	}
	infes, _ := readBoxes(r, entriesStart, iinf.end)
	exifID := int64(-1)
	for _, e := range infes {
		if e.typ != "infe" {
			continue
		}
		b, err := readAt(r, e.data, min(14, e.end-e.data))
		if err != nil {
			continue
		}
		c := &cursor{b: b}
		ver := c.u8()
		c.n(3) // flags
		var id uint64
		switch ver {
		case 2:
			id = c.u16()
		case 3:
			id = c.u32()
		default:
			continue
		}
		c.u16() // item protection index
		if c.p+4 <= len(b) && string(b[c.p:c.p+4]) == "Exif" {
			exifID = int64(id)
			break
		}
	}
	if exifID < 0 {
		return nil, ErrNotFound
	}

	// iloc: find where that item's data lives.
	payload, err := readAt(r, iloc.data, min(iloc.end-iloc.data, 8<<20))
	if err != nil {
		return nil, err
	}
	c := &cursor{b: payload}
	ver := c.u8()
	c.n(3)
	b1 := c.u8()
	offSize, lenSize := int(b1>>4), int(b1&15)
	b2 := c.u8()
	baseSize, idxSize := int(b2>>4), 0
	if ver == 1 || ver == 2 {
		idxSize = int(b2 & 15)
	}
	var count uint64
	if ver < 2 {
		count = c.u16()
	} else {
		count = c.u32()
	}
	type extent struct{ off, length int64 }
	var extents []extent
	method := 0
	for i := uint64(0); i < count && !c.bad; i++ {
		var id uint64
		if ver < 2 {
			id = c.u16()
		} else {
			id = c.u32()
		}
		m := 0
		if ver == 1 || ver == 2 {
			m = int(c.u16() & 0xF)
		}
		c.u16() // data reference index
		base := c.n(baseSize)
		ec := c.u16()
		for j := uint64(0); j < ec && !c.bad; j++ {
			if idxSize > 0 {
				c.n(idxSize)
			}
			eo, el := c.n(offSize), c.n(lenSize)
			if int64(id) == exifID {
				extents = append(extents, extent{int64(base + eo), int64(el)})
				method = m
			}
		}
	}
	if len(extents) == 0 {
		return nil, ErrNotFound
	}
	var baseOff int64
	switch method {
	case 0: // absolute file offsets
	case 1: // relative to the idat box inside meta
		idat := findBox(kids, "idat")
		if idat == nil {
			return nil, errors.New("heif: Exif item refers to a missing idat box")
		}
		baseOff = idat.data
	default:
		return nil, fmt.Errorf("heif: unsupported item construction method %d", method)
	}
	var buf []byte
	for _, e := range extents {
		length := e.length
		if length == 0 { // zero means "to the end"
			length = size - (baseOff + e.off)
		}
		b, err := readAt(r, baseOff+e.off, length)
		if err != nil {
			return nil, fmt.Errorf("heif: reading Exif item: %w", err)
		}
		buf = append(buf, b...)
	}
	return parseExifItem(buf)
}

var canonCR3UUID = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}

// parseCR3 reads the CMT1 (IFD0), CMT2 (Exif IFD) and CMT4 (GPS IFD) TIFF
// blocks that Canon stores in a uuid box inside moov.
func parseCR3(r io.ReaderAt, size int64) (Tags, error) {
	top, err := readBoxes(r, 0, size)
	if err != nil {
		return nil, err
	}
	moov := findBox(top, "moov")
	if moov == nil {
		return nil, ErrNotFound
	}
	kids, _ := readBoxes(r, moov.data, moov.end)
	for _, k := range kids {
		if k.typ != "uuid" || !bytes.Equal(k.uuid, canonCR3UUID) {
			continue
		}
		inner, _ := readBoxes(r, k.data, k.end)
		tags := Tags{}
		for _, name := range []string{"CMT1", "CMT2"} {
			if b := findBox(inner, name); b != nil {
				if t, err := ParseTIFF(r, b.data, b.end-b.data); err == nil {
					tags.merge(t)
				}
			}
		}
		if b := findBox(inner, "CMT4"); b != nil && len(tags) > 0 {
			if t, err := ParseGPSTIFF(r, b.data, b.end-b.data); err == nil {
				tags.merge(t)
			}
		}
		if len(tags) > 0 {
			return tags, nil
		}
	}
	return nil, ErrNotFound
}

func parseJXL(r io.ReaderAt, size int64) (Tags, error) {
	top, err := readBoxes(r, 0, size)
	if err != nil {
		return nil, err
	}
	b := findBox(top, "Exif")
	if b == nil {
		return nil, ErrNotFound
	}
	buf, err := readAt(r, b.data, b.end-b.data)
	if err != nil {
		return nil, err
	}
	return parseExifItem(buf)
}

// parseRAF uses the embedded JPEG preview, whose EXIF carries the camera data.
func parseRAF(r io.ReaderAt, size int64) (Tags, error) {
	h, err := readAt(r, 84, 8)
	if err != nil {
		return nil, err
	}
	off := int64(binary.BigEndian.Uint32(h[:4]))
	length := int64(binary.BigEndian.Uint32(h[4:8]))
	if off <= 0 || off+length > size {
		return nil, errors.New("raf: embedded JPEG offset out of range")
	}
	m, err := parseJPEG(r, off, off+length)
	return m.tags, err
}

// parseMRW reads the TTW block of a Minolta raw file, which is a TIFF header.
func parseMRW(r io.ReaderAt, size int64) (Tags, error) {
	h, err := readAt(r, 0, 8)
	if err != nil {
		return nil, err
	}
	end := min(8+int64(binary.BigEndian.Uint32(h[4:8])), size)
	pos := int64(8)
	for pos+8 <= end {
		bh, err := readAt(r, pos, 8)
		if err != nil {
			return nil, err
		}
		length := int64(binary.BigEndian.Uint32(bh[4:8]))
		if string(bh[:4]) == "\x00TTW" {
			return ParseTIFF(r, pos+8, length)
		}
		pos += 8 + length
	}
	return nil, ErrNotFound
}

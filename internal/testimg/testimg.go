// Package testimg builds minimal synthetic image files with EXIF data for
// tests. Every builder wraps a TIFF block produced by BuildTIFF into a
// specific container format.
package testimg

import (
	"bytes"
	"encoding/binary"

	"github.com/sollniss/camsync/exif"
)

// Entry is one IFD entry. If Build is set it is called with the absolute
// TIFF offset the entry's data will be written at and its result replaces
// Data; this lets nested structures such as maker notes use absolute offsets.
type Entry struct {
	Tag   uint16
	Type  uint16
	Data  []byte
	Build func(off int) []byte
}

// ASCII builds a NUL-terminated ASCII entry.
func ASCII(tag uint16, s string) Entry { return Entry{Tag: tag, Type: 2, Data: append([]byte(s), 0)} }

// BuildTIFF assembles a TIFF block with IFD0 and, if exif is non-nil, an
// Exif sub-IFD referenced from IFD0.
func BuildTIFF(bo binary.ByteOrder, ifd0, exifIFD []Entry) []byte {
	n0 := len(ifd0)
	if exifIFD != nil {
		n0++
	}
	ifd0Off := 8
	exifOff := ifd0Off + 2 + n0*12 + 4
	dataOff := exifOff
	if exifIFD != nil {
		dataOff += 2 + len(exifIFD)*12 + 4
	}
	var data bytes.Buffer
	var out bytes.Buffer
	if bo == binary.LittleEndian {
		out.WriteString("II")
	} else {
		out.WriteString("MM")
	}
	put := func(v any) { _ = binary.Write(&out, bo, v) }
	put(uint16(42))
	put(uint32(ifd0Off))
	writeIFD := func(entries []Entry, exifPtr int) {
		count := len(entries)
		if exifPtr > 0 {
			count++
		}
		put(uint16(count))
		for _, e := range entries {
			if e.Build != nil {
				e.Data = e.Build(dataOff + data.Len())
			}
			put(e.Tag)
			put(e.Type)
			put(uint32(len(e.Data)))
			if len(e.Data) <= 4 {
				v := make([]byte, 4)
				copy(v, e.Data)
				out.Write(v)
			} else {
				put(uint32(dataOff + data.Len()))
				data.Write(e.Data)
			}
		}
		if exifPtr > 0 {
			put(uint16(exif.TagExifIFD))
			put(uint16(4))
			put(uint32(1))
			put(uint32(exifPtr))
		}
		put(uint32(0))
	}
	if exifIFD != nil {
		writeIFD(ifd0, exifOff)
		writeIFD(exifIFD, 0)
	} else {
		writeIFD(ifd0, 0)
	}
	out.Write(data.Bytes())
	return out.Bytes()
}

// Simple builds a little-endian TIFF block with make, model and both
// DateTimeOriginal and DateTimeDigitized set to dto.
func Simple(mk, model, dto string) []byte {
	return BuildTIFF(binary.LittleEndian,
		[]Entry{ASCII(exif.TagMake, mk), ASCII(exif.TagModel, model)},
		[]Entry{ASCII(exif.TagDateTimeOriginal, dto), ASCII(exif.TagDateTimeDigitized, dto)})
}

func be32(v int) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, uint32(v)); return b }
func le32(v int) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, uint32(v)); return b }

// Box builds an ISO base media file format box.
func Box(typ string, payload ...[]byte) []byte {
	var b bytes.Buffer
	size := 8
	for _, p := range payload {
		size += len(p)
	}
	b.Write(be32(size))
	b.WriteString(typ)
	for _, p := range payload {
		b.Write(p)
	}
	return b.Bytes()
}

// JPEG wraps tiff into a JPEG with an APP0 segment before the APP1 Exif.
func JPEG(tiff []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	app0 := append([]byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"), 0)
	b.Write([]byte{0xFF, 0xE0, byte((len(app0) + 2) >> 8), byte(len(app0) + 2)})
	b.Write(app0)
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	b.Write([]byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)})
	b.Write(app1)
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x00, 0xFF, 0xD9})
	return b.Bytes()
}

// PNG wraps tiff into an eXIf chunk, optionally with the "Exif\0\0" prefix
// some writers add.
func PNG(tiff []byte, prefixed bool) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		b.Write(be32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		b.Write([]byte{0, 0, 0, 0}) // crc, not checked
	}
	chunk("IHDR", make([]byte, 13))
	if prefixed {
		tiff = append([]byte("Exif\x00\x00"), tiff...)
	}
	chunk("eXIf", tiff)
	chunk("IEND", nil)
	return b.Bytes()
}

// WebP wraps tiff into an EXIF chunk after a VP8X chunk.
func WebP(tiff []byte) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	body.WriteString("VP8X")
	body.Write(le32(10))
	body.Write(make([]byte, 10))
	body.WriteString("EXIF")
	body.Write(le32(len(tiff)))
	body.Write(tiff)
	if len(tiff)%2 == 1 {
		body.WriteByte(0)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write(le32(body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}

// HEIF wraps tiff into a HEIF Exif item. method 0 stores the item in mdat
// with absolute offsets, method 1 stores it in an idat box inside meta.
func HEIF(tiff []byte, method int) []byte {
	ftyp := Box("ftyp", []byte("heic"), be32(0), []byte("mif1heic"))
	item := append(be32(6), []byte("Exif\x00\x00")...) // Apple style: offset 6 then marker
	item = append(item, tiff...)
	build := func(offset int) []byte {
		infe := Box("infe", []byte{2, 0, 0, 0}, []byte{0, 1}, []byte{0, 0}, []byte("Exif"), []byte{0})
		iinf := Box("iinf", []byte{0, 0, 0, 0}, []byte{0, 1}, infe)
		var ilocBody []byte
		if method == 0 {
			ilocBody = append([]byte{0, 0, 0, 0, 0x44, 0x00}, []byte{0, 1}...) // v0, off/len size 4, base 0
			ilocBody = append(ilocBody, []byte{0, 1, 0, 0, 0, 1}...)           // id 1, dref 0, 1 extent
		} else {
			ilocBody = append([]byte{1, 0, 0, 0, 0x44, 0x00}, []byte{0, 1}...) // v1
			ilocBody = append(ilocBody, []byte{0, 1, 0, 1, 0, 0, 0, 1}...)     // id 1, method 1, dref 0, 1 extent
		}
		ilocBody = append(ilocBody, be32(offset)...)
		ilocBody = append(ilocBody, be32(len(item))...)
		iloc := Box("iloc", ilocBody)
		var idat []byte
		if method == 1 {
			idat = Box("idat", item)
		}
		return Box("meta", []byte{0, 0, 0, 0}, Box("hdlr", make([]byte, 4), []byte("pict"), make([]byte, 13)), iinf, iloc, idat)
	}
	meta := build(0)
	var out bytes.Buffer
	out.Write(ftyp)
	if method == 0 {
		meta = build(len(ftyp) + len(meta) + 8)
		out.Write(meta)
		out.Write(Box("mdat", item))
	} else {
		out.Write(meta)
	}
	return out.Bytes()
}

var canonUUID = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}

// CR3 builds a Canon CR3 skeleton with the given IFD0 and Exif TIFF blocks.
func CR3(ifd0, exifIFD []byte) []byte {
	uuid := Box("uuid", canonUUID, Box("CMT1", ifd0), Box("CMT2", exifIFD))
	moov := Box("moov", uuid)
	return bytes.Join([][]byte{Box("ftyp", []byte("crx "), be32(1), []byte("crx isom")), moov, Box("mdat", []byte("x"))}, nil)
}

// JXL wraps tiff into a JPEG XL container with an Exif box.
func JXL(tiff []byte) []byte {
	sig := []byte{0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}
	ftyp := Box("ftyp", []byte("jxl "), be32(0), []byte("jxl "))
	ex := Box("Exif", be32(0), tiff)
	return bytes.Join([][]byte{sig, ftyp, ex, Box("jxlc", []byte{0xFF, 0x0A})}, nil)
}

// RAF wraps a JPEG into a Fujifilm RAF header.
func RAF(jpeg []byte) []byte {
	hdr := make([]byte, 160)
	copy(hdr, "FUJIFILMCCD-RAW 0201FF383030")
	binary.BigEndian.PutUint32(hdr[84:], 160)
	binary.BigEndian.PutUint32(hdr[88:], uint32(len(jpeg)))
	return append(hdr, jpeg...)
}

// MRW wraps tiff into a Minolta MRW TTW block.
func MRW(tiff []byte) []byte {
	var blocks bytes.Buffer
	blocks.WriteString("\x00PRD")
	blocks.Write(be32(24))
	blocks.Write(make([]byte, 24))
	blocks.WriteString("\x00TTW")
	blocks.Write(be32(len(tiff)))
	blocks.Write(tiff)
	var b bytes.Buffer
	b.WriteString("\x00MRM")
	b.Write(be32(blocks.Len()))
	b.Write(blocks.Bytes())
	b.WriteString("rawdata")
	return b.Bytes()
}

// CanonMakerNote returns a MakerNote entry holding a Canon TimeInfo record
// with the given UTC offset in minutes. Canon maker notes are a plain IFD
// whose value offsets are relative to the TIFF header, so the entry is built
// once its final position is known.
func CanonMakerNote(bo binary.ByteOrder, tzMinutes int) Entry {
	return Entry{Tag: 0x927C, Type: 7, Build: func(off int) []byte {
		var b bytes.Buffer
		put := func(v any) { _ = binary.Write(&b, bo, v) }
		put(uint16(1))                // one entry
		put(uint16(0x35))             // TimeInfo
		put(uint16(9))                // int32s
		put(uint32(4))                // count
		put(uint32(off + 2 + 12 + 4)) // data follows the IFD
		put(uint32(0))                // next IFD
		put(int32(16))                // record size
		put(int32(tzMinutes))         // TimeZone
		put(int32(19))                // TimeZoneCity: Paris
		put(int32(60))                // DaylightSavings: on
		return b.Bytes()
	}}
}

// NikonMakerNote returns a MakerNote entry in the modern Nikon layout: a
// "Nikon\0" header, an embedded TIFF header in the given byte order and an
// IFD holding a WorldTime record with the base offset in minutes and the
// daylight saving flag.
func NikonMakerNote(bo binary.ByteOrder, tzMinutes int, dst bool) Entry {
	var b bytes.Buffer
	b.WriteString("Nikon\x00\x02\x11\x00\x00")
	if bo == binary.LittleEndian {
		b.WriteString("II")
	} else {
		b.WriteString("MM")
	}
	put := func(v any) { _ = binary.Write(&b, bo, v) }
	put(uint16(42))
	put(uint32(8)) // IFD follows the embedded header
	b.Write(nikonWorldTimeIFD(bo, tzMinutes, dst))
	return Entry{Tag: 0x927C, Type: 7, Data: b.Bytes()}
}

// NikonMakerNoteHeaderless returns a MakerNote entry in the old headerless
// Nikon layout: a plain IFD in the file's byte order.
func NikonMakerNoteHeaderless(bo binary.ByteOrder, tzMinutes int, dst bool) Entry {
	return Entry{Tag: 0x927C, Type: 7, Data: nikonWorldTimeIFD(bo, tzMinutes, dst)}
}

func nikonWorldTimeIFD(bo binary.ByteOrder, tzMinutes int, dst bool) []byte {
	var b bytes.Buffer
	put := func(v any) { _ = binary.Write(&b, bo, v) }
	put(uint16(1))    // one entry
	put(uint16(0x24)) // WorldTime
	put(uint16(7))    // undefined
	put(uint32(4))    // four bytes, stored inline
	put(int16(tzMinutes))
	if dst {
		b.WriteByte(1)
	} else {
		b.WriteByte(0)
	}
	b.WriteByte(2) // DateDisplayFormat D/M/Y
	put(uint32(0)) // next IFD
	return b.Bytes()
}

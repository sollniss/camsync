package exif_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/internal/testimg"
)

var (
	ifd0 = []testimg.Entry{
		testimg.ASCII(exif.TagMake, "Canon"),
		testimg.ASCII(exif.TagModel, "Canon EOS R5"),
		testimg.ASCII(exif.TagDateTime, "2024:05:01 12:34:56"),
	}
	exifIFD = []testimg.Entry{
		testimg.ASCII(exif.TagDateTimeOriginal, "2024:05:01 12:34:56"),
		testimg.ASCII(exif.TagDateTimeDigitized, "2024:05:01 12:34:57"),
		testimg.ASCII(exif.TagSubSecTimeOriginal, "42"),
		testimg.ASCII(exif.TagOffsetTimeOriginal, "+02:00"),
	}
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContainers(t *testing.T) {
	tiffLE := testimg.BuildTIFF(binary.LittleEndian, ifd0, exifIFD)
	tiffBE := testimg.BuildTIFF(binary.BigEndian, ifd0, exifIFD)
	cases := []struct {
		name, file string
		data       []byte
		format     string
	}{
		{"jpeg", "a.jpg", testimg.JPEG(tiffLE), "JPEG"},
		{"jpeg-be", "a.jpeg", testimg.JPEG(tiffBE), "JPEG"},
		{"tiff", "a.tif", tiffLE, "TIF"},
		{"nef", "a.NEF", tiffBE, "NEF"},
		{"png", "a.png", testimg.PNG(tiffLE, false), "PNG"},
		{"png-prefixed", "a.png", testimg.PNG(tiffLE, true), "PNG"},
		{"webp", "a.webp", testimg.WebP(tiffLE), "WebP"},
		{"heif", "a.heic", testimg.HEIF(tiffLE, 0), "HEIF"},
		{"heif-idat", "a.avif", testimg.HEIF(tiffLE, 1), "AVIF"},
		{"cr3", "a.cr3", testimg.CR3(testimg.BuildTIFF(binary.LittleEndian, ifd0, nil), testimg.BuildTIFF(binary.LittleEndian, exifIFD, nil)), "CR3"},
		{"jxl", "a.jxl", testimg.JXL(tiffBE), "JXL"},
		{"raf", "a.raf", testimg.RAF(testimg.JPEG(tiffLE)), "RAF"},
		{"mrw", "a.mrw", testimg.MRW(tiffBE), "MRW"},
		{"misnamed", "a.dng", testimg.JPEG(tiffLE), "JPEG"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, c.file, c.data)
			tags, format, err := exif.ReadFile(p)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if format != c.format {
				t.Errorf("format = %q, want %q", format, c.format)
			}
			want := map[uint16]string{
				exif.TagMake:               "Canon",
				exif.TagModel:              "Canon EOS R5",
				exif.TagDateTimeOriginal:   "2024:05:01 12:34:56",
				exif.TagDateTimeDigitized:  "2024:05:01 12:34:57",
				exif.TagSubSecTimeOriginal: "42",
				exif.TagOffsetTimeOriginal: "+02:00",
			}
			for tag, v := range want {
				if tags[tag] != v {
					t.Errorf("tag %#x = %q, want %q", tag, tags[tag], v)
				}
			}
		})
	}
}

func TestNoExifAndGarbage(t *testing.T) {
	p := writeTemp(t, "plain.jpg", []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	if _, _, err := exif.ReadFile(p); err != exif.ErrNotFound {
		t.Errorf("plain jpeg: err = %v, want ErrNotFound", err)
	}
	p = writeTemp(t, "junk.jpg", []byte("this is not an image at all"))
	if _, _, err := exif.ReadFile(p); err == nil {
		t.Error("junk: expected an error")
	}
	p = writeTemp(t, "trunc.jpg", testimg.JPEG(testimg.BuildTIFF(binary.LittleEndian, ifd0, exifIFD))[:40])
	if _, _, err := exif.ReadFile(p); err == nil {
		t.Error("truncated: expected an error")
	}
	// IFD0 only, no Exif sub-IFD.
	p = writeTemp(t, "ifd0.tif", testimg.BuildTIFF(binary.LittleEndian, ifd0, nil))
	tags, _, err := exif.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if tags[exif.TagDateTime] != "2024:05:01 12:34:56" || tags[exif.TagDateTimeOriginal] != "" {
		t.Errorf("ifd0 only: %v", tags)
	}
	if !exif.IsSupportedFile("x.CR2") || exif.IsSupportedFile("x.txt") {
		t.Error("IsSupportedFile")
	}
}

func TestCanonTimeZone(t *testing.T) {
	canon := []testimg.Entry{testimg.ASCII(exif.TagMake, "Canon"), testimg.ASCII(exif.TagModel, "Canon PowerShot G9 X")}
	cases := []struct {
		name    string
		bo      binary.ByteOrder
		ifd0    []testimg.Entry
		exifIFD []testimg.Entry
		want    string // OffsetTimeOriginal
	}{
		{"canon-le", binary.LittleEndian, canon,
			[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, "2018:03:27 23:59:31"), testimg.CanonMakerNote(binary.LittleEndian, 120)}, "+02:00"},
		{"canon-be-negative", binary.BigEndian, canon,
			[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, "2018:03:27 23:59:31"), testimg.CanonMakerNote(binary.BigEndian, -330)}, "-05:30"},
		{"canon-utc", binary.LittleEndian, canon,
			[]testimg.Entry{testimg.CanonMakerNote(binary.LittleEndian, 0)}, "+00:00"},
		{"standard-tag-wins", binary.LittleEndian, canon,
			[]testimg.Entry{testimg.ASCII(exif.TagOffsetTimeOriginal, "+09:00"), testimg.CanonMakerNote(binary.LittleEndian, 120)}, "+09:00"},
		{"not-canon", binary.LittleEndian, []testimg.Entry{testimg.ASCII(exif.TagMake, "SONY")},
			[]testimg.Entry{testimg.CanonMakerNote(binary.LittleEndian, 120)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, "a.jpg", testimg.JPEG(testimg.BuildTIFF(c.bo, c.ifd0, c.exifIFD)))
			tags, _, err := exif.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if tags[exif.TagOffsetTimeOriginal] != c.want {
				t.Errorf("OffsetTimeOriginal = %q, want %q (tags %v)", tags[exif.TagOffsetTimeOriginal], c.want, tags)
			}
			if c.want != "" && c.name != "standard-tag-wins" && tags[exif.TagOffsetTime] != c.want {
				t.Errorf("OffsetTime = %q, want %q", tags[exif.TagOffsetTime], c.want)
			}
		})
	}
}

func TestNikonTimeZone(t *testing.T) {
	nikon := []testimg.Entry{testimg.ASCII(exif.TagMake, "NIKON CORPORATION"), testimg.ASCII(exif.TagModel, "NIKON Z 6")}
	dto := testimg.ASCII(exif.TagDateTimeOriginal, "2018:03:27 23:59:31")
	cases := []struct {
		name    string
		bo      binary.ByteOrder
		ifd0    []testimg.Entry
		exifIFD []testimg.Entry
		want    string
	}{
		{"nikon-be", binary.BigEndian, nikon, []testimg.Entry{dto, testimg.NikonMakerNote(binary.BigEndian, 60, false)}, "+01:00"},
		{"nikon-dst-adds-hour", binary.BigEndian, nikon, []testimg.Entry{dto, testimg.NikonMakerNote(binary.BigEndian, 60, true)}, "+02:00"},
		{"nikon-mixed-order", binary.LittleEndian, nikon, []testimg.Entry{dto, testimg.NikonMakerNote(binary.BigEndian, -300, false)}, "-05:00"},
		{"nikon-headerless", binary.LittleEndian, nikon, []testimg.Entry{dto, testimg.NikonMakerNoteHeaderless(binary.LittleEndian, 330, false)}, "+05:30"},
		{"standard-tag-wins", binary.BigEndian, nikon, []testimg.Entry{testimg.ASCII(exif.TagOffsetTimeOriginal, "+09:00"), testimg.NikonMakerNote(binary.BigEndian, 60, true)}, "+09:00"},
		{"canon-note-in-nikon-file", binary.LittleEndian, nikon, []testimg.Entry{dto, testimg.CanonMakerNote(binary.LittleEndian, 120)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, "a.jpg", testimg.JPEG(testimg.BuildTIFF(c.bo, c.ifd0, c.exifIFD)))
			tags, _, err := exif.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if tags[exif.TagOffsetTimeOriginal] != c.want {
				t.Errorf("OffsetTimeOriginal = %q, want %q (tags %v)", tags[exif.TagOffsetTimeOriginal], c.want, tags)
			}
		})
	}
}

func TestGPS(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		ifd := append([]testimg.Entry{testimg.GPS(bo, "2024:05:01", 10, 34, 50.5)}, ifd0...)
		p := writeTemp(t, "gps.jpg", testimg.JPEG(testimg.BuildTIFF(bo, ifd, exifIFD)))
		tags, _, err := exif.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if tags[exif.TagGPSDateStamp] != "2024:05:01" || tags[exif.TagGPSTimeStamp] != "10:34:50.5" || tags[exif.TagDateTimeOriginal] != "2024:05:01 12:34:56" {
			t.Errorf("%v: tags = %v", bo, tags)
		}
	}
	// A GPS pointer to garbage keeps the other tags.
	bad := append([]testimg.Entry{{Tag: exif.TagGPSIFD, Type: 4, Data: []byte{0xFF, 0xFF, 0, 0}}}, ifd0...)
	tags, _, err := exif.ReadFile(writeTemp(t, "bad.jpg", testimg.JPEG(testimg.BuildTIFF(binary.LittleEndian, bad, exifIFD))))
	if err != nil || tags[exif.TagMake] != "Canon" || tags[exif.TagGPSTimeStamp] != "" {
		t.Errorf("bad GPS pointer: %v %v", tags, err)
	}
	// CR3 keeps GPS in CMT4, a TIFF block whose first IFD is the GPS IFD.
	cr3 := testimg.CR3(
		testimg.BuildTIFF(binary.LittleEndian, ifd0, nil),
		testimg.BuildTIFF(binary.LittleEndian, exifIFD, nil),
		testimg.GPSBlock(binary.BigEndian, "2024:05:01", 10, 34, 50),
	)
	tags, _, err = exif.ReadFile(writeTemp(t, "a.cr3", cr3))
	if err != nil || tags[exif.TagGPSTimeStamp] != "10:34:50" || tags[exif.TagGPSDateStamp] != "2024:05:01" || tags[exif.TagMake] != "Canon" {
		t.Errorf("cr3: %v %v", tags, err)
	}
}

func TestGPSPosition(t *testing.T) {
	for _, c := range []struct {
		lat, lon         float64
		wantLat, wantLon string
	}{
		{54.4250, 13.4350, "54.4250000", "13.4350000"},
		{-33.8568, -151.2153, "-33.8568000", "-151.2153000"},
		{0, 0, "", ""}, // no fix
	} {
		ifd := append([]testimg.Entry{testimg.GPSAt(binary.BigEndian, "2017:08:10", 9, 0, 0, c.lat, c.lon)}, ifd0...)
		tags, _, err := exif.ReadFile(writeTemp(t, "pos.jpg", testimg.JPEG(testimg.BuildTIFF(binary.BigEndian, ifd, exifIFD))))
		if err != nil {
			t.Fatal(err)
		}
		if tags[exif.TagGPSLatitude] != c.wantLat || tags[exif.TagGPSLongitude] != c.wantLon || tags[exif.TagGPSTimeStamp] != "09:00:00" {
			t.Errorf("%v,%v: tags = %v", c.lat, c.lon, tags)
		}
	}
}

func TestOrientation(t *testing.T) {
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		entries := append([]testimg.Entry{testimg.Short(bo, exif.TagOrientation, 6)}, ifd0...)
		tags, _, err := exif.ReadFile(writeTemp(t, "o.jpg", testimg.JPEG(testimg.BuildTIFF(bo, entries, nil))))
		if err != nil || tags[exif.TagOrientation] != "6" {
			t.Errorf("%v: orientation = %q, %v", bo, tags[exif.TagOrientation], err)
		}
	}
	// Out-of-range values are dropped.
	entries := append([]testimg.Entry{testimg.Short(binary.LittleEndian, exif.TagOrientation, 9)}, ifd0...)
	tags, _, _ := exif.ReadFile(writeTemp(t, "o.jpg", testimg.JPEG(testimg.BuildTIFF(binary.LittleEndian, entries, nil))))
	if _, ok := tags[exif.TagOrientation]; ok {
		t.Error("orientation 9 kept")
	}
}

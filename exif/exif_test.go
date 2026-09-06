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

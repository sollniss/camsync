package camsync

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/internal/testimg"
	"github.com/sollniss/camsync/xmp"
)

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFields(t *testing.T, path string) xmp.Fields {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := xmp.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestShiftAndReset(t *testing.T) {
	dir := t.TempDir()
	img := testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00"))
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), img)
	mustWrite(t, filepath.Join(dir, "DSC_0001.JPG"), testimg.JPEG(testimg.Simple("SONY", "ILCE-7M3", "2024:05:01 12:00:30")))
	mustWrite(t, filepath.Join(dir, "notes.txt"), []byte("hello"))
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "sub", "IMG_0002.JPG"), img)

	res, err := Scan(context.Background(), dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Photos) != 2 || res.Ignored != 1 {
		t.Fatalf("non-recursive scan: %d photos, %d ignored", len(res.Photos), res.Ignored)
	}
	res, err = Scan(context.Background(), dir, ScanOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	groups := GroupPhotos(res.Photos)
	if len(groups) != 2 || groups[0].Camera != "Canon EOS R5" || groups[1].Camera != "SONY ILCE-7M3" {
		t.Fatalf("groups = %+v", groups)
	}
	g, err := SelectGroup(groups, "r5")
	if err != nil || g.Camera != "Canon EOS R5" {
		t.Fatalf("SelectGroup: %v %v", g, err)
	}
	if _, err := SelectGroup(groups, "o"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous: %v", err)
	}
	if g, err := SelectGroup(groups, "2"); err != nil || g.Camera != "SONY ILCE-7M3" {
		t.Errorf("by index: %v %v", g, err)
	}

	for _, p := range g.Photos {
		ch, err := PlanShift(p, Shift{By: 150 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if created, err := ch.Write(true); err != nil || !created {
			t.Fatalf("dry run: %v %v", created, err)
		}
		if _, err := os.Stat(p.SidecarTarget); err == nil {
			t.Fatal("dry run created a sidecar")
		}
		if _, err := ch.Write(false); err != nil {
			t.Fatal(err)
		}
	}
	f := readFields(t, filepath.Join(dir, "sub", "IMG_0002.JPG.xmp"))
	for _, k := range []xmp.Field{xmp.DateTimeOriginal, xmp.DateTimeDigitized, xmp.CreateDate, xmp.DateCreated} {
		if f[k] != "2024-05-01T12:02:30" {
			t.Errorf("%v = %q", k, f[k])
		}
	}
	if f[xmp.Model] != "Canon EOS R5" {
		t.Errorf("model = %q", f[xmp.Model])
	}
	if _, err := os.Stat(filepath.Join(dir, "DSC_0001.JPG.xmp")); err == nil {
		t.Error("sony photo got a sidecar")
	}

	// Second shift accumulates; the offset is visible after a rescan.
	res, _ = Scan(context.Background(), dir, ScanOptions{Recursive: true})
	for _, p := range res.Photos {
		if p.Camera != "Canon EOS R5" {
			continue
		}
		if off, ok := p.Offset(); !ok || off != 150*time.Second {
			t.Errorf("%s offset = %v %v", p.Path, off, ok)
		}
		ch, err := PlanShift(p, Shift{By: -30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if created, err := ch.Write(false); err != nil || created {
			t.Fatalf("second write: created=%v err=%v", created, err)
		}
	}
	res, _ = Scan(context.Background(), dir, ScanOptions{Recursive: true})
	s := SelectGroupOrDie(t, res, "canon").Summary()
	if s.OffsetText() != "+2m" || s.Sidecars != 2 {
		t.Errorf("summary = %+v", s)
	}

	// Reset restores the file values; a photo without sidecar is skipped.
	for _, p := range res.Photos {
		ch, err := PlanReset(p)
		if p.Camera != "Canon EOS R5" {
			if err == nil {
				t.Errorf("%s: reset should be skipped", p.Path)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Write(false); err != nil {
			t.Fatal(err)
		}
	}
	if f := readFields(t, filepath.Join(dir, "IMG_0001.JPG.xmp")); f[xmp.DateTimeOriginal] != "2024-05-01T12:00:00" {
		t.Errorf("after reset: %v", f)
	}

	if after, _ := os.ReadFile(filepath.Join(dir, "IMG_0001.JPG")); !bytes.Equal(img, after) {
		t.Fatal("image file was modified")
	}
}

func SelectGroupOrDie(t *testing.T, res *ScanResult, q string) *Group {
	t.Helper()
	g, err := SelectGroup(GroupPhotos(res.Photos), q)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSidecarStylesAndCollisions(t *testing.T) {
	dir := t.TempDir()
	img := testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00"))
	mustWrite(t, filepath.Join(dir, "A.JPG"), img)
	mustWrite(t, filepath.Join(dir, "A.DNG"), img)
	mustWrite(t, filepath.Join(dir, "B.JPG"), img)
	// A pre-existing ext-style sidecar must be found regardless of style.
	mustWrite(t, filepath.Join(dir, "B.JPG.xmp"), xmp.New(xmp.Fields{xmp.DateTimeOriginal: "2024-05-01T12:05:00"}))

	res, err := Scan(context.Background(), dir, ScanOptions{SidecarStyle: SidecarByName})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Photo{}
	for _, p := range res.Photos {
		byName[filepath.Base(p.Path)] = p
	}
	if byName["A.JPG"].Err == nil || byName["A.DNG"].Err == nil {
		t.Error("shared sidecar collision not detected")
	}
	if _, err := PlanShift(byName["A.JPG"], Shift{By: time.Second}); err == nil {
		t.Error("colliding photo must not be planned")
	}
	if byName["B.JPG"].Sidecar != filepath.Join(dir, "B.JPG.xmp") {
		t.Errorf("B sidecar = %q", byName["B.JPG"].Sidecar)
	}
	if off, ok := byName["B.JPG"].Offset(); !ok || off != 5*time.Minute {
		t.Errorf("B offset = %v %v", off, ok)
	}

	res, _ = Scan(context.Background(), dir, ScanOptions{SidecarStyle: SidecarByExt})
	for _, p := range res.Photos {
		ch, err := PlanShift(p, Shift{By: time.Minute})
		if err != nil {
			t.Fatal(p.Path, err)
		}
		if _, err := ch.Write(false); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"A.JPG.xmp", "A.DNG.xmp", "B.JPG.xmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if f := readFields(t, filepath.Join(dir, "B.JPG.xmp")); f[xmp.DateTimeOriginal] != "2024-05-01T12:06:00" {
		t.Errorf("B after shift: %v", f)
	}
}

func TestSidecarWithoutDateFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "A.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	mustWrite(t, filepath.Join(dir, "A.xmp"), []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:darktable="http://darktable.sf.net/" darktable:xmp_version="5"/></rdf:RDF></x:xmpmeta>`))
	res, err := Scan(context.Background(), dir, ScanOptions{})
	if err != nil || len(res.Photos) != 1 {
		t.Fatal(err, res)
	}
	p := res.Photos[0]
	if p.Original == nil || p.Original.String() != "2024-05-01 12:00:00" || p.Sidecar == "" {
		t.Fatalf("photo = %+v", p)
	}
	ch, err := PlanShift(p, Shift{By: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Write(false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "A.xmp"))
	if !strings.Contains(string(data), `darktable:xmp_version="5"`) || !strings.Contains(string(data), `<exif:DateTimeOriginal>2024-05-01T12:00:10</exif:DateTimeOriginal>`) {
		t.Errorf("sidecar:\n%s", data)
	}
}

func TestCameraName(t *testing.T) {
	cases := [][3]string{
		{"Canon", "Canon EOS R5", "Canon EOS R5"},
		{"SONY", "ILCE-7M3", "SONY ILCE-7M3"},
		{"", "", "Unknown camera"},
		{"NIKON CORPORATION", "", "NIKON CORPORATION"},
		{"", "X-T5", "X-T5"},
	}
	for _, c := range cases {
		if got := CameraName(c[0], c[1]); got != c[2] {
			t.Errorf("CameraName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"Snowcorp SODA 10.3.2 / 26.6.2":     "Snowcorp SODA",
		"Adobe Photoshop CC 2019 (Windows)": "Adobe Photoshop CC",
		"darktable v4.6.1":                  "darktable",
		"17.1":                              "",
		"  ":                                "",
		"Snapseed":                          "Snapseed",
	} {
		if got := AppName(in); got != want {
			t.Errorf("AppName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Apps like SODA keep an EXIF block with every value blanked and record the
// date in the embedded XMP packet or IPTC block only.
func TestEmbeddedDatesFillBlankExif(t *testing.T) {
	dir := t.TempDir()
	blank := testimg.BuildTIFF(binary.BigEndian, []testimg.Entry{testimg.ASCII(exif.TagMake, ""), testimg.ASCII(exif.TagModel, "")}, nil)
	const soda = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"` +
		` xmp:CreateDate="2026-09-11T21:12:47" xmp:CreatorTool="Snowcorp SODA 10.3.2 / 26.6.2" photoshop:DateCreated="2026-09-11T21:12:47"/>` +
		`</rdf:RDF></x:xmpmeta>`
	iptc := testimg.IPTCSegment(
		testimg.IPTCDataset{Key: exif.IPTCDateCreated, Value: "20260911"},
		testimg.IPTCDataset{Key: exif.IPTCTimeCreated, Value: "211258"},
	)
	mustWrite(t, filepath.Join(dir, "xmp.jpg"), testimg.JPEGWith(blank, testimg.XMPSegment(soda), iptc))
	mustWrite(t, filepath.Join(dir, "iptc.jpg"), testimg.JPEGWith(blank, iptc))
	// EXIF wins over both.
	mustWrite(t, filepath.Join(dir, "exif.jpg"), testimg.JPEGWith(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00"), testimg.XMPSegment(soda), iptc))

	for _, c := range []struct{ file, camera, original string }{
		{"xmp.jpg", "Snowcorp SODA", "2026-09-11T21:12:47"},
		{"iptc.jpg", UnknownCamera, "2026-09-11T21:12:58"},
		{"exif.jpg", "Canon EOS R5", "2024-05-01T12:00:00"},
	} {
		p := LoadPhoto(filepath.Join(dir, c.file), SidecarByExt)
		if p.Err != nil || p.Camera != c.camera || p.FileOriginal == nil || p.Original == nil || p.Original.XMP() != c.original {
			t.Errorf("%s: err %v, camera %q, original %v, want %q %q", c.file, p.Err, p.Camera, p.Original, c.camera, c.original)
		}
		if p.Make != "" && c.camera == "Snowcorp SODA" {
			t.Errorf("%s: the app name must not become Make (it would be written to sidecars): %q", c.file, p.Make)
		}
	}
}

func TestZones(t *testing.T) {
	dir := t.TempDir()
	// Canon file: zone comes from the maker note and must survive the shift.
	mustWrite(t, filepath.Join(dir, "canon.jpg"), testimg.JPEG(testimg.BuildTIFF(binary.LittleEndian,
		[]testimg.Entry{testimg.ASCII(exif.TagMake, "Canon"), testimg.ASCII(exif.TagModel, "Canon PowerShot G9 X")},
		[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, "2018:03:27 23:59:31"), testimg.ASCII(exif.TagSubSecTimeOriginal, "09"), testimg.CanonMakerNote(binary.LittleEndian, 120)})))
	// File with only the generic OffsetTime tag: it is used for the original too.
	mustWrite(t, filepath.Join(dir, "offset.jpg"), testimg.JPEG(testimg.BuildTIFF(binary.LittleEndian,
		[]testimg.Entry{testimg.ASCII(exif.TagMake, "Nikon"), testimg.ASCII(exif.TagModel, "Z 6")},
		[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, "2018:03:27 23:59:31"), testimg.ASCII(exif.TagOffsetTime, "+01:00")})))
	// File without any zone: gets one only with Shift.Zone.
	mustWrite(t, filepath.Join(dir, "none.jpg"), testimg.JPEG(testimg.Simple("SONY", "ILCE-7M3", "2018:03:27 23:59:31")))

	res, err := Scan(context.Background(), dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Photo{}
	for _, p := range res.Photos {
		byName[filepath.Base(p.Path)] = p
	}
	if got := byName["canon.jpg"].Original.XMP(); got != "2018-03-27T23:59:31.09+02:00" {
		t.Errorf("canon original = %s", got)
	}
	if got := byName["offset.jpg"].Original.XMP(); got != "2018-03-27T23:59:31+01:00" {
		t.Errorf("offset original = %s", got)
	}
	if got := byName["none.jpg"].Original.XMP(); got != "2018-03-27T23:59:31" {
		t.Errorf("none original = %s", got)
	}

	ch, err := PlanShift(byName["canon.jpg"], Shift{By: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if ch.NewOriginal.XMP() != "2018-03-28T00:00:31.09+02:00" {
		t.Errorf("canon shifted = %s", ch.NewOriginal.XMP())
	}
	ch, err = PlanShift(byName["none.jpg"], Shift{Zone: "+02:00"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.NewOriginal.XMP() != "2018-03-27T23:59:31+02:00" || ch.Fields[xmp.DateTimeDigitized] != "2018-03-27T23:59:31+02:00" {
		t.Errorf("zone only = %s / %v", ch.NewOriginal.XMP(), ch.Fields)
	}
	if _, err := ch.Write(false); err != nil {
		t.Fatal(err)
	}
	res, _ = Scan(context.Background(), dir, ScanOptions{})
	for _, p := range res.Photos {
		if filepath.Base(p.Path) == "none.jpg" {
			if p.Original.TZ != "+02:00" {
				t.Errorf("zone not read back from sidecar: %+v", p.Original)
			}
			if off, ok := p.Offset(); !ok || off != 0 {
				t.Errorf("zone-only change must not count as an offset: %v %v", off, ok)
			}
		}
	}
}

func TestScanFollowsSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	mustWrite(t, filepath.Join(real, "IMG_0001.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	link := filepath.Join(t.TempDir(), "photos")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	res, err := Scan(context.Background(), link, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Photos) != 1 {
		t.Fatalf("got %d photos through the symlink, want 1", len(res.Photos))
	}
	// Paths are reported under the name the caller gave, not the target.
	if want := filepath.Join(link, "IMG_0001.JPG"); res.Photos[0].Path != want {
		t.Errorf("path = %q, want %q", res.Photos[0].Path, want)
	}
}

func TestAtomicWriteErrorDoesNotDoubleClose(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "IMG_0001.JPG.xmp")
	if err := os.Mkdir(target, 0o755); err != nil { // rename onto a directory fails
		t.Fatal(err)
	}
	err := atomicWrite(target, []byte("x"))
	if err == nil {
		t.Fatal("expected rename to fail")
	}
	if strings.Contains(err.Error(), "already closed") {
		t.Errorf("error reports a double close: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary file was not cleaned up: %v", entries)
	}
}

func TestSelectGroupPrefersRowNumberOverSubstring(t *testing.T) {
	groups := []*Group{{Camera: "Nikon D2X"}, {Camera: "Canon EOS R5"}, {Camera: "SONY ILCE-7M3"}}
	cases := map[string]string{
		"2":     "Canon EOS R5",  // row 2, although "Nikon D2X" contains a 2
		"5":     "Canon EOS R5",  // no row 5, so substring
		"d2x":   "Nikon D2X",     // substring
		"sony":  "SONY ILCE-7M3", // substring
		"nikon": "Nikon D2X",
	}
	for q, want := range cases {
		g, err := SelectGroup(groups, q)
		if err != nil || g.Camera != want {
			t.Errorf("SelectGroup(%q) = %v, %v; want %s", q, g, err, want)
		}
	}
	for _, q := range []string{"0", "4", "olympus", ""} {
		if g, err := SelectGroup(groups, q); err == nil {
			t.Errorf("SelectGroup(%q) = %v; want error", q, g.Camera)
		}
	}
}

func TestScanStopsWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, dir, ScanOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Scan with cancelled context: %v", err)
	}
}

func TestSidecarErrorsAreWrapped(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	if err := os.Mkdir(filepath.Join(dir, "IMG_0001.JPG.xmp"), 0o755); err != nil { // a directory where the sidecar should be
		t.Fatal(err)
	}
	p := LoadPhoto(filepath.Join(dir, "IMG_0001.JPG"), SidecarByExt)
	if p.Err != nil {
		t.Errorf("a directory is not a sidecar, got %v", p.Err)
	}
	mustWrite(t, filepath.Join(dir, "IMG_0002.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	mustWrite(t, filepath.Join(dir, "IMG_0002.JPG.xmp"), []byte("<broken"))
	p = LoadPhoto(filepath.Join(dir, "IMG_0002.JPG"), SidecarByExt)
	var syntaxErr *xml.SyntaxError
	if !errors.As(p.Err, &syntaxErr) {
		t.Errorf("sidecar parse error is not wrapped: %v", p.Err)
	}
}

func TestSidecarLookupFromListing(t *testing.T) {
	dir := t.TempDir()
	img := testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00"))
	sidecar := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="` + xmp.NSRDF + `"><rdf:Description xmlns:exif="` + xmp.NSExif + `" exif:DateTimeOriginal="2024-05-01T13:00:00"/></rdf:RDF></x:xmpmeta>`)

	// Odd casing of the extension is found on every file system.
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), img)
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG.Xmp"), sidecar)
	// A sidecar that is a symlink to a file counts; a dangling one does not.
	mustWrite(t, filepath.Join(dir, "IMG_0002.JPG"), img)
	mustWrite(t, filepath.Join(dir, "real.xmp"), sidecar)
	mustWrite(t, filepath.Join(dir, "IMG_0003.JPG"), img)
	linksOK := os.Symlink(filepath.Join(dir, "real.xmp"), filepath.Join(dir, "IMG_0002.JPG.xmp")) == nil &&
		os.Symlink(filepath.Join(dir, "gone.xmp"), filepath.Join(dir, "IMG_0003.JPG.xmp")) == nil

	res, err := Scan(context.Background(), dir, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Photo{}
	for _, p := range res.Photos {
		byName[filepath.Base(p.Path)] = p
	}
	if p := byName["IMG_0001.JPG"]; p.Sidecar != filepath.Join(dir, "IMG_0001.JPG.Xmp") || p.Original == nil || p.Original.T.Hour() != 13 {
		t.Errorf("mixed-case sidecar not used: %+v", p)
	}
	if linksOK {
		if p := byName["IMG_0002.JPG"]; p.Sidecar != filepath.Join(dir, "IMG_0002.JPG.xmp") || p.Original.T.Hour() != 13 {
			t.Errorf("symlinked sidecar not used: %+v", p)
		}
		if p := byName["IMG_0003.JPG"]; p.Sidecar != "" || p.Err != nil {
			t.Errorf("dangling symlink treated as sidecar: %+v", p)
		}
	}
	// The stray real.xmp is a sidecar by extension and not counted as ignored.
	if res.Ignored != 0 {
		t.Errorf("ignored = %d, want 0", res.Ignored)
	}
}

func TestScanWorkersAgree(t *testing.T) {
	dir := t.TempDir()
	for i := range 40 {
		name := fmt.Sprintf("IMG_%04d.JPG", i)
		mustWrite(t, filepath.Join(dir, name), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", fmt.Sprintf("2024:05:01 12:%02d:00", i))))
		if i%3 == 0 {
			mustWrite(t, filepath.Join(dir, name+".xmp"), xmp.New(xmp.Fields{xmp.DateTimeOriginal: fmt.Sprintf("2024-05-01T13:%02d:00", i)}))
		}
	}
	serial, err := Scan(context.Background(), dir, ScanOptions{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := Scan(context.Background(), dir, ScanOptions{Workers: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(serial.Photos) != 40 || len(parallel.Photos) != 40 {
		t.Fatalf("%d and %d photos, want 40", len(serial.Photos), len(parallel.Photos))
	}
	for i := range serial.Photos {
		a, b := serial.Photos[i], parallel.Photos[i]
		if a.Path != b.Path || a.Sidecar != b.Sidecar || a.Original.XMP() != b.Original.XMP() {
			t.Errorf("photo %d differs: %+v vs %+v", i, a, b)
		}
		if a.Path != filepath.Join(dir, fmt.Sprintf("IMG_%04d.JPG", i)) {
			t.Errorf("photo %d out of order: %s", i, a.Path)
		}
	}
}

func TestWriteUsesLoadedSidecar(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	sc := filepath.Join(dir, "IMG_0001.JPG.xmp")
	mustWrite(t, sc, xmp.New(xmp.Fields{xmp.DateTimeOriginal: "2024-05-01T12:00:00", xmp.Model: "kept"}))
	p := LoadPhoto(filepath.Join(dir, "IMG_0001.JPG"), SidecarByExt)
	// Replace the sidecar on disk after loading: Write must still start from
	// the document it planned against.
	mustWrite(t, sc, xmp.New(xmp.Fields{xmp.Model: "changed"}))
	ch, err := PlanShift(p, Shift{By: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Write(false); err != nil {
		t.Fatal(err)
	}
	if f := readFields(t, sc); f[xmp.Model] != "kept" || f[xmp.DateTimeOriginal] != "2024-05-01T12:01:00" {
		t.Errorf("written from disk instead of loaded content: %v", f)
	}
}

func BenchmarkScan(b *testing.B) {
	dir := b.TempDir()
	img := testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00"))
	for i := range 500 {
		name := filepath.Join(dir, fmt.Sprintf("IMG_%04d.JPG", i))
		if err := os.WriteFile(name, img, 0o644); err != nil {
			b.Fatal(err)
		}
		if i%2 == 0 {
			if err := os.WriteFile(name+".xmp", xmp.New(xmp.Fields{xmp.DateTimeOriginal: "2024-05-01T13:00:00"}), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	for _, workers := range []int{1, DefaultWorkers} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			for b.Loop() {
				if _, err := Scan(context.Background(), dir, ScanOptions{Workers: workers}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

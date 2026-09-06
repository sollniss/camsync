// Package camsync shifts the timestamps of photos taken with one camera
// by writing XMP sidecar files. Image files are never modified.
//
// The typical flow is Scan to load the photos of a directory, GroupPhotos to
// group them by camera, then PlanShift (or PlanReset) and Change.Write for
// every photo of the chosen group. Timestamps are read from the sidecar when
// it has them and from the image file otherwise, so shifts accumulate.
package camsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/xmp"
)

// SidecarStyle selects the file name used for newly created sidecars.
type SidecarStyle int

const (
	// SidecarByExt names sidecars IMG_0001.JPG.xmp. This is what Immich,
	// exiftool and darktable use and it never collides between a JPEG and a
	// raw file of the same name. It is the default.
	SidecarByExt SidecarStyle = iota
	// SidecarByName names sidecars IMG_0001.xmp (Adobe, digiKam).
	SidecarByName
)

// ParseSidecarStyle parses "ext" or "name"; the empty string means "ext".
func ParseSidecarStyle(s string) (SidecarStyle, error) {
	switch strings.ToLower(s) {
	case "ext", "":
		return SidecarByExt, nil
	case "name":
		return SidecarByName, nil
	}
	return 0, fmt.Errorf("unknown sidecar style %q (want name or ext)", s)
}

// SidecarPaths returns the path a new sidecar would get for the given style
// and every candidate path an existing sidecar may have, preferred first.
func SidecarPaths(path string, style SidecarStyle) (preferred string, candidates []string) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	if style == SidecarByExt {
		return path + ".xmp", []string{path + ".xmp", path + ".XMP", base + ".xmp", base + ".XMP"}
	}
	return base + ".xmp", []string{base + ".xmp", base + ".XMP", path + ".xmp", path + ".XMP"}
}

// findSidecar returns the first candidate that exists as a regular file.
func findSidecar(candidates []string) string {
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c
		}
	}
	return ""
}

// fileSet is the listing a scan collects, so that sidecars can be looked
// up without going back to the file system. Lookups are exact first and
// case-insensitive second, so IMG_0001.jpg.XMP is found for IMG_0001.jpg on
// every file system.
type fileSet struct {
	exact  map[string]bool
	folded map[string]string // lower-cased path -> first path seen
}

func (s *fileSet) add(path string) {
	if s.exact == nil {
		s.exact, s.folded = map[string]bool{}, map[string]string{}
	}
	s.exact[path] = true
	if key := strings.ToLower(path); s.folded[key] == "" {
		s.folded[key] = path
	}
}

// find returns the first candidate present in the set, or "".
func (s *fileSet) find(candidates []string) string {
	for _, c := range candidates {
		if s.exact[c] {
			return c
		}
	}
	for _, c := range candidates {
		if p := s.folded[strings.ToLower(c)]; p != "" {
			return p
		}
	}
	return ""
}

// Photo is one image file together with its sidecar state.
type Photo struct {
	Path   string
	Format string // short container label such as "JPEG" or "NEF"
	Camera string // display name derived from Make and Model
	Make   string
	Model  string

	// Values from the image file itself, which is never modified.
	FileOriginal  *Stamp
	FileDigitized *Stamp

	// Effective values: from the sidecar when it has them, else from the file.
	Original  *Stamp
	Digitized *Stamp

	Sidecar       string // existing sidecar path, "" if none
	SidecarTarget string // where a sidecar will be written
	Err           error  // problem that prevents shifting this photo

	sidecarData []byte // content of Sidecar as read while loading, if any
}

// Offset is the shift currently applied via the sidecar, if it can be
// determined.
func (p *Photo) Offset() (time.Duration, bool) {
	if p.Original == nil || p.FileOriginal == nil {
		return 0, false
	}
	return p.Original.Sub(*p.FileOriginal), true
}

// CameraName builds the display name for a camera, avoiding the duplicated
// vendor that models like "Canon EOS R5" would otherwise get.
func CameraName(mk, model string) string {
	mk, model = strings.TrimSpace(mk), strings.TrimSpace(model)
	switch {
	case mk == "" && model == "":
		return "Unknown camera"
	case model == "":
		return mk
	case mk == "" || strings.HasPrefix(strings.ToLower(model), strings.ToLower(mk)):
		return model
	}
	return mk + " " + model
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// stampsFromExif derives the timestamps from raw EXIF tags. The original
// falls back from DateTimeOriginal to DateTimeDigitized to DateTime. Cameras
// set all three OffsetTime tags to the same value, so any of them serves as
// the zone when the matching one is missing.
func stampsFromExif(t exif.Tags) (original, digitized *Stamp) {
	zone := firstNonEmpty(t[exif.TagOffsetTimeOriginal], t[exif.TagOffsetTime], t[exif.TagOffsetTimeDigitized])
	candidates := [][2]uint16{
		{exif.TagDateTimeOriginal, exif.TagSubSecTimeOriginal},
		{exif.TagDateTimeDigitized, exif.TagSubSecTimeDigitized},
		{exif.TagDateTime, exif.TagSubSecTime},
	}
	for _, c := range candidates {
		if s, ok := ParseExifStamp(t[c[0]], firstNonEmpty(t[c[1]], t[exif.TagSubSecTime]), zone); ok {
			original = &s
			break
		}
	}
	if s, ok := ParseExifStamp(t[exif.TagDateTimeDigitized], firstNonEmpty(t[exif.TagSubSecTimeDigitized], t[exif.TagSubSecTime]), zone); ok {
		digitized = &s
	}
	return original, digitized
}

// LoadPhoto reads the EXIF data of one image and its sidecar, if any.
// Problems are recorded in Photo.Err rather than returned, so the photo can
// still be listed.
func LoadPhoto(path string, style SidecarStyle) *Photo {
	preferred, candidates := SidecarPaths(path, style)
	return loadPhoto(path, preferred, findSidecar(candidates))
}

// loadPhoto does the work of LoadPhoto with the sidecar already resolved:
// target is where a new sidecar would go and sidecar the existing one, or "".
func loadPhoto(path, target, sidecar string) *Photo {
	p := &Photo{Path: path, SidecarTarget: target}
	tags, format, err := exif.ReadFile(path)
	p.Format = format
	if err != nil && !errors.Is(err, exif.ErrNotFound) {
		p.Err = err
	}
	p.Make, p.Model = tags[exif.TagMake], tags[exif.TagModel]
	p.FileOriginal, p.FileDigitized = stampsFromExif(tags)
	p.Original, p.Digitized = p.FileOriginal, p.FileDigitized

	if sc := sidecar; sc != "" {
		p.Sidecar, p.SidecarTarget = sc, sc
		data, err := os.ReadFile(sc)
		var fields xmp.Fields
		if err == nil {
			p.sidecarData = data // reused by Change.Write
			fields, err = xmp.Read(data)
		}
		if err != nil {
			p.Err = fmt.Errorf("sidecar %s: %w", sc, err)
		}
		if v := strings.TrimSpace(fields[xmp.Make]); v != "" {
			p.Make = v
		}
		if v := strings.TrimSpace(fields[xmp.Model]); v != "" {
			p.Model = v
		}
		for _, f := range []xmp.Field{xmp.DateTimeOriginal, xmp.CreateDate, xmp.DateCreated} {
			if s, ok := ParseXMPStamp(fields[f]); ok {
				p.Original = &s
				break
			}
		}
		if s, ok := ParseXMPStamp(fields[xmp.DateTimeDigitized]); ok {
			p.Digitized = &s
		}
	}
	p.Camera = CameraName(p.Make, p.Model)
	return p
}

// ScanOptions controls Scan.
type ScanOptions struct {
	Recursive    bool         // descend into sub-directories (hidden ones are skipped)
	SidecarStyle SidecarStyle // naming of sidecars that do not exist yet
	Workers      int          // photos read concurrently; 0 selects DefaultWorkers
}

// DefaultWorkers is the number of photos Scan reads concurrently when
// ScanOptions.Workers is zero. Reading a photo is a handful of small reads
// bound by storage latency, so a few in flight help a lot on SSDs and
// network shares while still being gentle on spinning disks.
const DefaultWorkers = 8

// ScanResult is what Scan found.
type ScanResult struct {
	Photos  []*Photo // sorted by path
	Ignored int      // regular files that are neither photos nor sidecars
}

// Scan loads every photo in dir. Two images that only differ in extension
// (IMG_1.JPG and IMG_1.CR2) would share IMG_1.xmp under SidecarByName; such
// photos get an Err so they are never shifted twice through one sidecar.
//
// The walk stops with ctx.Err() as soon as ctx is cancelled.
func Scan(ctx context.Context, dir string, opts ScanOptions) (*ScanResult, error) {
	// WalkDir does not follow a symbolic link given as the root, so a linked
	// photo directory would scan as empty. Walk the resolved directory but
	// report paths under the name the caller gave.
	walkRoot := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != filepath.Clean(dir) {
		walkRoot = resolved
	}
	// Pass one lists the files. Sidecars are then resolved against that
	// listing instead of probing the file system for every candidate name.
	res := &ScanResult{}
	var paths []string
	var files fileSet
	err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path == walkRoot {
				return nil
			}
			if !opts.Recursive || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		isXMP := strings.EqualFold(filepath.Ext(path), ".xmp")
		switch {
		case d.Type().IsRegular():
		case isXMP && d.Type()&fs.ModeSymlink != 0:
			// A sidecar reached through a symlink counts if it is a file.
			if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
				return nil
			}
		default:
			return nil
		}
		if walkRoot != dir {
			if rel, err := filepath.Rel(walkRoot, path); err == nil {
				path = filepath.Join(dir, rel)
			}
		}
		files.add(path)
		switch {
		case exif.IsSupportedFile(path):
			paths = append(paths, path)
		case !isXMP:
			res.Ignored++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Pass two reads the photos, a few at a time. Every goroutine writes its
	// own slot, so the result is in walk order without further coordination.
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	res.Photos = make([]*Photo, len(paths))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, path := range paths {
		if ctx.Err() != nil {
			break
		}
		target, candidates := SidecarPaths(path, opts.SidecarStyle)
		sidecar := files.find(candidates)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			res.Photos[i] = loadPhoto(path, target, sidecar)
			<-sem
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(res.Photos, func(i, j int) bool { return res.Photos[i].Path < res.Photos[j].Path })

	byTarget := map[string][]*Photo{}
	for _, p := range res.Photos {
		byTarget[p.SidecarTarget] = append(byTarget[p.SidecarTarget], p)
	}
	for target, ps := range byTarget {
		if len(ps) < 2 {
			continue
		}
		names := make([]string, 0, len(ps))
		for _, p := range ps {
			names = append(names, filepath.Base(p.Path))
		}
		for _, p := range ps {
			if p.Err == nil {
				p.Err = fmt.Errorf("sidecar %s would be shared by %s; use the ext sidecar style", filepath.Base(target), strings.Join(names, ", "))
			}
		}
	}
	return res, nil
}

// Group is the set of photos taken with one camera.
type Group struct {
	Camera string
	Photos []*Photo
}

// GroupPhotos groups photos by camera name, sorted by name with unknown
// cameras last.
func GroupPhotos(photos []*Photo) []*Group {
	byName := map[string]*Group{}
	for _, p := range photos {
		g := byName[p.Camera]
		if g == nil {
			g = &Group{Camera: p.Camera}
			byName[p.Camera] = g
		}
		g.Photos = append(g.Photos, p)
	}
	groups := make([]*Group, 0, len(byName))
	for _, g := range byName {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i].Camera, groups[j].Camera
		if (a == "Unknown camera") != (b == "Unknown camera") {
			return b == "Unknown camera"
		}
		return strings.ToLower(a) < strings.ToLower(b)
	})
	return groups
}

// Summary describes a group at a glance.
type Summary struct {
	First, Last *Stamp                // effective timestamp range
	Offsets     map[time.Duration]int // applied offset -> number of photos
	Sidecars    int
	NoDate      int
	Errors      int
}

// Summary computes the group's summary.
func (g *Group) Summary() Summary {
	s := Summary{Offsets: map[time.Duration]int{}}
	for _, p := range g.Photos {
		if p.Sidecar != "" {
			s.Sidecars++
		}
		if p.Err != nil {
			s.Errors++
		}
		if p.Original == nil {
			s.NoDate++
		} else {
			if s.First == nil || p.Original.T.Before(s.First.T) {
				s.First = p.Original
			}
			if s.Last == nil || p.Original.T.After(s.Last.T) {
				s.Last = p.Original
			}
		}
		if off, ok := p.Offset(); ok {
			s.Offsets[off]++
		}
	}
	return s
}

// OffsetText renders the applied offset: "-" when unknown, the offset when
// uniform, "mixed" otherwise.
func (s Summary) OffsetText() string {
	switch len(s.Offsets) {
	case 0:
		return "-"
	case 1:
		for d := range s.Offsets {
			return FormatDuration(d)
		}
	}
	return "mixed"
}

// Notes renders the irregularities of a group, or "" if there are none.
func (s Summary) Notes() string {
	var parts []string
	if len(s.Offsets) > 1 {
		type oc struct {
			d time.Duration
			n int
		}
		ocs := make([]oc, 0, len(s.Offsets))
		for d, n := range s.Offsets {
			ocs = append(ocs, oc{d, n})
		}
		sort.Slice(ocs, func(i, j int) bool { return ocs[i].n > ocs[j].n || (ocs[i].n == ocs[j].n && ocs[i].d < ocs[j].d) })
		offs := make([]string, 0, len(ocs))
		for _, o := range ocs {
			offs = append(offs, FormatDuration(o.d)+" ("+strconv.Itoa(o.n)+")")
		}
		parts = append(parts, "offsets: "+strings.Join(offs, ", "))
	}
	if s.NoDate > 0 {
		parts = append(parts, strconv.Itoa(s.NoDate)+" without date")
	}
	if s.Errors > 0 {
		parts = append(parts, strconv.Itoa(s.Errors)+" with errors")
	}
	return strings.Join(parts, "; ")
}

// SelectGroup finds a group by camera name: a case-insensitive exact match
// first, then the 1-based index in groups when the query is a number, then
// a unique substring. Numbers are tried before substrings because model
// names are full of digits ("EOS R5", "D850"), so "5" is far more likely to
// mean the fifth row than one of those cameras.
func SelectGroup(groups []*Group, query string) (*Group, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Camera)
		if strings.ToLower(g.Camera) == q {
			return g, nil
		}
	}
	if n, err := strconv.Atoi(q); err == nil && n >= 1 && n <= len(groups) {
		return groups[n-1], nil
	}
	var matches []string
	var match *Group
	for _, g := range groups {
		if strings.Contains(strings.ToLower(g.Camera), q) {
			matches = append(matches, g.Camera)
			match = g
		}
	}
	switch len(matches) {
	case 1:
		return match, nil
	case 0:
		return nil, fmt.Errorf("no camera matches %q; available: %s", query, strings.Join(names, " | "))
	}
	return nil, fmt.Errorf("camera %q is ambiguous: %s", query, strings.Join(matches, " | "))
}

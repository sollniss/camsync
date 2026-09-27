package camsync

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Layout says how the library's folders are meant to be organised.
type Layout string

const (
	// LayoutAuto keeps every folder's own scheme: trips found in the inbox
	// get a folder, trips inside period folders stay where they are.
	LayoutAuto Layout = "auto"
	// LayoutTrips gives every trip its own folder, pulling it out of period
	// folders as well.
	LayoutTrips Layout = "trips"
	// LayoutPeriods never creates trip folders.
	LayoutPeriods Layout = "periods"
)

// ParseLayout parses "auto", "trips" or "periods"; the empty string means
// "auto".
func ParseLayout(s string) (Layout, error) {
	switch l := Layout(strings.ToLower(s)); l {
	case "":
		return LayoutAuto, nil
	case LayoutAuto, LayoutTrips, LayoutPeriods:
		return l, nil
	}
	return "", fmt.Errorf("unknown layout %q (want auto, trips or periods)", s)
}

// LibraryOptions controls Organize.
type LibraryOptions struct {
	EventOptions
	// Inboxes are folders where photos land before they are sorted, given
	// as paths. Folders whose name says so (inbox, unsorted, import, DCIM
	// and the like) and the root itself, when it holds photos, count anyway.
	Inboxes []string
	Layout  Layout
}

// FolderKind says what a folder holds.
type FolderKind string

const (
	// FolderEvent holds one event, or a couple of events close together.
	FolderEvent FolderKind = "event"
	// FolderPeriod is a calendar year or month, by its name.
	FolderPeriod FolderKind = "period"
	// FolderInbox is where photos land before they are sorted.
	FolderInbox FolderKind = "inbox"
	// FolderMixed holds many events and follows no recognisable scheme.
	FolderMixed FolderKind = "mixed"
)

// Period is a calendar year or month, as named by a folder.
type Period struct {
	Start, End time.Time // [Start, End) on the wall clock
	Unit       string    // "year" or "month"
}

// Contains reports whether the wall clock t falls in the period.
func (p Period) Contains(t time.Time) bool { return !t.Before(p.Start) && t.Before(p.End) }

// Overlaps reports whether any of [first, last] falls in the period.
func (p Period) Overlaps(first, last time.Time) bool {
	return first.Before(p.End) && !last.Before(p.Start)
}

// String renders the period as 2006 or 2006-01.
func (p Period) String() string {
	if p.Unit == "year" {
		return p.Start.Format("2006")
	}
	return p.Start.Format("2006-01")
}

// Folder is one directory of the library and what it holds.
type Folder struct {
	Path   string
	Rel    string // relative to the library root; "." for the root itself
	Kind   FolderKind
	Period *Period // set for FolderPeriod
	// Photos are the folder's own photos, not those of sub-folders.
	Photos  []*Photo
	Undated int
	Cameras map[string]int
	// Events lists the events with photos in this folder, the one with the
	// most photos here first, and Counts how many of each.
	Events []*Event
	Counts map[*Event]int
	Note   string // why the folder was classified as it was

	layouts []string        // period layouts of the path components, "" for names that are none
	held    map[*Event]bool // see Holds
}

// Share is the fraction of the event's photos held by this folder.
func (f *Folder) Share(e *Event) float64 { return float64(f.Counts[e]) / float64(len(e.Photos)) }

// Main is the event with the most photos in the folder, nil when it holds
// no dated photo.
func (f *Folder) Main() *Event {
	if len(f.Events) == 0 {
		return nil
	}
	return f.Events[0]
}

// SuggestionKind is what a suggestion proposes.
type SuggestionKind string

const (
	// SuggestMove moves photos into an existing folder.
	SuggestMove SuggestionKind = "move"
	// SuggestCreate moves photos into a folder that does not exist yet.
	SuggestCreate SuggestionKind = "create"
	// SuggestMerge moves the whole content of a folder into another one,
	// leaving it empty.
	SuggestMerge SuggestionKind = "merge"
	// SuggestStray lists photos that do not belong where they are, with no
	// folder to move them to.
	SuggestStray SuggestionKind = "stray"
)

// Suggestion is one proposed change to the library.
type Suggestion struct {
	ID         string
	Kind       SuggestionKind
	Event      *Event
	From       *Folder
	To         string // destination directory; empty for SuggestStray
	Photos     []*Photo
	Reason     string
	Confidence Confidence
}

// Moves returns the file moves that carry out the suggestion.
func (s *Suggestion) Moves() []Move {
	if s.To == "" {
		return nil
	}
	moves := make([]Move, 0, len(s.Photos))
	for _, p := range s.Photos {
		moves = append(moves, Move{Path: p.Path, Sidecar: p.Sidecar, To: s.To})
	}
	return moves
}

// Note is an observation about the library that no move can fix.
type Note struct {
	Kind   string // "reset-clock", "undated", "no-camera"
	Text   string
	Folder *Folder
	Photos []*Photo
}

// Library is the picture Organize paints of a directory tree.
type Library struct {
	Root         string
	Home         *Position
	HomeInferred bool
	Events       []*Event
	Folders      []*Folder // sorted by path
	Suggestions  []*Suggestion
	Notes        []Note
	Undated      []*Photo
}

// Folder returns the folder at path, or nil.
func (l *Library) Folder(path string) *Folder {
	for _, f := range l.Folders {
		if f.Path == path {
			return f
		}
	}
	return nil
}

// Suggestion returns the suggestion with the id, or nil.
func (l *Library) Suggestion(id string) *Suggestion {
	for _, s := range l.Suggestions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Organize detects the events among the photos, works out what each folder
// under root holds and suggests where photos would rather be. See
// DetectEvents for the events, FolderKind for the folders and
// SuggestionKind for the suggestions. Nothing is moved.
func Organize(root string, photos []*Photo, opts LibraryOptions) *Library {
	root = filepath.Clean(root)
	if opts.Layout == "" {
		opts.Layout = LayoutAuto
	}
	lib := &Library{Root: root, HomeInferred: opts.Home == nil}
	lib.Events, lib.Home = DetectEvents(photos, opts.EventOptions)
	if lib.Home == nil {
		lib.HomeInferred = false
	}
	for _, p := range photos {
		if p.Original == nil {
			lib.Undated = append(lib.Undated, p)
		}
	}
	lib.Folders = buildFolders(root, photos, lib.Events)
	inboxes := map[string]bool{}
	for _, in := range opts.Inboxes {
		inboxes[filepath.Clean(in)] = true
	}
	for _, f := range lib.Folders {
		f.classify(inboxes[f.Path] || inboxes[filepath.Join(root, f.Rel)])
	}
	lib.suggest(opts.Layout)
	lib.observe()
	return lib
}

// buildFolders groups the photos by directory.
func buildFolders(root string, photos []*Photo, events []*Event) []*Folder {
	byPath := map[string]*Folder{}
	for _, p := range photos {
		dir := filepath.Dir(p.Path)
		f := byPath[dir]
		if f == nil {
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				rel = dir
			}
			f = &Folder{Path: dir, Rel: rel, Cameras: map[string]int{}, Counts: map[*Event]int{}}
			byPath[dir] = f
		}
		f.Photos = append(f.Photos, p)
		f.Cameras[p.Camera]++
		if p.Original == nil {
			f.Undated++
		}
	}
	for _, e := range events {
		for _, p := range e.Photos {
			f := byPath[filepath.Dir(p.Path)]
			if f.Counts[e] == 0 {
				f.Events = append(f.Events, e)
			}
			f.Counts[e]++
		}
	}
	folders := make([]*Folder, 0, len(byPath))
	for _, f := range byPath {
		sort.SliceStable(f.Events, func(i, j int) bool { return f.Counts[f.Events[i]] > f.Counts[f.Events[j]] })
		f.layouts, f.Period = parsePeriodPath(f.Rel)
		folders = append(folders, f)
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Path < folders[j].Path })
	return folders
}

var periodLayouts = []struct {
	re     *regexp.Regexp
	layout string
	unit   string
	parent string // required unit of the parent folder, "" for none
}{
	{regexp.MustCompile(`^\d{4}$`), "2006", "year", ""},
	{regexp.MustCompile(`^\d{4}-\d{2}$`), "2006-01", "month", ""},
	{regexp.MustCompile(`^\d{4}_\d{2}$`), "2006_01", "month", ""},
	{regexp.MustCompile(`^\d{4}\.\d{2}$`), "2006.01", "month", ""},
	{regexp.MustCompile(`^\d{6}$`), "200601", "month", ""},
	{regexp.MustCompile(`^\d{2}$`), "01", "month", "year"},
}

// parsePeriodPath reads the calendar period a relative folder path names:
// "2024", "2024-05", "2024/05". A name with more than the date in it, such
// as "2024-05 Rügen", names an event rather than a period. The layout of
// each path component is returned so that a sibling period can be named the
// same way; see periodPath.
func parsePeriodPath(rel string) (layouts []string, period *Period) {
	if rel == "." || rel == "" {
		return nil, nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	layouts = make([]string, len(parts))
	var t time.Time
	unit := ""
	for i, name := range parts {
		matched := false
		for _, pl := range periodLayouts {
			if !pl.re.MatchString(name) || pl.parent != "" && pl.parent != unit {
				continue
			}
			v, err := time.Parse(pl.layout, name)
			if err != nil || v.Year() < 1900 && pl.unit != "month" || v.Year() > 2100 {
				continue
			}
			if pl.parent != "" {
				v = time.Date(t.Year(), v.Month(), 1, 0, 0, 0, 0, time.UTC)
			}
			layouts[i], t, unit, matched = pl.layout, v, pl.unit, true
			break
		}
		if !matched {
			unit = ""
		}
	}
	if unit == "" {
		return layouts, nil
	}
	period = &Period{Start: t, Unit: unit}
	if unit == "year" {
		period.End = t.AddDate(1, 0, 0)
	} else {
		period.End = t.AddDate(0, 1, 0)
	}
	return layouts, period
}

// periodPath names the folder that would hold t in the scheme of the period
// folder f: the same path with every date component rendered for t.
func periodPath(root string, f *Folder, t time.Time) string {
	parts := strings.Split(filepath.ToSlash(f.Rel), "/")
	for i, layout := range f.layouts {
		if layout != "" {
			parts[i] = t.Format(layout)
		}
	}
	return filepath.Join(root, filepath.FromSlash(strings.Join(parts, "/")))
}

var inboxNameRe = regexp.MustCompile(`(?i)^(inbox|unsorted|incoming|import(s|ed)?|new|dcim|camera( uploads?| roll)?|uploads?|to ?sort|todo|misc|sort me|_?_?new)$`)

// Thresholds of the folder classification.
const (
	// eventCoverage is the fraction of a folder's dated photos its held
	// events must make up for it to be an event folder.
	eventCoverage = 0.8
	// eventFolderSpan is how far from a folder's main event another event
	// may lie and still be held by the folder: the break in a long trip,
	// the days around a wedding.
	eventFolderSpan = 60 * 24 * time.Hour
	// eventFolderEvents is how many events an event folder may hold.
	eventFolderEvents = 6
	// mergeShare is the fraction of a folder's photos that must belong to
	// the event of another folder for the two to be merged rather than the
	// photos moved.
	mergeShare = 0.9
)

// Holds reports whether the event is one the folder is there for: for an
// event folder its main event and the events close to it; anything else in
// the folder is a stray.
func (f *Folder) Holds(e *Event) bool { return f.held[e] }

// collectHeld works out the events a folder is there for: the main event
// and, by number of photos, up to eventFolderEvents events within
// eventFolderSpan of it.
func (f *Folder) collectHeld() {
	f.held = map[*Event]bool{}
	main := f.Main()
	if main == nil {
		return
	}
	f.held[main] = true
	for _, e := range f.Events[1:] {
		if len(f.held) >= eventFolderEvents {
			break
		}
		if e.First.Before(main.Last.Add(eventFolderSpan)) && e.Last.After(main.First.Add(-eventFolderSpan)) {
			f.held[e] = true
		}
	}
}

func (f *Folder) classify(inbox bool) {
	f.collectHeld()
	switch {
	case inbox:
		f.Kind, f.Note = FolderInbox, "given as inbox"
		return
	case f.Period != nil:
		f.Kind, f.Note = FolderPeriod, "named after a "+f.Period.Unit
		return
	case f.Rel == ".":
		f.Kind, f.Note = FolderInbox, "photos in the library root"
		return
	case inboxNameRe.MatchString(filepath.Base(f.Path)):
		f.Kind, f.Note = FolderInbox, "named like an inbox"
		return
	case len(f.Events) == 0:
		f.Kind, f.Note = FolderMixed, "no dated photos"
		return
	}
	dated := len(f.Photos) - f.Undated
	covered := 0
	for e := range f.held {
		covered += f.Counts[e]
	}
	switch {
	case float64(covered) < eventCoverage*float64(dated):
		f.Kind, f.Note = FolderMixed, fmt.Sprintf("holds %d events spread over time", len(f.Events))
	case len(f.held) == 1:
		f.Kind, f.Note = FolderEvent, "holds one event"
	default:
		f.Kind, f.Note = FolderEvent, fmt.Sprintf("holds %d events close together", len(f.held))
	}
}

// owner returns the event folder whose main event is e and that holds the
// most of it, or nil.
func (l *Library) owner(e *Event) *Folder {
	var best *Folder
	for _, f := range l.Folders {
		if f.Kind == FolderEvent && f.Main() == e && (best == nil || f.Counts[e] > best.Counts[e]) {
			best = f
		}
	}
	return best
}

// inbox returns the first inbox folder, preferring one that is not the
// root, or nil.
func (l *Library) inbox() *Folder {
	var root *Folder
	for _, f := range l.Folders {
		if f.Kind != FolderInbox {
			continue
		}
		if f.Rel != "." {
			return f
		}
		root = f
	}
	return root
}

// eventParent returns the directory new event folders go in: where most
// event folders are, else the root.
func (l *Library) eventParent() string {
	counts := map[string]int{}
	for _, f := range l.Folders {
		if f.Kind == FolderEvent {
			counts[filepath.Dir(f.Path)]++
		}
	}
	best, n := l.Root, 0
	for dir, c := range counts {
		if c > n || c == n && dir < best {
			best, n = dir, c
		}
	}
	return best
}

func (l *Library) suggest(layout Layout) {
	assigned := map[*Photo]bool{}
	add := func(s *Suggestion) {
		var photos []*Photo
		for _, p := range s.Photos {
			if !assigned[p] {
				photos = append(photos, p)
				assigned[p] = true
			}
		}
		if len(photos) == 0 {
			return
		}
		s.Photos = photos
		s.ID = fmt.Sprintf("s%d", len(l.Suggestions)+1)
		l.Suggestions = append(l.Suggestions, s)
	}
	// photosIn returns the event's photos in the folder, not yet assigned.
	photosIn := func(e *Event, f *Folder) []*Photo {
		var out []*Photo
		for _, p := range e.Photos {
			if filepath.Dir(p.Path) == f.Path && !assigned[p] {
				out = append(out, p)
			}
		}
		return out
	}
	inbox := l.inbox()
	parent := l.eventParent()

	for _, e := range l.Events {
		owner := l.owner(e)
		if owner != nil {
			for _, f := range l.Folders {
				if f == owner || f.Counts[e] == 0 {
					continue
				}
				switch f.Kind {
				case FolderEvent:
					if float64(f.Counts[e]) >= mergeShare*float64(len(f.Photos)) {
						add(&Suggestion{
							Kind: SuggestMerge, Event: e, From: f, To: owner.Path, Photos: f.Photos,
							Reason:     fmt.Sprintf("%s holds the same event as %s: %s", f.Rel, owner.Rel, describeEvent(e, f, owner)),
							Confidence: confidence(e, f, owner),
						})
						continue
					}
					add(&Suggestion{
						Kind: SuggestMove, Event: e, From: f, To: owner.Path, Photos: photosIn(e, f),
						Reason:     fmt.Sprintf("part of the event kept in %s, not of the one in %s", owner.Rel, f.Rel),
						Confidence: confidence(e, f, owner),
					})
				case FolderInbox, FolderMixed:
					add(&Suggestion{
						Kind: SuggestMove, Event: e, From: f, To: owner.Path, Photos: photosIn(e, f),
						Reason:     fmt.Sprintf("part of the event kept in %s: %s", owner.Rel, describeEvent(e, f, owner)),
						Confidence: confidence(e, f, owner),
					})
				case FolderPeriod:
					if layout == LayoutTrips {
						add(&Suggestion{
							Kind: SuggestMove, Event: e, From: f, To: owner.Path, Photos: photosIn(e, f),
							Reason:     fmt.Sprintf("part of the event kept in %s: %s", owner.Rel, describeEvent(e, f, owner)),
							Confidence: confidence(e, f, owner),
						})
					}
				}
			}
			continue
		}
		if e.Kind != EventTrip || layout == LayoutPeriods {
			continue
		}
		// A trip without a folder of its own. Gather it from the folders
		// it is spread over, when they are the kind photos are sorted out
		// of; what sits in period folders stays there unless asked.
		var photos []*Photo
		var sources []string
		for _, f := range l.Folders {
			if f.Counts[e] == 0 || f.Kind == FolderPeriod && layout != LayoutTrips {
				continue
			}
			if ps := photosIn(e, f); len(ps) > 0 {
				photos = append(photos, ps...)
				sources = append(sources, f.Rel)
			}
		}
		if len(photos)*2 < len(e.Photos) {
			continue
		}
		var from *Folder
		if len(sources) == 1 {
			from = l.Folder(filepath.Dir(photos[0].Path))
		}
		add(&Suggestion{
			Kind: SuggestCreate, Event: e, From: from, To: filepath.Join(parent, e.Name()), Photos: photos,
			Reason:     fmt.Sprintf("a trip without a folder, in %s: %s", strings.Join(sources, ", "), describeEvent(e, nil, nil)),
			Confidence: confidence(e, nil, nil),
		})
	}

	// Photos in period folders whose event lies entirely outside the
	// period, and photos in event folders that belong to no event kept
	// there.
	for _, f := range l.Folders {
		switch f.Kind {
		case FolderPeriod:
			byTarget := map[string][]*Photo{}
			for _, e := range f.Events {
				if f.Period.Overlaps(e.First, e.Last) {
					continue
				}
				for _, p := range photosIn(e, f) {
					target := periodPath(l.Root, f, p.Original.T)
					byTarget[target] = append(byTarget[target], p)
				}
			}
			targets := slices.Sorted(maps.Keys(byTarget))
			for _, target := range targets {
				kind := SuggestCreate
				if l.Folder(target) != nil {
					kind = SuggestMove
				}
				add(&Suggestion{
					Kind: kind, From: f, To: target, Photos: byTarget[target],
					Reason:     fmt.Sprintf("taken outside %s", f.Period),
					Confidence: ConfidenceHigh,
				})
			}
		case FolderEvent:
			for _, e := range f.Events {
				if f.Holds(e) {
					continue
				}
				ps := photosIn(e, f)
				if len(ps) == 0 {
					continue
				}
				s := &Suggestion{Kind: SuggestStray, Event: e, From: f, Photos: ps, Confidence: confidence(e, f, nil),
					Reason: fmt.Sprintf("not part of the event kept in %s: %s", f.Rel, describeEvent(e, nil, nil))}
				if inbox != nil && inbox != f {
					s.Kind, s.To = SuggestMove, inbox.Path
					s.Reason += "; back to the inbox"
				}
				add(s)
			}
		}
	}
}

// describeEvent says when the event was and, when the folders are given,
// what each holds of it.
func describeEvent(e *Event, a, b *Folder) string {
	var parts []string
	parts = append(parts, e.Name())
	if e.Placed() {
		parts = append(parts, fmt.Sprintf("%d photos located within %s", e.Located, FormatDistance(e.Radius)))
	} else {
		parts = append(parts, "no locations")
	}
	for _, f := range []*Folder{a, b} {
		if f == nil {
			continue
		}
		var cams []string
		for _, p := range e.Photos {
			if filepath.Dir(p.Path) == f.Path && !slices.Contains(cams, p.Camera) {
				cams = append(cams, p.Camera)
			}
		}
		parts = append(parts, fmt.Sprintf("%s has %d photos from %s", f.Rel, f.Counts[e], strings.Join(cams, ", ")))
	}
	return strings.Join(parts, "; ")
}

// confidence grades how sure the event membership is: high when the event
// has positions on both sides, medium when it has positions but one side
// is placed by time alone, low when only time says so.
func confidence(e *Event, a, b *Folder) Confidence {
	if !e.Placed() {
		return ConfidenceLow
	}
	for _, f := range []*Folder{a, b} {
		if f == nil {
			continue
		}
		located := false
		for _, p := range e.Photos {
			if p.Position != nil && filepath.Dir(p.Path) == f.Path {
				located = true
				break
			}
		}
		if !located {
			return ConfidenceMedium
		}
	}
	return ConfidenceHigh
}

// observe collects the notes.
func (l *Library) observe() {
	if len(l.Undated) > 0 {
		l.Notes = append(l.Notes, Note{Kind: "undated", Photos: l.Undated,
			Text: fmt.Sprintf("%d photos have no date and belong to no event", len(l.Undated))})
	}
	for _, e := range l.Events {
		if len(e.Cameras) != 1 || !looksReset(e.First) {
			continue
		}
		var camera string
		for c := range e.Cameras {
			camera = c
		}
		l.Notes = append(l.Notes, Note{Kind: "reset-clock", Photos: e.Photos,
			Text: fmt.Sprintf("%s: %d photos dated from %s; the clock looks reset, fix the dates before sorting", camera, len(e.Photos), e.First.Format("2006-01-02 15:04"))})
	}
	for _, f := range l.Folders {
		if f.Kind != FolderEvent || f.Cameras[UnknownCamera] == 0 {
			continue
		}
		var photos []*Photo
		for _, p := range f.Photos {
			if p.Camera == UnknownCamera {
				photos = append(photos, p)
			}
		}
		l.Notes = append(l.Notes, Note{Kind: "no-camera", Folder: f, Photos: photos,
			Text: fmt.Sprintf("%s: %d photos without camera data (screenshots, downloads, edits?)", f.Rel, len(photos))})
	}
}

// looksReset reports whether a first timestamp is where a camera clock
// starts after a reset: the first hours of a New Year's Day, or before
// digital cameras existed.
func looksReset(t time.Time) bool {
	return t.Year() < 1990 || t.Month() == time.January && t.Day() == 1 && t.Hour() < 3
}

package camsync

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// libraryPhotos builds photos under root from "folder/name camera date time
// [home|away]" lines. The folder name may contain spaces.
func libraryPhotos(root string, lines ...string) []*Photo {
	var photos []*Photo
	for _, line := range lines {
		f := strings.Fields(line)
		var lat, lon float64
		switch f[len(f)-1] {
		case "home":
			lat, lon, f = homeLat, homeLon, f[:len(f)-1]
		case "away":
			lat, lon, f = awayLat, awayLon, f[:len(f)-1]
		}
		n := len(f)
		when := f[n-2] + " " + f[n-1]
		path := strings.Join(f[:n-3], " ")
		p := snap(filepath.Join(root, filepath.FromSlash(path)), f[n-3], when, lat, lon)
		photos = append(photos, p)
	}
	return photos
}

// homeDays adds ten days of phone photos at home so that home is inferred.
func homeDays(root string) []string {
	var lines []string
	for d := 1; d <= 10; d++ {
		lines = append(lines, "2024-03/h"+pad(d)+".jpg Phone 2024-03-"+pad(d)+" 18:00 home")
	}
	return lines
}

func suggestionText(l *Library) []string {
	var out []string
	for _, s := range l.Suggestions {
		from := "-"
		if s.From != nil {
			from = s.From.Rel
		}
		to := "-"
		if s.To != "" {
			to, _ = filepath.Rel(l.Root, s.To)
		}
		out = append(out, string(s.Kind)+" "+from+" -> "+to+" x"+itoa(len(s.Photos))+" "+string(s.Confidence))
	}
	return out
}

func itoa(n int) string {
	if n >= 10 {
		return itoa(n/10) + itoa(n%10)
	}
	return string(rune('0' + n))
}

func expectSuggestions(t *testing.T, l *Library, want ...string) {
	t.Helper()
	got := suggestionText(l)
	if len(got) != len(want) {
		t.Fatalf("suggestions = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOrganizeMergesTwoCamerasOfOneTrip(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "lib")
	lines := append(homeDays(root),
		"Rügen Canon/c1.jpg Canon 2024-05-01 10:00",
		"Rügen Canon/c2.jpg Canon 2024-05-02 09:00",
		"Rügen Canon/c3.jpg Canon 2024-05-03 09:00",
		"Rügen Canon/c4.jpg Canon 2024-05-01 12:00",
		"Rügen Canon/c5.jpg Canon 2024-05-02 12:00",
		"Rügen Canon/c6.jpg Canon 2024-05-03 12:00",
		"Rügen Sony/s1.jpg Sony 2024-05-01 10:30",
		"Rügen Sony/s2.jpg Sony 2024-05-02 09:30",
		"Rügen Sony/s3.jpg Sony 2024-05-02 12:30",
		"Rügen Sony/s4.jpg Sony 2024-05-03 09:30",
	)
	l := Organize(root, libraryPhotos(root, lines...), LibraryOptions{})
	if len(l.Events) != 2 || l.Events[1].Kind != EventTrip {
		t.Fatalf("events = %+v", l.Events)
	}
	for _, f := range l.Folders {
		if f.Rel != "2024-03" && f.Kind != FolderEvent {
			t.Errorf("%s: %s (%s)", f.Rel, f.Kind, f.Note)
		}
	}
	if f := l.Folder(filepath.Join(root, "2024-03")); f == nil || f.Kind != FolderPeriod || f.Period.String() != "2024-03" {
		t.Errorf("period folder = %+v", f)
	}
	// Without positions the trip is placed by time alone: low confidence.
	expectSuggestions(t, l, "merge Rügen Sony -> Rügen Canon x4 low")
	s := l.Suggestions[0]
	if s.Event != l.Events[1] || len(s.Moves()) != 4 || s.Moves()[0].To != filepath.Join(root, "Rügen Canon") || !strings.Contains(s.Reason, "Rügen Sony has 4 photos from Sony") {
		t.Errorf("suggestion = %+v", s)
	}

	// A phone photo in each folder places both halves: high confidence.
	lines = append(lines, "Rügen Canon/p1.jpg Phone 2024-05-01 10:05 away", "Rügen Sony/p2.jpg Phone 2024-05-02 09:35 away")
	l = Organize(root, libraryPhotos(root, lines...), LibraryOptions{})
	expectSuggestions(t, l, "merge Rügen Sony -> Rügen Canon x5 high")
}

func TestOrganizeInbox(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "lib")
	lines := append(homeDays(root),
		// A sorted trip, with a phone photo that places it.
		"Trips/2024-05 Rügen/c1.jpg Canon 2024-05-01 10:00",
		"Trips/2024-05 Rügen/c2.jpg Canon 2024-05-02 11:00",
		"Trips/2024-05 Rügen/p1.jpg Phone 2024-05-02 11:10 away",
		// The inbox: two phone photos of that trip, a whole trip in June
		// (positions on the first and last day), single snaps at home.
		"Inbox/p2.jpg Phone 2024-05-01 12:00 away",
		"Inbox/p3.jpg Phone 2024-05-03 09:00 away",
		"Inbox/j01.jpg Phone 2024-06-10 09:00 away",
		"Inbox/j02.jpg Canon 2024-06-10 10:00",
		"Inbox/j03.jpg Canon 2024-06-10 11:00",
		"Inbox/j04.jpg Canon 2024-06-10 12:00",
		"Inbox/j05.jpg Canon 2024-06-11 10:00",
		"Inbox/j06.jpg Canon 2024-06-11 11:00",
		"Inbox/j07.jpg Canon 2024-06-11 12:00",
		"Inbox/j08.jpg Canon 2024-06-12 10:00",
		"Inbox/j09.jpg Canon 2024-06-12 11:00",
		"Inbox/j10.jpg Canon 2024-06-12 12:00",
		"Inbox/j11.jpg Phone 2024-06-12 13:00 away",
		"Inbox/s1.jpg Phone 2024-07-01 18:00 home",
		"Inbox/s2.jpg Phone 2024-07-05 18:00 home",
		"Inbox/s3.jpg Phone 2024-07-06 18:00 home",
	)
	l := Organize(root, libraryPhotos(root, lines...), LibraryOptions{})
	inbox := l.Folder(filepath.Join(root, "Inbox"))
	if inbox == nil || inbox.Kind != FolderInbox {
		t.Fatalf("inbox = %+v", inbox)
	}
	expectSuggestions(t, l,
		"move Inbox -> Trips/2024-05 Rügen x2 high",
		"create Inbox -> Trips/2024-06-10 - 2024-06-12 x11 high",
	)
	if s := l.Suggestions[1]; s.Event.Kind != EventTrip || s.Photos[0].Path != filepath.Join(root, "Inbox", "j01.jpg") {
		t.Errorf("create = %+v", s)
	}

	// Under the periods layout no trip folder is created; the move into
	// the existing folder stands.
	l = Organize(root, libraryPhotos(root, lines...), LibraryOptions{Layout: LayoutPeriods})
	expectSuggestions(t, l, "move Inbox -> Trips/2024-05 Rügen x2 high")

	// A folder given as inbox counts as one whatever its name.
	l = Organize(root, libraryPhotos(root, append(lines, "Stuff/x.jpg Phone 2024-05-02 12:00 away")...), LibraryOptions{Inboxes: []string{filepath.Join(root, "Stuff")}})
	if f := l.Folder(filepath.Join(root, "Stuff")); f.Kind != FolderInbox {
		t.Errorf("Stuff = %s (%s)", f.Kind, f.Note)
	}
	if got := suggestionText(l); len(got) != 3 || got[1] != "move Stuff -> Trips/2024-05 Rügen x1 high" {
		t.Errorf("suggestions = %q", got)
	}

	// A suggestion keeps its ID when it comes up again with other options,
	// and one that changed gets another, even in the same position.
	base := Organize(root, libraryPhotos(root, lines...), LibraryOptions{})
	periods := Organize(root, libraryPhotos(root, lines...), LibraryOptions{Layout: LayoutPeriods})
	if base.Suggestions[0].ID != periods.Suggestions[0].ID || base.Suggestions[0].ID == base.Suggestions[1].ID {
		t.Errorf("ids = %s %s, %s", base.Suggestions[0].ID, base.Suggestions[1].ID, periods.Suggestions[0].ID)
	}
	if l.Suggestions[1].ID == base.Suggestions[1].ID {
		t.Errorf("a different suggestion took over the id %s", l.Suggestions[1].ID)
	}
}

func TestOrganizeStraysAndPeriods(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "lib")
	lines := append(homeDays(root),
		"Trips/Rügen/c1.jpg Canon 2024-05-01 10:00",
		"Trips/Rügen/c2.jpg Canon 2024-05-02 11:00",
		"Trips/Rügen/p1.jpg Phone 2024-05-02 11:10 away",
		"Trips/Rügen/c3.jpg Canon 2024-05-02 12:00",
		"Trips/Rügen/c4.jpg Canon 2024-05-02 13:00",
		"Trips/Rügen/old.jpg Phone 2023-01-15 11:10 home", // a stray at home
		// A month folder with a photo from the next month, whose event
		// lies entirely in June, and a trip spilling over from May.
		"2024-05/a.jpg Phone 2024-05-20 10:00 home",
		"2024-05/b.jpg Phone 2024-05-31 10:00 away",
		"2024-05/c.jpg Phone 2024-06-01 10:00 away",
		"2024-05/d.jpg Phone 2024-06-20 10:00 home",
		"2024-07/e.jpg Phone 2024-07-20 10:00 home",
		"2024-07/f.jpg Phone 2024-03-20 10:00 home",
		"Inbox/i.jpg Phone 2024-09-20 10:00 home",
	)
	l := Organize(root, libraryPhotos(root, lines...), LibraryOptions{})
	expectSuggestions(t, l,
		"create 2024-05 -> 2024-06 x1 high",
		"move 2024-07 -> 2024-03 x1 high",
		"move Trips/Rügen -> Inbox x1 high",
	)
	if s := l.Suggestions[1]; s.Photos[0].Path != filepath.Join(root, "2024-07", "f.jpg") {
		t.Errorf("period move = %+v", s)
	}
	if s := l.Suggestions[2]; s.Kind != SuggestMove || !strings.Contains(s.Reason, "back to the inbox") {
		t.Errorf("stray = %+v", s)
	}

	// Without an inbox the stray has nowhere to go.
	l = Organize(root, libraryPhotos(root, lines[:len(lines)-1]...), LibraryOptions{})
	if got := suggestionText(l); got[2] != "stray Trips/Rügen -> - x1 high" {
		t.Errorf("suggestions = %q", got)
	}
	if l.Suggestions[2].Moves() != nil {
		t.Error("a stray has no moves")
	}
}

func TestOrganizeNestedPeriods(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "lib")
	l := Organize(root, libraryPhotos(root,
		"2024/05/a.jpg Phone 2024-05-20 10:00",
		"2024/05/b.jpg Phone 2025-01-20 10:00",
		"2024/12/c.jpg Phone 2024-12-20 10:00",
	), LibraryOptions{})
	f := l.Folder(filepath.Join(root, "2024", "05"))
	if f == nil || f.Kind != FolderPeriod || f.Period.Unit != "month" || f.Period.String() != "2024-05" {
		t.Fatalf("folder = %+v", f)
	}
	expectSuggestions(t, l, "create 2024/05 -> 2025/01 x1 high")

	layouts, period := parsePeriodPath("2024-05 Rügen")
	if period != nil || layouts[0] != "" {
		t.Errorf("named month = %v %v", layouts, period)
	}
	if _, period := parsePeriodPath("2024"); period == nil || period.Unit != "year" || !period.Contains(time.Date(2024, 12, 31, 23, 0, 0, 0, time.UTC)) || period.Contains(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("year = %+v", period)
	}
	if _, period := parsePeriodPath("Trips/05"); period != nil {
		t.Errorf("a bare month under a name is no period: %+v", period)
	}
}

func TestOrganizeLayoutTrips(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "lib")
	var lines []string
	for d := 1; d <= 3; d++ {
		for h := 9; h < 13; h++ {
			lines = append(lines, "2024-05/t"+pad(d)+pad(h)+".jpg Phone 2024-05-"+pad(d)+" "+pad(h)+":00 away")
		}
	}
	lines = append(lines, homeDays(root)...)
	l := Organize(root, libraryPhotos(root, lines...), LibraryOptions{})
	expectSuggestions(t, l)
	l = Organize(root, libraryPhotos(root, lines...), LibraryOptions{Layout: LayoutTrips})
	expectSuggestions(t, l, "create 2024-05 -> 2024-05-01 - 2024-05-03 x12 high")
	if _, err := ParseLayout("weekly"); err == nil {
		t.Error("unknown layout accepted")
	}
}

func TestOrganizeNotes(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "lib")
	photos := libraryPhotos(root,
		"Trip/a.jpg Canon 2024-05-01 10:00",
		"Trip/b.jpg Canon 2024-05-01 11:00",
		"Old/r1.jpg Sony 2010-01-01 00:03",
		"Old/r2.jpg Sony 2010-01-01 00:10",
	)
	photos = append(photos, &Photo{Path: filepath.Join(root, "Trip", "nodate.jpg"), Camera: "Canon"})
	photos = append(photos, snap(filepath.Join(root, "Trip", "shot.png"), UnknownCamera, "2024-05-01 10:30", 0, 0))
	l := Organize(root, photos, LibraryOptions{})
	kinds := map[string]string{}
	for _, n := range l.Notes {
		kinds[n.Kind] = n.Text
	}
	if len(kinds) != 3 || !strings.Contains(kinds["reset-clock"], "Sony: 2 photos dated from 2010-01-01 00:03") ||
		!strings.Contains(kinds["undated"], "1 photos") || !strings.Contains(kinds["no-camera"], "Trip: 1 photos") {
		t.Errorf("notes = %v", kinds)
	}
	if len(l.Undated) != 1 || l.Folder(filepath.Join(root, "Trip")).Undated != 1 {
		t.Error("undated photo not counted")
	}
}

package camsync

import (
	"testing"
	"time"
)

// photoAt builds a dated photo, located when lat or lon is non-zero.
func snap(path, camera, when string, lat, lon float64) *Photo {
	t, err := time.Parse("2006-01-02 15:04", when)
	if err != nil {
		panic(err)
	}
	p := &Photo{Path: path, Camera: camera, Original: &Stamp{T: t}}
	if lat != 0 || lon != 0 {
		p.Position = &Position{Lat: lat, Lon: lon}
	}
	return p
}

const (
	homeLat, homeLon = 52.52, 13.40 // Berlin
	awayLat, awayLon = 54.42, 13.43 // Rügen, 210 km north
)

func TestDetectEventsSplitsOnPauseJumpAndHome(t *testing.T) {
	var photos []*Photo
	// Ten days of snaps at home, one a day, establish home.
	for d := 1; d <= 10; d++ {
		photos = append(photos, snap("home"+string(rune('0'+d))+".jpg", "Phone", "2024-04-"+pad(d)+" 18:00", homeLat, homeLon))
	}
	// A trip: phone photo at home in the morning, camera photos on the
	// road without positions, phone photos away in the afternoon and on
	// the following days.
	photos = append(photos,
		snap("p1.jpg", "Phone", "2024-05-01 07:00", homeLat, homeLon),
		snap("c1.jpg", "Camera", "2024-05-01 11:00", 0, 0),
		snap("c2.jpg", "Camera", "2024-05-01 13:00", 0, 0),
		snap("p2.jpg", "Phone", "2024-05-01 15:00", awayLat, awayLon),
		snap("c3.jpg", "Camera", "2024-05-02 10:00", 0, 0),
		snap("p3.jpg", "Phone", "2024-05-03 09:00", awayLat, awayLon),
		// Back home a day later, then nothing for a week.
		snap("p4.jpg", "Phone", "2024-05-04 20:00", homeLat, homeLon),
		snap("p5.jpg", "Phone", "2024-05-12 20:00", homeLat, homeLon),
		&Photo{Path: "nodate.jpg", Camera: "Camera"},
	)
	events, home := DetectEvents(photos, EventOptions{})
	if home == nil || home.DistanceTo(Position{homeLat, homeLon}) > 1000 {
		t.Fatalf("home = %v", home)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Name()+" "+string(e.Kind))
	}
	want := []string{
		"2024-04-01 - 2024-04-10 home",
		"2024-05-01 home",
		"2024-05-01 - 2024-05-03 trip",
		"2024-05-04 home",
		"2024-05-12 home",
	}
	if len(got) != len(want) {
		t.Fatalf("events = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The jump was placed at the longest pause after the last home photo,
	// between p1 and c1, so the road photos belong to the trip.
	trip := events[2]
	if len(trip.Photos) != 5 || trip.Photos[0].Path != "c1.jpg" || trip.Cameras["Camera"] != 3 || trip.Located != 2 || !trip.Away {
		t.Errorf("trip = %+v", trip)
	}
	if trip.Days() != 3 || trip.Index != 3 || trip.Radius > 1000 {
		t.Errorf("days %d index %d radius %.0f", trip.Days(), trip.Index, trip.Radius)
	}
	if events[1].Photos[0].Path != "p1.jpg" || len(events[1].Photos) != 1 {
		t.Errorf("morning = %+v", events[1].Photos)
	}
}

func pad(d int) string {
	if d < 10 {
		return "0" + string(rune('0'+d))
	}
	return string(rune('0'+d/10)) + string(rune('0'+d%10))
}

func TestDetectEventsWithoutPositions(t *testing.T) {
	var photos []*Photo
	for h := 0; h < 12; h++ {
		photos = append(photos, snap("a"+string(rune('a'+h))+".jpg", "Camera", "2024-05-0"+string(rune('1'+h/6))+" "+pad(9+h%6)+":00", 0, 0))
	}
	photos = append(photos, snap("b.jpg", "Camera", "2024-06-01 09:00", 0, 0))
	events, home := DetectEvents(photos, EventOptions{})
	if home != nil {
		t.Errorf("home = %v", home)
	}
	if len(events) != 2 || events[0].Kind != EventTrip || events[0].Placed() || events[1].Kind != EventOuting {
		t.Fatalf("events = %+v", events)
	}
	// A smaller minimum makes the lone photo a trip too, once it lasts.
	events, _ = DetectEvents(photos, EventOptions{MinTripPhotos: 1, MinTripSpan: time.Nanosecond})
	if events[1].Kind != EventOuting {
		t.Errorf("a single photo has no span: %v", events[1].Kind)
	}
	events, _ = DetectEvents(photos, EventOptions{MaxGap: 30 * 24 * time.Hour})
	if len(events) != 1 {
		t.Errorf("gap of a month: %d events", len(events))
	}
}

func TestDetectEventsGivenHome(t *testing.T) {
	photos := []*Photo{
		snap("a.jpg", "Phone", "2024-05-01 09:00", awayLat, awayLon),
		snap("b.jpg", "Phone", "2024-05-01 10:00", awayLat, awayLon),
	}
	events, home := DetectEvents(photos, EventOptions{Home: &Position{homeLat, homeLon}})
	if home == nil || home.Lat != homeLat || len(events) != 1 || events[0].Kind != EventOuting || !events[0].Away {
		t.Errorf("events = %+v, home = %v", events, home)
	}
	// Without a home the only place is home, and the event is at home.
	events, home = DetectEvents(photos, EventOptions{})
	if home == nil || len(events) != 1 || events[0].Kind != EventHome {
		t.Errorf("inferred: events = %+v, home = %v", events, home)
	}
}

func TestInferHomePrefersDaysOverPhotos(t *testing.T) {
	var photos []*Photo
	for i := 0; i < 50; i++ { // a photo-heavy trip on two days
		photos = append(photos, snap("t.jpg", "Phone", "2024-05-0"+string(rune('1'+i%2))+" 12:"+pad(i), awayLat, awayLon))
	}
	for d := 1; d <= 3; d++ {
		photos = append(photos, snap("h.jpg", "Phone", "2024-06-0"+string(rune('0'+d))+" 12:00", homeLat, homeLon))
	}
	home, ok := InferHome(photos)
	if !ok || home.DistanceTo(Position{homeLat, homeLon}) > 100 {
		t.Errorf("home = %v, %v", home, ok)
	}
	if _, ok := InferHome(nil); ok {
		t.Error("no photos, no home")
	}
}

func TestDetectEventsIgnoresLoneOutlier(t *testing.T) {
	var photos []*Photo
	for d := 1; d <= 10; d++ {
		photos = append(photos, snap("h"+pad(d)+".jpg", "Phone", "2024-04-"+pad(d)+" 18:00", homeLat, homeLon))
	}
	photos = append(photos,
		snap("t1.jpg", "Phone", "2024-05-01 10:00", awayLat, awayLon),
		snap("c1.jpg", "Camera", "2024-05-01 12:00", 0, 0),
		// The partner's phone at home in the evening, while the trip goes on.
		snap("x.jpg", "Phone 2", "2024-05-01 19:00", homeLat, homeLon),
		snap("c2.jpg", "Camera", "2024-05-02 09:00", 0, 0),
		snap("t2.jpg", "Phone", "2024-05-02 10:00", awayLat, awayLon),
		snap("t3.jpg", "Phone", "2024-05-03 10:00", awayLat, awayLon),
	)
	events, _ := DetectEvents(photos, EventOptions{})
	if len(events) != 2 || len(events[1].Photos) != 6 || events[1].Kind != EventTrip {
		var got []string
		for _, e := range events {
			got = append(got, e.Name()+" "+string(e.Kind)+" "+itoa(len(e.Photos)))
		}
		t.Fatalf("events = %q", got)
	}
	// With nobody back at the trip afterwards the home photo is no outlier:
	// the trip ended, and the camera photo of the next morning is at home.
	photos = append(photos[:len(photos)-2], snap("y.jpg", "Phone", "2024-05-02 20:00", homeLat, homeLon), snap("z.jpg", "Phone", "2024-05-03 10:00", homeLat, homeLon))
	events, _ = DetectEvents(photos, EventOptions{})
	if len(events) != 3 || events[2].Kind != EventHome || len(events[2].Photos) != 4 {
		t.Errorf("confirmed change: %d events, last %v with %d photos", len(events), events[len(events)-1].Kind, len(events[len(events)-1].Photos))
	}
}

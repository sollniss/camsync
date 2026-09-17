package camsync

import (
	"testing"
	"time"
)

func TestTrackAt(t *testing.T) {
	at := func(min int) time.Time { return tripStart.Add(time.Duration(min) * time.Minute) }
	obs := func(min int, lat, lon float64, name string) PositionObservation {
		return PositionObservation{Instant: at(min), Position: Position{lat, lon}, Photo: &Photo{Path: "/trip/" + name}}
	}
	// A square visited twice, then a train ride of 30 km.
	track := &Track{paths: [][]PositionObservation{{
		obs(0, 54.0, 13.0, "a.jpg"),
		obs(12, 54.001, 13.001, "b.jpg"), // 130 m away
		obs(20, 54.001, 13.001, "c.jpg"),
		obs(30, 54.27, 13.001, "d.jpg"), // 30 km north
	}}}
	for _, c := range []struct {
		min          int
		ok           bool
		interpolated bool
		want         Position
		gap          time.Duration
	}{
		{3, true, true, Position{54.00025, 13.00025}, 9 * time.Minute}, // bridge: close in space
		{16, true, true, Position{54.001, 13.001}, 4 * time.Minute},    // both within 5m
		{23, true, false, Position{54.001, 13.001}, 3 * time.Minute},   // far apart: copy the nearest
		{26, true, false, Position{54.27, 13.001}, 4 * time.Minute},    //
		{40, false, false, Position{}, 0},                              // beyond the last photo
		{-4, true, false, Position{54.0, 13.0}, 4 * time.Minute},       // before the first
		{12, true, false, Position{54.001, 13.001}, 0},                 // exactly on an observation
	} {
		fix, ok := track.At(at(c.min), TrackOptions{})
		if ok != c.ok || fix.Interpolated != c.interpolated || (ok && (fix.DistanceTo(c.want) > 0.5 || fix.Gap != c.gap)) {
			t.Errorf("minute %d: %+v %v", c.min, fix, ok)
		}
	}
	fix, _ := track.At(at(3), TrackOptions{})
	if s := fix.String(); s != "54.0002500, 13.0002500 (between a.jpg and b.jpg, 9m)" {
		t.Errorf("String = %q", s)
	}
	// Without the bridge the nearest photo, three minutes away, is copied.
	if fix, ok := track.At(at(3), TrackOptions{BridgeDistance: 50}); !ok || fix.Interpolated || fix.From.Path != "/trip/a.jpg" {
		t.Errorf("bridge beyond the distance: %+v %v", fix, ok)
	}
	if _, ok := track.At(at(6), TrackOptions{BridgeDistance: 50}); ok {
		t.Error("nothing within 5m without the bridge")
	}
}

func TestTrackKeepsPathsApart(t *testing.T) {
	at := func(min int) time.Time { return tripStart.Add(time.Duration(min) * time.Minute) }
	obs := func(min int, lat float64, name string) PositionObservation {
		return PositionObservation{Instant: at(min), Position: Position{lat, 13}, Photo: &Photo{Path: name}}
	}
	// A phone walking, and a second camera 11 km away shortly after.
	track := &Track{paths: [][]PositionObservation{
		{obs(0, 54.4, "phone0"), obs(8, 54.4072, "phone8")},
		{obs(14, 54.5, "far14")},
	}}
	if track.Len() != 3 {
		t.Errorf("len = %d", track.Len())
	}
	// Minute 6: along the phone's path.
	fix, ok := track.At(at(6), TrackOptions{})
	if !ok || !fix.Interpolated || fix.From.Path != "phone0" || fix.To.Path != "phone8" || fix.Gap != 6*time.Minute {
		t.Errorf("minute 6 = %+v", fix)
	}
	// Minute 12: phone8 and far14 are both near, but no point between two
	// devices is made up; the nearer photo in time is copied.
	fix, ok = track.At(at(12), TrackOptions{})
	if !ok || fix.Interpolated || fix.From.Path != "far14" || fix.Gap != 2*time.Minute {
		t.Errorf("minute 12 = %+v", fix)
	}
}

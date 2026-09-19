package camsync

import (
	"testing"
	"time"
)

func TestTimeline(t *testing.T) {
	berlin := 2 * time.Hour
	gpsAt := func(utc time.Time) *time.Time { return &utc }
	day := tripStart.Add(10 * time.Hour) // UTC
	photos := []*Photo{
		// Recorded zone.
		photoAt("Apple", "iPhone 15", day.Add(berlin), "+02:00", nil),
		// No zone, but a GPS time: the zone is the difference, rounded.
		photoAt("Apple", "iPhone 15", day.Add(3*time.Hour+berlin+20*time.Second), "", gpsAt(day.Add(3*time.Hour))),
		// Flew to Tokyo the next day.
		photoAt("Apple", "iPhone 15", day.Add(24*time.Hour+9*time.Hour), "+09:00", nil),
		// A GPS fix 20 minutes stale: off the quarter-hour grid, so it must
		// not become a +02:15 observation.
		photoAt("Apple", "iPhone 15", day.Add(6*time.Hour+berlin), "", gpsAt(day.Add(6*time.Hour-20*time.Minute))),
		// Nothing to tell the zone.
		photoAt("Apple", "iPhone 15", day.Add(5*time.Hour), "", nil),
		{Camera: "undated"},
	}
	tl := NewTimeline(photos)
	if tl.Len() != 3 {
		t.Fatalf("len = %d", tl.Len())
	}
	if zs := tl.Zones(); len(zs) != 2 || zs[0] != berlin || zs[1] != 9*time.Hour {
		t.Errorf("zones = %v", zs)
	}
	if o, ok := tl.ZoneAt(day.Add(2*time.Hour), DefaultMaxZoneGap); !ok || o.Zone != berlin || o.Photo != photos[1] {
		t.Errorf("afternoon = %+v %v", o, ok)
	}
	if o, ok := tl.ZoneAt(day.Add(20*time.Hour), DefaultMaxZoneGap); !ok || o.Zone != 9*time.Hour {
		t.Errorf("closer to Tokyo = %+v %v", o, ok)
	}
	if _, ok := tl.ZoneAt(day.Add(-2*time.Hour), time.Hour); ok {
		t.Error("observation beyond the gap")
	}
	// By wall clock: 09:00 Tokyo on the next day is the Tokyo photo.
	if o, ok := tl.ZoneAtWall(day.Add(24*time.Hour+9*time.Hour), time.Hour); !ok || o.Zone != 9*time.Hour || !o.Wall().Equal(day.Add(33*time.Hour)) {
		t.Errorf("by wall clock = %+v %v", o, ok)
	}
	if NewTimeline(nil).Len() != 0 {
		t.Error("empty timeline")
	}
}

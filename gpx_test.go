package camsync

import (
	"strings"
	"testing"
	"time"
)

const sampleGPX = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="test" xmlns="http://www.topografix.com/GPX/1/1">
  <wpt lat="54.5" lon="13.5"><name>ignored</name></wpt>
  <trk><name>walk</name>
    <trkseg>
      <trkpt lat="54.4000" lon="13.4300"><ele>3.0</ele><time>2017-08-10T08:00:00Z</time></trkpt>
      <trkpt lat="54.4009" lon="13.4300"><time>2017-08-10T08:01:00.500Z</time></trkpt>
      <trkpt lat="54.4018" lon="13.4300"><time>2017-08-10T10:02:00+02:00</time></trkpt>
      <trkpt lat="54.4027" lon="13.4300"></trkpt>
    </trkseg>
    <trkseg>
      <trkpt lat="54.5000" lon="13.4300"><time>2017-08-10T09:00:00Z</time></trkpt>
    </trkseg>
  </trk>
</gpx>`

func TestParseGPX(t *testing.T) {
	paths, err := ParseGPX(strings.NewReader(sampleGPX), "/logs/walk.gpx")
	if err != nil || len(paths) != 2 || len(paths[0]) != 3 || len(paths[1]) != 1 {
		t.Fatalf("paths = %v, %v", paths, err)
	}
	start := time.Date(2017, 8, 10, 8, 0, 0, 0, time.UTC)
	if p := paths[0][2]; !p.Instant.Equal(start.Add(2*time.Minute)) || p.Lat != 54.4018 || p.Source != "/logs/walk.gpx" {
		t.Errorf("point with zone = %+v", p)
	}
	if p := paths[0][1]; !p.Instant.Equal(start.Add(60500 * time.Millisecond)) {
		t.Errorf("fractional second = %+v", p)
	}

	track := &Track{}
	for _, p := range paths {
		track.Add(p)
	}
	fix, ok := track.At(start.Add(90*time.Second), TrackOptions{})
	if !ok || !fix.Interpolated || fix.DistanceTo(Position{54.4013462, 13.43}) > 1 || fix.Gap != 30*time.Second {
		t.Fatalf("on the track = %+v %v", fix, ok)
	}
	if s := fix.String(); s != "54.4013462, 13.4300000 (along walk.gpx, 30s, 100m apart)" {
		t.Errorf("String = %q", s)
	}

	for _, bad := range []string{`<html/>`, `<gpx><trk><trkseg><trkpt lat="x" lon="1"><time>2017-08-10T08:00:00Z</time></trkpt></trkseg></trk></gpx>`,
		`<gpx><trk><trkseg><trkpt lat="1" lon="1"><time>yesterday</time></trkpt></trkseg></trk></gpx>`, `<gpx><trk>`} {
		if _, err := ParseGPX(strings.NewReader(bad), "bad.gpx"); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	if _, err := ReadGPX("/nonexistent/track.gpx"); err == nil {
		t.Error("missing file: no error")
	}
}

func TestDistanceText(t *testing.T) {
	for m, want := range map[float64]string{0: "0m", 129.6: "130m", 999.4: "999m", 1000: "1km", 1250: "1.2km", 12345: "12.3km"} {
		if got := FormatDistance(m); got != want {
			t.Errorf("FormatDistance(%v) = %q, want %q", m, got, want)
		}
	}
	for in, want := range map[string]float64{"500": 500, "500m": 500, "1.5km": 1500, " 2 KM ": 2000, "0": 0} {
		if got, err := ParseDistance(in); err != nil || got != want {
			t.Errorf("ParseDistance(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "far", "-5", "1e400", "5mi"} {
		if _, err := ParseDistance(in); err == nil {
			t.Errorf("ParseDistance(%q): no error", in)
		}
	}
}

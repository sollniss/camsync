package camsync

import (
	"math"
	"testing"
)

func TestPositionXMP(t *testing.T) {
	for _, c := range []struct {
		pos      Position
		lat, lon string
	}{
		{Position{54.425, 13.435}, "54,25.500000N", "13,26.100000E"},
		{Position{-33.8568, -151.2153}, "33,51.408000S", "151,12.918000W"},
		{Position{0.99999999999, 0}, "1,0.000000N", "0,0.000000E"},
	} {
		lat, lon := c.pos.XMP()
		if lat != c.lat || lon != c.lon {
			t.Errorf("%v: %s %s", c.pos, lat, lon)
		}
		back, ok := ParseXMPPosition(lat, lon)
		if !ok || back.DistanceTo(c.pos) > 0.01 {
			t.Errorf("round trip %v: %v %v", c.pos, back, ok)
		}
	}
	for _, c := range []struct {
		lat, lon string
		want     Position
		ok       bool
	}{
		{"54,25,30N", "13,26,6E", Position{54.425, 13.435}, true},
		{"-33.8568", "151.2153", Position{-33.8568, 151.2153}, true},
		{"54,61N", "13E", Position{}, false},
		{"91N", "13E", Position{}, false},
		{"", "13E", Position{}, false},
		{"54,25.5X", "13E", Position{}, false},
	} {
		got, ok := ParseXMPPosition(c.lat, c.lon)
		if ok != c.ok || (ok && got.DistanceTo(c.want) > 0.01) {
			t.Errorf("ParseXMPPosition(%q, %q) = %v %v", c.lat, c.lon, got, ok)
		}
	}
}

func TestPositionGeometry(t *testing.T) {
	// Sassnitz to Binz on Rügen: about 13 km.
	if d := (Position{54.5157, 13.6446}).DistanceTo(Position{54.4011, 13.6077}); math.Abs(d-12950) > 200 {
		t.Errorf("distance = %v", d)
	}
	mid := Position{10, 179}.Towards(Position{20, -179}, 0.5)
	if math.Abs(mid.Lat-15) > 1e-9 || math.Abs(math.Abs(mid.Lon)-180) > 1e-9 {
		t.Errorf("across the antimeridian = %v", mid)
	}
	if q := (Position{0, 0}).Towards(Position{4, 8}, 0.25); q != (Position{1, 2}) {
		t.Errorf("quarter = %v", q)
	}
}

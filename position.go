package camsync

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Position is a location in decimal degrees (WGS 84), south and west
// negative.
type Position struct {
	Lat, Lon float64
}

// String renders the position as "54.4250000, 13.4350000".
func (p Position) String() string {
	return strconv.FormatFloat(p.Lat, 'f', 7, 64) + ", " + strconv.FormatFloat(p.Lon, 'f', 7, 64)
}

// DistanceTo returns the great-circle distance to o in metres.
func (p Position) DistanceTo(o Position) float64 {
	const earthRadius = 6371008.8
	rad := math.Pi / 180
	dLat := (o.Lat - p.Lat) * rad
	dLon := (o.Lon - p.Lon) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(p.Lat*rad)*math.Cos(o.Lat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadius * math.Asin(math.Min(1, math.Sqrt(a)))
}

// Towards returns the position the fraction f of the way to o, following the
// shorter way around the antimeridian. Straight interpolation of the degrees
// is plenty for the few kilometres between two photos.
func (p Position) Towards(o Position, f float64) Position {
	dLon := o.Lon - p.Lon
	switch {
	case dLon > 180:
		dLon -= 360
	case dLon < -180:
		dLon += 360
	}
	lon := p.Lon + f*dLon
	switch {
	case lon > 180:
		lon -= 360
	case lon < -180:
		lon += 360
	}
	return Position{Lat: p.Lat + f*(o.Lat-p.Lat), Lon: lon}
}

// XMP renders the coordinates in the XMP form "54,25.500000N" and
// "13,26.100000E": whole degrees, decimal minutes and the direction.
func (p Position) XMP() (lat, lon string) {
	return xmpCoordinate(p.Lat, 'N', 'S'), xmpCoordinate(p.Lon, 'E', 'W')
}

func xmpCoordinate(deg float64, pos, neg byte) string {
	dir := pos
	if deg < 0 {
		dir, deg = neg, -deg
	}
	d := math.Floor(deg)
	m := (deg - d) * 60
	if math.Round(m*1e6) >= 60e6 { // 59.9999999 minutes rounds up to a whole degree
		d, m = d+1, 0
	}
	return strconv.Itoa(int(d)) + "," + strconv.FormatFloat(m, 'f', 6, 64) + string(dir)
}

// ParseXMPPosition parses XMP GPS coordinates: "DDD,MM.mmmk" or "DDD,MM,SSk",
// where k is the direction. It also accepts plain signed decimal degrees,
// which some tools write.
func ParseXMPPosition(lat, lon string) (Position, bool) {
	la, ok1 := parseXMPCoordinate(lat, 'N', 'S', 90)
	lo, ok2 := parseXMPCoordinate(lon, 'E', 'W', 180)
	return Position{la, lo}, ok1 && ok2
}

func parseXMPCoordinate(v string, pos, neg byte, limit float64) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	sign := 1.0
	switch unicode.ToUpper(rune(v[len(v)-1])) {
	case rune(pos):
		v = v[:len(v)-1]
	case rune(neg):
		v, sign = v[:len(v)-1], -1
	}
	parts := strings.Split(v, ",")
	if len(parts) > 3 {
		return 0, false
	}
	deg, scale := 0.0, 1.0
	for i, part := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || (i > 0 && (f < 0 || f >= 60)) {
			return 0, false
		}
		deg += f / scale
		scale *= 60
	}
	deg *= sign
	return deg, deg >= -limit && deg <= limit
}

// parseDecimalPosition parses the signed decimal degrees the exif package
// stores.
func parseDecimalPosition(lat, lon string) (Position, bool) {
	la, err1 := strconv.ParseFloat(lat, 64)
	lo, err2 := strconv.ParseFloat(lon, 64)
	return Position{la, lo}, lat != "" && lon != "" && err1 == nil && err2 == nil
}

package camsync

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Stamp is a wall-clock timestamp the way cameras record it. The time.Time
// inside is always in UTC and only used as a calendar container: no timezone
// conversion is ever applied. An optional UTC offset ("+02:00") and fractional
// seconds are carried along verbatim so they survive a shift unchanged.
type Stamp struct {
	T    time.Time
	Frac string // fractional second digits, without the dot
	TZ   string // "", "Z" or "+hh:mm"
}

// Add shifts the wall clock by d.
func (s Stamp) Add(d time.Duration) Stamp {
	s.T = s.T.Add(d)
	return s
}

// Sub returns the wall-clock difference s - o.
func (s Stamp) Sub(o Stamp) time.Duration { return s.T.Sub(o.T) }

// XMP formats the stamp as an XMP date: 2006-01-02T15:04:05[.frac][tz].
func (s Stamp) XMP() string {
	var b strings.Builder
	b.WriteString(s.T.Format("2006-01-02T15:04:05"))
	if s.Frac != "" {
		b.WriteByte('.')
		b.WriteString(s.Frac)
	}
	b.WriteString(s.TZ)
	return b.String()
}

// MarshalJSON encodes the stamp as its XMP string.
func (s Stamp) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(s.XMP())), nil
}

// UnmarshalJSON decodes an XMP date string.
func (s *Stamp) UnmarshalJSON(b []byte) error {
	v, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	st, ok := ParseXMPStamp(v)
	if !ok {
		return fmt.Errorf("invalid timestamp %q", v)
	}
	*s = st
	return nil
}

// String formats the stamp for display: the wall clock, followed by the
// zone when one is known.
func (s Stamp) String() string {
	if s.TZ == "" {
		return s.T.Format("2006-01-02 15:04:05")
	}
	return s.T.Format("2006-01-02 15:04:05 ") + s.TZ
}

var zoneRe = regexp.MustCompile(`^([+-])(\d{1,2}):?(\d{2})$`)

// ParseZone normalises a UTC offset given as "+02:00", "+0200", "-5:30",
// "Z" or "UTC" to the "+hh:mm" (or "Z") form used in XMP dates.
func ParseZone(v string) (string, error) {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "Z") || strings.EqualFold(v, "UTC") {
		return "Z", nil
	}
	m := zoneRe.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("invalid UTC offset %q (use forms like +02:00, -0530, Z)", v)
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	if h > 14 || mi > 59 {
		return "", fmt.Errorf("UTC offset %q is out of range", v)
	}
	b := []byte{m[1][0], byte('0' + h/10), byte('0' + h%10), ':', byte('0' + mi/10), byte('0' + mi%10)}
	return string(b), nil
}

var (
	// EXIF: "2006:01:02 15:04:05". Some cameras use dashes or a T; tolerate them.
	exifDateRe = regexp.MustCompile(`^(\d{4})[:\-](\d{2})[:\-](\d{2})[ T](\d{2}):(\d{2}):(\d{2})$`)
	// XMP: ISO 8601 with optional time, fraction and zone.
	xmpDateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2})(?:[.,](\d+))?)?)?(Z|[+-]\d{2}:\d{2})?$`)
	tzRe      = regexp.MustCompile(`^[+-]\d{2}:\d{2}$`)
	digitsRe  = regexp.MustCompile(`^\d+$`)
)

func makeStamp(parts []string) (Stamp, bool) {
	n := make([]int, 6)
	for i := range n {
		if i < len(parts) && parts[i] != "" {
			n[i], _ = strconv.Atoi(parts[i])
		}
	}
	y, mo, d, h, mi, s := n[0], n[1], n[2], n[3], n[4], n[5]
	if y == 0 || mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59 || s > 60 {
		return Stamp{}, false // "0000:00:00 00:00:00" and other garbage
	}
	return Stamp{T: time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC)}, true
}

// ParseExifStamp builds a Stamp from an EXIF DateTime* string plus the
// matching SubSecTime* and OffsetTime* values (either may be empty).
func ParseExifStamp(dt, subsec, tz string) (Stamp, bool) {
	m := exifDateRe.FindStringSubmatch(strings.TrimSpace(dt))
	if m == nil {
		return Stamp{}, false
	}
	s, ok := makeStamp(m[1:7])
	if !ok {
		return Stamp{}, false
	}
	subsec = strings.TrimSpace(subsec)
	if digitsRe.MatchString(subsec) {
		s.Frac = subsec
	}
	tz = strings.TrimSpace(tz)
	if tz == "Z" || tzRe.MatchString(tz) {
		s.TZ = tz
	}
	return s, true
}

// ParseGPSTime combines the EXIF GPSDateStamp ("2006:01:02") and the
// GPSTimeStamp as the exif package renders it ("15:04:05", optionally with
// fractional seconds) into a UTC time.
func ParseGPSTime(date, clock string) (time.Time, bool) {
	d, err := time.Parse("2006:01:02", strings.TrimSpace(date))
	if err != nil || d.Year() < 1990 { // GPS receivers without a fix report 1980 or 0000
		return time.Time{}, false
	}
	c, err := time.Parse("15:04:05", strings.TrimSpace(clock)) // fractions are accepted too
	if err != nil {
		return time.Time{}, false
	}
	return d.Add(c.Sub(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC))), true
}

// ZoneOffset returns the stamp's UTC offset, if it has one.
func (s Stamp) ZoneOffset() (time.Duration, bool) {
	if s.TZ == "Z" {
		return 0, true
	}
	m := zoneRe.FindStringSubmatch(s.TZ)
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	d := time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute
	if m[1] == "-" {
		d = -d
	}
	return d, true
}

// ParseXMPStamp parses an XMP (ISO 8601) date value.
func ParseXMPStamp(v string) (Stamp, bool) {
	m := xmpDateRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return Stamp{}, false
	}
	s, ok := makeStamp(m[1:7])
	if !ok {
		return Stamp{}, false
	}
	s.Frac = m[7]
	s.TZ = m[8]
	return s, true
}

// FormatDuration renders a shift compactly and always with a sign, such as
// +2m30s or -1d2h. Zero renders as "none".
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "none"
	}
	sign := "+"
	if d < 0 {
		sign = "-"
		d = -d
	}
	var b strings.Builder
	b.WriteString(sign)
	if days := d / (24 * time.Hour); days > 0 {
		b.WriteString(strconv.FormatInt(int64(days), 10))
		b.WriteByte('d')
		d -= days * 24 * time.Hour
	}
	if h := d / time.Hour; h > 0 {
		b.WriteString(strconv.FormatInt(int64(h), 10))
		b.WriteByte('h')
		d -= h * time.Hour
	}
	if m := d / time.Minute; m > 0 {
		b.WriteString(strconv.FormatInt(int64(m), 10))
		b.WriteByte('m')
		d -= m * time.Minute
	}
	if d > 0 || b.Len() == 1 {
		b.WriteString(strconv.FormatFloat(d.Seconds(), 'g', -1, 64))
		b.WriteByte('s')
	}
	return b.String()
}

var shiftRe = regexp.MustCompile(`^([+-]?)(?:(\d+)d)?(.*)$`)

// ParseShift parses a shift like "2m30s", "-45s", "1h" or "+1d2h". A leading
// sign applies to the whole value. Zero ("0", "0s") is a valid result;
// callers decide whether it makes sense for them.
func ParseShift(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	m := shiftRe.FindStringSubmatch(v)
	if m == nil || v == "" || v == "+" || v == "-" {
		return 0, fmt.Errorf("invalid duration %q", v)
	}
	var d time.Duration
	if m[2] != "" {
		days, err := strconv.Atoi(m[2])
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", v)
		}
		d += time.Duration(days) * 24 * time.Hour
	}
	if m[3] != "" {
		rest, err := time.ParseDuration(m[3])
		if err != nil || rest < 0 {
			return 0, fmt.Errorf("invalid duration %q (use forms like 2m30s, -45s, 1h, 1d2h)", v)
		}
		d += rest
	}
	if m[1] == "-" {
		d = -d
	}
	return d, nil
}

// FormatZone renders a UTC offset in the "+hh:mm" form of XMP dates.
func FormatZone(d time.Duration) string {
	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}
	return fmt.Sprintf("%s%02d:%02d", sign, int(d.Hours()), int(d.Minutes())%60)
}

// ParseZoneOffset parses a UTC offset in any form ParseZone accepts.
func ParseZoneOffset(v string) (time.Duration, error) {
	tz, err := ParseZone(v)
	if err != nil {
		return 0, err
	}
	d, _ := Stamp{TZ: tz}.ZoneOffset()
	return d, nil
}

package camsync

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseShift(t *testing.T) {
	cases := map[string]time.Duration{
		"2m30s": 150 * time.Second,
		"-45s":  -45 * time.Second,
		"+1h":   time.Hour,
		"1d2h":  26 * time.Hour,
		"-1d":   -24 * time.Hour,
		"1.5s":  1500 * time.Millisecond,
		"0":     0,
	}
	for in, want := range cases {
		got, err := ParseShift(in)
		if err != nil || got != want {
			t.Errorf("ParseShift(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1x", "-", "2m-30s"} {
		if _, err := ParseShift(bad); err == nil {
			t.Errorf("ParseShift(%q) should fail", bad)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                            "none",
		150 * time.Second:            "+2m30s",
		-45 * time.Second:            "-45s",
		time.Hour:                    "+1h",
		26*time.Hour + 5*time.Second: "+1d2h5s",
		-1500 * time.Millisecond:     "-1.5s",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestStamps(t *testing.T) {
	s, ok := ParseExifStamp("2024:12:31 23:59:30", "123", "-05:00")
	if !ok || s.XMP() != "2024-12-31T23:59:30.123-05:00" {
		t.Fatalf("exif parse: %v %v", s, ok)
	}
	if got := s.Add(45 * time.Second).XMP(); got != "2025-01-01T00:00:15.123-05:00" {
		t.Errorf("shift across year: %s", got)
	}
	for in, want := range map[string]string{
		"2024-05-01T12:34:56":           "2024-05-01T12:34:56",
		"2024-05-01T12:34:56.5Z":        "2024-05-01T12:34:56.5Z",
		"2024-05-01T12:34+02:00":        "2024-05-01T12:34:00+02:00",
		"2024-05-01":                    "2024-05-01T00:00:00",
		"  2024-05-01T12:34:56+02:00  ": "2024-05-01T12:34:56+02:00",
	} {
		got, ok := ParseXMPStamp(in)
		if !ok || got.XMP() != want {
			t.Errorf("ParseXMPStamp(%q) = %q %v, want %q", in, got.XMP(), ok, want)
		}
	}
	for _, bad := range []string{"", "2024:05:01 12:34:56", "0000-00-00T00:00:00", "2024-13-01"} {
		if _, ok := ParseXMPStamp(bad); ok {
			t.Errorf("ParseXMPStamp(%q) should fail", bad)
		}
	}
	for in, want := range map[[2]string]string{
		{"20260911", "211258"}:        "2026-09-11T21:12:58",
		{"20260911", "211258+0900"}:   "2026-09-11T21:12:58+09:00",
		{" 20260911 ", "074152-0530"}: "2026-09-11T07:41:52-05:30",
	} {
		got, ok := ParseIPTCStamp(in[0], in[1])
		if !ok || got.XMP() != want {
			t.Errorf("ParseIPTCStamp(%q, %q) = %q %v, want %q", in[0], in[1], got.XMP(), ok, want)
		}
	}
	for _, bad := range [][2]string{{"20260911", ""}, {"", "211258"}, {"00000000", "000000"}, {"2026-09-11", "21:12:58"}} {
		if _, ok := ParseIPTCStamp(bad[0], bad[1]); ok {
			t.Errorf("ParseIPTCStamp(%q, %q) should fail", bad[0], bad[1])
		}
	}
	if _, ok := ParseExifStamp("0000:00:00 00:00:00", "", ""); ok {
		t.Error("zero EXIF date should be rejected")
	}
}

func TestParseZone(t *testing.T) {
	good := map[string]string{"+02:00": "+02:00", "+0200": "+02:00", "-5:30": "-05:30", "Z": "Z", "utc": "Z", " +9:00 ": "+09:00"}
	for in, want := range good {
		if got, err := ParseZone(in); err != nil || got != want {
			t.Errorf("ParseZone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "2", "+2", "+15:00", "+02:60", "CEST", "+02:00:00"} {
		if _, err := ParseZone(bad); err == nil {
			t.Errorf("ParseZone(%q) should fail", bad)
		}
	}
	s, _ := ParseExifStamp("2024:05:01 12:00:00", "", "+02:00")
	if s.String() != "2024-05-01 12:00:00 +02:00" {
		t.Errorf("String with zone = %q", s.String())
	}
}

func TestStampJSON(t *testing.T) {
	s, _ := ParseExifStamp("2024:05:01 12:00:00", "5", "+02:00")
	b, err := json.Marshal(struct{ S *Stamp }{&s})
	if err != nil || string(b) != `{"S":"2024-05-01T12:00:00.5+02:00"}` {
		t.Errorf("marshal = %s %v", b, err)
	}
	var back struct{ S *Stamp }
	if err := json.Unmarshal(b, &back); err != nil || back.S.XMP() != s.XMP() {
		t.Errorf("unmarshal = %+v %v", back.S, err)
	}
	if err := json.Unmarshal([]byte(`{"S":"yesterday"}`), &back); err == nil {
		t.Error("bad stamp should fail")
	}
	if b, _ := json.Marshal(struct{ S *Stamp }{nil}); string(b) != `{"S":null}` {
		t.Errorf("nil = %s", b)
	}
}

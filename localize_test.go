package camsync

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sollniss/camsync/xmp"
)

func TestPlanLocalize(t *testing.T) {
	const berlin = 2 * time.Hour
	at := tripStart.Add(20 * time.Hour) // 22:00 in Berlin, 05:00 next day in Tokyo
	var instants []time.Time
	for i := range 6 {
		instants = append(instants, at.Add(time.Duration(i)*time.Hour))
	}
	phone := zonedGroup("Apple", "iPhone 15", instants, berlin, 0, "+02:00")
	phone.Photos[1].Original.TZ = "" // placed by the timeline, gets the zone
	jst := zonedGroup("Canon", "PowerShot G9 X", instants, 9*time.Hour, 0, "+09:00")
	dig := jst.Photos[0].Original.Add(time.Second)
	jst.Photos[0].Digitized = &dig
	utc := zonedGroup("SONY", "ILCE-7M3", instants, 0, 0, "")
	clocks := EstimateClocks([]*Group{jst, phone, utc}, ClockOptions{ClockZones: map[*Group]time.Duration{utc: 0}})

	ch, err := clocks.PlanLocalize(clocks.Estimate(jst), jst.Photos[0], LocalizeOptions{})
	if err != nil || ch.NewOriginal.String() != "2024-05-01 22:00:00 +02:00" || ch.Fields[xmp.DateTimeDigitized] != "2024-05-01T22:00:01+02:00" {
		t.Errorf("JST camera: %v %v %v", ch, err, ch.Fields)
	}
	ch, err = clocks.PlanLocalize(clocks.Estimate(utc), utc.Photos[0], LocalizeOptions{})
	if err != nil || ch.NewOriginal.String() != "2024-05-01 22:00:00 +02:00" {
		t.Errorf("UTC camera: %v %v", ch, err)
	}
	ch, err = clocks.PlanLocalize(clocks.Estimate(phone), phone.Photos[1], LocalizeOptions{})
	if err != nil || ch.NewOriginal.String() != "2024-05-01 23:00:00 +02:00" {
		t.Errorf("phone without zone: %v %v", ch, err)
	}
	if _, err := clocks.PlanLocalize(clocks.Estimate(phone), phone.Photos[0], LocalizeOptions{}); !errors.Is(err, ErrUnchanged) {
		t.Errorf("already local: %v", err)
	}

	// Out of reach of the timeline: skipped, or the fallback zone.
	far := photoAt("Canon", "PowerShot G9 X", at.Add(9*time.Hour+72*time.Hour), "+09:00", nil)
	if _, err := clocks.PlanLocalize(clocks.Estimate(jst), far, LocalizeOptions{}); err == nil {
		t.Error("photo out of reach should be skipped")
	}
	cet := time.Hour
	ch, err = clocks.PlanLocalize(clocks.Estimate(jst), far, LocalizeOptions{Zone: &cet})
	if err != nil || ch.NewOriginal.String() != "2024-05-04 21:00:00 +01:00" {
		t.Errorf("fallback zone: %v %v", ch, err)
	}

	// A zone-less camera whose clock zone is only a weak guess is not placed.
	unknown := zonedGroup("Ricoh", "GR III", instants[:1], 0, 0, "")
	clocks = EstimateClocks([]*Group{unknown, phone}, ClockOptions{})
	if e := clocks.Estimate(unknown); e.ClockZone.Source != ZoneFromActivity || e.Confidence != ConfidenceLow {
		t.Fatalf("one photo should give a weak guess: %+v", e)
	}
	if _, err := clocks.PlanLocalize(clocks.Estimate(unknown), unknown.Photos[0], LocalizeOptions{}); err == nil || !strings.Contains(err.Error(), "low confidence") {
		t.Errorf("weak clock zone guess should be skipped: %v", err)
	}
	unknown.Photos[0].Original.TZ = "Z" // a recorded zone needs no guess
	if _, err := clocks.PlanLocalize(clocks.Estimate(unknown), unknown.Photos[0], LocalizeOptions{}); err != nil {
		t.Errorf("recorded zone: %v", err)
	}
}

func TestOffsetIgnoresZoneRewrites(t *testing.T) {
	p := &Photo{
		FileOriginal: &Stamp{T: tripStart.Add(21 * time.Hour), TZ: "+09:00"},
		Original:     &Stamp{T: tripStart.Add(14*time.Hour - 150*time.Second), TZ: "+02:00"},
	}
	if d, ok := p.Offset(); !ok || d != -150*time.Second {
		t.Errorf("offset = %v %v", d, ok)
	}
	p.FileOriginal.TZ = "" // unknown: wall clocks
	if d, _ := p.Offset(); d != -7*time.Hour-150*time.Second {
		t.Errorf("zone-less file: %v", d)
	}
	if d, err := ParseZoneOffset("-0530"); err != nil || d != -5*time.Hour-30*time.Minute {
		t.Errorf("ParseZoneOffset = %v %v", d, err)
	}
}

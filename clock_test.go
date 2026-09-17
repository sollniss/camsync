package camsync

import (
	"math/rand/v2"
	"testing"
	"time"
)

var tripStart = time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)

// simulateTrip generates the photo times of two people on the same trip:
// moments worth a photo come every 30 minutes on average between 8:00 and
// 20:00, each person catches a moment with probability pRef or pCam, a few
// minutes early or late, and takes one to four frames.
func simulateTrip(rng *rand.Rand, days int, pRef, pCam float64) (ref, cam []time.Time) {
	for d := range days {
		t := tripStart.Add(time.Duration(d)*24*time.Hour + 8*time.Hour)
		end := t.Add(12 * time.Hour)
		for t.Before(end) {
			t = t.Add(time.Duration(rng.ExpFloat64() * float64(30*time.Minute)))
			for _, s := range []struct {
				p   float64
				out *[]time.Time
			}{{pRef, &ref}, {pCam, &cam}} {
				if rng.Float64() > s.p {
					continue
				}
				at := t.Add(time.Duration(rng.NormFloat64() * float64(2*time.Minute)))
				for i := range 1 + rng.IntN(4) {
					*s.out = append(*s.out, at.Add(time.Duration(i*(1+rng.IntN(5)))*time.Second))
				}
			}
		}
	}
	return ref, cam
}

func shifted(ts []time.Time, d time.Duration) []time.Time {
	out := make([]time.Time, len(ts))
	for i, t := range ts {
		out[i] = t.Add(d)
	}
	return out
}

func TestAlignFindsOffset(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	confident, close := 0, 0
	const trials = 40
	for range trials {
		ref, cam := simulateTrip(rng, 3, 0.7, 0.6)
		truth := time.Duration(rng.IntN(6*3600)-3*3600) * time.Second
		a, ok := Align(ref, shifted(cam, -truth), DefaultMaxOffset)
		if !ok {
			t.Fatal("no alignment")
		}
		if a.Confidence != ConfidenceLow {
			confident++
		}
		if (a.Offset - truth).Abs() <= 3*time.Minute {
			close++
		} else if a.Confidence == ConfidenceHigh {
			t.Errorf("high confidence but off by %v", a.Offset-truth)
		}
		if a.Matched == 0 {
			t.Errorf("no matched photos for %+v", a)
		}
	}
	if confident < trials*9/10 || close < trials*9/10 {
		t.Errorf("confident %d, close %d of %d", confident, close, trials)
	}
}

func TestAlignUnrelatedPhotosAreNotHighConfidence(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for range 40 {
		ref, _ := simulateTrip(rng, 2, 0.7, 0.6)
		_, cam := simulateTrip(rng, 2, 0.7, 0.6)
		if a, ok := Align(ref, cam, DefaultMaxOffset); ok && a.Confidence == ConfidenceHigh {
			t.Errorf("unrelated photos aligned with high confidence: %+v", a)
		}
	}
}

func TestAlignExactMoments(t *testing.T) {
	// Both cameras caught the same moments, the camera with a burst of two
	// frames, and it runs 10m2s to 10m4s behind.
	var ref, cam []time.Time
	for i, gap := range []int{0, 1300, 700, 2900, 400, 5000, 1800, 900} {
		at := tripStart.Add(time.Duration(i*3600+gap) * time.Second)
		ref = append(ref, at)
		cam = append(cam, at.Add(-10*time.Minute-4*time.Second), at.Add(-10*time.Minute-2*time.Second))
	}
	a, ok := Align(ref, cam, time.Hour)
	if !ok || (a.Offset-10*time.Minute-3*time.Second).Abs() > time.Second || a.Matched != len(cam) || a.Confidence != ConfidenceHigh {
		t.Errorf("alignment = %+v, %v", a, ok)
	}
	if _, ok := Align(ref, shifted(cam, -72*time.Hour), time.Hour); ok {
		t.Error("offset beyond the search range should not align")
	}
	if _, ok := Align(nil, cam, time.Hour); ok {
		t.Error("no reference should not align")
	}
}

func photoAt(mk, model string, t time.Time, tz string, gps *time.Time) *Photo {
	return &Photo{Make: mk, Model: model, Camera: CameraName(mk, model), Original: &Stamp{T: t, TZ: tz}, GPSTime: gps}
}

func TestCheckGPS(t *testing.T) {
	utc := tripStart.Add(10 * time.Hour)
	// Known zone: the camera shows 12:03 local where GPS says 10:00 UTC at +02:00.
	c, ok := CheckGPS([]*Photo{photoAt("Canon", "R5", utc.Add(2*time.Hour+3*time.Minute), "+02:00", &utc)})
	if !ok || c.Photos != 1 || c.Error != 3*time.Minute || c.Zone != 2*time.Hour || c.ZoneInferred {
		t.Errorf("known zone: %+v %v", c, ok)
	}
	// Unknown zone: the zone is the nearest quarter hour.
	stale := utc.Add(-20 * time.Minute) // one photo with a stale GPS fix is outvoted
	c, ok = CheckGPS([]*Photo{
		photoAt("Apple", "iPhone 15", utc.Add(-5*time.Hour+10*time.Second), "", &utc),
		photoAt("Apple", "iPhone 15", utc.Add(-5*time.Hour+12*time.Second), "", &utc),
		photoAt("Apple", "iPhone 15", utc.Add(-5*time.Hour), "", &stale),
	})
	if !ok || c.Photos != 3 || c.Error != 12*time.Second || c.Zone != -5*time.Hour || !c.ZoneInferred {
		t.Errorf("inferred zone: %+v %v", c, ok)
	}
	if _, ok := CheckGPS([]*Photo{photoAt("Canon", "R5", utc, "", nil)}); ok {
		t.Error("no GPS should report false")
	}
}

func TestIsPhone(t *testing.T) {
	for _, c := range []struct {
		mk, model string
		want      bool
	}{
		{"Apple", "iPhone 15 Pro", true},
		{"Google", "Pixel 8", true},
		{"samsung", "SM-S918B", true},
		{"SAMSUNG", "NX500", false},
		{"SONY", "XQ-DQ72", true},
		{"SONY", "ILCE-7M3", false},
		{"Sony", "F3111", true},
		{"SONY", "DSC-RX100M7", false},
		{"HTC", "HTC Desire 500", true},
		{"Xiaomi", "23049PCD8G", true},
		{"Canon", "Canon EOS R5", false},
		{"", "", false},
	} {
		if got := IsPhone(c.mk, c.model); got != c.want {
			t.Errorf("IsPhone(%q, %q) = %v", c.mk, c.model, got)
		}
	}
}

func TestEstimateClocks(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	ref, cam := simulateTrip(rng, 3, 0.8, 0.7)
	group := func(mk, model string, ts []time.Time) *Group {
		g := &Group{Camera: CameraName(mk, model)}
		for _, at := range ts {
			g.Photos = append(g.Photos, photoAt(mk, model, at, "", nil))
		}
		return g
	}
	phone := group("Apple", "iPhone 15", ref)
	behind := group("Canon", "Canon EOS R5", shifted(cam, -10*time.Minute))

	// A camera with GPS and a known zone that runs 90 seconds fast.
	gpsCam := &Group{Camera: "Nikon Z 6"}
	for _, at := range cam[:5] {
		utc := at.Add(-2 * time.Hour)
		gpsCam.Photos = append(gpsCam.Photos, photoAt("Nikon", "Z 6", at.Add(90*time.Second), "+02:00", &utc))
	}
	// A GPS camera without zone but with a good clock is a reference.
	goodGPS := &Group{Camera: "GoPro"}
	utc := ref[0].Add(-9 * time.Hour)
	goodGPS.Photos = append(goodGPS.Photos, photoAt("GoPro", "HERO12", ref[0].Add(5*time.Second), "", &utc))
	undated := &Group{Camera: "Scanner", Photos: []*Photo{{Camera: "Scanner"}}}

	groups := []*Group{behind, goodGPS, gpsCam, phone, undated}
	est := EstimateClocks(groups, ClockOptions{})
	if e := est[0]; !e.Found || e.Reference || e.Basis != BasisActivity || (e.Offset-10*time.Minute).Abs() > 3*time.Minute {
		t.Errorf("behind = %+v", e)
	}
	if e := est[1]; !e.Reference || e.Basis != BasisGPS || e.GPS == nil || !e.GPS.ZoneInferred {
		t.Errorf("good GPS = %+v", e)
	}
	if e := est[2]; !e.Found || e.Basis != BasisGPS || e.Offset != -90*time.Second || e.Matched != 5 || e.Confidence != ConfidenceHigh {
		t.Errorf("gps camera = %+v", e)
	}
	if e := est[3]; !e.Reference || e.Basis != BasisPhone || e.Found {
		t.Errorf("phone = %+v", e)
	}
	if e := est[4]; e.Found || e.Reference || e.Note != "no timestamps" {
		t.Errorf("undated = %+v", e)
	}

	// Naming a reference replaces the automatic choice.
	est = EstimateClocks([]*Group{phone, behind}, ClockOptions{References: []*Group{behind}})
	if e := est[0]; e.Reference || !e.Found || (e.Offset+10*time.Minute).Abs() > 3*time.Minute {
		t.Errorf("phone against user reference = %+v", e)
	}
	if e := est[1]; !e.Reference || e.Basis != BasisUser {
		t.Errorf("user reference = %+v", e)
	}

	// Without any reference nothing can be estimated.
	est = EstimateClocks([]*Group{behind}, ClockOptions{})
	if e := est[0]; e.Found || e.Note != "no reference camera" {
		t.Errorf("no reference = %+v", e)
	}
}

func TestEstimateClocksComparesMomentsInTime(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	ref, cam := simulateTrip(rng, 3, 0.8, 0.7)
	// The phone records +02:00 on half its photos only; the other half fall
	// back to that zone. The camera is on +09:00 home time and 2m30s fast.
	phone := &Group{Camera: "HTC Desire 500"}
	for i, at := range ref {
		tz := ""
		if i%2 == 0 {
			tz = "+02:00"
		}
		phone.Photos = append(phone.Photos, photoAt("HTC", "Desire 500", at, tz, nil))
	}
	camera := &Group{Camera: "Canon PowerShot G9 X"}
	for _, at := range cam {
		camera.Photos = append(camera.Photos, photoAt("Canon", "PowerShot G9 X", at.Add(7*time.Hour+150*time.Second), "+09:00", nil))
	}
	est := EstimateClocks([]*Group{camera, phone}, ClockOptions{})
	if e := est[0]; !e.Found || (e.Offset+150*time.Second).Abs() > 3*time.Minute || e.Zone != "+09:00" {
		t.Errorf("camera = %+v", e)
	}
	if e := est[1]; !e.Reference || e.Zone != "+02:00" {
		t.Errorf("phone = %+v", e)
	}

	// Without zones on either side, wall clocks are compared.
	for _, p := range camera.Photos {
		p.Original.TZ = ""
	}
	for _, p := range phone.Photos {
		p.Original.TZ = ""
	}
	est = EstimateClocks([]*Group{camera, phone}, ClockOptions{MaxOffset: 8 * time.Hour})
	if e := est[0]; !e.Found || (e.Offset+7*time.Hour+150*time.Second).Abs() > 3*time.Minute || e.Zone != "" {
		t.Errorf("wall clocks: %+v", e)
	}
}

package camsync

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/internal/testimg"
	"github.com/sollniss/camsync/xmp"
)

func TestGeotag(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2017, 8, 10, 8, 0, 0, 0, time.UTC) // 10:00 in Rügen
	write := func(name string, tiff []byte) {
		t.Helper()
		mustWrite(t, filepath.Join(dir, name), testimg.JPEG(tiff))
	}
	// The phone walks north, 100 m per minute, with GPS.
	for i := range 3 {
		utc := start.Add(time.Duration(i*4) * time.Minute)
		write(fmt.Sprintf("IMG_%d.JPG", i), testimg.BuildTIFF(binary.LittleEndian,
			[]testimg.Entry{testimg.ASCII(exif.TagMake, "HTC"), testimg.ASCII(exif.TagModel, "HTC Desire 500"),
				testimg.GPSAt(binary.LittleEndian, utc.Format("2006:01:02"), utc.Hour(), utc.Minute(), 0, 54.4+float64(i)*4*0.0009, 13.43)},
			[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, utc.Add(2*time.Hour).Format("2006:01:02 15:04:05"))}))
	}
	// The camera on Japan time photographs in between and much later.
	for _, m := range []int{2, 6, 60} {
		wall := start.Add(time.Duration(m)*time.Minute + 9*time.Hour).Format("2006:01:02 15:04:05")
		write(fmt.Sprintf("CAN_%03d.JPG", m), testimg.BuildTIFF(binary.LittleEndian,
			[]testimg.Entry{testimg.ASCII(exif.TagMake, "Canon"), testimg.ASCII(exif.TagModel, "Canon PowerShot G9 X")},
			[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, wall), testimg.ASCII(exif.TagOffsetTimeOriginal, "+09:00")}))
	}

	scan := func() ([]*Group, *Clocks) {
		t.Helper()
		res, err := Scan(context.Background(), dir, ScanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		groups := GroupPhotos(res.Photos)
		return groups, EstimateClocks(groups, ClockOptions{})
	}
	groups, clocks := scan()
	canon, phone := groups[0], groups[1]
	if p := phone.Photos[1]; p.Position == nil || p.PositionEstimated || p.Position.DistanceTo(Position{54.4036, 13.43}) > 1 {
		t.Fatalf("phone position = %v", p.Position)
	}
	track := clocks.Track()
	if track.Len() != 3 {
		t.Fatalf("track = %d", track.Len())
	}

	if _, err := clocks.PlanGeotag(clocks.Estimate(phone), phone.Photos[0], track, TrackOptions{}); !errors.Is(err, ErrHasPosition) {
		t.Errorf("measured position: %v", err)
	}
	var changes []*Change
	for i, want := range []Position{{54.4018, 13.43}, {54.4054, 13.43}} {
		ch, err := clocks.PlanGeotag(clocks.Estimate(canon), canon.Photos[i], track, TrackOptions{})
		if err != nil || !ch.Fix.Interpolated || ch.Fix.DistanceTo(want) > 1 || ch.Fields[xmp.GPSProcessingMethod] != GeotagMethod || ch.Fields[xmp.Make] != "Canon" {
			t.Fatalf("photo %d: %+v %v", i, ch, err)
		}
		changes = append(changes, ch)
	}
	if _, err := clocks.PlanGeotag(clocks.Estimate(canon), canon.Photos[2], track, TrackOptions{}); err == nil || err.Error() != "no located photo within 15m" {
		t.Errorf("out of reach: %v", err)
	}
	for _, ch := range changes {
		if _, err := ch.Write(false); err != nil {
			t.Fatal(err)
		}
	}

	// Estimates are recognised, left alone when unchanged and not evidence.
	groups, clocks = scan()
	canon = groups[0]
	if p := canon.Photos[0]; p.Position == nil || !p.PositionEstimated {
		t.Fatalf("estimated position not read back: %+v", p)
	}
	if clocks.Track().Len() != 3 {
		t.Error("estimates became evidence")
	}
	if _, err := clocks.PlanGeotag(clocks.Estimate(canon), canon.Photos[0], clocks.Track(), TrackOptions{}); !errors.Is(err, ErrUnchanged) {
		t.Errorf("second run: %v", err)
	}

	// Reset removes the estimate and keeps the rest of the sidecar.
	ch, err := PlanReset(canon.Photos[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Write(false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(canon.Photos[0].Sidecar)
	if f, _ := xmp.Read(data); f[xmp.GPSLatitude] != "" || f[xmp.GPSProcessingMethod] != "" || f[xmp.Model] != "Canon PowerShot G9 X" {
		t.Errorf("after reset: %v\n%s", f, data)
	}
}

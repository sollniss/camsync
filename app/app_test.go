package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sollniss/camsync"

	"github.com/sollniss/camsync/internal/testimg"
	"github.com/sollniss/camsync/xmp"
)

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func demoDir(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	canon := testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00"))
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), canon)
	mustWrite(t, filepath.Join(dir, "IMG_0002.JPG"), canon)
	mustWrite(t, filepath.Join(dir, "DSC_0001.JPG"), testimg.JPEG(testimg.Simple("SONY", "ILCE-7M3", "2024:05:01 12:00:30")))
	mustWrite(t,
		filepath.Join(dir, "nodate.png"),
		testimg.PNG(testimg.BuildTIFF(
			binary.LittleEndian,
			[]testimg.Entry{testimg.ASCII(0x010F, "SONY"), testimg.ASCII(0x0110, "ILCE-7M3")}, nil,
		), false),
	)
	mustWrite(t, filepath.Join(dir, "notes.txt"), []byte("hello"))
	return dir, canon
}

func TestList(t *testing.T) {
	dir, _ := demoDir(t)
	ctx := context.Background()
	res, err := (ListHandler{}).Handle(ctx, ListQuery{Scope{Dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Photos != 4 || res.Ignored != 1 || len(res.Cameras) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if c := res.Cameras[1]; c.Index != 2 || c.Name != "SONY ILCE-7M3" || len(c.Photos) != 2 || c.NoDate != 1 || c.OffsetText() != "none" {
		t.Errorf("sony = %+v", c)
	}
	if _, err := (ListHandler{}).Handle(ctx, ListQuery{Scope{Dir: filepath.Join(dir, "missing")}}); err == nil {
		t.Error("missing dir should fail")
	}
	if _, err := (ListHandler{}).Handle(ctx, ListQuery{Scope{Dir: filepath.Join(dir, "notes.txt")}}); err == nil {
		t.Error("file instead of dir should fail")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (ListHandler{}).Handle(cancelled, ListQuery{Scope{Dir: dir}}); err != context.Canceled {
		t.Errorf("cancelled: %v", err)
	}
}

func TestShiftAndReset(t *testing.T) {
	dir, canon := demoDir(t)
	ctx := context.Background()
	scope := Scope{Dir: dir}

	var seen []Entry
	h := ShiftHandler{Progress: func(e Entry) { seen = append(seen, e) }}

	// Dry run reports but writes nothing.
	res, err := h.Handle(ctx, ShiftCommand{Scope: scope, Camera: "r5", By: 150 * time.Second, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Camera != "Canon EOS R5" || res.Written != 2 || res.Created != 2 || !res.DryRun || len(seen) != 2 {
		t.Errorf("dry run = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "IMG_0001.JPG.xmp")); err == nil {
		t.Fatal("dry run wrote a sidecar")
	}

	res, err = h.Handle(ctx, ShiftCommand{Scope: scope, Camera: "1", By: 150 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 2 || res.Created != 2 || res.Skipped != 0 || res.Failed != 0 {
		t.Errorf("shift = %+v", res)
	}
	e := res.Entries[0]
	if e.Action != ActionCreate ||
		e.Before.String() != "2024-05-01 12:00:00" ||
		e.After.String() != "2024-05-01 12:02:30" ||
		filepath.Base(e.Sidecar) != "IMG_0001.JPG.xmp" {
		t.Errorf("entry = %+v", e)
	}

	// Sony: one photo has no date and is skipped; zone only, no shift.
	res, err = h.Handle(ctx, ShiftCommand{Scope: scope, Camera: "sony", Zone: "+0200"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || res.Skipped != 1 || res.Zone != "+02:00" || res.By != 0 {
		t.Errorf("zone shift = %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "DSC_0001.JPG.xmp"))
	if f, _ := xmp.Read(data); f[xmp.DateTimeOriginal] != "2024-05-01T12:00:30+02:00" {
		t.Errorf("sony sidecar = %v", f)
	}
	for _, e := range res.Entries {
		if strings.HasSuffix(e.Path, "nodate.png") && (e.Action != ActionSkip || e.Reason == "") {
			t.Errorf("nodate entry = %+v", e)
		}
	}

	// Validation.
	for _, bad := range []ShiftCommand{
		{Scope: scope, By: time.Second},                        // no camera
		{Scope: scope, Camera: "r5"},                           // nothing to do
		{Scope: scope, Camera: "r5", Zone: "CEST"},             // bad zone
		{Scope: scope, Camera: "nikon", By: time.Second},       // unknown camera
		{Scope: scope, Camera: "o", By: time.Second},           // ambiguous
		{Scope: Scope{Dir: ""}, Camera: "r5", By: time.Second}, // no dir
	} {
		if _, err := h.Handle(ctx, bad); err == nil {
			t.Errorf("%+v should fail", bad)
		}
	}

	// Reset.
	rres, err := (ResetHandler{}).Handle(ctx, ResetCommand{Scope: scope, Camera: "canon"})
	if err != nil {
		t.Fatal(err)
	}
	if rres.Written != 2 || rres.Created != 0 || rres.By != 0 || rres.Zone != "" {
		t.Errorf("reset = %+v", rres)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "IMG_0001.JPG.xmp"))
	if f, _ := xmp.Read(data); f[xmp.DateTimeOriginal] != "2024-05-01T12:00:00" {
		t.Errorf("after reset = %v", f)
	}
	if _, err := (ResetHandler{}).Handle(ctx, ResetCommand{Scope: scope}); err == nil {
		t.Error("reset without camera should fail")
	}

	// Cancellation stops between photos and returns the partial result.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	res, err = (ShiftHandler{}).Handle(cancelled, ShiftCommand{Scope: scope, Camera: "r5", By: time.Second})
	if err != context.Canceled || res != nil {
		t.Errorf("cancelled before start: %v %v", res, err)
	}

	if after, _ := os.ReadFile(filepath.Join(dir, "IMG_0001.JPG")); !bytes.Equal(canon, after) {
		t.Fatal("image file was modified")
	}
	_ = camsync.SidecarByExt
}

func TestValidationErrorsAreTyped(t *testing.T) {
	dir := t.TempDir()
	var verr *ValidationError
	_, err := (ShiftHandler{}).Handle(context.Background(), ShiftCommand{Scope: Scope{Dir: dir}, Camera: "x", By: time.Second})
	if !errors.As(err, &verr) {
		t.Errorf("unknown camera should be a ValidationError, got %v", err)
	}
	_, err = (ListHandler{}).Handle(context.Background(), ListQuery{Scope{Dir: filepath.Join(dir, "missing")}})
	if !errors.As(err, &verr) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing directory should be a ValidationError wrapping fs.ErrNotExist, got %v", err)
	}
}

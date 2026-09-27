package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/internal/testimg"
	"github.com/sollniss/camsync/preview"
)

// realJPEG wraps a TIFF block into a JPEG with pixels, so that previews work.
func realJPEG(t *testing.T, tiff []byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 3), uint8(y * 4), 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	var out bytes.Buffer
	out.Write(data[:2])
	out.Write([]byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)})
	out.Write(app1)
	out.Write(data[2:])
	return out.Bytes()
}

func library(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, mk, model, dto string, pos *[2]float64) {
		ifd0 := []testimg.Entry{testimg.ASCII(exif.TagMake, mk), testimg.ASCII(exif.TagModel, model)}
		if pos != nil {
			ifd0 = append(ifd0, testimg.GPSAt(binary.LittleEndian, "2024:05:02", 10, 0, 0, pos[0], pos[1]))
		}
		tiff := testimg.BuildTIFF(binary.LittleEndian, ifd0, []testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, dto)})
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, realJPEG(t, tiff), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	away := &[2]float64{54.42, 13.43}
	for d := 1; d <= 3; d++ {
		for h := 9; h <= 12; h++ {
			write("Trips/Rügen/c"+string(rune('0'+d))+string(rune('a'+h))+".jpg", "Canon", "EOS R5", "2024:05:0"+string(rune('0'+d))+" "+string(rune('0'+h/10))+string(rune('0'+h%10))+":00:00", nil)
		}
	}
	write("Trips/Rügen/p1.jpg", "Apple", "iPhone 15", "2024:05:02 10:00:00", away)
	write("Inbox/p2.jpg", "Apple", "iPhone 15", "2024:05:01 11:30:00", away)
	write("Inbox/p3.jpg", "Apple", "iPhone 15", "2024:05:03 11:30:00", away)
	return root
}

func newServer(t *testing.T, root string) (*Server, *httptest.Server) {
	t.Helper()
	scope := app.Scope{Dir: root}
	res, err := app.OrganizeHandler{}.Handle(context.Background(), app.OrganizeQuery{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	s := New(scope, res, preview.NewCache(filepath.Join(t.TempDir(), "cache"), 1))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func getJSON(t *testing.T, ts *httptest.Server, path string, v any) *http.Response {
	t.Helper()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if v != nil {
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return res
}

func post(t *testing.T, ts *httptest.Server, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(res.Body)
	return res, out.Bytes()
}

func TestLibraryAndOptions(t *testing.T) {
	root := library(t)
	_, ts := newServer(t, root)

	var lib LibraryJSON
	if res := getJSON(t, ts, "/api/library", &lib); res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	if lib.Root != root || len(lib.Photos) != 15 || len(lib.Events) != 1 || len(lib.Folders) != 2 || len(lib.Suggestions) != 1 {
		t.Fatalf("library = %d photos, %d events, %d folders, %d suggestions", len(lib.Photos), len(lib.Events), len(lib.Folders), len(lib.Suggestions))
	}
	if lib.Options.Gap != "1d" || lib.Options.Jump != "50km" || lib.Options.Layout != "auto" || lib.Options.MinTripPhotos != 10 {
		t.Errorf("options = %+v", lib.Options)
	}
	s := lib.Suggestions[0]
	if s.Kind != "move" || s.ToRel != "Trips/Rügen" || !s.Exists || len(s.Photos) != 2 || lib.Photos[s.Photos[0]].Rel != "Inbox/p2.jpg" {
		t.Errorf("suggestion = %+v", s)
	}
	if e := lib.Events[0]; e.Center == nil || len(e.Folders) != 2 || *lib.Photos[e.Photos[0]].Event != 0 {
		t.Errorf("event = %+v", e)
	}

	// Replanning with a tiny gap splits the days.
	if res := getJSON(t, ts, "/api/library?gap=1h&layout=periods&home=52.52,13.40", &lib); res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	if len(lib.Events) < 3 || lib.Options.Gap != "1h" || lib.Options.Layout != "periods" || lib.Home == nil || lib.Home.Inferred || lib.Options.Home == "" {
		t.Errorf("replanned = %d events, options %+v, home %+v", len(lib.Events), lib.Options, lib.Home)
	}
	// The options stick, and an empty home infers again.
	getJSON(t, ts, "/api/library?home=", &lib)
	if lib.Options.Gap != "1h" || !lib.Home.Inferred {
		t.Errorf("after clearing home: %+v %+v", lib.Options, lib.Home)
	}
	for _, q := range []string{"gap=-1", "gap=abc", "jump=0", "layout=weekly", "home=x", "minTripPhotos=0"} {
		if res := getJSON(t, ts, "/api/library?"+q, nil); res.StatusCode != 400 {
			t.Errorf("%s: %s", q, res.Status)
		}
	}
}

func TestPreview(t *testing.T) {
	root := library(t)
	_, ts := newServer(t, root)
	path := filepath.Join(root, "Inbox", "p2.jpg")
	res, err := http.Get(ts.URL + "/api/preview?path=" + url.QueryEscape(path) + "&size=40")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("%s %s", res.Status, res.Header.Get("Content-Type"))
	}
	img, err := jpeg.Decode(res.Body)
	if err != nil || img.Bounds().Dx() != 40 || img.Bounds().Dy() != 30 {
		t.Errorf("preview = %v, %v", img.Bounds(), err)
	}
	// Only scanned photos are served, whatever the path says.
	for _, p := range []string{"/etc/passwd", filepath.Join(root, "Inbox", "missing.jpg"), filepath.Join(root, "..", "x.jpg")} {
		if res := getJSON(t, ts, "/api/preview?path="+url.QueryEscape(p), nil); res.StatusCode != 404 {
			t.Errorf("%s: %s", p, res.Status)
		}
	}
	if res := getJSON(t, ts, "/api/preview?path="+url.QueryEscape(path)+"&size=x", nil); res.StatusCode != 400 {
		t.Errorf("bad size: %s", res.Status)
	}
	if res := getJSON(t, ts, "/", nil); res.StatusCode != 200 {
		t.Errorf("index: %s", res.Status)
	}
	if res := getJSON(t, ts, "/vendor/leaflet/leaflet.js", nil); res.StatusCode != 200 {
		t.Errorf("leaflet: %s", res.Status)
	}
}

func TestApplyAndRescan(t *testing.T) {
	root := library(t)
	_, ts := newServer(t, root)
	var lib LibraryJSON
	getJSON(t, ts, "/api/library", &lib)
	s := lib.Suggestions[0]
	var moves []camsync.Move
	for _, i := range s.Photos {
		moves = append(moves, camsync.Move{Path: lib.Photos[i].Path, To: s.To})
	}
	headers := map[string]string{"X-Camsync": "1"}

	// Without the header, or from another origin, nothing happens.
	if res, _ := post(t, ts, "/api/apply", applyRequest{Moves: moves}, nil); res.StatusCode != 403 {
		t.Errorf("no header: %s", res.Status)
	}
	if res, _ := post(t, ts, "/api/apply", applyRequest{Moves: moves}, map[string]string{"X-Camsync": "1", "Origin": "http://evil.example"}); res.StatusCode != 403 {
		t.Errorf("cross origin: %s", res.Status)
	}
	if res, _ := post(t, ts, "/api/rescan", nil, nil); res.StatusCode != 403 {
		t.Errorf("rescan without header: %s", res.Status)
	}
	// Photos outside the scan are refused.
	if res, body := post(t, ts, "/api/apply", applyRequest{Moves: []camsync.Move{{Path: "/etc/passwd", To: root}}}, headers); res.StatusCode != 400 || !strings.Contains(string(body), "not a photo") {
		t.Errorf("outside: %s %s", res.Status, body)
	}
	if res, body := post(t, ts, "/api/apply", applyRequest{Moves: []camsync.Move{{Path: moves[0].Path, To: filepath.Dir(root)}}}, headers); res.StatusCode != 400 || !strings.Contains(string(body), "outside") {
		t.Errorf("destination outside: %s %s", res.Status, body)
	}

	res, body := post(t, ts, "/api/apply", applyRequest{Moves: moves, DryRun: true}, headers)
	var out applyResponse
	if err := json.Unmarshal(body, &out); err != nil || res.StatusCode != 200 {
		t.Fatalf("dry run: %s %s", res.Status, body)
	}
	if !out.DryRun || out.Moved != 2 || out.Library != nil {
		t.Errorf("dry run = %+v", out)
	}
	if _, err := os.Stat(moves[0].Path); err != nil {
		t.Error("dry run moved a file")
	}

	res, body = post(t, ts, "/api/apply", applyRequest{Moves: moves}, headers)
	if err := json.Unmarshal(body, &out); err != nil || res.StatusCode != 200 {
		t.Fatalf("apply: %s %s", res.Status, body)
	}
	if out.Moved != 2 || out.Failed != 0 || out.Library == nil || len(out.Library.Suggestions) != 0 {
		t.Errorf("apply = moved %d failed %d, library %v", out.Moved, out.Failed, out.Library != nil)
	}
	if _, err := os.Stat(filepath.Join(root, "Trips", "Rügen", "p2.jpg")); err != nil {
		t.Error(err)
	}
	// The moved photo is known under its new path.
	getJSON(t, ts, "/api/library", &lib)
	if len(lib.Folders) != 1 || len(lib.Photos) != 15 {
		t.Errorf("after apply: %d folders, %d photos", len(lib.Folders), len(lib.Photos))
	}
	res, body = post(t, ts, "/api/rescan", nil, headers)
	if err := json.Unmarshal(body, &lib); err != nil || res.StatusCode != 200 || len(lib.Photos) != 15 {
		t.Errorf("rescan: %s %s", res.Status, body)
	}
}

func TestServe(t *testing.T) {
	root := library(t)
	s, _ := newServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	var got string
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- Serve(ctx, "127.0.0.1:0", s, func(url string) { got = url; close(ready) })
	}()
	<-ready
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("url = %q", got)
	}
	if res, err := http.Get(got + "api/library"); err != nil || res.StatusCode != 200 {
		t.Errorf("serve: %v %v", res, err)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("serve returned %v", err)
	}
}

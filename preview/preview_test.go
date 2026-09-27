package preview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gradient builds a w x h image that is red on the left and blue on the
// right, so that orientation can be told from the result.
func gradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{B: 255, A: 255}
			if x < w/2 {
				c = color.NRGBA{R: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeResult(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, _, b, _ := c.RGBA()
	return r > 0x8000 && b < 0x8000
}

func TestExtractScalesAndOrients(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(path, encodeJPEG(t, gradient(640, 480)), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := Extract(path, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	img := decodeResult(t, data)
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 48 {
		t.Errorf("size = %v", b)
	}
	if !isRed(img.At(5, 24)) || isRed(img.At(58, 24)) {
		t.Error("colours moved")
	}

	// Rotated 90 degrees clockwise the red half is at the top.
	data, err = Extract(path, 64, 6)
	if err != nil {
		t.Fatal(err)
	}
	img = decodeResult(t, data)
	if b := img.Bounds(); b.Dx() != 48 || b.Dy() != 64 {
		t.Errorf("rotated size = %v", b)
	}
	if !isRed(img.At(24, 5)) || isRed(img.At(24, 58)) {
		t.Error("rotation wrong")
	}

	// A small image is not enlarged.
	small := filepath.Join(dir, "small.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(20, 10)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err = Extract(small, 64, 0)
	if err != nil {
		t.Fatal(err)
	}
	if b := decodeResult(t, data).Bounds(); b.Dx() != 20 || b.Dy() != 10 {
		t.Errorf("small = %v", b)
	}
}

func TestExtractEmbedded(t *testing.T) {
	dir := t.TempDir()
	// A pretend raw file: a header, a tiny thumbnail, a usable preview and
	// some bytes that look like a JPEG start but are not one.
	var raw bytes.Buffer
	raw.WriteString("FAKERAW\x00\x00\x00")
	raw.Write(encodeJPEG(t, gradient(32, 24)))
	raw.Write([]byte{0xFF, 0xD8, 0xFF, 0x00, 0x00})
	raw.Write(encodeJPEG(t, gradient(400, 300)))
	raw.Write(encodeJPEG(t, gradient(1600, 1200)))
	raw.Write(bytes.Repeat([]byte{0}, 100))
	path := filepath.Join(dir, "a.nef")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := Extract(path, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	img := decodeResult(t, data)
	if b := img.Bounds(); b.Dx() != 200 || b.Dy() != 150 {
		t.Errorf("size = %v", b)
	}
	if !isRed(img.At(10, 75)) || isRed(img.At(190, 75)) {
		t.Error("colours moved")
	}

	// Nothing usable inside.
	junk := filepath.Join(dir, "b.heic")
	if err := os.WriteFile(junk, bytes.Repeat([]byte{1, 2, 3}, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(junk, 200, 0); !errors.Is(err, ErrNoPreview) {
		t.Errorf("junk: %v", err)
	}
}

func TestCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(path, encodeJPEG(t, gradient(300, 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewCache(filepath.Join(dir, "cache"), 0)
	first, err := c.Get(path, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, "cache", "*", "*.jpg"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries = %v, %v", entries, err)
	}
	// The cached file is served as is.
	if err := os.WriteFile(entries[0], []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := c.Get(path, 100, 0)
	if err != nil || string(second) != "cached" || bytes.Equal(first, second) {
		t.Errorf("second = %q, %v", second, err)
	}
	// Another size is another entry.
	if _, err := c.Get(path, 50, 0); err != nil {
		t.Fatal(err)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "cache", "*", "*.jpg")); len(entries) != 2 {
		t.Errorf("entries = %v", entries)
	}
	// Failures are remembered.
	junk := filepath.Join(dir, "junk.dat")
	if err := os.WriteFile(junk, []byte("nothing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(junk, 50, 0); !errors.Is(err, ErrNoPreview) {
		t.Errorf("junk: %v", err)
	}
	if _, err := c.Get(junk, 50, 0); !errors.Is(err, ErrNoPreview) || len(c.failed) != 1 {
		t.Errorf("junk again: %v, failed %v", err, c.failed)
	}
	if _, err := c.Get(filepath.Join(dir, "missing.jpg"), 50, 0); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}

	// Pruning drops the preview used least recently.
	entries, _ = filepath.Glob(filepath.Join(dir, "cache", "*", "*.jpg"))
	old := time.Now().Add(-48 * time.Hour)
	for _, e := range entries {
		if err := os.Chtimes(e, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Get(path, 100, 0); err != nil { // used again, so kept
		t.Fatal(err)
	}
	freed, err := c.Prune(int64(len("cached")))
	if err != nil || freed == 0 {
		t.Fatalf("prune = %d, %v", freed, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "cache", "*", "*.jpg")); len(left) != 1 {
		t.Errorf("left = %v", left)
	} else if data, _ := os.ReadFile(left[0]); string(data) != "cached" {
		t.Errorf("kept the wrong preview: %q", data)
	}
	if freed, err := NewCache(filepath.Join(dir, "none"), 1).Prune(0); freed != 0 || err != nil {
		t.Errorf("missing cache: %d %v", freed, err)
	}
}

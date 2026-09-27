// Package preview renders small JPEG previews of photos for a user
// interface. JPEG and PNG files are decoded and scaled down; every other
// format is searched for the JPEG preview that raw files embed, so no raw
// decoding is needed. Files are only ever read.
package preview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// DefaultSize is the long side of a preview when none is asked for.
const DefaultSize = 320

// ErrNoPreview is returned for images no preview can be made of: formats
// without an embedded JPEG that the standard library cannot decode.
var ErrNoPreview = errors.New("preview: no usable image found")

const (
	maxPixels      = 120_000_000 // refuse to decode anything larger
	maxCandidates  = 64          // embedded JPEG starts examined per file
	scanChunk      = 1 << 20
	minEmbeddedDim = 64 // smaller embedded images are icons, not previews
)

// Extract renders the image at path as a JPEG whose long side is at most
// size pixels, rotated as the EXIF orientation (1 to 8, 0 for unknown)
// says. It returns ErrNoPreview when the file cannot be decoded.
func Extract(path string, size, orientation int) ([]byte, error) {
	if size <= 0 {
		size = DefaultSize
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	img, err := decode(f, st.Size(), size)
	if err != nil {
		return nil, err
	}
	out := orient(scale(img, size), orientation)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decode returns the image itself for JPEG and PNG files and the best
// embedded JPEG otherwise. want is the preview size, which chooses among
// several embedded images.
func decode(f *os.File, size int64, want int) (image.Image, error) {
	head := make([]byte, 8)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return decodeChecked(io.NewSectionReader(f, 0, size), jpeg.DecodeConfig, jpeg.Decode)
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return decodeChecked(io.NewSectionReader(f, 0, size), png.DecodeConfig, png.Decode)
	}
	return decodeEmbedded(f, size, want)
}

func decodeChecked(r *io.SectionReader, config func(io.Reader) (image.Config, error), dec func(io.Reader) (image.Image, error)) (image.Image, error) {
	c, err := config(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoPreview, err)
	}
	if c.Width*c.Height > maxPixels {
		return nil, fmt.Errorf("%w: %dx%d is too large", ErrNoPreview, c.Width, c.Height)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, err := dec(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoPreview, err)
	}
	return img, nil
}

// decodeEmbedded finds the JPEG images inside a file by their start marker
// and decodes the smallest one that is still larger than the preview, or
// the largest when none is.
func decodeEmbedded(f *os.File, size int64, want int) (image.Image, error) {
	type candidate struct {
		off  int64
		w, h int
	}
	var cands []candidate
	buf := make([]byte, scanChunk+2)
	for off := int64(0); off < size && len(cands) < maxCandidates; off += scanChunk {
		n, err := f.ReadAt(buf, off)
		if err != nil && err != io.EOF {
			return nil, err
		}
		chunk := buf[:n]
		for i := 0; i < len(chunk)-2 && len(cands) < maxCandidates; {
			j := bytes.Index(chunk[i:], []byte{0xFF, 0xD8, 0xFF})
			if j < 0 {
				break
			}
			start := off + int64(i+j)
			if i+j < scanChunk { // a marker straddling the overlap belongs to the next chunk
				c, err := jpeg.DecodeConfig(io.NewSectionReader(f, start, size-start))
				if err == nil && c.Width >= minEmbeddedDim && c.Height >= minEmbeddedDim && c.Width*c.Height <= maxPixels {
					cands = append(cands, candidate{start, c.Width, c.Height})
				}
			}
			i += j + 1
		}
	}
	if len(cands) == 0 {
		return nil, ErrNoPreview
	}
	best := cands[0]
	for _, c := range cands[1:] {
		long, bestLong := max(c.w, c.h), max(best.w, best.h)
		switch {
		case bestLong < want && long > bestLong: // bigger is better until it fits
			best = c
		case long >= want && (bestLong < want || long < bestLong): // the smallest that fits
			best = c
		}
	}
	img, err := jpeg.Decode(io.NewSectionReader(f, best.off, size-best.off))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoPreview, err)
	}
	return img, nil
}

// scale shrinks img so that its long side is size pixels, averaging a grid
// of samples per output pixel. Images already small enough are copied.
func scale(img image.Image, size int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if w <= size && h <= size {
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
		return out
	}
	f := float64(size) / float64(max(w, h))
	nw, nh := max(1, int(math.Round(float64(w)*f))), max(1, int(math.Round(float64(h)*f)))
	samples := min(4, int(math.Ceil(1/f)))
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	at := sampler(img)
	for y := 0; y < nh; y++ {
		y0 := float64(y) / f
		y1 := float64(y+1) / f
		for x := 0; x < nw; x++ {
			x0 := float64(x) / f
			x1 := float64(x+1) / f
			var r, g, bl, n uint32
			for sy := 0; sy < samples; sy++ {
				py := b.Min.Y + min(h-1, int(y0+(y1-y0)*(float64(sy)+0.5)/float64(samples)))
				for sx := 0; sx < samples; sx++ {
					px := b.Min.X + min(w-1, int(x0+(x1-x0)*(float64(sx)+0.5)/float64(samples)))
					cr, cg, cb := at(px, py)
					r, g, bl, n = r+cr, g+cg, bl+cb, n+1
				}
			}
			i := out.PixOffset(x, y)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 0xFF
		}
	}
	return out
}

// sampler returns a fast pixel reader for the common decoded formats.
func sampler(img image.Image) func(x, y int) (r, g, b uint32) {
	switch m := img.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32) {
			c := m.YCbCrAt(x, y)
			r, g, b := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
			return uint32(r) << 8, uint32(g) << 8, uint32(b) << 8
		}
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			return uint32(m.Pix[i]) << 8, uint32(m.Pix[i+1]) << 8, uint32(m.Pix[i+2]) << 8
		}
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			return uint32(m.Pix[i]) << 8, uint32(m.Pix[i+1]) << 8, uint32(m.Pix[i+2]) << 8
		}
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32) {
			v := uint32(m.GrayAt(x, y).Y) << 8
			return v, v, v
		}
	}
	return func(x, y int) (uint32, uint32, uint32) {
		r, g, b, _ := img.At(x, y).RGBA()
		return r, g, b
	}
}

// orient applies the EXIF orientation so that the preview is upright.
func orient(img *image.RGBA, orientation int) *image.RGBA {
	if orientation <= 1 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if orientation >= 5 {
		nw, nh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // mirrored and rotated 90 CCW
				dx, dy = y, x
			case 6: // rotated 90 CW
				dx, dy = h-1-y, x
			case 7: // mirrored and rotated 90 CW
				dx, dy = h-1-y, w-1-x
			case 8: // rotated 90 CCW
				dx, dy = y, w-1-x
			}
			si, di := img.PixOffset(x, y), out.PixOffset(dx, dy)
			copy(out.Pix[di:di+4], img.Pix[si:si+4])
		}
	}
	return out
}

// Cache keeps previews on disk, keyed by the file's path, size and
// modification time, and limits how many are rendered at once. Failures
// are remembered for the life of the cache so that a file that cannot be
// decoded is not read again on every request.
type Cache struct {
	Dir string

	sem    chan struct{}
	mu     sync.Mutex
	failed map[string]error
}

// NewCache creates a cache in dir, rendering at most workers previews at
// once (0 selects 2). The directory is created on first use.
func NewCache(dir string, workers int) *Cache {
	if workers <= 0 {
		workers = 2
	}
	return &Cache{Dir: dir, sem: make(chan struct{}, workers), failed: map[string]error{}}
}

// DefaultCacheDir is the preview cache in the user's cache directory.
func DefaultCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "camsync", "previews"), nil
}

// Get returns the preview of path, rendering it when it is not cached.
func (c *Cache) Get(path string, size, orientation int) ([]byte, error) {
	if size <= 0 {
		size = DefaultSize
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := cacheKey(path, size, orientation, st)
	c.mu.Lock()
	err, known := c.failed[key]
	c.mu.Unlock()
	if known {
		return nil, err
	}
	file := filepath.Join(c.Dir, key[:2], key+".jpg")
	if data, err := os.ReadFile(file); err == nil {
		return data, nil
	}
	c.sem <- struct{}{}
	defer func() { <-c.sem }()
	data, err := Extract(path, size, orientation)
	if err != nil {
		if errors.Is(err, ErrNoPreview) {
			c.mu.Lock()
			c.failed[key] = err
			c.mu.Unlock()
		}
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
		tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
		if err := os.WriteFile(tmp, data, 0o644); err == nil {
			if err := os.Rename(tmp, file); err != nil {
				_ = os.Remove(tmp)
			}
		}
	}
	return data, nil
}

func cacheKey(path string, size, orientation int, st os.FileInfo) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%d", abs, size, orientation, st.Size(), st.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:])
}

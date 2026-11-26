package cover_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
)

// halves is w×h, red on its left half and blue on its right.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func encoded(t *testing.T, enc func(*bytes.Buffer, image.Image) error, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := enc(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func asJPEG(b *bytes.Buffer, img image.Image) error { return jpeg.Encode(b, img, nil) }
func asPNG(b *bytes.Buffer, img image.Image) error  { return png.Encode(b, img) }
func asGIF(b *bytes.Buffer, img image.Image) error  { return gif.Encode(b, img, nil) }

// A 4×2 red lossless WebP, made with cwebp.
const webp = "UklGRhwAAABXRUJQVlA4TA8AAAAvA0AAAAcQ5Y/+ByKi/wEA"

func decoded(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil || format != "jpeg" {
		t.Fatalf("kept %q, %v", format, err)
	}
	return img
}

func TestNormalizeAcceptsEachFormat(t *testing.T) {
	src := halves(40, 20)
	raw, err := base64.StdEncoding.DecodeString(webp)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		b    []byte
		w, h int
	}{
		"jpeg": {encoded(t, asJPEG, src), 40, 20},
		"png":  {encoded(t, asPNG, src), 40, 20},
		"gif":  {encoded(t, asGIF, src), 40, 20},
		"webp": {raw, 4, 2},
	} {
		out, err := cover.Normalize(c.b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := decoded(t, out).Bounds(); got.Dx() != c.w || got.Dy() != c.h {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestNormalizeScalesDown(t *testing.T) {
	out, err := cover.Normalize(encoded(t, asPNG, halves(3200, 800)))
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded(t, out).Bounds(); got.Dx() != cover.MaxSide || got.Dy() != cover.MaxSide/4 {
		t.Fatalf("%v", got)
	}
}

func TestNormalizeRefusesWhatIsNotAnImage(t *testing.T) {
	for name, b := range map[string][]byte{
		"text":      []byte("<html>not an image</html>"),
		"empty":     nil,
		"truncated": encoded(t, asPNG, halves(40, 20))[:60],
	} {
		if _, err := cover.Normalize(b); !errors.Is(err, cover.ErrNotImage) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// pngHeader is a PNG that claims w×h and holds no pixels.
func pngHeader(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	chunk := append([]byte("IHDR"), ihdr...)
	b := append([]byte("\x89PNG\r\n\x1a\n"), binary.BigEndian.AppendUint32(nil, 13)...)
	b = append(b, chunk...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(chunk))
}

// The canvas a file claims is checked before any pixel is decoded.
func TestNormalizeRefusesAHugeCanvas(t *testing.T) {
	if _, err := cover.Normalize(pngHeader(20000, 20000)); !errors.Is(err, cover.ErrTooManyPixels) {
		t.Fatal(err)
	}
}

// withOrientation puts an Exif segment naming orientation o after the
// JPEG's start marker, as a camera writes it.
func withOrientation(b []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01")
	tiff = binary.BigEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := append([]byte{0xFF, 0xE1}, binary.BigEndian.AppendUint16(nil, uint16(len(seg)+2))...) //nolint:gosec // a few dozen bytes
	return append(append(append([]byte{}, b[:2]...), append(app1, seg...)...), b[2:]...)
}

// A camera's Exif orientation is applied, so a portrait photo stays
// upright once its metadata is gone.
func TestNormalizeAppliesExifOrientation(t *testing.T) {
	src := encoded(t, asJPEG, halves(40, 20))
	out, err := cover.Normalize(withOrientation(src, 6))
	if err != nil {
		t.Fatal(err)
	}
	img := decoded(t, out)
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("%v", b)
	}
	// Turned clockwise, the red left half is on top.
	if r, _, bl, _ := img.At(10, 5).RGBA(); r < bl {
		t.Errorf("top is not red: %v", img.At(10, 5))
	}
	if bytes.Contains(out, []byte("Exif")) {
		t.Error("metadata kept")
	}
}

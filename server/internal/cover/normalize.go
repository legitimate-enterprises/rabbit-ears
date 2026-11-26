package cover

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"sync"

	// Registered with image for Decode; JPEG comes with image/jpeg.
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/webp"

	xdraw "golang.org/x/image/draw"
)

// Bounds on what Normalize takes and keeps.
const (
	// MaxPixels bounds the canvas a file may claim, so a small file cannot
	// make the server decode a huge one.
	MaxPixels = 50_000_000
	// MaxSide is the longer side of what is kept.
	MaxSide = 1600
)

// Errors Normalize answers for files it will not keep.
var (
	ErrNotImage      = errors.New("not a JPEG, PNG, GIF or WebP image")
	ErrTooManyPixels = errors.New("an image of more than 50 megapixels")
)

// decoding admits one decode at a time, bounding the memory uploads take.
var decoding sync.Mutex

// Normalize proves b is an image and returns it as a JPEG at most MaxSide
// on its longer side, upright by its Exif orientation, transparency on
// black, and with no metadata.
func Normalize(b []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, ErrTooManyPixels
	}
	decoding.Lock()
	defer decoding.Unlock()
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotImage
	}
	w, h := fit(src.Bounds().Dx(), src.Bounds().Dy())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.Black, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	if format == "jpeg" {
		dst = orient(dst, orientation(b))
	}
	var out bytes.Buffer
	err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85})
	return out.Bytes(), err
}

// fit scales w×h down to fit MaxSide, never up.
func fit(w, h int) (int, int) {
	if w <= MaxSide && h <= MaxSide {
		return w, h
	}
	if w >= h {
		return MaxSide, max(1, h*MaxSide/w)
	}
	return max(1, w*MaxSide/h), MaxSide
}

// orientation reads the Exif orientation (1 to 8) of a JPEG, 1 when it has
// none.
func orientation(b []byte) int {
	for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
		marker, size := b[i+1], int(binary.BigEndian.Uint16(b[i+2:]))
		end := i + 2 + size
		if marker == 0xDA || size < 2 || end > len(b) {
			return 1
		}
		if seg := b[i+4 : end]; marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return exifOrientation(seg[6:])
		}
		i = end
	}
	return 1
}

// exifOrientation finds tag 0x0112 in the first IFD of a TIFF block.
func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder = binary.BigEndian
	if string(t[:2]) == "II" {
		order = binary.LittleEndian
	}
	ifd := int(order.Uint32(t[4:]))
	if ifd < 0 || ifd+2 > len(t) {
		return 1
	}
	n := int(order.Uint16(t[ifd:]))
	for k := range n {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if order.Uint16(t[e:]) == 0x0112 {
			if o := int(order.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient turns img as Exif orientation o says it should be shown.
func orient(img *image.RGBA, o int) *image.RGBA {
	if o <= 1 {
		return img
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			default:
				dx, dy = y, w-1-x
			}
			copy(out.Pix[out.PixOffset(dx, dy):][:4], img.Pix[img.PixOffset(x, y):][:4])
		}
	}
	return out
}

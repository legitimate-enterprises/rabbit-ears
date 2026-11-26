// Package ident makes a station's ident, the card a station shows between
// two programs: who it is, how long until it is back and what is up next.
// The card is drawn here, one frame a minute, and ffmpeg stitches the frames
// into HLS segments with a silent audio track, ahead of time: an ident
// takes no encoder while it airs.
package ident

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// SegmentSeconds is the length of every segment of an ident but its last,
// which ends where the next program starts.
const SegmentSeconds = 6

// InitName is the fMP4 initialization segment in an ident's directory.
const InitName = "init.mp4"

// Row is one program of a card's Up next list.
type Row struct {
	At      time.Time
	Title   string // the show, or the movie
	Episode string // "S7 E17"; empty for a movie
	Name    string // the episode's title, or the movie's year
}

// Card is what an ident shows. Its times are drawn in their own location.
type Card struct {
	Station string
	Rating  string // "" draws no badge
	Next    time.Time
	// Rows are the programs up next, the first in a card of its own; more
	// than four are not drawn.
	Rows []Row
}

// BackIn is the minutes from the minute of now until the next program,
// counted up, and at least one.
func (c Card) BackIn(now time.Time) int {
	return max(1, int(math.Ceil(c.Next.Sub(now.Truncate(time.Minute)).Minutes())))
}

// Key names the ident of this card that starts at from: another card, or
// another start, is another key.
func (c Card) Key(from time.Time) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\x00%d\x00%s\x00%s", from.UnixMilli(), c.Next.UnixMilli(), c.Station, c.Rating)
	for _, r := range c.rows() {
		_, _ = fmt.Fprintf(h, "\x00%d\x00%s\x00%s\x00%s", r.At.UnixMilli(), r.Title, r.Episode, r.Name)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c Card) rows() []Row { return c.Rows[:min(len(c.Rows), 4)] }

// Durations are the lengths, in seconds, of the segments of an ident that
// runs for length.
func Durations(length time.Duration) []float64 {
	var out []float64
	for left := length.Seconds(); left > 0.0005; left -= SegmentSeconds {
		out = append(out, math.Round(min(left, SegmentSeconds)*1000)/1000)
	}
	return out
}

//go:embed fonts/*.ttf
var fontFiles embed.FS

const (
	regular = "Regular"
	medium  = "Medium"
	semi    = "SemiBold"
	bold    = "Bold"
)

var (
	fonts     = map[string]*opentype.Font{}
	faces     = map[string]font.Face{}
	fontsOnce sync.Once
	// drawing is held for a whole frame: a kept face draws each glyph
	// through one buffer of its own, so it serves one frame at a time.
	drawing sync.Mutex
)

// face is Inter at one weight and pixel size. Faces are kept: a card draws
// the same few for every frame. The caller holds drawing.
func face(weight string, px float64) font.Face {
	fontsOnce.Do(func() {
		for _, w := range []string{regular, medium, semi, bold} {
			b, err := fontFiles.ReadFile("fonts/Inter-" + w + ".ttf")
			if err == nil {
				fonts[w], err = opentype.Parse(b)
			}
			if err != nil {
				panic("ident: bundled font: " + err.Error())
			}
		}
	})
	key := weight + strconv.FormatFloat(px, 'f', -1, 64)
	if f, ok := faces[key]; ok {
		return f
	}
	f, err := opentype.NewFace(fonts[weight], &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic("ident: bundled font: " + err.Error())
	}
	faces[key] = f
	return f
}

var (
	background = color.RGBA{0x1c, 0x1c, 0x1e, 0xff}
	primary    = color.RGBA{0xf2, 0xf2, 0xf7, 0xff}
	secondary  = color.RGBA{0xc7, 0xc7, 0xcc, 0xff}
	muted      = color.RGBA{0x98, 0x98, 0x9d, 0xff}
	accent     = color.RGBA{0xf2, 0xb3, 0x3d, 0xff}
	panel      = color.RGBA{0x2a, 0x2a, 0x2d, 0xff}
	outline    = color.NRGBA{0xff, 0xff, 0xff, 0x24}
)

// The frame, its margins and its two columns, in pixels.
const (
	width, height = 1920, 1080
	marginX       = 120
	marginY       = 96
	listWidth     = 720
	columnGap     = 120
	nameWidth     = width - 2*marginX - listWidth - columnGap
)

type canvas struct{ *image.RGBA }

// advance is the width of s with tracking em added after each glyph.
func advance(f font.Face, s string, tracking float64) float64 {
	var w fixed.Int26_6
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			w += f.Kern(prev, r)
		}
		a, _ := f.GlyphAdvance(r)
		w += a
		prev = r
	}
	return float64(w)/64 + tracking*float64(len([]rune(s)))
}

// text draws s with its baseline at y, from x, and returns where it ends.
func (c canvas) text(f font.Face, col color.Color, x, y float64, s string, tracking float64) float64 {
	d := font.Drawer{Dst: c.RGBA, Src: image.NewUniform(col), Face: f, Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			d.Dot.X += f.Kern(prev, r)
		}
		d.DrawString(string(r))
		d.Dot.X += fixed.Int26_6(tracking * 64)
		prev = r
	}
	return float64(d.Dot.X) / 64
}

// fit is s cut to max pixels wide, with an ellipsis when it was cut.
func fit(f font.Face, s string, limit float64) string {
	if advance(f, s, 0) <= limit {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && advance(f, strings.TrimRight(string(r), " ")+"…", 0) > limit {
		r = r[:len(r)-1]
	}
	return strings.TrimRight(string(r), " ") + "…"
}

// pen draws a path into a rasterizer that covers only the path's box.
type pen struct {
	z      *vector.Rasterizer
	ox, oy float32
}

// roundRect adds a rounded rectangle, clockwise, or the other way round
// when hole, which cuts it out of a path already there.
func (p pen) roundRect(x0, y0, x1, y1, r float64, hole bool) {
	const k = 0.5523 // a quarter circle as one cubic
	type pt struct{ x, y float64 }
	corners := []struct{ from, c1, c2, to pt }{
		{pt{x1 - r, y0}, pt{x1 - r + k*r, y0}, pt{x1, y0 + r - k*r}, pt{x1, y0 + r}},
		{pt{x1, y1 - r}, pt{x1, y1 - r + k*r}, pt{x1 - r + k*r, y1}, pt{x1 - r, y1}},
		{pt{x0 + r, y1}, pt{x0 + r - k*r, y1}, pt{x0, y1 - r + k*r}, pt{x0, y1 - r}},
		{pt{x0, y0 + r}, pt{x0, y0 + r - k*r}, pt{x0 + r - k*r, y0}, pt{x0 + r, y0}},
	}
	at := func(v pt) (float32, float32) { return float32(v.x) - p.ox, float32(v.y) - p.oy }
	cube := func(a, b, c pt) {
		ax, ay := at(a)
		bx, by := at(b)
		cx, cy := at(c)
		p.z.CubeTo(ax, ay, bx, by, cx, cy)
	}
	p.z.MoveTo(at(pt{x0 + r, y0}))
	for i := range corners {
		if c := corners[i]; !hole {
			p.z.LineTo(at(c.from))
			cube(c.c1, c.c2, c.to)
		} else {
			c = corners[len(corners)-1-i]
			p.z.LineTo(at(c.to))
			cube(c.c2, c.c1, c.from)
		}
	}
	p.z.ClosePath()
}

// shape fills, in col, what draw adds to a path inside the box given.
func (c canvas) shape(col color.Color, x0, y0, x1, y1 float64, draw func(pen)) {
	ox, oy := int(math.Floor(x0)), int(math.Floor(y0))
	z := vector.NewRasterizer(int(math.Ceil(x1))-ox, int(math.Ceil(y1))-oy)
	draw(pen{z, float32(ox), float32(oy)})
	z.Draw(c.RGBA, image.Rect(ox, oy, ox+z.Size().X, oy+z.Size().Y), image.NewUniform(col), image.Point{})
}

// box fills a rounded rectangle.
func (c canvas) box(col color.Color, x0, y0, x1, y1, r float64) {
	c.shape(col, x0, y0, x1, y1, func(p pen) { p.roundRect(x0, y0, x1, y1, r, false) })
}

// frame strokes a rounded rectangle, the stroke w wide and inside it.
func (c canvas) frame(col color.Color, x0, y0, x1, y1, r, w float64) {
	c.shape(col, x0, y0, x1, y1, func(p pen) {
		p.roundRect(x0, y0, x1, y1, r, false)
		p.roundRect(x0+w, y0+w, x1-w, y1-w, max(r-w, 0), true)
	})
}

// line strokes from one point to another, w wide with round ends.
func (c canvas) line(col color.Color, x0, y0, x1, y1, w float64) {
	dx, dy := x1-x0, y1-y0
	n := math.Hypot(dx, dy)
	px, py := -dy/n*w/2, dx/n*w/2
	c.shape(col, min(x0, x1)-w, min(y0, y1)-w, max(x0, x1)+w, max(y0, y1)+w, func(p pen) {
		p.z.MoveTo(float32(x0+px)-p.ox, float32(y0+py)-p.oy)
		p.z.LineTo(float32(x1+px)-p.ox, float32(y1+py)-p.oy)
		p.z.LineTo(float32(x1-px)-p.ox, float32(y1-py)-p.oy)
		p.z.LineTo(float32(x0-px)-p.ox, float32(y0-py)-p.oy)
		p.z.ClosePath()
	})
	for _, e := range [][2]float64{{x0, y0}, {x1, y1}} {
		c.box(col, e[0]-w/2, e[1]-w/2, e[0]+w/2, e[1]+w/2, w/2)
	}
}

// logo draws the rabbit ears, two antenna strokes over a rounded set, in a
// square of size at x, y.
func (c canvas) logo(x, y, size float64) {
	u := size / 24
	at := func(px, py float64) (float64, float64) { return x + px*u, y + py*u }
	sw := 2 * u
	ax, ay := at(12, 13)
	lx, ly := at(6, 3)
	rx, ry := at(18, 3)
	c.line(accent, ax, ay, lx, ly, sw)
	c.line(accent, ax, ay, rx, ry, sw)
	x0, y0 := at(4, 13)
	x1, y1 := at(20, 21)
	c.frame(accent, x0-sw/2, y0-sw/2, x1+sw/2, y1+sw/2, 2*u+sw/2, sw)
}

// Inter's ascent and line height, in ems.
const (
	ascent     = 0.96875
	lineHeight = 1.2109375
)

// Frame draws the card as it shows during the minute of now, told in
// Next's location: 1920 by 1080, with no seconds anywhere, so every frame
// of one minute is the same.
func (c Card) Frame(now time.Time) *image.RGBA {
	drawing.Lock()
	defer drawing.Unlock()
	img := canvas{image.NewRGBA(image.Rect(0, 0, width, height))}
	draw.Draw(img, img.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)

	// Top: the logo and name left, the time right, on one center line.
	const top = marginY + 28
	img.logo(marginX, marginY, 56)
	brand := face(semi, 36)
	img.text(brand, primary, marginX+56+20, top+36*(ascent-lineHeight/2), "Rabbit Ears", 0)
	tf := face(medium, 32)
	now12 := now.In(c.Next.Location()).Format("3:04 PM")
	img.text(tf, muted, width-marginX-advance(tf, now12, 0), top+32*(ascent-lineHeight/2), now12, 0)

	// Bottom left, upward from the margin: Back in, the name, the label.
	const bottom = height - marginY
	base := bottom - 88*(lineHeight-ascent)
	word, number := face(medium, 44), face(bold, 88)
	n := c.BackIn(now)
	unit := "minutes"
	if n == 1 {
		unit = "minute"
	}
	x := img.text(word, secondary, marginX, base, "Back in", 0) + 20
	x = img.text(number, accent, x, base, strconv.Itoa(n), 0) + 20
	img.text(word, secondary, x, base, unit, 0)

	nameBottom := bottom - 88*lineHeight - 28
	size := 168.0
	for ; size > 80 && advance(face(bold, size), c.Station, -0.02*size) > nameWidth; size -= 8 {
	}
	nf := face(bold, size)
	name := c.Station
	if advance(nf, name, -0.02*size) > nameWidth {
		name = fit(nf, name, nameWidth)
	}
	// A line as tall as its type sits its baseline this far above its foot.
	img.text(nf, primary, marginX, nameBottom-size*(0.5+lineHeight/2-ascent), name, -0.02*size)

	labelMid := nameBottom - 168 - 28 - 22
	lf := face(semi, 30)
	x = img.text(lf, accent, marginX, labelMid+30*(ascent-lineHeight/2), "STATION", 0.12*30)
	if c.Rating != "" {
		rf := face(semi, 26)
		x += 24
		w := advance(rf, c.Rating, 0) + 2*(12+2)
		img.frame(outline, x, labelMid-22, x+w, labelMid+22, 8, 2)
		img.text(rf, secondary, x+14, labelMid+26*(ascent-lineHeight/2), c.Rating, 0)
	}

	c.list(img, bottom)
	return img.RGBA
}

// list draws Up next in the right column, upward from bottom: plain rows
// under the next program's card under the heading.
func (c Card) list(img canvas, bottom float64) {
	rows := c.rows()
	if len(rows) == 0 {
		return
	}
	const left, pad, timeWidth, gap = width - marginX - listWidth, 32, 130, 32
	textX := float64(left + pad + timeWidth + gap)
	room := float64(width-marginX-pad) - textX
	y := bottom
	rowFace, rowTime := face(regular, 30), face(semi, 30)
	const rowHeight = 30*lineHeight + 16
	for i := len(rows) - 1; i >= 1; i-- {
		r := rows[i]
		base := y - 8 - 30*(lineHeight-ascent)
		img.text(rowTime, muted, left+pad, base, r.At.Format("3:04"), 0)
		img.text(rowFace, secondary, textX, base, fit(rowFace, join(r.Title, r.Name), room), 0)
		y -= rowHeight + 20
	}
	r := rows[0]
	titleFace, subFace := face(semi, 40), face(regular, 30)
	sub := join(r.Episode, r.Name)
	inner := 40 * lineHeight
	if sub != "" {
		inner += 6 + 30*lineHeight
	}
	cardTop := y - inner - 2*28
	img.box(panel, left, cardTop, width-marginX, y, 20)
	base := cardTop + 28 + 40*ascent
	img.text(face(bold, 36), accent, left+pad, base, r.At.Format("3:04"), 0)
	img.text(titleFace, primary, textX, base, fit(titleFace, r.Title, room), 0)
	if sub != "" {
		img.text(subFace, secondary, textX, cardTop+28+40*lineHeight+6+30*ascent, fit(subFace, sub, room), 0)
	}
	hf := face(semi, 28)
	img.text(hf, muted, left, cardTop-20-28*(lineHeight-ascent), "UP NEXT", 0.12*28)
}

func join(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " · " + b
}

// fps is the ident's frame rate. Its picture changes once a minute, so the
// rate only sets how far the video can run past the exact end.
const fps = 1

// Render writes the part of c's ident that airs from from until until, at
// or before c.Next, into dir: the segments Durations names, 0.m4s onward
// with InitName, or 0.ts onward when ts. A dir that exists is a finished
// ident and is left alone. ffmpeg runs at low priority and encodes in
// software, so it holds no transcode slot.
func Render(ctx context.Context, dir string, c Card, from, until time.Time, ts bool) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	length := until.Sub(from)
	if length <= 0 {
		return fmt.Errorf("ident: nothing between %s and %s", from, until)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".ident-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	frames := filepath.Join(tmp, "frames")
	if err := os.Mkdir(frames, 0o750); err != nil {
		return err
	}
	// One frame for each minute the ident touches, and one more so the
	// last has a successor to be held until.
	first := from.Truncate(time.Minute)
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	for i, m := 0, first; !m.After(until.Truncate(time.Minute).Add(time.Minute)); i, m = i+1, m.Add(time.Minute) {
		f, err := os.Create(filepath.Join(frames, fmt.Sprintf("%04d.png", i))) //nolint:gosec // a path of this function's own making
		if err != nil {
			return err
		}
		err = enc.Encode(f, c.Frame(m))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	out := filepath.Join(tmp, "out")
	if err := os.Mkdir(out, 0o750); err != nil {
		return err
	}
	gop := strconv.Itoa(SegmentSeconds * fps)
	args := []string{"-n", "19", "ffmpeg", "-hide_banner", "-nostdin", "-v", "error", "-y",
		"-framerate", "1/60", "-i", filepath.Join(frames, "%04d.png"),
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo",
		// The frames sit on the minutes of the clock; the ident starts
		// partway into the first.
		"-vf", fmt.Sprintf("fps=%d,trim=start=%.3f,setpts=PTS-STARTPTS,format=yuv420p", fps, from.Sub(first).Seconds()),
		"-t", strconv.FormatFloat(length.Seconds(), 'f', 3, 64),
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "stillimage", "-profile:v", "high", "-bf", "0",
		"-g", gop, "-keyint_min", gop, "-sc_threshold", "0",
		"-c:a", "aac", "-b:a", "32k", "-ac", "2",
		// The pictures come a minute at a time and the sound evenly: with
		// no limit the muxer waits for both, so a segment holds its own.
		"-max_interleave_delta", "0",
		"-map_metadata", "-1", "-f", "hls", "-hls_time", strconv.Itoa(SegmentSeconds), "-hls_list_size", "0", "-start_number", "0"}
	name := "%d.m4s"
	if ts {
		name = "%d.ts"
		args = append(args, "-hls_segment_type", "mpegts")
	} else {
		args = append(args, "-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", InitName)
	}
	args = append(args, "-hls_segment_filename", filepath.Join(out, name), filepath.Join(out, "ident.m3u8"))
	//nolint:gosec // arguments are built from server paths and times
	if b, err := exec.CommandContext(ctx, "nice", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ident: ffmpeg: %w: %s", err, strings.TrimSpace(string(b)))
	}
	if err := os.Rename(out, dir); err != nil && !os.IsExist(err) {
		// Another render of the same ident got there first.
		if _, serr := os.Stat(dir); serr != nil {
			return err
		}
	}
	return nil
}

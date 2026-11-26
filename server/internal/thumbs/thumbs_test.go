package thumbs

import (
	"context"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeJPEG(t *testing.T, path string, luma uint8) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = luma
	}
	f, err := os.Create(path) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSeekMs(t *testing.T) {
	cases := []struct {
		pos        int
		start, end int64
		retry      bool
		want       int64
	}{
		{0, 0, 60000, false, 15000},
		{0, 0, 60000, true, 10000},
		{1, 60000, 120000, false, 64000},
		{1, 60000, 120000, true, 70000},
		{2, 100000, 102000, false, 100000}, // shorter than the offset: chapter start
		{2, 100000, 105000, true, 100000},
	}
	for _, c := range cases {
		if got := SeekMs(c.pos, c.start, c.end, c.retry); got != c.want {
			t.Errorf("SeekMs(%d,%d,%d,%v) = %d, want %d", c.pos, c.start, c.end, c.retry, got, c.want)
		}
	}
}

func TestMeanLuma(t *testing.T) {
	dir := t.TempDir()
	dark, bright := filepath.Join(dir, "d.jpg"), filepath.Join(dir, "b.jpg")
	writeJPEG(t, dark, 2)
	writeJPEG(t, bright, 180)
	if l, err := MeanLuma(dark); err != nil || l >= DarkLuma {
		t.Errorf("dark %v %v", l, err)
	}
	if l, err := MeanLuma(bright); err != nil || l < 170 || l > 190 {
		t.Errorf("bright %v %v", l, err)
	}
	if _, err := MeanLuma(filepath.Join(dir, "none.jpg")); err == nil {
		t.Error("want error")
	}
}

func TestDarkFrameRetriesLater(t *testing.T) {
	var seeks []int64
	gen := func(_ context.Context, seek int64, out string) error {
		seeks = append(seeks, seek)
		if len(seeks) == 1 {
			writeJPEG(t, out, 1)
		} else {
			writeJPEG(t, out, 150)
		}
		return nil
	}
	out := filepath.Join(t.TempDir(), "c.jpg")
	if err := chapter(context.Background(), gen, 1, 60000, 120000, out); err != nil {
		t.Fatal(err)
	}
	if len(seeks) != 2 || seeks[0] != 64000 || seeks[1] != 70000 {
		t.Errorf("seeks %v", seeks)
	}
	if l, _ := MeanLuma(out); l < 100 {
		t.Errorf("kept the dark frame: %v", l)
	}
}

func TestBrightFrameKeptFirstTime(t *testing.T) {
	n := 0
	gen := func(_ context.Context, _ int64, out string) error { n++; writeJPEG(t, out, 150); return nil }
	if err := chapter(context.Background(), gen, 0, 0, 60000, filepath.Join(t.TempDir(), "c.jpg")); err != nil || n != 1 {
		t.Errorf("%d %v", n, err)
	}
}

func TestFFmpegOnFixture(t *testing.T) {
	src, _ := filepath.Abs("../../../fixtures/chapters.mkv")
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	out := filepath.Join(t.TempDir(), "sub", "c.jpg")
	if err := Chapter(context.Background(), src, 1, 3000, 7000, out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil || cfg.Width == 0 {
		t.Errorf("%v %v", cfg, err)
	}
}

func TestCover(t *testing.T) {
	in, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", "music", "The Example Band", "First Light (2024)", "01 Opening.flac"))
	if _, err := os.Stat(in); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	out := filepath.Join(t.TempDir(), "a", "cover.jpg")
	if err := Cover(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if c, err := jpeg.DecodeConfig(f); err != nil || c.Width != 300 {
		t.Fatalf("%+v %v", c, err)
	}
	if err := Cover(context.Background(), filepath.Join(filepath.Dir(in), "02 Second Wind.mp3"), out); err == nil {
		t.Fatal("a file without a picture fails")
	}
}

// Art passes over dark points a quarter and a half in, keeps the first
// bright frame, and crops its poster from it.
func TestArtSkipsDarkFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("crops with ffmpeg")
	}
	var seeks []int64
	gen := func(_ context.Context, seek int64, out string) error {
		seeks = append(seeks, seek)
		luma := uint8(1)
		if len(seeks) == 3 {
			luma = 150
		}
		writeJPEG(t, out, luma)
		return nil
	}
	dir := t.TempDir()
	if err := art(context.Background(), gen, 100000, filepath.Join(dir, "a", "frame.jpg"), filepath.Join(dir, "a", "poster.jpg")); err != nil {
		t.Fatal(err)
	}
	if len(seeks) != 3 || seeks[0] != 25000 || seeks[1] != 50000 || seeks[2] != 10000 {
		t.Errorf("seeks %v", seeks)
	}
	if l, _ := MeanLuma(filepath.Join(dir, "a", "frame.jpg")); l < 100 {
		t.Errorf("kept a dark frame: %v", l)
	}
}

func jpegSize(t *testing.T, p string) image.Config {
	t.Helper()
	f, err := os.Open(p) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	c, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// On a 16:9 fixture the frame keeps its shape at 1280 wide and the poster
// is its centre at 2:3, 500 wide.
func TestArtOnFixture(t *testing.T) {
	src, _ := filepath.Abs("../../../fixtures/h264-high41.mp4")
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	dir := t.TempDir()
	frame, poster := filepath.Join(dir, "frame.jpg"), filepath.Join(dir, "poster.jpg")
	if err := Art(context.Background(), src, 10000, frame, poster); err != nil {
		t.Fatal(err)
	}
	if c := jpegSize(t, frame); c.Width != 1280 || c.Height != 720 {
		t.Errorf("frame %dx%d", c.Width, c.Height)
	}
	if c := jpegSize(t, poster); c.Width != 500 || c.Height < 748 || c.Height > 752 {
		t.Errorf("poster %dx%d", c.Width, c.Height)
	}
}

// A video's attached picture, as a Matroska cover attachment carries it,
// is read by its stream.
func TestPictureOfAVideo(t *testing.T) {
	src, _ := filepath.Abs("../../../fixtures/h264-high41.mp4")
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	dir := t.TempDir()
	cover, withCover := filepath.Join(dir, "cover.jpg"), filepath.Join(dir, "covered.mkv")
	writeJPEG(t, cover, 200)
	//nolint:gosec // test paths
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", src, "-attach", cover, "-metadata:s:t", "mimetype=image/jpeg",
		"-metadata:s:t", "filename=cover.jpg", "-c", "copy", withCover).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	out := filepath.Join(dir, "got.jpg")
	if err := Picture(context.Background(), withCover, 2, out); err != nil {
		t.Fatal(err)
	}
	if c := jpegSize(t, out); c.Width != 16 {
		t.Errorf("picture %dx%d", c.Width, c.Height)
	}
}

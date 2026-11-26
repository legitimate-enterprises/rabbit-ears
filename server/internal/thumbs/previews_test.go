package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// parseBIF reads a Roku BIF back into its frame interval and JPEGs.
func parseBIF(t *testing.T, b []byte) (int, [][]byte) {
	t.Helper()
	if len(b) < 64 || !bytes.Equal(b[:8], []byte{0x89, 'B', 'I', 'F', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("not a BIF: % x", b[:min(len(b), 16)])
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[8:]); v != 0 {
		t.Fatalf("version %d", v)
	}
	n, interval := int(le.Uint32(b[12:])), int(le.Uint32(b[16:]))
	if !bytes.Equal(b[20:64], make([]byte, 44)) {
		t.Fatal("reserved header bytes are not zero")
	}
	var frames [][]byte
	for i := range n + 1 {
		at := 64 + 8*i
		ts, off := le.Uint32(b[at:]), le.Uint32(b[at+4:])
		if i == n {
			if ts != 0xFFFFFFFF || int(off) != len(b) {
				t.Fatalf("end entry %x %d of %d", ts, off, len(b))
			}
			break
		}
		if int(ts) != i {
			t.Fatalf("entry %d timestamp %d", i, ts)
		}
		end := le.Uint32(b[at+12:])
		frames = append(frames, b[off:end])
	}
	return interval, frames
}

func TestBIFLayout(t *testing.T) {
	var buf bytes.Buffer
	frames := [][]byte{[]byte("one"), []byte("second"), []byte("3")}
	if err := WriteBIF(&buf, 10000, frames); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) != 64+8*4+10 {
		t.Fatalf("%d bytes", len(b))
	}
	if off := binary.LittleEndian.Uint32(b[68:]); off != 96 {
		t.Errorf("first image at %d, want right after the index", off)
	}
	interval, got := parseBIF(t, b)
	if interval != 10000 || fmt.Sprint(got) != fmt.Sprint(frames) {
		t.Errorf("%d %q", interval, got)
	}
}

// loop concatenates a 10 s fixture with itself into a 60 s file with a
// keyframe every 10 s.
func loop(t *testing.T, name string) string {
	t.Helper()
	src, _ := filepath.Abs("../../../fixtures/" + name)
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	p := filepath.Join(t.TempDir(), "looped"+filepath.Ext(name))
	//nolint:gosec // test inputs are fixtures and temp paths
	if out, err := exec.Command("ffmpeg", "-v", "error", "-stream_loop", "5", "-i", src, "-c", "copy", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return p
}

func TestPreviewsOnFixture(t *testing.T) {
	for _, name := range []string{"h264-high41.mp4", "hdr10-main10.mkv"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "7")
			fs, err := Previews(context.Background(), loop(t, name), dir)
			if err != nil {
				t.Fatal(err)
			}
			if fs.Count != 6 || fs.Width != 320 || fs.Height != 180 || fs.IntervalMs != 10000 {
				t.Fatalf("%+v", fs)
			}
			b, err := os.ReadFile(filepath.Join(dir, BIFName)) //nolint:gosec // test temp path
			if err != nil {
				t.Fatal(err)
			}
			interval, frames := parseBIF(t, b)
			if interval != 10000 || len(frames) != fs.Count {
				t.Fatalf("BIF interval %d, %d frames", interval, len(frames))
			}
			for i, f := range frames {
				onDisk, err := os.ReadFile(FramePath(dir, i))
				if err != nil || !bytes.Equal(onDisk, f) {
					t.Fatalf("frame %d differs from the BIF: %v", i, err)
				}
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(f))
				if err != nil || cfg.Width != 320 || cfg.Height != 180 {
					t.Fatalf("frame %d: %+v %v", i, cfg, err)
				}
			}
			if _, err := os.Stat(FramePath(dir, fs.Count)); err == nil {
				t.Error("a frame past the count")
			}
		})
	}
}

// A file with a single keyframe still gets its first frame.
func TestPreviewsOfAShortFile(t *testing.T) {
	src, _ := filepath.Abs("../../../fixtures/h264-high41.mp4")
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	fs, err := Previews(context.Background(), src, t.TempDir())
	if err != nil || fs.Count != 1 {
		t.Fatalf("%+v %v", fs, err)
	}
}

// A run that fails leaves the frames already served from dir in place,
// whole, so the stored count still describes them.
func TestFailedPreviewsKeepTheOldOnes(t *testing.T) {
	src, _ := filepath.Abs("../../../fixtures/h264-high41.mp4")
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixture missing; run make deps")
	}
	dir := filepath.Join(t.TempDir(), "7")
	if _, err := Previews(context.Background(), src, dir); err != nil {
		t.Fatal(err)
	}
	bif, _ := os.ReadFile(filepath.Join(dir, BIFName)) //nolint:gosec // test temp path
	if _, err := Previews(context.Background(), filepath.Join(t.TempDir(), "gone.mkv"), dir); err == nil {
		t.Fatal("a missing input must fail")
	}
	after, err := os.ReadFile(filepath.Join(dir, BIFName)) //nolint:gosec // test temp path
	if err != nil || !bytes.Equal(after, bif) {
		t.Fatalf("the BIF went: %v", err)
	}
	if _, err := os.Stat(FramePath(dir, 0)); err != nil {
		t.Fatalf("frame 0 went: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), "*")); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
}

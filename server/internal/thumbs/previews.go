package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Seek preview frames are one every PreviewIntervalMs, PreviewWidth wide.
const (
	PreviewIntervalMs = 10000
	PreviewWidth      = 320
	// BIFName is the file, beside the frames, that holds them all for Roku.
	BIFName = "previews.bif"
)

// Frames describes a directory of seek preview frames.
type Frames struct {
	IntervalMs, Count, Width, Height int
}

// FramePath is frame n's JPEG in a previews directory.
func FramePath(dir string, n int) string { return filepath.Join(dir, strconv.Itoa(n)+".jpg") }

// Previews replaces dir with the seek preview frames of input and their
// BIF. Only keyframes are decoded, in one ffmpeg run; frame n is the last
// keyframe that rounds to n intervals, repeated across gaps longer than one.
// The set is built beside dir and swapped in whole, so until then, and
// after a failure, dir keeps serving the set before.
func Previews(ctx context.Context, input, dir string) (Frames, error) {
	next := dir + ".next"
	fr, err := previews(ctx, input, next)
	if err == nil {
		if err = os.RemoveAll(dir); err == nil {
			err = os.Rename(next, dir)
		}
	}
	if err != nil {
		_ = os.RemoveAll(next)
		return Frames{}, err
	}
	return fr, nil
}

func previews(ctx context.Context, input, dir string) (Frames, error) {
	if err := os.RemoveAll(dir); err != nil {
		return Frames{}, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Frames{}, err
	}
	// eof_action=pass keeps the last keyframe, which is the only one of a
	// file shorter than an interval.
	vf := fmt.Sprintf("fps=fps=1000/%d:round=near:start_time=0:eof_action=pass,scale=%d:-2:out_range=full,format=yuvj420p", PreviewIntervalMs, PreviewWidth)
	//nolint:gosec // input comes from the library walk, never user input
	cmd := exec.CommandContext(ctx, "nice", "-n", "19", "ffmpeg", "-v", "error", "-nostdin", "-skip_frame", "nokey", "-i", input, "-map", "0:V:0",
		"-vf", vf, "-q:v", "5", "-start_number", "0", filepath.Join(dir, "%d.jpg"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Frames{}, fmt.Errorf("ffmpeg previews %s: %w: %s", input, err, strings.TrimSpace(stderr.String()))
	}
	var frames [][]byte
	for n := 0; ; n++ {
		b, err := os.ReadFile(FramePath(dir, n))
		if os.IsNotExist(err) {
			break
		} else if err != nil {
			return Frames{}, err
		}
		frames = append(frames, b)
	}
	if len(frames) == 0 {
		return Frames{}, fmt.Errorf("ffmpeg previews %s: no frames", input)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(frames[0]))
	if err != nil {
		return Frames{}, err
	}
	f, err := os.Create(filepath.Join(dir, BIFName)) //nolint:gosec // dir is under the data directory
	if err != nil {
		return Frames{}, err
	}
	if err := WriteBIF(f, PreviewIntervalMs, frames); err != nil {
		_ = f.Close()
		return Frames{}, err
	}
	return Frames{PreviewIntervalMs, len(frames), cfg.Width, cfg.Height}, f.Close()
}

// WriteBIF writes frames as a Roku BIF: a 64-byte header carrying the
// interval in milliseconds, an index of (frame number, offset) pairs closed
// by 0xFFFFFFFF and the end offset, then the JPEGs back to back.
func WriteBIF(w io.Writer, intervalMs int, frames [][]byte) error {
	le := binary.LittleEndian
	head := make([]byte, 64, 64+8*(len(frames)+1))
	copy(head, []byte{0x89, 'B', 'I', 'F', '\r', '\n', 0x1a, '\n'})
	le.PutUint32(head[12:], uint32(len(frames))) //nolint:gosec // a few thousand frames
	le.PutUint32(head[16:], uint32(intervalMs))  //nolint:gosec // a positive constant
	off := uint32(cap(head))                     //nolint:gosec // bounded by the frame count
	for i, f := range frames {
		head = le.AppendUint32(head, uint32(i)) //nolint:gosec // as above
		head = le.AppendUint32(head, off)
		off += uint32(len(f)) //nolint:gosec // JPEGs of 320 px
	}
	head = le.AppendUint32(le.AppendUint32(head, 0xFFFFFFFF), off)
	if _, err := w.Write(head); err != nil {
		return err
	}
	for _, f := range frames {
		if _, err := w.Write(f); err != nil {
			return err
		}
	}
	return nil
}

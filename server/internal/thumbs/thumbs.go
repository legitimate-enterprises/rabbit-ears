// Package thumbs makes chapter thumbnails and seek preview frames with ffmpeg.
package thumbs

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DarkLuma is the mean luma (0 to 255) below which a frame is rejected.
const DarkLuma = 20

const (
	offsetMs      = 4000
	firstOffsetMs = 15000 // chapter 0 starts on logos and credits
	retryMs       = 10000
)

type generator func(ctx context.Context, seekMs int64, out string) error

// SeekMs is where to grab a chapter's frame: an offset past its start, or
// the start of a chapter shorter than the offset.
func SeekMs(position int, startMs, endMs int64, retry bool) int64 {
	off := int64(offsetMs)
	switch {
	case retry:
		off = retryMs
	case position == 0:
		off = firstOffsetMs
	}
	if startMs+off >= endMs {
		return startMs
	}
	return startMs + off
}

// MeanLuma decodes a JPEG and returns its mean luma.
func MeanLuma(path string) (float64, error) {
	f, err := os.Open(path) //nolint:gosec // caller-owned thumbnail path
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		return 0, err
	}
	b := img.Bounds()
	var sum, n float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 257
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("empty image %s", path)
	}
	return sum / n, nil
}

// Chapter writes a thumbnail of one chapter of input to out.
func Chapter(ctx context.Context, input string, position int, startMs, endMs int64, out string) error {
	return chapter(ctx, ffmpegFrame(input), position, startMs, endMs, out)
}

func chapter(ctx context.Context, gen generator, position int, startMs, endMs int64, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	if err := gen(ctx, SeekMs(position, startMs, endMs, false), out); err != nil {
		return err
	}
	if l, err := MeanLuma(out); err == nil && l >= DarkLuma {
		return nil
	}
	// Keep whatever the retry produces; a dark scene beats no thumbnail.
	return gen(ctx, SeekMs(position, startMs, endMs, true), out)
}

// ffmpegFrame picks the best of 48 frames after the seek point.
func ffmpegFrame(input string) generator {
	return func(ctx context.Context, seekMs int64, out string) error {
		//nolint:gosec // input comes from the library walk, never user input
		cmd := exec.CommandContext(ctx, "nice", "-n", "19", "ffmpeg", "-v", "error", "-y", "-ss", strconv.FormatFloat(float64(seekMs)/1000, 'f', 3, 64),
			"-i", input, "-vf", "thumbnail=n=48,scale=640:-2", "-frames:v", "1", "-q:v", "4", out)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("ffmpeg thumbnail %s: %w: %s", input, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// Cover writes the embedded picture of an audio file, such as an album's
// front cover, to out as a JPEG no wider than 1000 pixels.
func Cover(ctx context.Context, input, out string) error { return picture(ctx, input, "0:v:0", out) }

// Picture writes stream, a video file's attached picture, to out as Cover
// does.
func Picture(ctx context.Context, input string, stream int, out string) error {
	return picture(ctx, input, "0:"+strconv.Itoa(stream), out)
}

func picture(ctx context.Context, input, stream, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	//nolint:gosec // input comes from the library walk, never user input
	cmd := exec.CommandContext(ctx, "nice", "-n", "19", "ffmpeg", "-v", "error", "-y", "-i", input, "-map", stream, "-frames:v", "1",
		"-vf", "scale='min(1000,iw)':-2", "-q:v", "3", "-f", "image2", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg cover %s: %w: %s", input, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// artAt are where Art looks for a frame, as fractions of the runtime: a
// quarter in is past opening logos and titles, and later points stand in
// when that is a dark scene.
var artAt = []float64{0.25, 0.5, 0.1}

// Art writes a frame of a video, the best of 48 after a point some way in
// that is not dark, to frame as a JPEG 1280 wide, and its centre cropped
// to 2:3 to poster, 500 wide. A video whose every point is dark keeps the
// last.
func Art(ctx context.Context, input string, durationMs int64, frame, poster string) error {
	return art(ctx, ffmpegArt(input), durationMs, frame, poster)
}

func art(ctx context.Context, gen generator, durationMs int64, frame, poster string) error {
	if err := os.MkdirAll(filepath.Dir(frame), 0o750); err != nil {
		return err
	}
	for _, at := range artAt {
		if err := gen(ctx, int64(float64(durationMs)*at), frame); err != nil {
			return err
		}
		if l, err := MeanLuma(frame); err == nil && l >= DarkLuma {
			break
		}
	}
	return crop(ctx, frame, poster)
}

func ffmpegArt(input string) generator {
	return func(ctx context.Context, seekMs int64, out string) error {
		//nolint:gosec // input comes from the library walk, never user input
		cmd := exec.CommandContext(ctx, "nice", "-n", "19", "ffmpeg", "-v", "error", "-y", "-ss", strconv.FormatFloat(float64(seekMs)/1000, 'f', 3, 64),
			"-i", input, "-vf", "thumbnail=n=48,scale=1280:-2", "-frames:v", "1", "-q:v", "3", "-update", "1", out)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("ffmpeg frame %s: %w: %s", input, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

func crop(ctx context.Context, frame, poster string) error {
	//nolint:gosec // both are paths the caller made
	cmd := exec.CommandContext(ctx, "nice", "-n", "19", "ffmpeg", "-v", "error", "-y", "-i", frame, "-vf", "crop='min(iw,ih*2/3)':ih,scale=500:-2",
		"-q:v", "3", "-update", "1", poster)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg poster %s: %w: %s", frame, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

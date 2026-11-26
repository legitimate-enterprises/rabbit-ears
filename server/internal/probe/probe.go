// Package probe runs ffprobe once per file and turns its JSON into the
// mediafile facts the playback decision reads.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

// Result is everything ffprobe reported about one file. Tracks and Chapters
// carry no IDs; the caller stores them against a file.
type Result struct {
	Container  string
	DurationMs *int64
	Bitrate    *int64
	Tracks     []*mediafile.Track
	Chapters   []*mediafile.Chapter
	// Tags are the container's tags with lower-case keys, plus the first
	// audio stream's for keys the container lacks.
	Tags map[string]string
	// Cover reports an embedded picture, such as an album's front cover or
	// a video's cover attachment, and CoverStream is the first one's index.
	Cover       bool
	CoverStream int
}

type probeJSON struct {
	Format struct {
		Name     string            `json:"format_name"`
		Duration string            `json:"duration"`
		BitRate  string            `json:"bit_rate"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		Index        int               `json:"index"`
		Type         string            `json:"codec_type"`
		Codec        string            `json:"codec_name"`
		Profile      string            `json:"profile"`
		Level        int               `json:"level"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		PixFmt       string            `json:"pix_fmt"`
		BitDepth     string            `json:"bits_per_raw_sample"`
		AvgRate      string            `json:"avg_frame_rate"`
		RRate        string            `json:"r_frame_rate"`
		Primaries    string            `json:"color_primaries"`
		Transfer     string            `json:"color_transfer"`
		Channels     int               `json:"channels"`
		Layout       string            `json:"channel_layout"`
		SampleRate   string            `json:"sample_rate"`
		BitRate      string            `json:"bit_rate"`
		Tags         map[string]string `json:"tags"`
		Disposition  map[string]int    `json:"disposition"`
		SideDataList []struct {
			Type      string `json:"side_data_type"`
			DVProfile *int   `json:"dv_profile"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Chapters []struct {
		Start string            `json:"start_time"`
		End   string            `json:"end_time"`
		Tags  map[string]string `json:"tags"`
	} `json:"chapters"`
}

// Run probes path with ffprobe.
func Run(ctx context.Context, path string) (*Result, error) {
	//nolint:gosec // path comes from the library walk, never user input
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters", path)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w: %s", path, err, strings.TrimSpace(errb.String()))
	}
	return Parse(out.Bytes(), path)
}

var textSubs = map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true}

var trailingDepth = regexp.MustCompile(`(\d+)(?:le|be)?$`)

// Parse converts ffprobe JSON; path only disambiguates containers that share
// a demuxer (Matroska and WebM, MOV and MP4).
func Parse(data []byte, path string) (*Result, error) {
	var p probeJSON
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	r := &Result{Container: container(p.Format.Name, path)}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		ms := int64(d * 1000)
		r.DurationMs = &ms
	}
	r.Bitrate = i64(p.Format.BitRate)
	r.Tags = map[string]string{}
	addTags(r.Tags, p.Format.Tags)
	audioTagged := false
	for _, s := range p.Streams {
		if s.Disposition["attached_pic"] == 1 {
			if !r.Cover {
				r.Cover, r.CoverStream = true, s.Index
			}
			continue
		}
		if s.Type == mediafile.Audio && !audioTagged {
			addTags(r.Tags, s.Tags)
			audioTagged = true
		}
		t := &mediafile.Track{StreamIndex: s.Index, Kind: s.Type, Codec: s.Codec, Profile: s.Profile,
			Language: s.Tags["language"], Title: s.Tags["title"],
			IsDefault: s.Disposition["default"] == 1, IsForced: s.Disposition["forced"] == 1, Bitrate: i64(s.BitRate)}
		switch s.Type {
		case mediafile.Video:
			t.Level = nz(s.Level)
			t.Width, t.Height = nz(s.Width), nz(s.Height)
			t.FrameRate = rate(s.AvgRate, s.RRate)
			t.PixelFormat, t.ColorPrimaries, t.ColorTransfer = s.PixFmt, s.Primaries, s.Transfer
			t.BitDepth = depth(s.BitDepth, s.PixFmt)
			t.HDRFormat = hdr(s.Transfer, func(name string) bool {
				for _, sd := range s.SideDataList {
					if strings.Contains(sd.Type, name) {
						return true
					}
				}
				return false
			})
			for _, sd := range s.SideDataList {
				if sd.DVProfile != nil {
					t.DVProfile = sd.DVProfile
				}
			}
		case mediafile.Audio:
			t.Channels, t.ChannelLayout, t.SampleRate = nz(s.Channels), s.Layout, nz(int(*orZero(i64(s.SampleRate))))
		case mediafile.Subtitle:
			b := textSubs[s.Codec]
			t.SubtitleIsText = &b
		default:
			continue
		}
		r.Tracks = append(r.Tracks, t)
	}
	for i, c := range p.Chapters {
		st, _ := strconv.ParseFloat(c.Start, 64)
		en, _ := strconv.ParseFloat(c.End, 64)
		r.Chapters = append(r.Chapters, &mediafile.Chapter{Position: i, StartMs: int64(st*1000 + .5), EndMs: int64(en*1000 + .5), Title: c.Tags["title"]})
	}
	return r, nil
}

// addTags copies tags into dst under lower-case keys, keeping any already there.
func addTags(dst, tags map[string]string) {
	for k, v := range tags {
		if k = strings.ToLower(k); dst[k] == "" {
			dst[k] = v
		}
	}
}

func container(format, path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch {
	case strings.HasPrefix(format, "matroska"):
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case strings.HasPrefix(format, "mov,mp4"):
		if ext == "mov" {
			return "mov"
		}
		return "mp4"
	case format == "mpegts":
		return "ts"
	}
	return strings.Split(format, ",")[0]
}

// hdr names the dynamic range of a video stream; has reports a side-data entry.
func hdr(transfer string, has func(string) bool) string {
	switch {
	case has("DOVI"):
		return "dolby_vision"
	case has("SMPTE2094-40"):
		return "hdr10plus"
	case transfer == "smpte2084":
		return "hdr10"
	case transfer == "arib-std-b67":
		return "hlg"
	}
	return ""
}

func depth(raw, pixFmt string) *int {
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return &n
	}
	if pixFmt == "" {
		return nil
	}
	n := 8
	if m := trailingDepth.FindStringSubmatch(pixFmt); m != nil {
		if v, _ := strconv.Atoi(m[1]); v >= 9 && v <= 16 {
			n = v
		}
	}
	return &n
}

func rate(rates ...string) *float64 {
	for _, s := range rates {
		a, b, ok := strings.Cut(s, "/")
		if !ok {
			continue
		}
		n, _ := strconv.ParseFloat(a, 64)
		d, _ := strconv.ParseFloat(b, 64)
		if n > 0 && d > 0 {
			f := n / d
			return &f
		}
	}
	return nil
}

func i64(s string) *int64 {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return &n
	}
	return nil
}

func orZero(p *int64) *int64 {
	if p == nil {
		var z int64
		return &z
	}
	return p
}

func nz(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// Package transcode turns a plan entry into ffmpeg invocations and runs them:
// HLS fragmented MP4 or MPEG-TS segments encoded lazily near the playhead,
// one encoder per session, a limit on simultaneous video transcodes, and
// subtitle sidecars.
package transcode

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// SegmentSeconds is the HLS segment length. Converted video forces a
// keyframe on every boundary so segments are exactly this long.
const SegmentSeconds = 6

// InitName is the fMP4 initialization segment in an encoder's directory.
const InitName = "init.mp4"

// runInit is where one encoder run writes its initialization segment; the
// Manager publishes the first run's as InitName and keeps it.
const runInit = "run.mp4"

// SegmentName is the file of fMP4 segment n in an encoder's directory.
func SegmentName(n int) string { return segmentName(n, false) }

// segmentName is the file of segment n, MPEG-TS when ts.
func segmentName(n int, ts bool) string { return strconv.Itoa(n) + segmentExt(ts) }

func segmentExt(ts bool) string {
	if ts {
		return ".ts"
	}
	return ".m4s"
}

// Job is one encoder run: an entry at one rung, starting at a segment.
type Job struct {
	Input    string
	Entry    *session.PlanEntry
	Video    *mediafile.Track // nil for a file without video
	Audio    *mediafile.Track // nil for a file without audio
	Subtitle *mediafile.Track // read only when the entry burns it in
	Rung     playback.Rung
	Segment  int
	OutDir   string
	// Bounds, for copied video, are the segment starts; the encoder then
	// writes one file per keyframe and the Manager joins them into segments.
	Bounds []Bound
}

// gops reports whether the encoder writes one file per keyframe.
func (j Job) gops() bool {
	return j.Bounds != nil && j.Video != nil && j.Entry.VideoAction != session.ActionConvert
}

// gopName is the encoder's file for the n-th keyframe of its run, when
// copying video against Bounds.
func gopName(n int, ts bool) string { return "g" + segmentName(n, ts) }

// vtDecodes is the source codecs every Apple silicon VideoToolbox decodes. The
// hardware path keeps frames on the GPU, so a source outside it must decode
// in software; the encoder stays in hardware either way.
var vtDecodes = map[string]bool{"h264": true, "hevc": true, "mpeg2video": true, "prores": true}

const sdr = "color_matrix=bt709:color_primaries=bt709:color_transfer=bt709"

// lead is how many seconds after the source's timeline every run's sits. A
// muxer handed a time before zero, as a run from the start is by leading
// B-frames' decode times and by an audio encoder's first packet, moves that
// whole run later, off every other run's timeline.
const lead = 1

// Args builds the ffmpeg command line for j. Timestamps sit lead seconds
// after the source's timeline, so segment n starts at lead+n*SegmentSeconds
// whichever segment the encoder started from; the keyframe expression sees
// time from the seek point, so it needs no offset. Copied video can only
// split on the source's keyframes; with Bounds its segments follow them
// exactly.
func Args(j Job) []string {
	e := j.Entry
	convert, _, hwDecode := j.modes()
	start := j.Segment * SegmentSeconds
	gops := j.gops()
	seek := strconv.Itoa(start)
	if gops {
		// Just before the keyframe: ffmpeg seeks a container that is not
		// indexed by presentation time, such as Matroska with B-frames, to an
		// earlier keyframe, and -copypriorss 0 drops what comes before the
		// seek. Converted audio, trimmed to the seek, starts with the keyframe.
		seek = strconv.FormatFloat(j.Bounds[j.Segment].At-0.0005, 'f', -1, 64)
	}

	a := []string{"-hide_banner", "-nostdin", "-v", "error", "-y"}
	if hwDecode {
		a = append(a, "-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld")
	}
	a = append(a, "-ss", seek, "-copyts", "-i", j.Input)
	offset := lead
	if convert {
		// Trim copied audio to the seek point too, or the muxer measures the
		// first segment from an earlier audio packet and cuts it short.
		a = append(a, "-ss", strconv.Itoa(start))
		offset += start
	}
	a = append(a, "-output_ts_offset", strconv.Itoa(offset))
	if gops {
		a = append(a, "-copypriorss", "0")
	}
	a = append(a, streamArgs(j)...)
	hlsTime, first, name := strconv.Itoa(SegmentSeconds), j.Segment, "%d"+segmentExt(e.TS)
	if gops {
		// A cut at every keyframe; the Manager groups them by Bounds.
		hlsTime, first, name = "0.001", 0, "g%d"+segmentExt(e.TS)
	}
	a = append(a, "-map_metadata", "-1", "-map_chapters", "-1", "-f", "hls", "-hls_time", hlsTime)
	if e.TS {
		a = append(a, "-hls_segment_type", "mpegts")
	} else {
		// frag_discont writes every fragment at its own decode time, so
		// segments from encoders started at different points line up.
		a = append(a, "-hls_segment_type", "fmp4", "-hls_segment_options", "movflags=+frag_discont", "-hls_fmp4_init_filename", runInit)
	}
	return append(a, "-hls_flags", "temp_file", "-hls_list_size", "0", "-start_number", strconv.Itoa(first),
		"-hls_segment_filename", filepath.Join(j.OutDir, name), filepath.Join(j.OutDir, "encoder.m3u8"))
}

// modes reports whether j converts video, burns a subtitle into it, and
// decodes it in hardware.
func (j Job) modes() (convert, burn, hwDecode bool) {
	e := j.Entry
	convert = e.VideoAction == session.ActionConvert && j.Video != nil
	burn = convert && e.SubtitleAction == session.SubtitleBurn && j.Subtitle != nil
	hwDecode = convert && e.Encoder == session.EncoderHardware && !burn && vtDecodes[j.Video.Codec]
	return
}

// streamArgs maps j's video and audio, each encoded or copied as its entry
// says.
func streamArgs(j Job) []string {
	e := j.Entry
	convert, burn, hwDecode := j.modes()
	var a []string
	if j.Video != nil {
		switch {
		case !convert:
			a = append(a, "-map", stream(j.Video), "-c:v", "copy")
			if j.Video.Codec == "hevc" && !e.TS {
				// As Annex B the muxer builds the init segment's parameter
				// sets from the stream; a source that keeps them only there
				// otherwise gets none, and no Apple player decodes that.
				a = append(a, "-tag:v", "hvc1", "-bsf:v", "hevc_mp4toannexb")
			}
		case burn:
			// A subtitle's canvas has a size of its own, as a UHD disc's 1080p
			// PGS, so picture and subtitle each scale to the rung to meet.
			size := fmt.Sprintf("scale=w=%d:h=%d", j.Rung.Width, j.Rung.Height)
			overlay := "overlay=eof_action=pass"
			if e.VideoCodec == "hevc" {
				// overlay blends in 8 bits unless told otherwise.
				overlay += ":format=yuv420p10"
			}
			a = append(a, "-filter_complex", fmt.Sprintf("[%s]%s%s[v];[%s]%s[s];[v][s]%s,format=%s[out]",
				stream(j.Video), size, swToneMap(e), stream(j.Subtitle), size, overlay, pixFmt(e)), "-map", "[out]")
		case hwDecode:
			vf := fmt.Sprintf("scale_vt=w=%d:h=%d", j.Rung.Width, j.Rung.Height)
			if e.ToneMap {
				vf += ":" + sdr
			}
			a = append(a, "-map", stream(j.Video), "-vf", vf)
		default:
			a = append(a, "-map", stream(j.Video), "-vf", fmt.Sprintf("scale=w=%d:h=%d%s,format=%s", j.Rung.Width, j.Rung.Height, swToneMap(e), pixFmt(e)))
		}
		if convert {
			a = append(a, videoEncoder(e)...)
			b := j.Rung.Bitrate
			// A huge GOP leaves the forced boundary keyframes as the only ones.
			a = append(a, "-b:v", strconv.FormatInt(b, 10), "-maxrate", strconv.FormatInt(b, 10), "-bufsize", strconv.FormatInt(2*b, 10),
				"-g", "100000", "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", SegmentSeconds))
		}
	}
	if j.Audio != nil {
		a = append(a, "-map", stream(j.Audio))
		if e.AudioAction == session.ActionConvert {
			a = append(a, "-c:a", "aac", "-ac", strconv.Itoa(max(e.AudioChannels, 1)), "-b:a", strconv.FormatInt(playback.AudioBitrate(e, j.Video == nil), 10))
		} else {
			a = append(a, "-c:a", "copy")
		}
	}
	return a
}

// FileArgs builds the ffmpeg command line converting j into one MP4 (an
// M4A for a song) at out, whole and with its index first so it plays
// while still arriving. ffmpeg reports its progress on stdout.
func FileArgs(j Job, out string) []string {
	_, _, hwDecode := j.modes()
	a := []string{"-hide_banner", "-nostdin", "-v", "error", "-y", "-progress", "pipe:1", "-nostats"}
	if hwDecode {
		a = append(a, "-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld")
	}
	a = append(append(a, "-i", j.Input), streamArgs(j)...)
	return append(a, "-map_metadata", "-1", "-movflags", "+faststart", "-f", "mp4", out)
}

func stream(t *mediafile.Track) string { return "0:" + strconv.Itoa(t.StreamIndex) }

// swToneMap is swscale's perceptual HDR-to-SDR mapping, as options that
// follow a scale filter's size.
func swToneMap(e *session.PlanEntry) string {
	if !e.ToneMap {
		return ""
	}
	return ":out_transfer=bt709:out_primaries=bt709:out_color_matrix=bt709:intent=perceptual"
}

func pixFmt(e *session.PlanEntry) string {
	switch {
	case e.VideoCodec != "hevc":
		return "yuv420p"
	case e.Encoder == session.EncoderHardware:
		return "p010le"
	}
	return "yuv420p10le"
}

// videoEncoder names the encoder for e. The software ones make no B-frames:
// those shift only a run that starts at zero, off every other run's timeline.
func videoEncoder(e *session.PlanEntry) []string {
	hw := e.Encoder == session.EncoderHardware
	if e.VideoCodec == "hevc" {
		enc := []string{"-c:v", "libx265", "-preset", "fast", "-bf", "0", "-x265-params", "log-level=error:hdr10=1"}
		if hw {
			enc = []string{"-c:v", "hevc_videotoolbox"}
		}
		return append(enc, "-profile:v", "main10", "-tag:v", "hvc1",
			"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc")
	}
	if hw {
		return []string{"-c:v", "h264_videotoolbox", "-profile:v", "high"}
	}
	return []string{"-c:v", "libx264", "-preset", "veryfast", "-bf", "0", "-profile:v", "high"}
}

// SidecarArgs extracts text subtitle tracks as WebVTT in one read of input:
// the track at each stream index of outs to its file.
func SidecarArgs(input string, outs map[int]string) []string {
	a := []string{"-hide_banner", "-nostdin", "-v", "error", "-y", "-i", input}
	for _, i := range slices.Sorted(maps.Keys(outs)) {
		a = append(a, "-map", "0:"+strconv.Itoa(i), "-c:s", "webvtt", "-f", "webvtt", outs[i])
	}
	return a
}

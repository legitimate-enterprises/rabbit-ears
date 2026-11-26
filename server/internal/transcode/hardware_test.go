package transcode

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

func TestHardwareNeedsTheVideoToolboxEncoder(t *testing.T) {
	mac := []byte(" V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)\n V....D hevc_videotoolbox    VideoToolbox H.265 Encoder (codec hevc)\n")
	linux := []byte(" V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)\n V....D libx265              libx265 H.265 / HEVC (codec hevc)\n")
	if !hasHardware(mac) {
		t.Error("an ffmpeg listing the VideoToolbox encoders encodes in hardware")
	}
	if hasHardware(linux) || hasHardware(nil) {
		t.Error("an ffmpeg with only software encoders, or none, does not")
	}
}

// hostEncoder is the encoder a plan on this host converts with first.
func hostEncoder() string {
	if Hardware() {
		return session.EncoderHardware
	}
	return session.EncoderSoftware
}

// needHardware skips a test of the hardware path on a host without it.
func needHardware(t *testing.T) {
	t.Helper()
	if !Hardware() {
		t.Skip("this host's ffmpeg has no VideoToolbox encoder")
	}
}

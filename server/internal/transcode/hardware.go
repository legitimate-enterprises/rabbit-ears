package transcode

import (
	"bytes"
	"os/exec"
	"sync"
)

var hardware = sync.OnceValue(func() bool {
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	return hasHardware(out)
})

// Hardware reports whether this host's ffmpeg has the VideoToolbox
// encoders that Args and FileArgs name for session.EncoderHardware. A host
// without them, such as any Linux host, converts video only in software.
// ffmpeg is asked once.
func Hardware() bool { return hardware() }

// hasHardware reads an ffmpeg -encoders listing.
// shortcut: ffmpeg built with VideoToolbox is taken to mean the hardware
// encodes, true on Apple silicon; probe an encode if Intel Macs without a
// media engine ever matter.
func hasHardware(encoders []byte) bool { return bytes.Contains(encoders, []byte("h264_videotoolbox")) }

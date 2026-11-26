package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"testing"
)

// allPackets is every byte of one stream's packets, read from the start.
func allPackets(t *testing.T, path, stream string) []byte {
	t.Helper()
	b, _ := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", path, "-map", stream, "-c", "copy", "-f", "data", "-").Output() //nolint:gosec // test fixture
	return b
}

// remux writes the file again with other tags, into dir under name.
func remux(t *testing.T, path, dir, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", path, "-map", "0", "-c", "copy", //nolint:gosec // test fixture
		"-metadata", "title=Another title", "-metadata", "comment=longer than the tags were before", out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return out
}

// A fingerprint is the digest of the first and the last span of the main
// stream's packets, whatever read found the last.
func TestFingerprintIsTheStreamsEnds(t *testing.T) {
	defer func(n int) { fingerprintSpan = n }(fingerprintSpan)
	fingerprintSpan = 16 << 10
	for name, stream := range map[string]string{"h264-high41.mp4": "0:V:0", "chapters.mkv": "0:V:0", "sample.flac": "0:a:0", "sample.mp3": "0:a:0"} {
		path := fixture(t, name)
		all := allPackets(t, path, stream)
		if len(all) <= 2*fingerprintSpan {
			t.Fatalf("%s: %d bytes of packets are no test of the ends", name, len(all))
		}
		sum := sha256.Sum256(append(append([]byte{}, all[:fingerprintSpan]...), all[len(all)-fingerprintSpan:]...))
		got, err := Fingerprint(context.Background(), path)
		if want := fingerprintVersion + hex.EncodeToString(sum[:]); err != nil || got != want || !Fingerprinted(got) {
			t.Errorf("%s: %q %v, want %q", name, got, err, want)
		}
	}
}

// Tags and the container are no part of it, and a stream shorter than a
// span is all of it.
func TestFingerprintOutlastsTagsAndContainers(t *testing.T) {
	dir := t.TempDir()
	video, audio := fixture(t, "h264-high41.mp4"), fixture(t, "sample.flac")
	same := map[string][]string{
		video: {remux(t, video, dir, "tagged.mp4"), remux(t, video, dir, "other.mkv")},
		audio: {remux(t, audio, dir, "tagged.flac")},
	}
	seen := map[string]string{}
	for path, others := range same {
		want, err := Fingerprint(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if was, dup := seen[want]; dup {
			t.Errorf("%s and %s share %q", was, path, want)
		}
		seen[want] = path
		for _, o := range others {
			if got, err := Fingerprint(context.Background(), o); err != nil || got != want {
				t.Errorf("%s: %q %v, want %q", filepath.Base(o), got, err, want)
			}
		}
	}
}

func TestFingerprintOfNoMediaFails(t *testing.T) {
	if got, err := Fingerprint(context.Background(), fixture(t, "sample.srt")); err == nil {
		t.Errorf("got %q", got)
	}
}

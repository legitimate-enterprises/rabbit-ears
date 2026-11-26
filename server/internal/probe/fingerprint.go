package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// fingerprintVersion starts every fingerprint, so one made another way
// never equals one made this way. Change it with what a fingerprint is of.
const fingerprintVersion = "v1:"

// fingerprintSpan is how much of each end of the stream is read.
var fingerprintSpan = 1 << 20

// Fingerprinted reports whether fp is a fingerprint as they are made now.
func Fingerprinted(fp string) bool { return strings.HasPrefix(fp, fingerprintVersion) }

// Fingerprint identifies the file at path by its media alone: a digest of
// the first and the last fingerprintSpan bytes of its main stream's
// packets, the whole of a shorter stream standing for both. The main
// stream is the first video stream that is no picture, or else the first
// audio stream. The file's name, times, tags and container are no part of
// it, so it outlasts a rename, a new set of tags and a remux, and is
// shared by two files of the same media.
func Fingerprint(ctx context.Context, path string) (string, error) {
	var errs []error
	for _, stream := range []string{"0:V:0", "0:a:0"} {
		head, _, err := packets(ctx, path, stream, 0, fingerprintSpan)
		if len(head) == 0 {
			errs = append(errs, err)
			continue
		}
		tail := head
		// The last span is the same wherever the read starts, once the
		// read holds a span; a seek past the start reads the whole stream.
		for back := 4; len(head) == fingerprintSpan; back *= 4 {
			var read int
			if tail, read, err = packets(ctx, path, stream, back, 0); err != nil {
				return "", err
			}
			if read >= fingerprintSpan || back > 1<<20 {
				break
			}
		}
		sum := sha256.Sum256(append(append(make([]byte, 0, len(head)+len(tail)), head...), tail...))
		return fingerprintVersion + hex.EncodeToString(sum[:]), nil
	}
	return "", fmt.Errorf("fingerprint %s: no media stream: %w", path, errors.Join(errs...))
}

// packets copies one stream's packets out of the file with ffmpeg. With
// limit it returns the first limit bytes and stops ffmpeg there, which is
// no error. Without, it reads from back seconds before the end to the
// end, and returns the last fingerprintSpan bytes and how many it read.
func packets(ctx context.Context, path, stream string, back, limit int) ([]byte, int, error) {
	args := []string{"-n", "19", "ffmpeg", "-nostdin", "-v", "error"}
	if back > 0 {
		args = append(args, "-sseof", "-"+strconv.Itoa(back))
	}
	cmd := exec.CommandContext(ctx, "nice", append(args, "-i", path, "-map", stream, "-c", "copy", "-f", "data", "-")...) //nolint:gosec // fixed binary, path from the library scan
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	var kept []byte
	var read int
	if limit > 0 {
		kept, err = io.ReadAll(io.LimitReader(out, int64(limit)))
		read = len(kept)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if len(kept) == 0 && err == nil {
			err = errors.New(strings.TrimSpace(stderr.String()))
		}
		return kept, read, err
	}
	buf := make([]byte, 1<<16)
	for {
		n, rerr := out.Read(buf)
		read += n
		if kept = append(kept, buf[:n]...); len(kept) > 2*fingerprintSpan {
			kept = append(kept[:0], kept[len(kept)-fingerprintSpan:]...)
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, 0, fmt.Errorf("ffmpeg %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return kept[max(0, len(kept)-fingerprintSpan):], read, nil
}

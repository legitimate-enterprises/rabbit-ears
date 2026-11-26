package transcode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// partSuffix marks a conversion still being written; a finished one is
// renamed to its own name, so a file under that name is always whole.
const partSuffix = ".part"

// yieldEvery is how often a running conversion checks for live transcodes.
var yieldEvery = 250 * time.Millisecond

// ConvertFile converts j into one file at out, at the lowest CPU priority.
// It starts only while busy reports false, and pauses while it reports true,
// so a conversion never runs beside a live transcode nor takes its slot.
// progress hears the share of durationMs written so far; log hears the
// start and each quarter passed. A failed or cancelled conversion leaves
// nothing at out.
func ConvertFile(ctx context.Context, j Job, out string, durationMs int64, busy func() bool, progress func(float64), log *slog.Logger) error {
	for busy() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(yieldEvery):
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	part := out + partSuffix
	defer func() { _ = os.Remove(part) }()
	args := FileArgs(j, part)
	//nolint:gosec // arguments are built from probed facts and server paths
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	// A process paused for live playback still has to die when cancelled.
	cmd.Cancel = func() error {
		_ = cmd.Process.Signal(syscall.SIGCONT)
		return cmd.Process.Kill()
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	log.Info("Conversion started", "ffmpeg_command", "ffmpeg "+strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", ErrEncoder, err)
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 19)
	done := make(chan struct{})
	go yield(cmd.Process, busy, done)
	read(stdout, durationMs, progress, log)
	err = cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: %w: %s", ErrEncoder, err, tail(stderr.String(), 400))
	}
	return os.Rename(part, out)
}

// yield stops p while busy reports true and continues it after, until done.
func yield(p *os.Process, busy func() bool, done <-chan struct{}) {
	t := time.NewTicker(yieldEvery)
	defer t.Stop()
	stopped := false
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if b := busy(); b != stopped {
				sig := syscall.SIGCONT
				if b {
					sig = syscall.SIGSTOP
				}
				if p.Signal(sig) == nil {
					stopped = b
				}
			}
		}
	}
}

// read follows ffmpeg's progress report, out_time_us lines, as a share of
// durationMs, logging each quarter below the whole once; an unknown
// duration reports only the end.
func read(r io.Reader, durationMs int64, progress func(float64), log *slog.Logger) {
	s := bufio.NewScanner(r)
	quarters := 0
	for s.Scan() {
		k, v, _ := strings.Cut(s.Text(), "=")
		switch {
		case k == "out_time_us" && durationMs > 0:
			if us, err := strconv.ParseInt(v, 10, 64); err == nil && us > 0 {
				f := min(float64(us)/1000/float64(durationMs), 1)
				progress(f)
				if q := int(f * 4); q > quarters && q < 4 {
					quarters = q
					log.Info(fmt.Sprintf("Conversion %d%% done", q*25))
				}
			}
		case k == "progress" && v == "end":
			progress(1)
		}
	}
}

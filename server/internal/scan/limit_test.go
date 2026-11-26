package scan

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
)

func shortLimits(t *testing.T) {
	t.Helper()
	run, floor := runLimit, previewsFloor
	runLimit, previewsFloor = 50*time.Millisecond, time.Nanosecond
	t.Cleanup(func() { runLimit, previewsFloor = run, floor })
}

// A probe that never returns fails its own file and the scan goes on.
func TestAProbeThatNeverReturnsFailsOnlyItsFile(t *testing.T) {
	shortLimits(t)
	e := newEnv(t, library.Movies, "")
	e.s.Probe = func(ctx context.Context, p string) (*probe.Result, error) {
		if filepath.Base(p) == "stuck.mkv" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &probe.Result{Container: "mkv"}, nil
	}
	touch(t, filepath.Join(e.root, "A (2001)", "stuck.mkv"))
	touch(t, filepath.Join(e.root, "B (2002)", "m.mkv"))
	if got := scanSummary(t, e); got != "2 movies added; 1 file failed to probe" {
		t.Errorf("got %q", got)
	}
}

// A chapter thumbnail that never returns leaves its chapter without one.
func TestAThumbnailThatNeverReturnsIsSkipped(t *testing.T) {
	shortLimits(t)
	e := newEnv(t, library.Movies, "")
	e.s.Probe = func(context.Context, string) (*probe.Result, error) {
		return &probe.Result{Container: "mkv", Chapters: []*mediafile.Chapter{{Position: 0, EndMs: 1000}}}, nil
	}
	e.s.Thumb = func(ctx context.Context, _ string, _ int, _, _ int64, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	touch(t, filepath.Join(e.root, "A (2001)", "m.mkv"))
	if got := scanSummary(t, e); got != "1 movie added" {
		t.Errorf("got %q", got)
	}
}

// Previews that outlast their limit are recorded as failed, like any other
// failure, so the files behind them are reached.
func TestPreviewsPastTheirLimitFail(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	// No runtime leaves the limit at its floor.
	e.s.Probe = func(ctx context.Context, p string) (*probe.Result, error) {
		res, err := probe.Run(ctx, p)
		if res != nil {
			res.DurationMs = nil
		}
		return res, err
	}
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	e.mustScan()
	shortLimits(t)
	if sum, _, err := e.s.MakePreviews(context.Background(), nil, nil); err != nil || sum != "1 file, 1 failed" {
		t.Fatal(sum, err)
	}
	if left, _ := e.files.NeedingPreviews(); len(left) != 0 {
		t.Fatalf("%d files left", len(left))
	}
}

// Previews read a whole file, so a long one gets longer.
func TestPreviewsLimitGrowsWithRuntime(t *testing.T) {
	threeHours := (3 * time.Hour).Milliseconds()
	if got := previewsLimit(&mediafile.File{DurationMs: &threeHours}); got != 6*time.Hour {
		t.Errorf("three hours: %v", got)
	}
	if got := previewsLimit(&mediafile.File{}); got != previewsFloor {
		t.Errorf("no runtime: %v", got)
	}
}

// A failed probe is kept on its file, with ffprobe's error, until a probe
// of it succeeds.
func TestAFailedProbeIsRecordedUntilOneSucceeds(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fail := true
	e.s.Probe = func(context.Context, string) (*probe.Result, error) {
		if fail {
			return nil, errors.New("Invalid data found when processing input")
		}
		return &probe.Result{Container: "mkv"}, nil
	}
	p := filepath.Join(e.root, "A (2001)", "a.mkv")
	touch(t, p)
	e.mustScan()
	f, err := e.files.FileByPath(p)
	if err != nil || f.ProbedAt != nil || f.ProbeFailedAt == nil || !f.ProbeFailedAt.Equal(*e.clock) ||
		f.ProbeError != "Invalid data found when processing input" {
		t.Fatalf("after a failed probe %+v %v", f, err)
	}
	fail = false
	e.mustScan()
	if f, _ = e.files.FileByPath(p); f.ProbedAt == nil || f.ProbeFailedAt != nil || f.ProbeError != "" {
		t.Fatalf("after a probe that worked %+v", f)
	}
}

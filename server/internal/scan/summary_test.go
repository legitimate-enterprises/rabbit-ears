package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func scanSummary(t *testing.T, e *env) string {
	t.Helper()
	sum, err := e.s.Scan(context.Background(), e.lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sum.String()
}

func TestScanSummaryMovies(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	e.s.Probe = func(context.Context, string) (*probe.Result, error) { return &probe.Result{Container: "mkv"}, nil }
	for _, n := range []string{"A (2001)", "B (2002)", "C (2003)"} {
		touch(t, filepath.Join(e.root, n, "m.mkv"))
	}
	if got := scanSummary(t, e); got != "3 movies added" {
		t.Errorf("first scan: %q", got)
	}
	if got := scanSummary(t, e); got != "No changes; 3 movies checked" {
		t.Errorf("rescan: %q", got)
	}
	if err := os.WriteFile(filepath.Join(e.root, "A (2001)", "m.mkv"), []byte("longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := scanSummary(t, e); got != "1 movie updated" {
		t.Errorf("changed file: %q", got)
	}
	if err := os.RemoveAll(filepath.Join(e.root, "B (2002)")); err != nil {
		t.Fatal(err)
	}
	if got := scanSummary(t, e); got != "No changes; 2 movies checked; 1 file missing" {
		t.Errorf("file gone: %q", got)
	}
}

func TestScanSummaryTVAndFailures(t *testing.T) {
	e := newEnv(t, library.TV, "")
	e.s.Probe = func(_ context.Context, p string) (*probe.Result, error) {
		if filepath.Base(p) == "Show.S01E03.mkv" {
			return nil, os.ErrInvalid
		}
		return &probe.Result{Container: "mkv"}, nil
	}
	for _, f := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv", "Show.S01E03.mkv"} {
		touch(t, filepath.Join(e.root, "Show (2010)", "Season 01", f))
	}
	touch(t, filepath.Join(e.root, "Other (2011)", "Season 01", "Other.S01E01.mkv"))
	want := "2 shows, 4 episodes added; 1 file failed to probe"
	if got := scanSummary(t, e); got != want {
		t.Errorf("got %q", got)
	}
	// The unprobed file is retried, and fails again, without counting as a change.
	if got := scanSummary(t, e); got != "No changes; 4 episodes checked; 1 file failed to probe" {
		t.Errorf("rescan: %q", got)
	}
}

func TestScanSummaryCountsFailedLookups(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	e.s.Probe = func(context.Context, string) (*probe.Result, error) { return &probe.Result{Container: "mkv"}, nil }
	e.tmdbUp.Store(false)
	touch(t, filepath.Join(e.root, "A (2001)", "m.mkv"))
	touch(t, filepath.Join(e.root, "B (2002)", "m.mkv"))
	if got := scanSummary(t, e); got != "2 movies added; 2 metadata lookups failed" {
		t.Errorf("got %q", got)
	}
}

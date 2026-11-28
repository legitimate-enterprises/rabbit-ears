package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func newRunner(t *testing.T, e *env) (*Runner, *task.Repo) {
	t.Helper()
	d := dbOf(e)
	tasks := task.New(d)
	return NewRunner(e.s, tasks), tasks
}

func TestNextNightly(t *testing.T) {
	loc := time.FixedZone("x", -5*3600)
	now := time.Date(2026, 3, 1, 1, 30, 0, 0, loc)
	if got := nextNightly(now, 3); !got.Equal(time.Date(2026, 3, 1, 3, 0, 0, 0, loc)) {
		t.Errorf("before hour: %v", got)
	}
	now = time.Date(2026, 3, 1, 3, 0, 0, 0, loc)
	if got := nextNightly(now, 3); !got.Equal(time.Date(2026, 3, 2, 3, 0, 0, 0, loc)) {
		t.Errorf("at hour: %v", got)
	}
	now = time.Date(2026, 3, 1, 23, 0, 0, 0, loc)
	if got := nextNightly(now, 0); !got.Equal(time.Date(2026, 3, 2, 0, 0, 0, 0, loc)) {
		t.Errorf("midnight: %v", got)
	}
}

func TestSyncCreatesAndPrunesTasks(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(); err != nil { // idempotent
		t.Fatal(err)
	}
	ts, _ := tasks.List()
	if len(ts) != 4 {
		t.Fatalf("tasks %+v", ts)
	}
	var scanTask *task.Task
	for _, x := range ts {
		if strings.HasPrefix(x.Kind, "scan:") {
			scanTask = x
		}
	}
	if scanTask == nil || scanTask.Name != "Scan L" || scanTask.NextRunAt == nil || scanTask.IntervalSeconds == nil || *scanTask.IntervalSeconds != 86400 {
		t.Fatalf("scan task %+v", scanTask)
	}
	if err := e.s.Libraries.Delete(e.lib.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	if ts, _ = tasks.List(); len(ts) != 3 || ts[0].Kind != "local" || ts[1].Kind != "metadata" || ts[2].Kind != "previews" {
		t.Errorf("stale scan task kept: %+v", ts)
	}
}

// A library deleted and made again under its name, so under a new id, gets
// its scan task in the same sync that drops the old library's.
func TestSyncReplacesTheScanTaskOfALibraryMadeAgain(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	must(t, e.s.Libraries.Delete(e.lib.ID))
	again := &library.Library{Name: e.lib.Name, Kind: library.Movies}
	must(t, e.s.Libraries.Create(again, nil))
	must(t, r.Sync())
	ts, _ := tasks.List()
	var kinds []string
	for _, x := range ts {
		if strings.HasPrefix(x.Kind, "scan:") {
			kinds = append(kinds, x.Kind)
		}
	}
	if want := ScanKind(again.ID); len(kinds) != 1 || kinds[0] != want {
		t.Fatalf("scan tasks %v, want %s", kinds, want)
	}
}

func TestTickRunsDueScanAndRecordsRun(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	r, tasks := newRunner(t, e)
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil { // new libraries scan at once
		t.Fatal(err)
	}
	if len(e.list(item.Movie)) != 1 {
		t.Fatal("not scanned")
	}
	st, _ := tasks.ByName("Scan L")
	run, err := tasks.LastRun(st.ID)
	if err != nil || run.Status != task.Succeeded || run.FinishedAt == nil || run.Progress != 1 {
		t.Fatalf("run %+v %v", run, err)
	}
	if run.Summary != "1 movie added" {
		t.Errorf("summary %q", run.Summary)
	}
	if st.NextRunAt == nil || !st.NextRunAt.After(*e.clock) {
		t.Errorf("next run %v", st.NextRunAt)
	}
	lib, _ := e.s.Libraries.Get(e.lib.ID)
	if lib.LastScannedAt == nil {
		t.Error("library not marked scanned")
	}
	// Not due again until the next night; the Local metadata task it made
	// due runs instead.
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again, err := tasks.LastRun(st.ID); err != nil || again.ID != run.ID {
		t.Errorf("ran twice %+v %v", again, err)
	}
	lt, _ := tasks.ByName("Local metadata")
	if lr, err := tasks.LastRun(lt.ID); err != nil || lr.Status != task.Succeeded || lr.Summary != "1 title" {
		t.Errorf("local metadata %+v %v", lr, err)
	}
}

func TestTriggerMakesScanDueAndFailureIsRecorded(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	_ = r.Sync()
	_ = r.Tick(context.Background())
	if err := os.RemoveAll(e.root); err != nil {
		t.Fatal(err)
	}
	if err := r.Trigger(e.lib.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := tasks.ByName("Scan L")
	run, _ := tasks.LastRun(st.ID)
	if run.Status != task.Failed || run.Error == "" {
		t.Fatalf("run %+v", run)
	}
	if st.NextRunAt == nil || !st.NextRunAt.After(*e.clock) {
		t.Errorf("failed task not rescheduled: %v", st.NextRunAt)
	}
}

func TestMetadataTaskRetriesDueItems(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	fixtureCopy(t, "hdr10-main10.mkv", filepath.Join(e.root, "The Matrix (1999)", "m.mkv"))
	e.tmdbUp.Store(false)
	r, tasks := newRunner(t, e)
	_ = r.Sync()
	_ = r.Tick(context.Background())
	m := e.list(item.Movie)[0]
	if md, _ := e.items.Metadata(m.ID); md.Status != item.Unmatched {
		t.Fatal("expected outage")
	}
	e.tmdbUp.Store(true)
	*e.clock = e.clock.Add(2 * time.Hour)
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if md, _ := e.items.Metadata(m.ID); md.Status != item.Matched {
		t.Errorf("not retried: %+v", md)
	}
	mt, _ := tasks.ByName("Refresh metadata")
	if run, err := tasks.LastRun(mt.ID); err != nil || run.Status != task.Succeeded {
		t.Errorf("%+v %v", run, err)
	}
}

func TestInterruptedRunsFailedAtStart(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	_ = r.Sync()
	st, _ := tasks.ByName("Scan L")
	if _, err := tasks.StartRun(st.ID, *e.clock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Run(ctx)
	if run, _ := tasks.LastRun(st.ID); run.Status != task.Failed || run.Error != "interrupted" {
		t.Errorf("%+v", run)
	}
}

// Pausing or disabling a task while an earlier task of the same tick runs
// holds it, and leaves it disabled.
func TestPauseAndDisableTakeHoldMidTick(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	paused := false
	r.Paused = func() bool { return paused }
	var during func()
	r.Stations = func(context.Context) (string, error) { during(); return "", nil }
	must(t, r.Sync())
	for _, name := range []string{"Refresh metadata", "Seek previews"} {
		other, _ := tasks.ByName(name)
		other.NextRunAt = nil
		must(t, tasks.Update(other))
	}
	scan, _ := tasks.ByName("Scan L")
	stations, _ := tasks.ByName("Schedule stations")
	due := func() { // stations first, then the scan
		for _, x := range []*task.Task{stations, scan} {
			x.NextRunAt = e.clock
			must(t, tasks.Update(x))
		}
	}
	held := func(when string) {
		t.Helper()
		must(t, r.Tick(context.Background()))
		if _, err := tasks.LastRun(scan.ID); err == nil {
			t.Fatalf("scan ran after %s", when)
		}
	}
	during = func() {
		off, _ := tasks.Get(scan.ID)
		off.Enabled = false
		must(t, tasks.Update(off))
	}
	due()
	held("it was disabled")
	if got, _ := tasks.Get(scan.ID); got.Enabled {
		t.Fatal("scan enabled again")
	}
	scan.Enabled, during = true, func() { paused = true }
	due()
	held("tasks were paused")
}

func TestPauseHoldsScheduledRunsButNotRequests(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	r, tasks := newRunner(t, e)
	paused := true
	r.Paused = func() bool { return paused }
	_ = r.Sync()
	if err := r.Trigger(e.lib.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := tasks.ByName("Scan L")
	if _, err := tasks.LastRun(st.ID); err == nil || *e.probes != 0 {
		t.Fatal("a due or file-change scan must not start while paused")
	}
	mt, _ := tasks.ByName("Refresh metadata")
	if err := tasks.RequestRun(mt.ID, *e.clock); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run, err := tasks.LastRun(mt.ID); err != nil || run.Status != task.Succeeded {
		t.Fatalf("Run now runs while paused: %+v %v", run, err)
	}
	if req, _ := tasks.Requested(); len(req) != 0 {
		t.Fatal("a request is used up by its run")
	}
	if _, err := tasks.LastRun(st.ID); err == nil {
		t.Fatal("still held")
	}
	paused = false
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.list(item.Movie)) != 1 {
		t.Fatal("held scan runs once unpaused")
	}
}

// The Seek previews task runs after a scan and covers only files without
// previews.
func TestSeekPreviewsTaskFollowsAScan(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	r, tasks := newRunner(t, e)
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	pt, err := tasks.ByName("Seek previews")
	if err != nil || pt.Kind != "previews" || pt.IntervalSeconds != nil || pt.NextRunAt == nil {
		t.Fatalf("%+v %v", pt, err)
	}
	for range 2 { // the scan, then the previews it made due
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	m := e.list(item.Movie)[0]
	fs, _ := e.files.FilesForItem(m.ID)
	p, err := e.s.Previews.ByMediaFile(fs[0].ID)
	if err != nil || p.Count != 1 || p.Width != 320 || p.IntervalMs != 10000 || p.Dir != filepath.Join(e.s.PreviewDir, fs[0].ID.String()) {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, "previews.bif")); err != nil {
		t.Fatal(err)
	}
	pt, _ = tasks.ByName("Seek previews")
	run, err := tasks.LastRun(pt.ID)
	if err != nil || run.Status != task.Succeeded || run.Summary != "1 file" || pt.NextRunAt != nil {
		t.Fatalf("%+v %v %v", run, err, pt.NextRunAt)
	}

	// A later scan makes it due again; it finds nothing to do.
	if err := r.Trigger(e.lib.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if again, _ := tasks.LastRun(pt.ID); again.ID == run.ID || again.Summary != "0 files" {
		t.Fatalf("%+v", again)
	}

	// A file gone from disk keeps its frames.
	if err := os.RemoveAll(filepath.Join(e.root, "A (2001)")); err != nil {
		t.Fatal(err)
	}
	e.mustScan()
	if _, err := os.Stat(filepath.Join(p.Dir, "previews.bif")); err != nil {
		t.Fatalf("frames removed: %v", err)
	}
}

// A file that changes loses its previews whatever its modification time
// says, and gets them once.
func TestSeekPreviewsFollowAChangedFile(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	p := filepath.Join(e.root, "A (2001)", "a.mkv")
	fixtureCopy(t, "h264-high41.mp4", p)
	e.mustScan()
	_, _, err := e.s.MakePreviews(context.Background(), nil, nil)
	must(t, err)
	fs, _ := e.files.FilesForItem(e.list(item.Movie)[0].ID)
	made, err := e.s.Previews.ByMediaFile(fs[0].ID)
	must(t, err)
	fixtureCopy(t, "chapters.mkv", p)
	old := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, os.Chtimes(p, old, old))
	e.mustScan()
	if need, _ := e.files.NeedingPreviews(); len(need) != 1 {
		t.Fatalf("replaced by an older file: %d need previews", len(need))
	}
	if _, err := os.Stat(made.Dir); !os.IsNotExist(err) {
		t.Fatalf("old frames kept: %v", err)
	}
	future := time.Now().Add(365 * 24 * time.Hour)
	must(t, os.Chtimes(p, future, future))
	e.mustScan()
	_, _, err = e.s.MakePreviews(context.Background(), nil, nil)
	must(t, err)
	if need, _ := e.files.NeedingPreviews(); len(need) != 0 {
		t.Fatalf("a future modification time: %d still need previews", len(need))
	}
}

// The Seek previews task records no failure against a file that is not on
// disk, whether or not a scan has marked it yet, so the file gets its
// previews once it is back as it was.
func TestSeekPreviewsWaitForAFileThatIsGone(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	dir := filepath.Join(e.root, "A (2001)")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(dir, "a.mp4"))
	e.mustScan()
	away := filepath.Join(t.TempDir(), "A")
	previews := func() string {
		t.Helper()
		sum, _, err := e.s.MakePreviews(context.Background(), nil, nil)
		must(t, err)
		return sum
	}
	must(t, os.Rename(dir, away))
	if got := previews(); got != "0 files" {
		t.Fatalf("gone and not yet marked: %q", got)
	}
	e.mustScan()
	if got := previews(); got != "0 files" {
		t.Fatalf("marked: %q", got)
	}
	must(t, os.Rename(away, dir))
	e.mustScan()
	if got := previews(); got != "1 file" {
		t.Fatalf("back: %q", got)
	}
}

// A long previews run gives way between files to a task of an earlier
// phase that falls due, and is made due again to carry on where it stopped.
func TestSeekPreviewsYieldToADueTask(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "B (2002)", "b.mp4"))
	e.mustScan()
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	pt, _ := tasks.ByName("Seek previews")
	st, _ := tasks.ByName("Scan L")
	must(t, r.Trigger(e.lib.ID))
	must(t, r.execute(context.Background(), pt))
	run, _ := tasks.LastRun(pt.ID)
	if run.Status != task.Succeeded || run.Summary != "1 file, more to do" {
		t.Fatalf("%+v", run)
	}
	if pt, _ = tasks.ByName("Seek previews"); pt.NextRunAt == nil {
		t.Fatal("previews not made due again")
	}
	must(t, r.Tick(context.Background()))
	sr, _ := tasks.LastRun(st.ID)
	again, _ := tasks.LastRun(pt.ID)
	if sr.ID.Compare(run.ID) < 0 || again.ID.Compare(sr.ID) < 0 || again.Summary != "1 file" {
		t.Fatalf("the scan did not run before previews carried on: %+v %+v", sr, again)
	}
	if left, _ := e.files.NeedingPreviews(); len(left) != 0 {
		t.Fatalf("%d files left", len(left))
	}
}

// A library deleted while its previews are being made fails nothing and
// leaves no frames behind.
func TestSeekPreviewsOfADeletedLibraryGoQuietly(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "B (2002)", "b.mp4"))
	e.mustScan()
	var second uid.ID
	deleted := func() bool {
		need, err := e.files.NeedingPreviews()
		must(t, err)
		second = need[0].ID
		must(t, library.New(e.d).Delete(e.lib.ID))
		return false
	}
	sum, _, err := e.s.MakePreviews(context.Background(), nil, deleted)
	if err != nil || sum != "1 file" {
		t.Fatalf("%q %v", sum, err)
	}
	if _, err := os.Stat(filepath.Join(e.s.PreviewDir, second.String())); !os.IsNotExist(err) {
		t.Fatalf("frames of a deleted file: %v", err)
	}
}

// A file whose previews fail is not tried again until it changes.
func TestFailedPreviewsAreNotRetriedUntilTheFileChanges(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	p := filepath.Join(e.root, "A (2001)", "a.mp4")
	fixtureCopy(t, "h264-high41.mp4", p)
	e.mustScan()
	info, err := os.Stat(p)
	must(t, err)
	must(t, os.WriteFile(p, []byte("not a film"), 0o600))
	must(t, os.Chtimes(p, info.ModTime(), info.ModTime()))
	if sum, _, err := e.s.MakePreviews(context.Background(), nil, nil); err != nil || sum != "1 file, 1 failed" {
		t.Fatal(sum, err)
	}
	if sum, _, _ := e.s.MakePreviews(context.Background(), nil, nil); sum != "0 files" {
		t.Fatal(sum)
	}
	fs, _ := e.files.FilesForItem(e.list(item.Movie)[0].ID)
	fs[0].FileModifiedAt = fs[0].FileModifiedAt.Add(time.Second)
	must(t, e.files.UpdateFile(fs[0]))
	if left, _ := e.files.NeedingPreviews(); len(left) != 1 {
		t.Fatal("a changed file is tried again")
	}
}

// A previews run cut short by a stop resumes at the next start.
func TestInterruptedPreviewsResumeAtStart(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	_ = r.Sync()
	pt, _ := tasks.ByName("Seek previews")
	pt.NextRunAt = nil
	must(t, tasks.Update(pt))
	if _, err := tasks.StartRun(pt.ID, *e.clock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Run(ctx)
	if pt, _ = tasks.ByName("Seek previews"); pt.NextRunAt == nil {
		t.Fatal("not due again")
	}
}

// A previews run a stop cancels is due again, though it is finished as failed.
func TestCancelledPreviewsRunIsDueAgain(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	e.mustScan()
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	pt, _ := tasks.ByName("Seek previews")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	must(t, r.execute(ctx, pt))
	if pt, _ = tasks.ByName("Seek previews"); pt.NextRunAt == nil {
		t.Fatal("not due again")
	}
}

// Refresh metadata works through a backlog larger than one batch, and moves
// on past rows whose provider is off.
func TestRefreshMetadataReachesPastABatchItCannotServe(t *testing.T) {
	e := newEnv(t, library.Music, "") // no TMDB key
	e.s.Music = &fakeMusic{}
	early, late := e.clock.Add(-2*time.Hour), e.clock.Add(-time.Hour)
	for n := range retryBatch {
		m := &item.Item{LibraryID: e.lib.ID, Kind: item.Movie, ParsedTitle: "M" + strconv.Itoa(n)}
		must(t, e.items.Create(m))
		must(t, e.items.SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Unmatched, RetryAt: &early}))
	}
	artist := &item.Item{LibraryID: e.lib.ID, Kind: item.Artist, ParsedTitle: "The Example Band"}
	must(t, e.items.Create(artist))
	album := &item.Item{LibraryID: e.lib.ID, ParentID: &artist.ID, Kind: item.Album, ParsedTitle: "First Light"}
	must(t, e.items.Create(album))
	must(t, e.items.SetMetadata(&item.Metadata{ItemID: album.ID, Provider: item.MusicBrainz, Status: item.Unmatched, RetryAt: &late}))
	r, _ := newRunner(t, e)
	must(t, r.Sync())
	for range 2 {
		must(t, r.Tick(context.Background()))
	}
	if m, err := e.items.Metadata(album.ID); err != nil || m.Status != item.Matched {
		t.Fatalf("album %+v %v", m, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStationsTaskRunsHourly(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	runs := 0
	r.Stations = func(context.Context) (string, error) { runs++; return "2 stations scheduled", nil }
	*e.clock = e.clock.Add(20 * time.Minute)
	must(t, tasks.Create(&task.Task{Name: "Schedule stations", Kind: StationsKind, IntervalSeconds: ptr(86400), Enabled: true, NextRunAt: e.clock}))
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	st, err := tasks.ByName("Schedule stations")
	if err != nil || st.Kind != StationsKind || st.IntervalSeconds == nil || *st.IntervalSeconds != 3600 || st.NextRunAt == nil {
		t.Fatalf("%+v %v", st, err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ = tasks.ByName("Schedule stations"); runs != 1 || st.NextRunAt == nil ||
		!st.NextRunAt.Equal(time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("runs %d, next %v", runs, st.NextRunAt)
	}
	if last, err := tasks.LastRun(st.ID); err != nil || last.Summary != "2 stations scheduled" || last.Status != task.Succeeded {
		t.Fatalf("%+v %v", last, err)
	}
}

func TestPurgeTaskRunsDaily(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	runs := 0
	r.Purge = func(context.Context) (string, error) { runs++; return "3 tombstones and 0 actions purged", nil }
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	p, err := tasks.ByName("Purge sync history")
	if err != nil || p.Kind != PurgeKind || p.IntervalSeconds == nil || *p.IntervalSeconds != 86400 || p.NextRunAt == nil {
		t.Fatalf("%+v %v", p, err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, _ = tasks.ByName("Purge sync history"); runs != 1 || p.NextRunAt == nil || !p.NextRunAt.Equal(e.clock.Add(24*time.Hour)) {
		t.Fatalf("runs %d, next %v", runs, p.NextRunAt)
	}
	if last, err := tasks.LastRun(p.ID); err != nil || last.Summary != "3 tombstones and 0 actions purged" || last.Status != task.Succeeded {
		t.Fatalf("%+v %v", last, err)
	}
}

// The Seek previews task keeps each video file's keyframes before its
// previews, so a first play need not probe them; a file whose probe fails
// is left out until it changes.
func TestSeekPreviewsKeepKeyframes(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	p := filepath.Join(e.root, "A (2001)", "a.mp4")
	fixtureCopy(t, "h264-high41.mp4", p)
	e.mustScan()
	engine := transcode.New(transcode.Config{Root: t.TempDir(), Files: e.files})
	e.s.Bounds = func(ctx context.Context, f *mediafile.File) error {
		_, err := engine.Bounds(ctx, f)
		return err
	}
	if sum, _, err := e.s.MakePreviews(context.Background(), nil, nil); err != nil || sum != "1 keyframe probe, 1 file" {
		t.Fatalf("%q %v", sum, err)
	}
	fs, err := e.files.FilesForItem(e.list(item.Movie)[0].ID)
	must(t, err)
	if at, err := e.files.Keyframes(fs[0].ID, fs[0].FileModifiedAt); err != nil || len(at) == 0 || at[0] != 0 {
		t.Fatalf("%v %v", at, err)
	}

	probes := 0
	e.s.Bounds = func(context.Context, *mediafile.File) error {
		probes++
		return errors.New("no keyframes")
	}
	later := time.Now().Add(time.Hour)
	must(t, os.Chtimes(p, later, later))
	e.mustScan()
	for range 2 {
		if _, _, err := e.s.MakePreviews(context.Background(), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if need, _ := e.files.NeedingKeyframes(); probes != 1 || len(need) != 0 {
		t.Fatalf("%d probes, %d files need keyframes", probes, len(need))
	}
}

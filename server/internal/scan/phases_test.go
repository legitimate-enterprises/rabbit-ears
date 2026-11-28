package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// images counts the scanner's generated images, a fake writing each one.
func images(s *Scanner) *atomic.Int32 {
	n := &atomic.Int32{}
	write := func(out string) error {
		n.Add(1)
		return errors.Join(os.MkdirAll(filepath.Dir(out), 0o750), os.WriteFile(out, []byte("jpeg"), 0o600))
	}
	s.Thumb = func(_ context.Context, _ string, _ int, _, _ int64, out string) error { return write(out) }
	s.Cover = func(_ context.Context, _, out string) error { return write(out) }
	s.Picture = func(_ context.Context, _ string, _ int, out string) error { return write(out) }
	s.Art = func(_ context.Context, _ string, _ int64, frame, poster string) error {
		return errors.Join(write(frame), write(poster))
	}
	return n
}

// addLibrary is another library of kind over a new folder that fill fills.
func (e *env) addLibrary(kind string, fill func(root string)) *library.Library {
	e.t.Helper()
	l := &library.Library{Name: kind, Kind: kind}
	must(e.t, e.s.Libraries.Create(l, nil))
	root := e.t.TempDir()
	must(e.t, e.s.Libraries.AddFolder(&library.Folder{LibraryID: l.ID, Path: root}, nil))
	fill(root)
	return l
}

func copyFixtures(t *testing.T, dir, root string) {
	t.Helper()
	src, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", dir))
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("fixtures missing; run make deps")
	}
	must(t, os.CopyFS(root, os.DirFS(src)))
}

// A library scan of any kind, with every provider set and remote data on,
// records what is on disk and nothing slower: no provider is called and no
// image is made, yet every title is listed under its parsed name with its
// files probed, so it plays.
func TestDiscoveryCallsNoProviderAndMakesNoImage(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	net := &network{}
	e.s.Provider, e.s.Music, e.s.Avatars, e.s.YouTube = net, net, net, youtube.New(e.d)
	made := images(e.s)
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamplight (2011)", "Lamplight (2011).mkv"))
	libs := []*library.Library{e.lib,
		e.addLibrary(library.TV, func(root string) {
			fixtureCopy(t, "chapters.mkv", filepath.Join(root, "Show", "Season 01", "Show - S01E01.mkv"))
		}),
		e.addLibrary(library.Music, func(root string) { copyFixtures(t, "music", root) }),
		e.addLibrary(library.YouTube, func(root string) { copyFixtures(t, "youtube", root) }),
	}
	for _, l := range libs {
		if _, err := e.s.Scan(context.Background(), l, nil); err != nil {
			t.Fatalf("scan %s: %v", l.Kind, err)
		}
	}
	if n, m := net.calls.Load(), made.Load(); n != 0 || m != 0 {
		t.Fatalf("discovery made %d provider calls and %d images", n, m)
	}
	for kind, want := range map[string]string{item.Movie: "Lamplight", item.Show: "Show", item.Album: "First Light", item.Video: "Building a Bench"} {
		its, err := e.items.List(item.Filter{Kind: kind, Limit: 100})
		must(t, err)
		if !slices.ContainsFunc(its, func(it *item.Item) bool { return it.ParsedTitle == want }) {
			t.Errorf("no %s %q in %v", kind, want, titles(its))
		}
	}
	for _, l := range libs {
		fs, err := e.files.FilesInLibrary(l.ID)
		must(t, err)
		for _, f := range fs {
			if tr, _ := e.files.Tracks(f.ID, ""); f.ProbedAt == nil || len(tr) == 0 {
				t.Errorf("%s not probed", f.Path)
			}
		}
	}
}

// runOrder is the kinds of the tasks run so far, in the order they started.
func runOrder(t *testing.T, tasks *task.Repo) []string {
	t.Helper()
	runs, err := tasks.History(nil, 1000)
	must(t, err)
	slices.SortFunc(runs, func(a, b *task.Run) int { return a.ID.Compare(b.ID) })
	var kinds []string
	for _, r := range runs {
		tk, err := tasks.Get(r.TaskID)
		must(t, err)
		kind := tk.Kind
		if strings.HasPrefix(kind, scanKindPrefix) {
			kind = "scan"
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

// With two libraries due and metadata and thumbnail work waiting, every
// scan runs first, then remote metadata, local metadata, then thumbnails.
func TestTickRunsScansThenMetadataThenThumbnails(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	f := &fakeTMDB{results: []tmdb.Result{{ID: 7, Title: "Lamplight", ReleaseDate: "2011-01-01"}}}
	e.s.Provider = f
	images(e.s)
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamplight (2011)", "Lamplight (2011).mkv"))
	e.addLibrary(library.TV, func(root string) {
		fixtureCopy(t, "chapters.mkv", filepath.Join(root, "Show", "Season 01", "Show - S01E01.mkv"))
	})
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	must(t, r.Tick(context.Background()))
	if got, want := runOrder(t, tasks), []string{"scan", "scan", MetadataKind, LocalKind, PreviewsKind}; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if f.called("search") != 2 {
		t.Errorf("metadata looked up %v", f.calls)
	}
	if need, _ := e.files.NeedingPreviews(); len(need) != 0 {
		t.Errorf("%d files without previews", len(need))
	}
}

// A scan that falls due while thumbnails are being made runs before the
// thumbnails left, which carry on after it with nothing lost.
func TestAScanDueMidThumbnailsRunsBeforeTheRest(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	images(e.s)
	for _, d := range []string{"A (2001)", "B (2002)"} {
		fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, d, "m.mkv"))
	}
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	st, _ := tasks.ByName("Scan L")
	scansAt := map[string]int{} // a movie folder, to the scans run when its first chapter thumbnail was made
	thumb := e.s.Thumb
	e.s.Thumb = func(ctx context.Context, in string, pos int, start, end int64, out string) error {
		dir := filepath.Base(filepath.Dir(in))
		if _, ok := scansAt[dir]; !ok {
			runs, err := tasks.History(&st.ID, 10)
			must(t, err)
			scansAt[dir] = len(runs)
		}
		if len(scansAt) == 1 {
			must(t, r.Trigger(e.lib.ID))
		}
		return thumb(ctx, in, pos, start, end, out)
	}
	for range 2 {
		must(t, r.Tick(context.Background()))
	}
	phases := []string{"scan", MetadataKind, LocalKind, PreviewsKind}
	if got, want := runOrder(t, tasks), append(phases, phases...); !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if scansAt["A (2001)"] != 1 || scansAt["B (2002)"] != 2 {
		t.Fatalf("scans run before each file's thumbnails %v", scansAt)
	}
	pt, _ := tasks.ByName("Seek previews")
	if runs, _ := tasks.History(&pt.ID, 10); len(runs) != 2 || runs[1].Summary != "3 chapter thumbnails, 0 files, more to do" {
		t.Fatalf("the run that gave way %+v", runs[len(runs)-1])
	}
	for _, m := range e.list(item.Movie) {
		fs, _ := e.files.FilesForItem(m.ID)
		chs, _ := e.files.Chapters(fs[0].ID)
		for _, c := range chs {
			if c.ThumbnailPath == "" {
				t.Errorf("%s chapter %d has no thumbnail", fs[0].Path, c.Position)
			}
		}
	}
	if need, _ := e.files.NeedingPreviews(); len(need) != 0 {
		t.Errorf("%d files without previews", len(need))
	}
}

// A server stopped after a scan finds the later phases' work in what the
// scan stored: a new Runner looks the titles up and makes their art and
// thumbnails, and running each phase again does nothing.
func TestPhasesResumeFromStoredState(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	f := &fakeTMDB{results: []tmdb.Result{{ID: 7, Title: "Lamplight", ReleaseDate: "2011-01-01"}}}
	e.s.Provider = f
	made := images(e.s)
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamplight (2011)", "Lamplight (2011).mkv"))
	first, tasks := newRunner(t, e)
	must(t, first.Sync())
	st, _ := tasks.ByName("Scan L")
	must(t, first.execute(context.Background(), st))
	if len(f.calls) != 0 || made.Load() != 0 {
		t.Fatalf("the scan called %v and made %d images", f.calls, made.Load())
	}

	again := NewRunner(e.s, tasks)
	must(t, again.Sync())
	must(t, again.Tick(context.Background()))
	m := e.list(item.Movie)[0]
	if e.matched(m) != 7 {
		t.Fatalf("not looked up: %v", f.calls)
	}
	fs, _ := e.files.FilesForItem(m.ID)
	chs, _ := e.files.Chapters(fs[0].ID)
	if len(chs) != 3 || slices.ContainsFunc(chs, func(c *mediafile.Chapter) bool { return c.ThumbnailPath == "" }) {
		t.Fatalf("chapters %+v", chs)
	}
	if _, err := e.s.Previews.ByMediaFile(fs[0].ID); err != nil {
		t.Fatal(err)
	}
	if lm, err := e.items.LocalRow(m.ID); err != nil || lm.PosterPath == "" {
		t.Fatalf("local art %+v %v", lm, err)
	}

	calls, images := len(f.calls), made.Load()
	if _, _, err := e.s.RetryDue(context.Background(), retryBatch, nil); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []func(context.Context, func(float64), func() bool) (string, bool, error){e.s.MakeLocal, e.s.MakePreviews} {
		if _, more, err := phase(context.Background(), nil, nil); err != nil || more {
			t.Fatal(more, err)
		}
	}
	if len(f.calls) != calls || made.Load() != images {
		t.Fatalf("a second pass called %v and made %d images", f.calls[calls:], made.Load()-images)
	}
}

// A file the watcher sees arrive is scanned, then looked up as the next
// task, then gets its thumbnails, all in the tick the trigger wakes.
func TestAWatchedFileIsLookedUpNextThenThumbnailed(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	f := &fakeTMDB{results: []tmdb.Result{{ID: 7, Title: "Lamplight", ReleaseDate: "2011-01-01"}}}
	e.s.Provider = f
	images(e.s)
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	must(t, r.Tick(context.Background()))
	before := len(runOrder(t, tasks))
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamplight (2011)", "Lamplight (2011).mkv"))
	must(t, r.Trigger(e.lib.ID)) // what the watcher calls once the folder is quiet
	must(t, r.Tick(context.Background()))
	if got, want := runOrder(t, tasks)[before:], []string{"scan", MetadataKind, LocalKind, PreviewsKind}; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	m := e.list(item.Movie)[0]
	fs, _ := e.files.FilesForItem(m.ID)
	if chs, _ := e.files.Chapters(fs[0].ID); e.matched(m) != 7 || len(chs) != 3 || chs[0].ThumbnailPath == "" {
		t.Fatalf("matched %d, chapters %+v", e.matched(m), chs)
	}
}

// The Smart lists task runs after the local metadata its rules read and
// before thumbnails; a change a rule reads makes it due again, under any
// clock, and a failed run is recorded with its summary.
func TestSmartListsRunAfterMetadataAndWhenTheLibraryChanges(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	r, tasks := newRunner(t, e)
	runs := 0
	r.Smart = func(context.Context) (string, error) {
		if runs++; runs == 2 {
			return "0 of 2 smart lists changed", errors.New("1 smart lists failed: boom")
		}
		return "0 of 2 smart lists changed", nil
	}
	must(t, r.Sync())
	sm, err := tasks.ByName("Smart lists")
	if err != nil || sm.Kind != SmartKind || sm.IntervalSeconds != nil || sm.NextRunAt == nil || !sm.Enabled {
		t.Fatalf("%+v %v", sm, err)
	}
	must(t, r.Tick(context.Background()))
	if got, want := runOrder(t, tasks), []string{"scan", MetadataKind, LocalKind, SmartKind, PreviewsKind}; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if sm, _ = tasks.ByName("Smart lists"); sm.NextRunAt != nil {
		t.Fatalf("an on-demand task waits for a change: %v", sm.NextRunAt)
	}

	must(t, e.items.Create(&item.Item{LibraryID: e.lib.ID, Kind: item.Movie, ParsedTitle: "New"}))
	must(t, r.Tick(context.Background()))
	if runs != 2 {
		t.Fatalf("%d runs", runs)
	}
	last, err := tasks.LastRun(sm.ID)
	if err != nil || last.Status != task.Failed || last.Error != "1 smart lists failed: boom" || last.Summary != "0 of 2 smart lists changed" {
		t.Fatalf("%+v %v", last, err)
	}
	must(t, r.Tick(context.Background()))
	if runs != 2 {
		t.Fatalf("a failed run is not retried until something changes: %d runs", runs)
	}
}

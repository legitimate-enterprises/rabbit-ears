package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
)

func TestWatcherTriggersOncePerQuietPeriod(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	got := make(chan int64, 10)
	w, err := NewWatcher(e.s.Libraries, func(id int64) { got <- id }, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	dir := filepath.Join(e.root, "New (2020)") // a new folder is picked up too
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(dir, "m.mkv"), []byte{byte(i)}, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case id := <-got:
		if id != e.lib.ID {
			t.Errorf("library %d", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no trigger")
	}
	select {
	case <-got:
		t.Error("triggered twice for one burst")
	case <-time.After(400 * time.Millisecond):
	}
}

func TestWatcherIgnoresPartialFilesAndUnwatchedLibraries(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	got := make(chan int64, 10)
	w, _ := NewWatcher(e.s.Libraries, func(id int64) { got <- id }, 50*time.Millisecond)
	w.Sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	_ = os.WriteFile(filepath.Join(e.root, "m.mkv.part"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(e.root, ".hidden"), []byte("x"), 0o600)
	select {
	case <-got:
		t.Error("partial file triggered a scan")
	case <-time.After(400 * time.Millisecond):
	}
	e.lib.WatchFiles = false
	if err := e.s.Libraries.Update(e.lib); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.root, "m.mkv"), []byte("x"), 0o600)
	select {
	case <-got:
		t.Error("unwatched library triggered")
	case <-time.After(400 * time.Millisecond):
	}
}

func TestWatcherSyncWalksAWatchedRootOnce(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	sub := filepath.Join(e.root, "A (2001)")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	got := make(chan int64, 10)
	w, err := NewWatcher(e.s.Libraries, func(id int64) { got <- id }, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Sync()
	// Nothing reads events yet, so only a walk could come to watch this.
	late := filepath.Join(sub, "Disc 2")
	if err := os.Mkdir(late, 0o750); err != nil {
		t.Fatal(err)
	}
	w.Sync()
	if slices.Contains(w.fsw.WatchList(), late) {
		t.Fatal("the second Sync walked a root it already watches")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	if err := os.WriteFile(filepath.Join(sub, "a.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("a change under the root went unseen after the second Sync")
	}
}

// The watcher leaves out the folders a scan does.
func TestWalkTreeSkipsExtrasWhereScansDo(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Extras/Season 1", "Show/Extras", "Show/Season 1"} {
		must(t, os.MkdirAll(filepath.Join(root, d), 0o750))
	}
	watched := func(kind string) []string {
		var got []string
		walkTree(root, kind, func(p string) { got = append(got, strings.TrimPrefix(p, root)) })
		slices.Sort(got)
		return got
	}
	if got, want := watched(library.TV), []string{"", "/Extras", "/Extras/Season 1", "/Show", "/Show/Season 1"}; !slices.Equal(got, want) {
		t.Errorf("TV %v, want %v", got, want)
	}
	if got, want := watched(library.Music), []string{"", "/Extras", "/Extras/Season 1", "/Show", "/Show/Extras", "/Show/Season 1"}; !slices.Equal(got, want) {
		t.Errorf("Music %v, want %v", got, want)
	}
}

func TestWatcherWatchesARootAgainOnceItIsBack(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	got := make(chan int64, 10)
	w, err := NewWatcher(e.s.Libraries, func(id int64) { got <- id }, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	triggered := func(after string) {
		t.Helper()
		select {
		case <-got:
		case <-time.After(3 * time.Second):
			t.Fatal("no trigger after " + after)
		}
	}

	if err := os.Remove(e.root); err != nil {
		t.Fatal(err)
	}
	triggered("the root went away") // and the trigger is spent before the next one is awaited
	w.Sync()
	if err := os.Mkdir(e.root, 0o750); err != nil {
		t.Fatal(err)
	}
	w.Sync()
	if err := os.WriteFile(filepath.Join(e.root, "m.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	triggered("a change under the root that came back")
}

// Files marked missing while a volume was away come back with a scan when
// it does, not at the next nightly one; a root that stays does not scan.
func TestWatcherTriggersAScanWhenARootIsBack(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	got := make(chan int64, 10)
	w, err := NewWatcher(e.s.Libraries, func(id int64) { got <- id }, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.fsw.Close() }()
	w.Sync()
	if len(got) != 0 {
		t.Fatal("a root that was always there triggered a scan")
	}
	must(t, os.Remove(e.root))
	w.Sync()
	if len(got) != 0 {
		t.Fatal("a root that went away triggered a scan")
	}
	must(t, os.Mkdir(e.root, 0o750))
	w.Sync()
	w.Sync()
	if len(got) != 1 || <-got != e.lib.ID {
		t.Fatalf("triggers %d, want one for the library", len(got))
	}
}

func TestWatcherStopsWatchingAFolderTakenOutOfItsLibrary(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	gone, err := e.s.Libraries.AddFolder(e.lib.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(e.s.Libraries, func(int64) {}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.fsw.Close() }()
	w.Sync()
	if err := e.s.Libraries.RemoveFolder(gone.ID); err != nil {
		t.Fatal(err)
	}
	w.Sync()
	if got := w.fsw.WatchList(); !slices.Equal(got, []string{e.root}) {
		t.Errorf("watching %v, want only %s", got, e.root)
	}
}

func TestWatcherFollowsAFolderGivenANewPath(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	f, err := e.s.Libraries.AddFolder(e.lib.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(e.s.Libraries, func(int64) {}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.fsw.Close() }()
	w.Sync()
	f.Path = t.TempDir()
	if err := e.s.Libraries.UpdateFolder(f); err != nil {
		t.Fatal(err)
	}
	w.Sync()
	got := w.fsw.WatchList()
	slices.Sort(got)
	want := []string{e.root, f.Path}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("watching %v, want %v", got, want)
	}
}

func TestWatcherReleasesDescriptorsWhenARootComesAndGoes(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fill := func(dir string) {
		must(t, os.MkdirAll(dir, 0o750))
		for i := range 300 {
			must(t, os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".mkv"), nil, 0o600))
		}
	}
	stay := t.TempDir()
	fill(stay)
	vol := filepath.Join(t.TempDir(), "vol")
	fill(vol)
	for _, p := range []string{stay, vol} {
		_, err := e.s.Libraries.AddFolder(e.lib.ID, p)
		must(t, err)
	}
	open := func() int {
		fds, err := os.ReadDir("/dev/fd")
		must(t, err)
		return len(fds)
	}
	w, err := NewWatcher(e.s.Libraries, func(int64) {}, time.Second)
	must(t, err)
	w.Sync()
	watching := open()
	for range 3 {
		must(t, os.RemoveAll(vol))
		w.Sync()
		fill(vol)
		w.Sync()
	}
	if now := open(); now > watching+10 {
		t.Errorf("open descriptors grew from %d to %d", watching, now)
	}
}

// A root whose entries, a descriptor each, would take watching past its
// budget is left to the scheduled scan and the roots that fit stay watched.
// It is not walked again every Sync, and is watched once the budget frees
// or, at the next recount, once it has shrunk to fit.
func TestWatcherLeavesARootPastItsBudgetToTheScan(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	big := t.TempDir()
	for i := range 5 {
		must(t, os.WriteFile(filepath.Join(big, strconv.Itoa(i)+".mkv"), nil, 0o600))
	}
	_, err := e.s.Libraries.AddFolder(e.lib.ID, big)
	must(t, err)
	w, err := NewWatcher(e.s.Libraries, func(int64) {}, time.Second)
	must(t, err)
	defer func() { closeWatcher(w.fsw) }()
	watching := func(want ...string) {
		t.Helper()
		got := w.fsw.WatchList()
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("watching %v, want %v", got, want)
		}
	}
	w.budget = 6 // the root takes 1, big 6
	w.Sync()
	watching(e.root)
	w.Sync()
	watching(e.root)
	folders, err := e.s.Libraries.Folders(e.lib.ID)
	must(t, err)
	root := slices.IndexFunc(folders, func(f *library.Folder) bool { return f.Path == e.root })
	must(t, e.s.Libraries.RemoveFolder(folders[root].ID))
	w.Sync()
	watching(big)

	_, err = e.s.Libraries.AddFolder(e.lib.ID, e.root)
	must(t, err)
	closeWatcher(w.fsw)
	w, err = NewWatcher(e.s.Libraries, func(int64) {}, time.Second)
	must(t, err)
	w.budget = 4
	w.Sync()
	watching(e.root)
	for i := range 3 {
		must(t, os.Remove(filepath.Join(big, strconv.Itoa(i)+".mkv")))
	}
	w.Sync()
	watching(e.root)
	s := w.skipped[big]
	s.at = s.at.Add(-recount)
	w.skipped[big] = s
	w.Sync()
	watching(e.root, big)
}

func TestWatcherCoversASymlinkedRoot(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	real := t.TempDir()
	if err := os.Remove(e.root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, e.root); err != nil {
		t.Fatal(err)
	}
	got := make(chan int64, 10)
	w, err := NewWatcher(e.s.Libraries, func(id int64) { got <- id }, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	if err := os.WriteFile(filepath.Join(real, "m.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("no trigger for a change under a symlinked root")
	}
}

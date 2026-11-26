package scan

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
)

// Watcher turns filesystem events under watched libraries into one trigger
// per library once the folder has been quiet for the debounce period, so a
// copy in progress causes a single scan after it finishes.
type Watcher struct {
	libs     *library.Repo
	trigger  func(libraryID int64)
	debounce time.Duration

	mu      sync.Mutex
	fsw     *fsnotify.Watcher
	roots   map[string]int // library folders walked and watched, to the budget they hold
	budget  int            // watches the roots may hold together, an entry each
	skipped map[string]skip
	away    map[string]bool // roots Sync found absent and has not seen since
	timers  map[int64]*time.Timer
}

// skip is a root left unwatched, of entries counted at at.
type skip struct {
	entries int
	at      time.Time
}

// recount is how long a root left unwatched waits to be counted again, in
// case it shrank, unless the budget frees enough for it sooner.
const recount = time.Hour

// NewWatcher returns a Watcher calling trigger for changed libraries.
func NewWatcher(libs *library.Repo, trigger func(libraryID int64), debounce time.Duration) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{fsw: fsw, libs: libs, trigger: trigger, debounce: debounce, roots: map[string]int{}, budget: watchBudget(),
		skipped: map[string]skip{}, away: map[string]bool{}, timers: map[int64]*time.Timer{}}, nil
}

// Sync watches every folder of every library that has file watching on,
// recursively, and no other. It is safe to call repeatedly: a root is walked
// once, when it is first seen or back after being away, and the folders made
// under it later arrive as events; a root back after being away has its
// library's scan triggered. A root whose entries would take watching
// past its budget is left to the scheduled scan, with one warning, until
// the budget frees enough for it or a recount finds it shrunk to fit.
func (w *Watcher) Sync() {
	libs, err := w.libs.List()
	if err != nil {
		logstore.Log("scan").Warn("Watcher could not list libraries", "err", err)
		return
	}
	want := map[string]*library.Library{} // root to its library
	var order []string
	for _, l := range libs {
		if !l.WatchFiles {
			continue
		}
		folders, err := w.libs.Folders(l.ID)
		if err != nil {
			logstore.Log("scan").Warn("Watcher could not list folders", "err", err)
			return
		}
		for _, f := range folders {
			want[f.Path] = l
			order = append(order, f.Path)
		}
	}
	present := func(root string) bool {
		_, err := os.Stat(root)
		return err == nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for root := range w.roots {
		if _, ok := want[root]; ok && present(root) {
			continue
		}
		// The watches of an unmounted volume are dead, and fsnotify cannot
		// renew them, so every root starts over on a new watcher.
		// shortcut: a root that goes and is back between two calls is not
		// noticed, fix it if a volume ever remounts that fast.
		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			logstore.Log("scan").Warn("Watcher could not start over", "err", err)
			return
		}
		closeWatcher(w.fsw)
		w.fsw, w.roots = fsw, map[string]int{}
		break
	}
	for root := range w.skipped {
		if _, ok := want[root]; !ok {
			delete(w.skipped, root)
		}
	}
	for root := range w.away {
		if _, ok := want[root]; !ok {
			delete(w.away, root)
		}
	}
	free := w.budget
	for _, n := range w.roots {
		free -= n
	}
	for _, root := range order {
		if _, ok := w.roots[root]; ok {
			continue
		}
		if !present(root) {
			w.away[root] = true
			continue
		}
		// What changed while the volume was away raised no event, and its
		// files stay marked missing until a scan sees them.
		if w.away[root] {
			delete(w.away, root)
			w.trigger(want[root].ID)
		}
		s, was := w.skipped[root]
		if was && s.entries > free && time.Since(s.at) < recount {
			continue
		}
		n := walkTree(root, want[root].Kind, func(string) {})
		if n > free {
			if !was {
				logstore.Log("scan").Warn("Watcher left a folder too large to watch to the scheduled scan", "library", want[root].Name, "path", root,
					"entries", n, "budget", w.budget, "free", free)
			}
			w.skipped[root] = skip{n, time.Now()}
			continue
		}
		delete(w.skipped, root)
		w.roots[root] = n
		free -= n
		addTree(w.fsw, root, want[root].Kind)
	}
}

func addTree(fsw *fsnotify.Watcher, root, kind string) {
	walkTree(root, kind, func(p string) {
		if err := fsw.Add(p); err != nil {
			logstore.Log("scan").Warn("Watcher could not watch folder", "path", p, "err", err)
		}
	})
}

// walkTree calls watch with each folder under root that watching covers and
// returns how many entries they hold, root included: fsnotify's kqueue
// backend opens a descriptor for each.
func walkTree(root, kind string, watch func(p string)) int {
	n := 0
	top := root + string(filepath.Separator) // makes WalkDir follow a symlinked root
	_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped; the nightly scan still covers it
		}
		n++
		if !d.IsDir() {
			return nil
		}
		if p != top && skipDir(kind, root, p) {
			return fs.SkipDir
		}
		if p == top {
			p = root // watch paths stay the clean root, which Sync compares against
		}
		watch(p)
		return nil
	})
	return n
}

// closeWatcher releases every watch's descriptor first, which fsnotify's
// kqueue Close skips once it has marked the watcher closed.
func closeWatcher(fsw *fsnotify.Watcher) {
	for _, p := range fsw.WatchList() {
		_ = fsw.Remove(p)
	}
	_ = fsw.Close()
}

func (w *Watcher) current() *fsnotify.Watcher {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fsw
}

var partial = map[string]bool{".part": true, ".tmp": true, ".crdownload": true, ".!qb": true, ".download": true}

func ignorable(name string) bool {
	return strings.HasPrefix(name, ".") || partial[strings.ToLower(filepath.Ext(name))]
}

// Run delivers triggers until ctx ends, then closes the watcher.
func (w *Watcher) Run(ctx context.Context) {
	for {
		fsw := w.current()
		select {
		case <-ctx.Done():
			closeWatcher(w.current())
			return
		// A closed channel is a watcher Sync has replaced.
		case ev, ok := <-fsw.Events:
			if ok {
				w.handle(ev)
			}
		case err, ok := <-fsw.Errors:
			if ok {
				logstore.Log("scan").Warn("Watcher error", "err", err)
			}
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	if ev.Op == fsnotify.Chmod || ignorable(filepath.Base(ev.Name)) {
		return
	}
	lib, err := w.libs.ForPath(ev.Name)
	if err != nil || !lib.WatchFiles {
		return
	}
	if ev.Has(fsnotify.Create) {
		if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
			// shortcut: what a watched root gains after its walk is not
			// charged to the budget until the watcher starts over; recount
			// watched roots if libraries ever grow that much between restarts.
			addTree(w.current(), ev.Name, lib.Kind)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if t := w.timers[lib.ID]; t != nil {
		t.Stop()
	}
	id := lib.ID
	w.timers[id] = time.AfterFunc(w.debounce, func() { w.trigger(id) })
}

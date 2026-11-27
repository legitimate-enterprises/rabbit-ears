package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// network is every metadata provider at once, counting each call and
// failing it as an outage would.
type network struct{ calls atomic.Int32 }

var errCalled = errors.New("a provider was called")

func (n *network) call() error { n.calls.Add(1); return errCalled }

func (n *network) Enabled() bool { return true }
func (n *network) Find(context.Context, string, string) ([]tmdb.Result, []tmdb.Result, error) {
	return nil, nil, n.call()
}
func (n *network) ReleaseGroup(context.Context, string) (*musicbrainz.Details, error) {
	return nil, n.call()
}
func (n *network) SearchMovie(context.Context, string, *int) ([]tmdb.Result, error) {
	return nil, n.call()
}
func (n *network) SearchTV(context.Context, string, *int) ([]tmdb.Result, error) {
	return nil, n.call()
}
func (n *network) Movie(context.Context, int64) (*tmdb.Details, error)        { return nil, n.call() }
func (n *network) TV(context.Context, int64) (*tmdb.Details, error)           { return nil, n.call() }
func (n *network) Season(context.Context, int64, int) ([]tmdb.Episode, error) { return nil, n.call() }
func (n *network) Collection(context.Context, int64) (*tmdb.CollectionRef, error) {
	return nil, n.call()
}
func (n *network) CacheImage(context.Context, string, string, string) (string, error) {
	return "", n.call()
}
func (n *network) SearchRelease(context.Context, string, string) ([]musicbrainz.Release, error) {
	return nil, n.call()
}
func (n *network) Release(context.Context, string) (*musicbrainz.Details, error) {
	return nil, n.call()
}
func (n *network) Artist(context.Context, string) (*musicbrainz.Details, error) { return nil, n.call() }
func (n *network) CacheCover(context.Context, string, string) (string, error)   { return "", n.call() }
func (n *network) Fetch(context.Context, string) (string, error)                { return "", n.call() }

// Under Local only no scan, lookup, refresh or local metadata run of a
// movie, TV, music or YouTube library calls a provider. Allowing remote
// data then has the hourly Refresh metadata task look up what the scans
// left, with nothing rescanned.
func TestLocalOnlyCallsNoProvider(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	net := &network{}
	e.s.Provider, e.s.Music, e.s.Avatars, e.s.YouTube = net, net, net, youtube.New(e.d)
	must(t, e.s.Settings.SetSources(serverinfo.Default))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lamplight (2011)", "Lamplight (2011).mp4"))
	libs := []*library.Library{e.lib}
	add := func(kind string, fill func(root string)) {
		l := &library.Library{Name: kind, Kind: kind}
		must(t, e.s.Libraries.Create(l, nil))
		root := t.TempDir()
		err := e.s.Libraries.AddFolder(&library.Folder{LibraryID: l.ID, Path: root}, nil)
		must(t, err)
		fill(root)
		libs = append(libs, l)
	}
	add(library.TV, func(root string) {
		fixtureCopy(t, "chapters.mkv", filepath.Join(root, "Show", "Season 01", "Show - S01E01.mkv"))
	})
	for kind, dir := range map[string]string{library.Music: "music", library.YouTube: "youtube"} {
		add(kind, func(root string) {
			src, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", dir))
			if _, err := os.Stat(src); err != nil {
				t.Skip("fixtures missing; run make deps")
			}
			must(t, os.CopyFS(root, os.DirFS(src)))
		})
	}
	ctx := context.Background()
	for _, l := range libs {
		sum, err := e.s.Scan(ctx, l, nil)
		if err != nil || sum.LookupFailed != 0 {
			t.Fatalf("scan %s: %q %v", l.Kind, sum, err)
		}
		if _, err := e.s.MatchLibrary(ctx, l); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.MatchMusic(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.s.RetryDue(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.MakeLocal(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := net.calls.Load(); n != 0 {
		t.Fatalf("%d provider calls under Local only", n)
	}

	must(t, e.s.Settings.SetSources(serverinfo.Sources{Source: serverinfo.Local, Mode: serverinfo.Preferred}))
	if _, err := e.s.RetryDue(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if n := net.calls.Load(); n < 4 {
		t.Fatalf("the refresh asked providers %d times; want the movie, the show, the albums and a channel", n)
	}
}

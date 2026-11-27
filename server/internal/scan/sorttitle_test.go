package scan

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
)

// A movie sorts by the title it shows in each phase of a scan: its parsed
// title once discovered, the provider's once matched, and its parsed title
// again when the settings stop showing the provider's.
func TestSortTitleFollowsTheTitleEachPhaseShows(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	e.s.Provider = &fakeTMDB{results: []tmdb.Result{{ID: 11, Title: "A Quiet Place", ReleaseDate: "2018-04-06"}}}
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "The Hush (2018) {tmdb-11}", "The Hush (2018).mp4"))
	sorts := func(phase, want string) {
		t.Helper()
		ms := e.list(item.Movie)
		if len(ms) != 1 || ms[0].SortTitle != want {
			t.Fatalf("%s: movies %+v, want one sorting as %q", phase, ms, want)
		}
	}
	if _, err := e.s.Scan(context.Background(), e.lib, nil); err != nil {
		t.Fatal(err)
	}
	sorts("discovered", "Hush")
	if _, _, err := e.s.lookUp(context.Background(), e.lib, nil); err != nil {
		t.Fatal(err)
	}
	sorts("matched", "Quiet Place")
	sources(t, e.d, serverinfo.Sources{Source: serverinfo.Local, Mode: serverinfo.Only})
	sorts("remote off", "Hush")
}

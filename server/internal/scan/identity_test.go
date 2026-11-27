package scan

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (e *env) editions(itemID uid.ID) []*edition.Edition {
	e.t.Helper()
	es, err := edition.New(e.d).ForItem(itemID)
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func (e *env) onlyMovie() *item.Item {
	e.t.Helper()
	ms := e.list(item.Movie)
	if len(ms) != 1 {
		e.t.Fatalf("movies %+v", ms)
	}
	return ms[0]
}

// stretch makes a file whose name holds "+N" probe N minutes longer than it
// is, so untagged cuts can be told apart with the small fixtures.
func stretch(e *env) {
	real := e.s.Probe
	e.s.Probe = func(ctx context.Context, p string) (*probe.Result, error) {
		r, err := real(ctx, p)
		if err == nil && r.DurationMs != nil {
			for _, m := range []string{"+10", "+2"} {
				if strings.Contains(filepath.Base(p), m) {
					n := int64(10)
					if m == "+2" {
						n = 2
					}
					d := *r.DurationMs + n*60000
					r.DurationMs = &d
				}
			}
		}
		return r, err
	}
}

// Every video file in a movie's folder is that movie: an untagged second
// copy joins the default edition, a tagged file is its own edition, and an
// untagged file more than CutGapMs longer is an unnamed cut.
func TestMovieFolderHoldsEveryFile(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	stretch(e)
	dir := filepath.Join(e.root, "Lantern (2001)")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(dir, "Lantern (2001) [WEBDL-720p]-GRP.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(dir, "Lantern (2001) [Bluray-1080p]-GRP +2.mkv"))
	fixtureCopy(t, "hdr10-main10.mkv", filepath.Join(dir, "Lantern (2001) {edition-Director's Cut} [Bluray-2160p]-GRP.mkv"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(dir, "Lantern (2001) [WEBDL-720p]-GRP +10.mp4"))
	e.mustScan()
	m := e.onlyMovie()
	es := e.editions(m.ID)
	var got []string
	for _, ed := range es {
		got = append(got, fmt.Sprintf("%s:%d", ed.Name, ed.Files))
	}
	if strings.Join(got, ",") != "2-minute cut:2,10-minute cut:1,Director's Cut:1" || !es[0].Default {
		t.Fatalf("editions %v", got)
	}
}

// movieOf is the movie the file at path is filed under.
func (e *env) movieOf(path string) *item.Item {
	e.t.Helper()
	f, err := e.files.FileByPath(path)
	must(e.t, err)
	it, err := e.items.Get(*f.ItemID)
	must(e.t, err)
	return it
}

// A file in a folder that states a year the folder does not, or an id
// none of the folder's movie shares, is a movie of its own name, the
// first file in the folder too; one stating the folder's year, or none,
// is the folder's movie.
func TestMovieFolderFileStatingAnotherIsItsOwn(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	iron, thor := filepath.Join(e.root, "Marvel", "Iron Man (2008).mp4"), filepath.Join(e.root, "Marvel", "Thor (2011).mkv")
	fixtureCopy(t, "h264-high41.mp4", iron)
	fixtureCopy(t, "chapters.mkv", thor)
	dir := filepath.Join(e.root, "Inception (2010)")
	for _, n := range []string{"Inception (2010) - Director's Cut.mkv", "Inception.mkv", "extras.mkv"} {
		fixtureCopy(t, "chapters.mkv", filepath.Join(dir, n))
	}
	e.mustScan()
	got := titles(e.list(item.Movie))
	slices.Sort(got)
	mi, mt := e.movieOf(iron), e.movieOf(thor)
	if !slices.Equal(got, []string{"Inception", "Iron Man", "Thor"}) || mi.ParsedYear == nil || *mi.ParsedYear != 2008 || mt.ParsedYear == nil || *mt.ParsedYear != 2011 {
		t.Errorf("movies %q", got)
	}
	if files, _ := e.files.FilesForItem(e.movieOf(filepath.Join(dir, "extras.mkv")).ID); len(files) != 3 {
		t.Errorf("inception files %d", len(files))
	}

	f := movies()
	e = newEnv(t, library.Movies, "")
	e.s.Provider = f
	dir = filepath.Join(e.root, "Lantern (2001) {tmdb-11}")
	a, b := filepath.Join(dir, "Lantern (2001) {tmdb-11}.mp4"), filepath.Join(dir, "Lantern (2001) {tmdb-12}.mkv")
	fixtureCopy(t, "chapters.mkv", b)
	e.mustScan()
	if mb := e.movieOf(b); e.matched(mb) != 12 || len(e.reviews("")) != 0 {
		t.Fatalf("b matched %d, reviews %+v", e.matched(mb), e.reviews(""))
	}
	fixtureCopy(t, "h264-high41.mp4", a)
	e.mustScan()
	if ma, mb := e.movieOf(a), e.movieOf(b); ma.ID == mb.ID || e.matched(ma) != 11 || e.matched(mb) != 12 || len(e.reviews("")) != 0 {
		t.Fatalf("a in %s matched %d, b in %s matched %d, reviews %+v", ma.ID, e.matched(ma), mb.ID, e.matched(mb), e.reviews(""))
	}
}

// A file already filed under its folder's movie keeps it, whatever its
// own name states.
func TestKnownMovieFolderFileKeepsItsMovie(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	iron, thor := filepath.Join(e.root, "Marvel", "Iron Man (2008).mp4"), filepath.Join(e.root, "Marvel", "Thor (2011).mkv")
	fixtureCopy(t, "h264-high41.mp4", iron)
	fixtureCopy(t, "chapters.mkv", thor)
	e.mustScan()
	marvel, other := e.movieOf(iron), e.movieOf(thor)
	must(t, edition.New(e.d).Fold(marvel.ID, other.ID))
	marvel.ParsedTitle, marvel.ParsedYear = "Marvel", nil
	must(t, e.items.Update(marvel))
	e.mustScan()
	m := e.onlyMovie()
	files, _ := e.files.FilesForItem(m.ID)
	if m.ID != marvel.ID || m.ParsedTitle != "Marvel" || len(files) != 2 {
		t.Fatalf("movie %+v files %d", m, len(files))
	}
}

// Replacing a movie's only file under a new name keeps the movie and its
// edition, so watch state and playlists that hold them survive an upgrade.
// The old file's record goes, as the movie has the new one.
func TestReplacedMovieFileKeepsItsIDs(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	dir := filepath.Join(e.root, "Lantern (2001)")
	old := filepath.Join(dir, "Lantern (2001) [WEBDL-1080p]-GRP.mp4")
	fixtureCopy(t, "h264-high41.mp4", old)
	e.mustScan()
	before := e.onlyMovie()
	ed := e.editions(before.ID)[0].ID

	must(t, os.Remove(old))
	upgraded := filepath.Join(dir, "Lantern (2001) [Bluray-2160p]-GRP.mkv")
	fixtureCopy(t, "chapters.mkv", upgraded)
	e.mustScan()

	after := e.onlyMovie()
	files, _ := e.files.FilesForItem(after.ID)
	if after.ID != before.ID || len(files) != 1 || files[0].Path != upgraded || e.missing(upgraded) || e.editions(after.ID)[0].ID != ed {
		t.Fatalf("before %s after %s files %+v", before.ID, after.ID, files)
	}
}

// Across folders and loose in the library, a movie is its title and year,
// ignoring case; without a year two files stay two movies.
func TestMovieRecognizedByTitleAndYear(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001)", "a.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "lantern (2001) [4K]", "b.mkv"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "LANTERN.2001.1080p.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Harbor", "harbor.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Harbor.mp4"))
	e.mustScan()
	var lantern, harbor int
	for _, m := range e.list(item.Movie) {
		files, _ := e.files.FilesForItem(m.ID)
		switch strings.ToLower(m.ParsedTitle) {
		case "lantern":
			lantern++
			if len(files) != 3 {
				t.Errorf("lantern files %d", len(files))
			}
		case "harbor":
			harbor++
		}
	}
	if lantern != 1 || harbor != 2 {
		t.Fatalf("lantern %d harbor %d", lantern, harbor)
	}
}

// oneFilm is a provider that matches every movie title to TMDB id 7.
type oneFilm struct{ Provider }

func (oneFilm) Enabled() bool { return true }

func (oneFilm) SearchMovie(_ context.Context, title string, year *int) ([]tmdb.Result, error) {
	r := tmdb.Result{ID: 7, Title: title}
	if year != nil {
		r.ReleaseDate = fmt.Sprintf("%d-01-01", *year)
	}
	return []tmdb.Result{r}, nil
}

func (oneFilm) Movie(_ context.Context, id int64) (*tmdb.Details, error) {
	return &tmdb.Details{ID: id, Title: "Lantern"}, nil
}

func (oneFilm) CacheImage(context.Context, string, string, string) (string, error) { return "", nil }

// A movie made in a scan that matches the TMDB id an older movie holds is
// folded into it.
func TestNewMovieWithKnownTMDBIDFolds(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	e.s.Provider = oneFilm{}
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001)", "a.mp4"))
	e.mustScan()
	older := e.onlyMovie()
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamplight (2002)", "b.mkv"))
	e.mustScan()
	m := e.onlyMovie()
	files, _ := e.files.FilesForItem(m.ID)
	if m.ID != older.ID || len(files) != 2 {
		t.Fatalf("movie %s (older %s) files %d", m.ID, older.ID, len(files))
	}
}

// refs lists what points at movies: allow lists, playlist entries and
// station programs with their editions' titles, collection entries, and
// watch state as account:item:watched:position:last played, ids in hex.
func (e *env) refs() string {
	e.t.Helper()
	var s string
	must(e.t, e.d.QueryRow(`SELECT
		(SELECT group_concat(lower(hex(item_id))) FROM account_allow) || ' ' ||
		(SELECT group_concat(lower(hex(p.item_id)) || '/' || lower(hex(d.item_id))) FROM playlist_entry p JOIN edition d ON d.id = p.edition_id) || ' ' ||
		(SELECT group_concat(lower(hex(p.item_id)) || '/' || lower(hex(d.item_id))) FROM station_program p JOIN edition d ON d.id = p.edition_id) || ' ' ||
		(SELECT group_concat(lower(hex(item_id))) FROM collection_movie) || ' ' ||
		(SELECT group_concat(lower(hex(account_id)) || ':' || lower(hex(item_id)) || ':' || watched || ':' || position_ms || ':' || last_played_at, ',')
			FROM (SELECT * FROM watch_state ORDER BY account_id))`).Scan(&s))
	return s
}

// hexOf is an id as refs and SQL blob literals spell it.
func hexOf(id uid.ID) string { return hex.EncodeToString(id[:]) }

// Two movies a later match gives one TMDB id are one title, the older: it
// takes the newer's files, and what pointed at the newer points at it,
// keeping per profile the more recently played watch state, watched if
// either was. Doing it again changes nothing.
func TestMatchedDuplicateMoviesFold(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001)", "a.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lantern", "b.mkv"))
	e.mustScan()
	ms := e.list(item.Movie)
	if len(ms) != 2 {
		t.Fatalf("movies %+v", ms)
	}
	slices.SortFunc(ms, func(a, b *item.Item) int { return a.ID.Compare(b.ID) })
	older, newer := ms[0].ID, ms[1].ID
	ed := e.editions(newer)[0].ID
	lit := func(id uid.ID) string { return "x'" + hexOf(id) + "'" }
	for _, q := range []string{
		`INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (#1,'adult','A',0,0), (#3,'adult','B',0,0)`,
		`INSERT INTO household (id, name, adult_id, created_at, modified_at) VALUES (#1,'H',#1,0,0)`,
		`INSERT INTO account (id, kind, name, household_id, created_at, modified_at) VALUES (#2,'kid','K',#1,0,0)`,
		`INSERT INTO account_allow (account_id, item_id) VALUES (#2,NEWER)`,
		`INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (#1,#1,'P','movies',0,0)`,
		`INSERT INTO playlist_entry (id, playlist_id, item_id, edition_id, rank, added_at, created_at, modified_at) VALUES (#1,#1,NEWER,ED,'a0',0,0,0)`,
		`INSERT INTO collection (id, source, household_id, name, created_at, modified_at) VALUES (#1,'custom',#1,'C',0,0)`,
		`INSERT INTO collection_movie (collection_id, item_id) VALUES (#1,NEWER)`,
		`INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, modified_at) VALUES (#1,#1,#1,'S','private',0,0)`,
		`INSERT INTO station_program (id, station_id, item_id, edition_id, entry_id, loop, position, starts_at, ends_at, created_at, modified_at)
			VALUES (#1,#1,NEWER,ED,#1,0,0,0,1,0,0)`,
		`INSERT INTO watch_state (account_id, item_id, watched, position_ms, last_played_at, changed_at)
			VALUES (#1,OLDER,1,0,100,100), (#1,NEWER,0,5000,200,200), (#3,NEWER,0,7000,50,50)`,
	} {
		_, err := e.d.Exec(strings.NewReplacer("OLDER", lit(older), "NEWER", lit(newer), "ED", lit(ed),
			"#1", lit(uid.N(1)), "#2", lit(uid.N(2)), "#3", lit(uid.N(3))).Replace(q))
		must(t, err)
	}
	e.s.Provider = oneFilm{}
	e.mustScan()
	m := e.onlyMovie()
	files, _ := e.files.FilesForItem(m.ID)
	want := fmt.Sprintf("%[1]s %[1]s/%[1]s %[1]s/%[1]s %[1]s %[2]s:%[1]s:1:5000:200,%[3]s:%[1]s:0:7000:50", hexOf(older), hexOf(uid.N(1)), hexOf(uid.N(3)))
	if m.ID != older || len(files) != 2 || e.refs() != want {
		t.Fatalf("movie %s (older %s) files %d refs %q, want %q", m.ID, older, len(files), e.refs(), want)
	}
	e.mustScan()
	if _, _, err := e.s.RetryDue(context.Background(), 100, nil); err != nil {
		t.Fatal(err)
	}
	if m := e.onlyMovie(); m.ID != older || e.refs() != want {
		t.Fatalf("again: movie %s refs %q", m.ID, e.refs())
	}
}

// The metadata task folds the duplicates its matches make.
func TestRetriedMatchFolds(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	e.tmdbUp.Store(false)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001)", "a.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamplight (2002)", "b.mkv"))
	e.mustScan()
	*e.clock = e.clock.Add(2 * RetryDelay)
	e.s.Provider = oneFilm{}
	if _, _, err := e.s.RetryDue(context.Background(), 100, nil); err != nil {
		t.Fatal(err)
	}
	if files, _ := e.files.FilesForItem(e.onlyMovie().ID); len(files) != 2 {
		t.Fatalf("files %d", len(files))
	}
}

// byTitle matches Lantern to TMDB id 7 and every other title to 8.
type byTitle struct{ oneFilm }

func (byTitle) SearchMovie(ctx context.Context, title string, year *int) ([]tmdb.Result, error) {
	rs, err := oneFilm{}.SearchMovie(ctx, title, year)
	if title != "Lantern" {
		rs[0].ID = 8
	}
	return rs, err
}

// Movies of different TMDB ids, or of one id in different libraries, stay
// apart.
func TestOnlyOneLibrarysMoviesOfOneIDFold(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	e.s.Provider = byTitle{}
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001)", "a.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Harbor (2003)", "b.mkv"))
	other := &library.Library{Name: "M", Kind: library.Movies}
	must(t, e.s.Libraries.Create(other, nil))
	dir := t.TempDir()
	err := e.s.Libraries.AddFolder(&library.Folder{LibraryID: other.ID, Path: dir}, nil)
	must(t, err)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(dir, "Lantern (2001)", "c.mp4"))
	e.mustScan()
	_, err = e.s.Scan(context.Background(), other, nil)
	must(t, err)
	e.mustScan()
	if _, _, err := e.s.RetryDue(context.Background(), 100, nil); err != nil {
		t.Fatal(err)
	}
	n, err := e.items.Count(item.Filter{LibraryID: &other.ID, Kind: item.Movie})
	must(t, err)
	if len(e.list(item.Movie)) != 2 || n != 1 {
		t.Fatalf("movies %d and %d", len(e.list(item.Movie)), n)
	}
}

// A file naming two episodes is both, and goes with them; specials sit in
// season 0 from a Specials folder.
func TestDoubleEpisodeFileAndSpecials(t *testing.T) {
	e := newEnv(t, library.TV, "")
	show := filepath.Join(e.root, "Lamp Street")
	double := filepath.Join(show, "Season 10", "Lamp Street (2010) - S10E17-E18 - Two Parts [WEBDL-1080p]-GRP.mkv")
	fixtureCopy(t, "chapters.mkv", double)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(show, "Specials", "Lamp Street (2010) - S00E01 - Extra [WEBDL-1080p]-GRP.mp4"))
	e.mustScan()
	eps := e.list(item.Episode)
	if len(eps) != 3 {
		t.Fatalf("episodes %+v", eps)
	}
	var nums []int
	for _, ep := range eps {
		files, _ := e.files.FilesForItem(ep.ID)
		if len(files) != 1 {
			t.Fatalf("episode %d files %d", *ep.EpisodeNumber, len(files))
		}
		season, _ := e.items.Get(*ep.ParentID)
		nums = append(nums, *season.SeasonNumber*100+*ep.EpisodeNumber)
	}
	if fmt.Sprint(nums) != "[1 1017 1018]" {
		t.Fatalf("season and episode numbers %v", nums)
	}
	must(t, os.Remove(double))
	e.mustScan()
	if eps := e.list(item.Episode); len(eps) != 3 || !e.missing(double) {
		t.Fatalf("after removal %+v, missing %v", eps, e.missing(double))
	}
}

// A show whose folder was renamed stays the same show, with its episodes.
func TestRenamedShowFolderKeepsTheShow(t *testing.T) {
	e := newEnv(t, library.TV, "")
	old := filepath.Join(e.root, "Lamp Street")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(old, "Season 01", "Lamp Street - S01E01 - One.mp4"))
	e.mustScan()
	show, ep := e.list(item.Show)[0], e.list(item.Episode)[0]
	renamed := filepath.Join(e.root, "Lamp Street (2010)")
	must(t, os.Rename(old, renamed))
	e.mustScan()
	shows, eps := e.list(item.Show), e.list(item.Episode)
	if len(shows) != 1 || shows[0].ID != show.ID || shows[0].Path != renamed || len(eps) != 1 || eps[0].ID != ep.ID {
		t.Fatalf("shows %+v episodes %+v", shows, eps)
	}
}

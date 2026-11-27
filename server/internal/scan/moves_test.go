package scan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// retag moves a file as a tagger does that writes it again: the media is
// the same, the tags, size and modification time are not.
func retag(t *testing.T, from, to string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(to), 0o750))
	if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", from, "-map", "0", "-c", "copy", //nolint:gosec // test temp file
		"-metadata", "title=Written again", to).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	must(t, os.Remove(from))
}

// rename moves a file or folder as a rename on one volume does: its
// content and modification time stay.
func rename(t *testing.T, from, to string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(to), 0o750))
	must(t, os.Rename(from, to))
}

// clone copies a file with its modification time, as an earlier scan
// would have seen a renamed file beside its old record.
func clone(t *testing.T, from, to string) {
	t.Helper()
	st, err := os.Stat(from)
	must(t, err)
	b, err := os.ReadFile(from) //nolint:gosec // test temp file
	must(t, err)
	must(t, os.MkdirAll(filepath.Dir(to), 0o750))
	must(t, os.WriteFile(to, b, 0o600)) //nolint:gosec // test temp file
	must(t, os.Chtimes(to, st.ModTime(), st.ModTime()))
}

// play records that account uid.N(1) played the item to position at time at.
func (e *env) play(itemID uid.ID, position, at int) {
	e.t.Helper()
	_, err := e.d.Exec(`INSERT OR IGNORE INTO account (id, kind, name, created_at, modified_at) VALUES (?,'adult','A',0,0)`, uid.N(1))
	must(e.t, err)
	_, err = e.d.Exec(`INSERT INTO watch_state (account_id, item_id, watched, position_ms, last_played_at, changed_at) VALUES (?,?,0,?,?,?)`,
		uid.N(1), itemID, position, at, at)
	must(e.t, err)
}

// plays is each item:position played, by item id.
func (e *env) plays() string {
	e.t.Helper()
	rows, err := e.d.Query(`SELECT item_id, position_ms FROM watch_state ORDER BY item_id`)
	must(e.t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id uid.ID
		var pos int
		must(e.t, rows.Scan(&id, &pos))
		out = append(out, fmt.Sprintf("%s:%d", id, pos))
	}
	must(e.t, rows.Err())
	return strings.Join(out, ",")
}

// latest is the later made of two system rows, whose ids uid.New mints in order.
func latest(a, b uid.ID) uid.ID {
	if a.Compare(b) > 0 {
		return a
	}
	return b
}

// state is every item and file of the library, to tell that nothing was
// made, lost or left missing: kinds and ids, then file ids with whether
// each is missing.
func (e *env) state() string {
	e.t.Helper()
	var s string
	must(e.t, e.d.QueryRow(`SELECT (SELECT group_concat(kind || hex(id), ' ') FROM (SELECT * FROM item ORDER BY id)) || ' | ' ||
		(SELECT group_concat(hex(id) || CASE WHEN missing_at IS NULL THEN '' ELSE '?' END, ' ') FROM (SELECT * FROM media_file ORDER BY id))`).Scan(&s))
	return s
}

func (e *env) reviews(reason string) []*review.Review {
	e.t.Helper()
	rs, err := e.s.Reviews.List(review.Filter{LibraryID: &e.lib.ID, Reason: reason})
	must(e.t, err)
	return rs
}

// A movie whose folder and file are both renamed, as Radarr renames them,
// is the same movie with the same file record at the new path, and takes
// the new name's title and year.
func TestRenamedMovieKeepsItsRecords(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	old := filepath.Join(e.root, "Lantern (2001)", "Lantern.2001.1080p.mp4")
	fixtureCopy(t, "h264-high41.mp4", old)
	e.mustScan()
	before := e.state()
	e.play(e.onlyMovie().ID, 5000, 100)

	moved := filepath.Join(e.root, "The Lantern (2002) {tmdb-5}", "The Lantern (2002) {tmdb-5} [WEBDL-1080p]-GRP.mp4")
	rename(t, old, moved)
	probes := *e.probes
	e.mustScan()
	m := e.onlyMovie()
	if f, err := e.files.FileByPath(moved); err != nil || *f.ItemID != m.ID || e.state() != before || *e.probes != probes {
		t.Fatalf("file %+v %v, state %q was %q, %d probes", f, err, e.state(), before, *e.probes-probes)
	}
	if m.ParsedTitle != "The Lantern" || m.ParsedYear == nil || *m.ParsedYear != 2002 || e.plays() != fmt.Sprintf("%s:5000", m.ID) {
		t.Fatalf("movie %+v plays %q", m, e.plays())
	}
	e.mustScan()
	if e.state() != before {
		t.Fatalf("again: %q", e.state())
	}
}

// One of a movie's files moved out to a folder of another name, while
// the rest stay, is no rename of the movie: the movie keeps its name and
// the file, with its record, is filed by its new one.
func TestFileMovedOutOfAMovieLeavesIt(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	stays, leaves := filepath.Join(e.root, "Lantern (2001)", "Lantern.mp4"), filepath.Join(e.root, "Lantern (2001)", "Lamplight.mkv")
	fixtureCopy(t, "h264-high41.mp4", stays)
	fixtureCopy(t, "chapters.mkv", leaves)
	e.mustScan()
	first := e.onlyMovie()
	was, err := e.files.FileByPath(leaves)
	must(t, err)

	moved := filepath.Join(e.root, "Lamplight (2003)", "Lamplight.mkv")
	rename(t, leaves, moved)
	e.mustScan()
	ms := e.list(item.Movie)
	f, err := e.files.FileByPath(moved)
	must(t, err)
	if got := titles(ms); len(ms) != 2 || !slices.Equal(got, []string{"Lamplight", "Lantern"}) || f.ID != was.ID || *f.ItemID == first.ID || e.missing(moved) {
		t.Fatalf("movies %q, file %+v was %+v", got, f, was)
	}
}

// An earlier scan that took a renamed movie for a new one left the old
// movie with a missing file and made a second movie. The next scan folds
// the second into the first, with what was played under either.
func TestEarlierDuplicateOfARenamedMovieIsMerged(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	old := filepath.Join(e.root, "Lantern (2001)", "Lantern.2001.1080p.mp4")
	fixtureCopy(t, "h264-high41.mp4", old)
	e.mustScan()
	before := e.state()
	first := e.onlyMovie()
	e.play(first.ID, 5000, 100)

	moved := filepath.Join(e.root, "The Lantern (2001)", "The Lantern (2001).mp4")
	clone(t, old, moved)
	e.mustScan()
	ms := e.list(item.Movie)
	if len(ms) != 2 {
		t.Fatalf("a copy beside the original is a second movie: %+v", ms)
	}
	e.play(latest(ms[0].ID, ms[1].ID), 9000, 200)
	must(t, os.RemoveAll(filepath.Dir(old)))
	e.mustScan()

	m := e.onlyMovie()
	if f, err := e.files.FileByPath(moved); err != nil || *f.ItemID != first.ID || m.ID != first.ID || e.state() != before ||
		e.plays() != fmt.Sprintf("%s:9000", first.ID) {
		t.Fatalf("file %+v %v, movie %s was %s, state %q was %q, plays %q", f, err, m.ID, first.ID, e.state(), before, e.plays())
	}
}

// A copy of the same movie file elsewhere in the same folder joined the
// movie as a second file by an earlier scan; once the original is gone
// the two records are one.
func TestEarlierCopyInTheSameMovieIsMerged(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	old := filepath.Join(e.root, "Lantern (2001)", "Lantern.2001.1080p.mp4")
	fixtureCopy(t, "h264-high41.mp4", old)
	e.mustScan()
	before := e.state()
	moved := filepath.Join(e.root, "Lantern (2001) {tmdb-5}", "Lantern (2001) {tmdb-5}.mp4")
	clone(t, old, moved)
	e.mustScan()
	if fs, _ := e.files.FilesForItem(e.onlyMovie().ID); len(fs) != 2 {
		t.Fatalf("files %+v", fs)
	}
	must(t, os.RemoveAll(filepath.Dir(old)))
	e.mustScan()
	if f, err := e.files.FileByPath(moved); err != nil || e.state() != before {
		t.Fatalf("file %+v %v, state %q was %q", f, err, e.state(), before)
	}
}

// Sonarr renames a show's folder to another title with its TVDB id and
// its files to match, with no season folders. The show, its season, its
// episodes and their files are the ones there were.
func TestRenamedShowAndEpisodesKeepTheirRecords(t *testing.T) {
	e := newEnv(t, library.TV, "")
	old := filepath.Join(e.root, "Lamp Street")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(old, "Season 01", "Lamp Street - S01E01 - One.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(old, "Season 01", "Lamp Street - S01E02 - Two.mkv"))
	e.mustScan()
	before := e.state()
	show := e.list(item.Show)[0]
	e.play(e.list(item.Episode)[1].ID, 5000, 100)

	renamed := filepath.Join(e.root, "Lamp Street US (2010) {tvdb-555}")
	rename(t, filepath.Join(old, "Season 01", "Lamp Street - S01E01 - One.mp4"), filepath.Join(renamed, "Lamp Street US (2010) - S01E01 - One [WEBDL-1080p]-GRP.mp4"))
	rename(t, filepath.Join(old, "Season 01", "Lamp Street - S01E02 - Two.mkv"), filepath.Join(renamed, "Lamp Street US (2010) - S01E02 - Two [WEBDL-1080p]-GRP.mkv"))
	must(t, os.RemoveAll(old))
	e.mustScan()
	shows := e.list(item.Show)
	if len(shows) != 1 || shows[0].ID != show.ID || shows[0].Path != renamed || shows[0].ParsedTitle != "Lamp Street US" || e.state() != before {
		t.Fatalf("shows %+v, state %q was %q", shows, e.state(), before)
	}
	e.mustScan()
	if e.state() != before {
		t.Fatalf("again: %q", e.state())
	}
}

// An earlier scan made a second show of the renamed folder. The next scan
// folds its episodes into the first show's and it is gone.
func TestEarlierDuplicateOfARenamedShowIsMerged(t *testing.T) {
	e := newEnv(t, library.TV, "")
	old := filepath.Join(e.root, "Lamp Street", "Season 01", "Lamp Street - S01E01 - One.mp4")
	fixtureCopy(t, "h264-high41.mp4", old)
	e.mustScan()
	before := e.state()
	show, ep := e.list(item.Show)[0], e.list(item.Episode)[0]
	e.play(ep.ID, 5000, 100)

	renamed := filepath.Join(e.root, "Lamp Street US (2010) {tvdb-555}")
	clone(t, old, filepath.Join(renamed, "Lamp Street US (2010) - S01E01 - One.mp4"))
	e.mustScan()
	eps := e.list(item.Episode)
	if len(e.list(item.Show)) != 2 || len(eps) != 2 {
		t.Fatalf("shows %+v", e.list(item.Show))
	}
	e.play(latest(eps[0].ID, eps[1].ID), 9000, 200)
	must(t, os.RemoveAll(filepath.Join(e.root, "Lamp Street")))
	e.mustScan()
	shows := e.list(item.Show)
	if len(shows) != 1 || shows[0].ID != show.ID || shows[0].Path != renamed || e.state() != before || e.plays() != fmt.Sprintf("%s:9000", ep.ID) {
		t.Fatalf("shows %+v, state %q was %q, plays %q", shows, e.state(), before, e.plays())
	}
}

// The second show an earlier scan made of a renamed folder has since
// taken episodes the first never had. They join the first show with what
// was played of them, and the second show is gone for good.
func TestEarlierDuplicateShowWithNewerEpisodesIsMerged(t *testing.T) {
	e := newEnv(t, library.TV, "")
	old := filepath.Join(e.root, "Lamp Street", "Season 01", "Lamp Street - S01E01 - One.mp4")
	fixtureCopy(t, "h264-high41.mp4", old)
	e.mustScan()
	show := e.list(item.Show)[0]

	renamed := filepath.Join(e.root, "Lamp Street US (2010) {tvdb-555}")
	clone(t, old, filepath.Join(renamed, "Lamp Street US (2010) - S01E01 - One.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(renamed, "Lamp Street US (2010) - S01E02 - Two.mkv"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(renamed, "Season 02", "Lamp Street US (2010) - S02E01 - Three.mkv"))
	e.mustScan()
	var newer []uid.ID
	for _, ep := range e.list(item.Episode) {
		if f, _ := e.files.FilesForItem(ep.ID); len(f) == 1 && filepath.Ext(f[0].Path) == ".mkv" {
			newer = append(newer, ep.ID)
			e.play(ep.ID, 7000, 300)
		}
	}
	must(t, os.RemoveAll(filepath.Join(e.root, "Lamp Street")))
	e.mustScan()
	after := e.state()
	shows := e.list(item.Show)
	if len(shows) != 1 || shows[0].ID != show.ID || shows[0].Path != renamed || len(newer) != 2 || len(e.list(item.Season)) != 2 ||
		e.plays() != fmt.Sprintf("%s:7000,%s:7000", newer[0], newer[1]) {
		t.Fatalf("shows %+v, seasons %+v, newer %v, plays %q, state %q", shows, e.list(item.Season), newer, e.plays(), after)
	}
	e.mustScan()
	if e.state() != after {
		t.Fatalf("again: %q was %q", e.state(), after)
	}
}

// Two shows of one title whose folders are both renamed each keep their
// own episodes: what tells them apart is their files, not their names.
func TestRenamedShowsOfOneTitleKeepTheirOwnEpisodes(t *testing.T) {
	e := newEnv(t, library.TV, "")
	us, uk := filepath.Join(e.root, "The Office (US)"), filepath.Join(e.root, "The Office (UK)")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(us, "The Office - S01E01.mp4"))
	e.mustScan() // the US show is the older, which a guess by title would move first
	fixtureCopy(t, "chapters.mkv", filepath.Join(uk, "The Office - S01E01.mkv"))
	e.mustScan()
	before := e.state()
	usShow, err := e.items.ByPath(e.lib.ID, item.Show, us)
	must(t, err)
	ukShow, err := e.items.ByPath(e.lib.ID, item.Show, uk)
	must(t, err)

	newUK, newUS := filepath.Join(e.root, "The Office (2001) {tvdb-78107}"), filepath.Join(e.root, "The Office US (2005) {tvdb-73244}")
	rename(t, filepath.Join(uk, "The Office - S01E01.mkv"), filepath.Join(newUK, "The Office (2001) - S01E01 - Downsize.mkv"))
	rename(t, filepath.Join(us, "The Office - S01E01.mp4"), filepath.Join(newUS, "The Office US (2005) - S01E01 - Pilot.mp4"))
	must(t, os.RemoveAll(us))
	must(t, os.RemoveAll(uk))
	e.mustScan()
	gotUS, err := e.items.Get(usShow.ID)
	must(t, err)
	gotUK, err := e.items.Get(ukShow.ID)
	must(t, err)
	if gotUS.Path != newUS || gotUK.Path != newUK || e.state() != before {
		t.Fatalf("US at %q, UK at %q, state %q was %q", gotUS.Path, gotUK.Path, e.state(), before)
	}
}

// A song whose album folder and file are renamed, as DroppedNeedle
// renames them, is the same song under the same album and artist, with
// its plays and playlist entries.
func TestRenamedSongKeepsItsRecords(t *testing.T) {
	e, _ := musicEnv(t)
	e.mustScan()
	before := e.state()
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	song, err := e.items.ByPath(e.lib.ID, item.Song, filepath.Join(album, "01 Opening.flac"))
	must(t, err)
	e.play(song.ID, 5000, 100)

	renamed := filepath.Join(e.root, "The Example Band", "First Light (2024) [mb-release-"+fakeRelease+"]")
	rename(t, album, renamed)
	rename(t, filepath.Join(renamed, "01 Opening.flac"), filepath.Join(renamed, "1-01 - Opening.flac"))
	e.mustScan()
	got, err := e.items.ByPath(e.lib.ID, item.Song, filepath.Join(renamed, "1-01 - Opening.flac"))
	must(t, err)
	if got.ID != song.ID || e.state() != before || e.plays() != fmt.Sprintf("%s:5000", song.ID) {
		t.Fatalf("song %s was %s, state %q was %q, plays %q", got.ID, song.ID, e.state(), before, e.plays())
	}
}

// An album and an artist named by their folders, their files having no
// tags, are the same album and artist when the folders are renamed.
func TestRenamedUntaggedAlbumAndArtistKeepTheirRecords(t *testing.T) {
	e, _ := musicEnv(t)
	e.mustScan()
	before := e.state()
	rename(t, filepath.Join(e.root, "Folder Artist", "Folder Album (2019)"), filepath.Join(e.root, "The Folder Artist", "Folder Album Deluxe (2020)"))
	must(t, os.RemoveAll(filepath.Join(e.root, "Folder Artist")))
	e.mustScan()
	names := append(titles(e.list(item.Artist)), titles(e.list(item.Album))...)
	if e.state() != before || !slices.Contains(names, "The Folder Artist") || !slices.Contains(names, "Folder Album Deluxe") || slices.Contains(names, "Folder Artist") {
		t.Fatalf("state %q was %q, names %q", e.state(), before, names)
	}
}

// Songs an earlier scan made again at their new paths are folded into
// the songs they are.
func TestEarlierDuplicateOfARenamedSongIsMerged(t *testing.T) {
	e, _ := musicEnv(t)
	e.mustScan()
	before := e.state()
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	song, err := e.items.ByPath(e.lib.ID, item.Song, filepath.Join(album, "01 Opening.flac"))
	must(t, err)
	e.play(song.ID, 5000, 100)

	renamed := filepath.Join(e.root, "The Example Band", "First Light (2024) [mb-release-"+fakeRelease+"]")
	for _, name := range []string{"01 Opening.flac", "02 Second Wind.mp3", "03 Last Call.m4a"} {
		clone(t, filepath.Join(album, name), filepath.Join(renamed, name))
	}
	e.mustScan()
	if n := len(e.list(item.Song)); n != 7 {
		t.Fatalf("%d songs with the copies", n)
	}
	dup, err := e.items.ByPath(e.lib.ID, item.Song, filepath.Join(renamed, "01 Opening.flac"))
	must(t, err)
	e.play(dup.ID, 9000, 200)
	must(t, os.RemoveAll(album))
	e.mustScan()
	got, err := e.items.ByPath(e.lib.ID, item.Song, filepath.Join(renamed, "01 Opening.flac"))
	must(t, err)
	if got.ID != song.ID || e.state() != before || e.plays() != fmt.Sprintf("%s:9000", song.ID) {
		t.Fatalf("song %s was %s, state %q was %q, plays %q", got.ID, song.ID, e.state(), before, e.plays())
	}
}

// A channel folder renamed with a video file renamed inside it are the
// same channel and video.
func TestRenamedChannelAndVideoKeepTheirRecords(t *testing.T) {
	e := youtubeEnv(t)
	e.mustScan()
	before := e.state()
	old := filepath.Join(e.root, "Clockwork Corner")
	ch, err := e.items.ByPath(e.lib.ID, item.Channel, old)
	must(t, err)
	v, err := e.items.ByPath(e.lib.ID, item.Video, filepath.Join(old, "Gear Trains.mp4"))
	must(t, err)

	renamed := filepath.Join(e.root, "The Clockwork Corner")
	rename(t, old, renamed)
	rename(t, filepath.Join(renamed, "Gear Trains.mp4"), filepath.Join(renamed, "s2021e010100 - Gear Trains.mp4"))
	e.mustScan()
	gotCh, err := e.items.ByPath(e.lib.ID, item.Channel, renamed)
	must(t, err)
	gotV, err := e.items.ByPath(e.lib.ID, item.Video, filepath.Join(renamed, "s2021e010100 - Gear Trains.mp4"))
	must(t, err)
	if gotCh.ID != ch.ID || gotV.ID != v.ID || *gotV.ParentID != ch.ID || e.state() != before {
		t.Fatalf("channel %s was %s, video %s was %s, state %q was %q", gotCh.ID, ch.ID, gotV.ID, v.ID, e.state(), before)
	}
}

// A folder renamed only in case on a volume that ignores case, as macOS
// volumes do by default, is the folder it was: its titles, files and
// what was played keep their records.
func TestFolderRenamedInCaseKeepsItsRecords(t *testing.T) {
	for _, c := range []struct{ kind, fixture, old, renamed, file string }{
		{library.Movies, "chapters.mkv", "night harbor (1987)", "Night Harbor (1987)", "night harbor (1987).mkv"},
		{library.TV, "h264-high41.mp4", "lamp street", "Lamp Street", "Season 01/Lamp Street - S01E01.mp4"},
		{library.Music, "sample.flac", "the artist", "The Artist", "Album/01 Song.flac"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			e := newEnv(t, c.kind, "")
			old := filepath.Join(e.root, c.old, c.file)
			fixtureCopy(t, c.fixture, old)
			e.mustScan()
			before := e.state()
			was, err := e.files.FileByPath(old)
			must(t, err)
			e.play(*was.ItemID, 5000, 100)

			rename(t, filepath.Join(e.root, c.old), filepath.Join(e.root, c.renamed))
			if _, err := os.Lstat(old); err != nil {
				t.Skip("the volume tells case apart")
			}
			e.mustScan()
			f, err := e.files.FileByPath(filepath.Join(e.root, c.renamed, c.file))
			if err != nil || f.ID != was.ID || e.state() != before || e.plays() != fmt.Sprintf("%s:5000", *was.ItemID) {
				t.Fatalf("file %+v %v was %+v, state %q was %q, plays %q", f, err, was, e.state(), before, e.plays())
			}
		})
	}
}

// Two shows of one title whose folders are renamed only in case, on a
// volume that ignores case, each keep their own episodes by their files.
func TestShowsOfOneTitleRenamedInCaseKeepTheirRecords(t *testing.T) {
	e := newEnv(t, library.TV, "")
	us, uk := filepath.Join(e.root, "the office (us)"), filepath.Join(e.root, "the office (uk)")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(us, "The Office - S01E01.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(uk, "The Office - S01E01.mkv"))
	e.mustScan()
	before := e.state()

	rename(t, us, filepath.Join(e.root, "The Office (US)"))
	rename(t, uk, filepath.Join(e.root, "The Office (UK)"))
	if _, err := os.Lstat(us); err != nil {
		t.Skip("the volume tells case apart")
	}
	e.mustScan()
	if e.state() != before {
		t.Fatalf("state %q was %q", e.state(), before)
	}
}

// A file moved by a tool that writes it again has another size and time,
// and is the same file by its media: the movie and the song keep their
// records and what was played.
func TestRetaggedAndRenamedFilesKeepTheirRecords(t *testing.T) {
	for _, c := range []struct{ kind, fixture, old, moved string }{
		{library.Movies, "h264-high41.mp4", "Lantern (2001)/Lantern.mp4", "Lantern (2001) {tmdb-5}/Lantern (2001).mp4"},
		{library.Music, "sample.flac", "Artist/Album/01 Song.flac", "Artist/Album (2001)/01-01 - Song.flac"},
	} {
		e := newEnv(t, c.kind, "")
		old, moved := filepath.Join(e.root, c.old), filepath.Join(e.root, c.moved)
		fixtureCopy(t, c.fixture, old)
		e.mustScan()
		was, err := e.files.FileByPath(old)
		must(t, err)
		e.play(*was.ItemID, 5000, 100)

		retag(t, old, moved)
		e.mustScan()
		f, err := e.files.FileByPath(moved)
		if err != nil || f.ID != was.ID || *f.ItemID != *was.ItemID || f.SizeBytes == was.SizeBytes || e.missing(moved) ||
			e.plays() != fmt.Sprintf("%s:5000", *was.ItemID) {
			t.Fatalf("%s: file %+v %v was %+v, plays %q", c.kind, f, err, was, e.plays())
		}
		if _, err := e.files.FileByPath(old); err == nil {
			t.Fatalf("%s: the old path kept a record", c.kind)
		}
	}
}

// A file recorded before fingerprints were kept gets one at the next
// scan, and is not probed again for it.
func TestRecordedFilesAreFingerprinted(t *testing.T) {
	for _, c := range []struct{ kind, fixture, path string }{
		{library.Movies, "h264-high41.mp4", "Lantern (2001)/Lantern.mp4"},
		{library.Music, "sample.flac", "Artist/Album/01 Song.flac"},
	} {
		e := newEnv(t, c.kind, "")
		path := filepath.Join(e.root, c.path)
		fixtureCopy(t, c.fixture, path)
		e.mustScan()
		e.mustScan()
		_, err := e.d.Exec(`UPDATE media_file SET fingerprint = NULL`)
		must(t, err)
		probes := *e.probes
		e.mustScan()
		if f, err := e.files.FileByPath(path); err != nil || !probe.Fingerprinted(f.Fingerprint) || *e.probes != probes {
			t.Fatalf("%s: file %+v %v, %d probes", c.kind, f, err, *e.probes-probes)
		}
	}
}

// A copy whose original is still on disk is a second file, and stays one
// when the original is deleted: titles that hold the same media are each
// their own.
func TestCopyOfAFileIsNotItMoved(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	original, copied := filepath.Join(e.root, "Lantern (2001)", "Lantern.mp4"), filepath.Join(e.root, "Lamplight (2003)", "Lamplight.mp4")
	fixtureCopy(t, "h264-high41.mp4", original)
	e.mustScan()
	first := e.onlyMovie()
	fixtureCopy(t, "h264-high41.mp4", copied)
	later := e.clock.Add(-48 * 3600e9)
	must(t, os.Chtimes(copied, later, later))
	e.mustScan()
	ms := e.list(item.Movie)
	if got, err := e.items.Get(first.ID); err != nil || len(ms) != 2 || got.ParsedTitle != "Lantern" || len(e.reviews("")) != 0 {
		t.Fatalf("a copy beside its original: %+v %v, reviews %+v", ms, err, e.reviews(""))
	}
	second, err := e.files.FileByPath(copied)
	must(t, err)
	e.play(*second.ItemID, 5000, 100)

	must(t, os.Remove(original))
	e.mustScan()
	e.mustScan()
	f, err := e.files.FileByPath(copied)
	if err != nil || f.ID != second.ID || *f.ItemID != *second.ItemID || !e.missing(original) || e.plays() != fmt.Sprintf("%s:5000", *second.ItemID) {
		t.Fatalf("the copy once its original is gone: %+v %v was %+v, plays %q", f, err, second, e.plays())
	}
}

// Two records of identical files that are both gone, and one such file
// newly found, could be either: nothing is merged and both are listed
// for review until the question is gone.
func TestAmbiguousMoveIsLeftForReview(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	a, b := filepath.Join(e.root, "Lantern (2001)", "Lantern.mp4"), filepath.Join(e.root, "Lamplight (2003)", "Lamplight.mp4")
	fixtureCopy(t, "h264-high41.mp4", a)
	clone(t, a, b)
	e.mustScan()
	before := e.list(item.Movie)
	c := filepath.Join(e.root, "Glow (2005)", "Glow.mp4")
	rename(t, a, c)
	must(t, os.Remove(b))
	e.mustScan()

	rs := e.reviews(review.AmbiguousMove)
	var subjects []uid.ID
	for _, r := range rs {
		subjects = append(subjects, *r.ItemID)
		if len(r.Evidence) != 2 || r.Evidence[0].Role != review.Stated || r.Evidence[1].Role != review.Candidate || r.Evidence[1].Path != c {
			t.Errorf("evidence %+v", r.Evidence)
		}
	}
	slices.SortFunc(subjects, uid.ID.Compare)
	if len(e.list(item.Movie)) != 3 || !e.missing(a) || !e.missing(b) ||
		!slices.Equal(subjects, slices.SortedFunc(slices.Values([]uid.ID{before[0].ID, before[1].ID}), uid.ID.Compare)) {
		t.Fatalf("movies %+v, reviews of %v", e.list(item.Movie), subjects)
	}
	fixtureCopy(t, "h264-high41.mp4", b)
	e.mustScan()
	if rs := e.reviews(review.AmbiguousMove); len(rs) != 0 {
		t.Fatalf("still for review once one is back: %+v", rs)
	}
}

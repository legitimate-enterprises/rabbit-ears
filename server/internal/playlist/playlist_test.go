package playlist_test

import (
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
)

type fixture struct {
	r     *playlist.Repo
	items *item.Repo
	adult int64
	lib   int64
	songs []int64
	d     *sql.DB
}

// songsAlbumArtist is the artist of the fixture's album.
func (f fixture) songsAlbumArtist(t *testing.T) *int64 {
	t.Helper()
	song, err := f.items.Get(f.songs[0])
	if err != nil {
		t.Fatal(err)
	}
	album, err := f.items.Get(*song.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	return album.ParentID
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.OpenPreferLocal(t)
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A"}
	if err := account.New(d).Create(a); err != nil {
		t.Fatal(err)
	}
	l := &library.Library{Name: "Music", Kind: library.Music}
	if err := library.New(d).Create(l); err != nil {
		t.Fatal(err)
	}
	f := fixture{r: playlist.New(d), items: item.New(d), adult: a.ID, lib: l.ID, d: d}
	artist := &item.Item{LibraryID: l.ID, Kind: item.Artist, ParsedTitle: "Artist"}
	if err := f.items.Create(artist); err != nil {
		t.Fatal(err)
	}
	album := &item.Item{LibraryID: l.ID, ParentID: &artist.ID, Kind: item.Album, ParsedTitle: "Album"}
	if err := f.items.Create(album); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"One", "Two", "Three", "Four"} {
		s := &item.Item{LibraryID: l.ID, ParentID: &album.ID, Kind: item.Song, ParsedTitle: title}
		if err := f.items.Create(s); err != nil {
			t.Fatal(err)
		}
		f.songs = append(f.songs, s.ID)
	}
	return f
}

func TestPlaylistCRUD(t *testing.T) {
	f := setup(t)
	road := &playlist.Playlist{AccountID: f.adult, Name: "Road trip", Kind: playlist.Music}
	movies := &playlist.Playlist{AccountID: f.adult, Name: "movie night", Kind: playlist.Movies}
	for _, p := range []*playlist.Playlist{road, movies} {
		if err := f.r.Create(p); err != nil || p.ID == 0 {
			t.Fatal(err)
		}
	}
	if err := f.r.Create(&playlist.Playlist{AccountID: f.adult, Name: "x", Kind: "podcast"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	all, err := f.r.List(f.adult, "", 10, 0)
	if err != nil || len(all) != 2 || all[0].ID != movies.ID {
		t.Fatalf("by name ignoring case: %v %v", all, err)
	}
	if music, _ := f.r.List(f.adult, playlist.Music, 10, 0); len(music) != 1 || music[0].ID != road.ID {
		t.Fatalf("music only: %v", music)
	}
	road.Name = "Sunday drive"
	if err := f.r.Update(road); err != nil {
		t.Fatal(err)
	}
	played := time.Now().UTC().Truncate(time.Millisecond)
	if err := f.r.MarkPlayed(road.ID, played); err != nil {
		t.Fatal(err)
	}
	got, err := f.r.Get(road.ID)
	if err != nil || got.Name != "Sunday drive" || got.LastPlayedAt == nil || !got.LastPlayedAt.Equal(played) {
		t.Fatalf("%+v %v", got, err)
	}
	if err := f.r.Delete(movies.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Get(movies.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func order(t *testing.T, f fixture, playlistID int64) []int64 {
	t.Helper()
	es, err := f.r.Entries(playlistID, item.Audience{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, e := range es {
		if e.Position != i {
			t.Fatalf("position %d at %d", e.Position, i)
		}
		ids = append(ids, e.ItemID)
	}
	return ids
}

func TestEntriesOrder(t *testing.T) {
	f := setup(t)
	p := &playlist.Playlist{AccountID: f.adult, Name: "P", Kind: playlist.Music}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	one, two, three, four := f.songs[0], f.songs[1], f.songs[2], f.songs[3]
	add := func(itemID int64, at *int) *playlist.Entry {
		t.Helper()
		e := &playlist.Entry{PlaylistID: p.ID, ItemID: itemID}
		if err := f.r.AddEntry(e, at); err != nil {
			t.Fatal(err)
		}
		return e
	}
	zero, far := 0, 99
	e1 := add(one, nil)
	add(two, nil)
	e3 := add(three, &zero)
	add(four, &far)
	add(one, nil)
	if got := order(t, f, p.ID); !slices.Equal(got, []int64{three, one, two, four, one}) {
		t.Fatalf("after adds %v", got)
	}
	if n, _ := f.r.CountEntries(p.ID); n != 5 {
		t.Fatalf("count %d", n)
	}
	two2 := 2
	if err := f.r.MoveEntry(e3.ID, &two2); err != nil {
		t.Fatal(err)
	}
	if got := order(t, f, p.ID); !slices.Equal(got, []int64{one, two, three, four, one}) {
		t.Fatalf("after move down %v", got)
	}
	if err := f.r.MoveEntry(e3.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := order(t, f, p.ID); !slices.Equal(got, []int64{one, two, four, one, three}) {
		t.Fatalf("after move to end %v", got)
	}
	if err := f.r.MoveEntry(e3.ID, &zero); err != nil {
		t.Fatal(err)
	}
	if err := f.r.RemoveEntry(e1.ID); err != nil {
		t.Fatal(err)
	}
	if got := order(t, f, p.ID); !slices.Equal(got, []int64{three, two, four, one}) {
		t.Fatalf("after move up and remove %v", got)
	}
	if e, err := f.r.Entry(e3.ID); err != nil || e.Position != 0 || e.ItemID != three {
		t.Fatalf("%+v %v", e, err)
	}
	// A deleted item drops out of every playlist; the next write closes the gap.
	if err := f.items.Delete(two); err != nil {
		t.Fatal(err)
	}
	if err := f.r.AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: two + 100}, nil); !errors.Is(err, db.ErrReference) {
		t.Fatalf("missing item: %v", err)
	}
	add(one, &zero)
	if got := order(t, f, p.ID); !slices.Equal(got, []int64{one, three, four, one}) {
		t.Fatalf("after cascade %v", got)
	}
}

// Totals counts the play order, adds up its first files' durations and names
// the first entry's item, whose art stands for the playlist.
func TestTotals(t *testing.T) {
	f := setup(t)
	p := &playlist.Playlist{AccountID: f.adult, Name: "P", Kind: playlist.Music}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	if n, ms, first, err := f.r.Totals(p.ID, item.Audience{}); err != nil || n != 0 || ms != 0 || first != nil {
		t.Fatalf("empty: %d %d %v %v", n, ms, first, err)
	}
	files := mediafile.New(f.d)
	for i, id := range f.songs[:2] {
		ms := int64(1000 * (i + 1))
		if err := files.CreateFile(&mediafile.File{ItemID: id, Path: "/s/" + strconv.Itoa(i), ModifiedAt: time.Now(), DurationMs: &ms}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{f.songs[1], f.songs[0], f.songs[1], f.songs[2]} {
		if err := f.r.AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: id}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n, ms, first, err := f.r.Totals(p.ID, item.Audience{}); err != nil || n != 4 || ms != 5000 || first == nil || *first != f.songs[1] {
		t.Fatalf("%d %d %v %v", n, ms, first, err)
	}
}

// A container entry stands for its playable children in library order, read
// at the time of asking; single items stand for themselves; nothing is
// deduplicated.
func TestPlayOrder(t *testing.T) {
	f := setup(t)
	n := func(v int) *int { return &v }
	mk := func(it *item.Item) int64 {
		t.Helper()
		if err := f.items.Create(it); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	files := mediafile.New(f.d)
	timed := func(id, ms int64) {
		t.Helper()
		if err := files.CreateFile(&mediafile.File{ItemID: id, Path: "/f/" + strconv.FormatInt(id, 10), ModifiedAt: time.Now(), DurationMs: &ms}); err != nil {
			t.Fatal(err)
		}
	}
	tv := &library.Library{Name: "TV", Kind: library.TV}
	if err := library.New(f.d).Create(tv); err != nil {
		t.Fatal(err)
	}
	show := mk(&item.Item{LibraryID: tv.ID, Kind: item.Show, ParsedTitle: "Show"})
	s2 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(2)})
	s0 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(0)})
	s1 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(1)})
	s2e1 := mk(&item.Item{LibraryID: tv.ID, ParentID: &s2, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	special := mk(&item.Item{LibraryID: tv.ID, ParentID: &s0, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	s1e2 := mk(&item.Item{LibraryID: tv.ID, ParentID: &s1, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(2)})
	s1e1 := mk(&item.Item{LibraryID: tv.ID, ParentID: &s1, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	timed(s1e1, 1000)
	timed(s1e2, 2000)

	tvp := &playlist.Playlist{AccountID: f.adult, Name: "TV", Kind: playlist.TV}
	if err := f.r.Create(tvp); err != nil {
		t.Fatal(err)
	}
	add := func(p *playlist.Playlist, itemID int64) int64 {
		t.Helper()
		e := &playlist.Entry{PlaylistID: p.ID, ItemID: itemID}
		if err := f.r.AddEntry(e, nil); err != nil {
			t.Fatal(err)
		}
		return e.ID
	}
	eShow, eSeason, eEp := add(tvp, show), add(tvp, s1), add(tvp, s2e1)
	rows := func(p *playlist.Playlist, limit, offset int) (entries, items []int64) {
		t.Helper()
		got, err := f.r.PlayOrder(p.ID, item.Audience{}, limit, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			entries, items = append(entries, r.EntryID), append(items, r.ItemID)
		}
		return entries, items
	}
	entries, items := rows(tvp, 100, 0)
	if !slices.Equal(items, []int64{s1e1, s1e2, s2e1, special, s1e1, s1e2, s2e1}) ||
		!slices.Equal(entries, []int64{eShow, eShow, eShow, eShow, eSeason, eSeason, eEp}) {
		t.Fatalf("tv: entries %v items %v", entries, items)
	}
	if _, page := rows(tvp, 2, 3); !slices.Equal(page, []int64{special, s1e1}) {
		t.Fatalf("paged %v", page)
	}
	if n, ms, first, err := f.r.Totals(tvp.ID, item.Audience{}); err != nil || n != 7 || ms != 6000 || first == nil || *first != show {
		t.Fatalf("totals %d %d %v %v", n, ms, first, err)
	}
	all, err := f.r.EntriesTotals(tvp.ID, item.Audience{})
	if err != nil || len(all) != 3 {
		t.Fatalf("every entry's totals %v %v", all, err)
	}
	for id, want := range map[int64][2]int64{eShow: {4, 3000}, eSeason: {2, 3000}, eEp: {1, 0}} {
		if n, ms, err := f.r.EntryTotals(id, item.Audience{}); err != nil || int64(n) != want[0] || ms != want[1] {
			t.Fatalf("entry %d: %d %d %v", id, n, ms, err)
		}
		if got := all[id]; int64(got.Count) != want[0] || got.DurationMs != want[1] {
			t.Fatalf("entry %d among all: %+v", id, got)
		}
	}
	// A new episode joins the show's and the season's entries with no edit.
	late := mk(&item.Item{LibraryID: tv.ID, ParentID: &s1, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(3)})
	if _, items := rows(tvp, 100, 0); !slices.Equal(items, []int64{s1e1, s1e2, late, s2e1, special, s1e1, s1e2, late, s2e1}) {
		t.Fatalf("after a new episode %v", items)
	}

	// Music: the fixture's album (no year, songs One to Four by creation)
	// plus a newer album by the same artist, a guest song on another
	// artist's album, and track numbers that reorder the newer album.
	artist := *f.songsAlbumArtist(t)
	newer := mk(&item.Item{LibraryID: f.lib, ParentID: &artist, Kind: item.Album, ParsedTitle: "Later", ParsedYear: n(2020)})
	older := mk(&item.Item{LibraryID: f.lib, ParentID: &artist, Kind: item.Album, ParsedTitle: "Earlier", ParsedYear: n(2010)})
	b := mk(&item.Item{LibraryID: f.lib, ParentID: &newer, Kind: item.Song, ParsedTitle: "B", DiscNumber: n(1), TrackNumber: n(2)})
	a := mk(&item.Item{LibraryID: f.lib, ParentID: &newer, Kind: item.Song, ParsedTitle: "A", DiscNumber: n(1), TrackNumber: n(1)})
	c := mk(&item.Item{LibraryID: f.lib, ParentID: &newer, Kind: item.Song, ParsedTitle: "C", DiscNumber: n(2), TrackNumber: n(1)})
	old := mk(&item.Item{LibraryID: f.lib, ParentID: &older, Kind: item.Song, ParsedTitle: "Old", TrackNumber: n(1)})
	other := mk(&item.Item{LibraryID: f.lib, Kind: item.Artist, ParsedTitle: "Other"})
	compilation := mk(&item.Item{LibraryID: f.lib, ParentID: &other, Kind: item.Album, ParsedTitle: "Compilation", ParsedYear: n(2015)})
	guest := mk(&item.Item{LibraryID: f.lib, ParentID: &compilation, Kind: item.Song, ParsedTitle: "Guest", ArtistID: &artist})
	mk(&item.Item{LibraryID: f.lib, ParentID: &compilation, Kind: item.Song, ParsedTitle: "Not theirs"})

	mp := &playlist.Playlist{AccountID: f.adult, Name: "Music", Kind: playlist.Music}
	if err := f.r.Create(mp); err != nil {
		t.Fatal(err)
	}
	add(mp, artist)
	add(mp, newer)
	add(mp, f.songs[3])
	_, items = rows(mp, 100, 0)
	one, two, three, four := f.songs[0], f.songs[1], f.songs[2], f.songs[3]
	if want := []int64{a, b, c, guest, old, four, one, three, two, a, b, c, four}; !slices.Equal(items, want) {
		t.Fatalf("music %v, want %v", items, want)
	}
}

// The play order and its counts keep only what the audience may see: a
// container never expands to an item the reader could not open itself.
func TestPlayOrderKeepsToTheAudience(t *testing.T) {
	f := setup(t)
	n := func(v int) *int { return &v }
	mk := func(it *item.Item) int64 {
		t.Helper()
		if err := f.items.Create(it); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	tv := &library.Library{Name: "TV", Kind: library.TV}
	if err := library.New(f.d).Create(tv); err != nil {
		t.Fatal(err)
	}
	show := mk(&item.Item{LibraryID: tv.ID, Kind: item.Show, ParsedTitle: "Show"})
	s1 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(1)})
	s2 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(2)})
	allowed := mk(&item.Item{LibraryID: tv.ID, ParentID: &s1, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	mk(&item.Item{LibraryID: tv.ID, ParentID: &s2, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	home := &household.Household{Name: "Home", AdultID: f.adult}
	if err := household.New(f.d).Create(home); err != nil {
		t.Fatal(err)
	}
	kid := &account.Account{Kind: account.Kid, Name: "Kid", HouseholdID: &home.ID}
	if err := account.New(f.d).Create(kid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Exec(`INSERT INTO account_allow (account_id, item_id) VALUES (?, ?)`, kid.ID, s1); err != nil {
		t.Fatal(err)
	}
	p := &playlist.Playlist{AccountID: f.adult, Name: "TV", Kind: playlist.TV}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	e := &playlist.Entry{PlaylistID: p.ID, ItemID: show}
	if err := f.r.AddEntry(e, nil); err != nil {
		t.Fatal(err)
	}
	// The songs are in a music library, which kids never see.
	if err := f.r.AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: f.songs[0]}, nil); err != nil {
		t.Fatal(err)
	}
	a := item.Audience{Kid: true, AccountID: kid.ID, AllowList: true}
	rows, err := f.r.PlayOrder(p.ID, a, 100, 0)
	if err != nil || len(rows) != 1 || rows[0].ItemID != allowed || rows[0].EntryID != e.ID {
		t.Fatalf("kid play order %+v %v", rows, err)
	}
	if n, _, _, err := f.r.Totals(p.ID, a); err != nil || n != 1 {
		t.Fatalf("kid totals %d %v", n, err)
	}
	if n, _, err := f.r.EntryTotals(e.ID, a); err != nil || n != 1 {
		t.Fatalf("kid entry totals %d %v", n, err)
	}
	if all, err := f.r.EntriesTotals(p.ID, a); err != nil || len(all) != 1 || all[e.ID].Count != 1 {
		t.Fatalf("kid entries' totals %v %v", all, err)
	}
	if n, _, _, err := f.r.Totals(p.ID, item.Audience{}); err != nil || n != 3 {
		t.Fatalf("an adult sees all %d %v", n, err)
	}

	// A kid is allowed a playlist they may see all of; an adult hiding kids'
	// content one they may see any of; anyone an empty one.
	allows := func(p int64, a item.Audience, want bool) {
		t.Helper()
		if ok, err := f.r.Allows(p, a); err != nil || ok != want {
			t.Fatalf("allows %d %+v = %v %v, want %v", p, a, ok, err, want)
		}
	}
	allows(p.ID, a, false)
	allows(p.ID, item.Audience{}, true)
	allows(p.ID, item.Audience{HideKids: true}, true)
	if _, err := f.d.Exec(`UPDATE item SET kids_content = 1 WHERE id = ?`, show); err != nil {
		t.Fatal(err)
	}
	allows(p.ID, item.Audience{HideKids: true}, true)
	only := &playlist.Playlist{AccountID: f.adult, Name: "Only", Kind: playlist.TV}
	if err := f.r.Create(only); err != nil {
		t.Fatal(err)
	}
	allows(only.ID, a, true)
	if err := f.r.AddEntry(&playlist.Entry{PlaylistID: only.ID, ItemID: allowed}, nil); err != nil {
		t.Fatal(err)
	}
	allows(only.ID, a, true)
	allows(only.ID, item.Audience{HideKids: true}, false)
}

// A title's entry plays the edition it names, else the default; a
// collection plays each movie's default. Rows carry the edition and its
// runtime, and an entry whose edition lost its files is skipped.
func TestPlayOrderEditions(t *testing.T) {
	f := setup(t)
	films := &library.Library{Name: "Films", Kind: library.Movies}
	if err := library.New(f.d).Create(films); err != nil {
		t.Fatal(err)
	}
	m := &item.Item{LibraryID: films.ID, Kind: item.Movie, ParsedTitle: "Lantern"}
	if err := f.items.Create(m); err != nil {
		t.Fatal(err)
	}
	files, eds := mediafile.New(f.d), edition.New(f.d)
	file := func(name string, minutes int64, tag string) (int64, int64) {
		t.Helper()
		ms := minutes * 60000
		fl := &mediafile.File{Path: "/m/" + name, ModifiedAt: time.Now(), DurationMs: &ms}
		if err := files.CreateFile(fl); err != nil {
			t.Fatal(err)
		}
		id, err := eds.Place(fl.ID, m.ID, tag, &ms, true)
		if err != nil {
			t.Fatal(err)
		}
		return fl.ID, id
	}
	_, std := file("a.mkv", 100, "")
	extFile, ext := file("b.mkv", 130, "Extended")
	p := &playlist.Playlist{AccountID: f.adult, Name: "P", Kind: playlist.Movies}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	for _, e := range []*playlist.Entry{{PlaylistID: p.ID, ItemID: m.ID, EditionID: ext}, {PlaylistID: p.ID, ItemID: m.ID}} {
		if err := f.r.AddEntry(e, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.r.PlayOrder(p.ID, item.Audience{}, 100, 0)
	if err != nil || len(rows) != 2 || rows[0].EditionID != ext || rows[1].EditionID != std ||
		rows[0].RuntimeMs == nil || *rows[0].RuntimeMs != 130*60000 || *rows[1].RuntimeMs != 100*60000 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if n, ms, _, err := f.r.Totals(p.ID, item.Audience{}); err != nil || n != 2 || ms != 230*60000 {
		t.Fatalf("totals %d %d %v", n, ms, err)
	}
	if err := files.DeleteFile(extFile); err != nil {
		t.Fatal(err)
	}
	rows, err = f.r.PlayOrder(p.ID, item.Audience{}, 100, 0)
	if err != nil || len(rows) != 1 || rows[0].EditionID != std {
		t.Fatalf("after the edition's files went %+v %v", rows, err)
	}
}

// A title's default edition is the first with a file on disk, as for
// playback.
func TestPlayOrderDefaultSkipsMissingFiles(t *testing.T) {
	f := setup(t)
	films := &library.Library{Name: "Films", Kind: library.Movies}
	if err := library.New(f.d).Create(films); err != nil {
		t.Fatal(err)
	}
	m := &item.Item{LibraryID: films.ID, Kind: item.Movie, ParsedTitle: "Lantern"}
	if err := f.items.Create(m); err != nil {
		t.Fatal(err)
	}
	files, eds := mediafile.New(f.d), edition.New(f.d)
	var ed []int64
	for i, minutes := range []int64{100, 120} {
		ms := minutes * 60000
		fl := &mediafile.File{Path: "/m/" + strconv.Itoa(i), ModifiedAt: time.Now(), DurationMs: &ms}
		if err := files.CreateFile(fl); err != nil {
			t.Fatal(err)
		}
		id, err := eds.Place(fl.ID, m.ID, "", &ms, true)
		if err != nil {
			t.Fatal(err)
		}
		ed = append(ed, id)
		if at := time.Now(); i == 0 {
			if err := files.SetMissing(fl, &at); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := &playlist.Playlist{AccountID: f.adult, Name: "P", Kind: playlist.Movies}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := f.r.AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: m.ID}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := f.r.PlayOrder(p.ID, item.Audience{}, 100, 0)
	if err != nil || len(rows) != 1 || rows[0].EditionID != ed[1] {
		t.Fatalf("rows %+v %v, want edition %d", rows, err, ed[1])
	}
}

// Expand plays one target as an entry naming it would: a show's episodes
// in season order, specials last; a channel's videos oldest first; a title
// as itself; each with its library, to the audience alone.
func TestExpand(t *testing.T) {
	f := setup(t)
	n := func(v int) *int { return &v }
	mk := func(it *item.Item) int64 {
		t.Helper()
		if err := f.items.Create(it); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	tv := &library.Library{Name: "TV", Kind: library.TV}
	yt := &library.Library{Name: "YouTube", Kind: library.YouTube}
	for _, l := range []*library.Library{tv, yt} {
		if err := library.New(f.d).Create(l); err != nil {
			t.Fatal(err)
		}
	}
	show := mk(&item.Item{LibraryID: tv.ID, Kind: item.Show, ParsedTitle: "Show"})
	s0 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(0)})
	s1 := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Show", SeasonNumber: n(1)})
	special := mk(&item.Item{LibraryID: tv.ID, ParentID: &s0, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	e2 := mk(&item.Item{LibraryID: tv.ID, ParentID: &s1, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(2)})
	e1 := mk(&item.Item{LibraryID: tv.ID, ParentID: &s1, Kind: item.Episode, ParsedTitle: "Show", EpisodeNumber: n(1)})
	channel := mk(&item.Item{LibraryID: yt.ID, Kind: item.Channel, ParsedTitle: "Channel"})
	newer := mk(&item.Item{LibraryID: yt.ID, ParentID: &channel, Kind: item.Video, ParsedTitle: "Newer"})
	older := mk(&item.Item{LibraryID: yt.ID, ParentID: &channel, Kind: item.Video, ParsedTitle: "Older"})
	undated := mk(&item.Item{LibraryID: yt.ID, ParentID: &channel, Kind: item.Video, ParsedTitle: "Undated"})
	for id, at := range map[int64]int64{newer: 2000, older: 1000} {
		if _, err := f.d.Exec(`INSERT INTO item_metadata (item_id, provider, status, title, published_at) VALUES (?, 'youtube', 'matched', '', ?)`, id, at); err != nil {
			t.Fatal(err)
		}
	}
	files := mediafile.New(f.d)
	for _, id := range []int64{special, e1, e2, newer, older, undated, f.songs[0]} {
		if err := files.CreateFile(&mediafile.File{ItemID: id, Path: "/f/" + strconv.FormatInt(id, 10), ModifiedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	expand := func(itemID int64, a item.Audience) (ids []int64, libs []int64) {
		t.Helper()
		rows, err := f.r.Expand(itemID, 0, 0, a)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			ids, libs = append(ids, r.ItemID), append(libs, r.LibraryID)
		}
		return ids, libs
	}
	if got, libs := expand(show, item.Audience{}); !slices.Equal(got, []int64{e1, e2, special}) || libs[0] != tv.ID {
		t.Fatalf("show %v %v", got, libs)
	}
	if got, _ := expand(channel, item.Audience{}); !slices.Equal(got, []int64{older, newer, undated}) {
		t.Fatalf("channel %v", got)
	}
	if got, libs := expand(newer, item.Audience{}); !slices.Equal(got, []int64{newer}) || libs[0] != yt.ID {
		t.Fatalf("a video %v", got)
	}
	if got, _ := expand(channel, item.Audience{Kid: true}); len(got) != 0 {
		t.Fatalf("a channel not for kids gives a kid nothing: %v", got)
	}
	if got, _ := expand(f.songs[0], item.Audience{}); !slices.Equal(got, []int64{f.songs[0]}) {
		t.Fatalf("a song %v", got)
	}
}

// Ratings reads each row's rating where kids' filters read it: an
// episode's from its show, a movie's its own; an unrated row is "".
func TestRatings(t *testing.T) {
	f := setup(t)
	tv := &library.Library{Name: "TV", Kind: library.TV}
	if err := library.New(f.d).Create(tv); err != nil {
		t.Fatal(err)
	}
	mk := func(it *item.Item) int64 {
		t.Helper()
		if err := f.items.Create(it); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	one := 1
	show := mk(&item.Item{LibraryID: tv.ID, Kind: item.Show, ParsedTitle: "Harbor"})
	season := mk(&item.Item{LibraryID: tv.ID, ParentID: &show, Kind: item.Season, ParsedTitle: "Harbor", SeasonNumber: &one})
	mk(&item.Item{LibraryID: tv.ID, ParentID: &season, Kind: item.Episode, ParsedTitle: "Harbor", EpisodeNumber: &one})
	if err := f.items.SetMetadata(&item.Metadata{ItemID: show, Status: item.Matched, Title: "Harbor", ContentRating: "TV-14"}); err != nil {
		t.Fatal(err)
	}
	p := &playlist.Playlist{AccountID: f.adult, Name: "Mixed", Kind: playlist.TV}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	if got, err := f.r.Ratings(p.ID); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, id := range []int64{show, f.songs[0]} {
		if err := f.r.AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: id}, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.r.Ratings(p.ID)
	if slices.Sort(got); err != nil || !slices.Equal(got, []string{"", "TV-14"}) {
		t.Fatalf("%q %v", got, err)
	}
}

func TestShares(t *testing.T) {
	f := setup(t)
	b := &account.Account{Kind: account.Adult, Email: "b@example.test", Name: "B"}
	if err := account.New(f.d).Create(b); err != nil {
		t.Fatal(err)
	}
	p := &playlist.Playlist{AccountID: f.adult, Name: "Zed", Kind: playlist.Music}
	own := &playlist.Playlist{AccountID: b.ID, Name: "Alpha", Kind: playlist.Music}
	for _, x := range []*playlist.Playlist{p, own} {
		if err := f.r.Create(x); err != nil {
			t.Fatal(err)
		}
	}
	sh := &playlist.Share{PlaylistID: p.ID, AccountID: b.ID, Access: playlist.Read}
	if err := f.r.CreateShare(sh); err != nil || sh.CreatedAt.IsZero() {
		t.Fatal(err, sh)
	}
	if err := f.r.CreateShare(&playlist.Share{PlaylistID: p.ID, AccountID: b.ID, Access: playlist.Write}); !errors.Is(err, db.ErrConflict) {
		t.Fatal(err)
	}
	got, err := f.r.Share(p.ID, b.ID)
	if err != nil || got.Access != playlist.Read || !got.CreatedAt.Equal(sh.CreatedAt) {
		t.Fatal(err, got)
	}
	sh.Access = playlist.Write
	if err := f.r.UpdateShare(sh); err != nil {
		t.Fatal(err)
	}
	if all, err := f.r.Shares(p.ID, 10, 0); err != nil || len(all) != 1 || all[0].Access != playlist.Write {
		t.Fatal(err, all)
	}
	list, err := f.r.List(b.ID, "", 10, 0)
	if err != nil || len(list) != 2 || list[0].ID != own.ID || list[1].ID != p.ID {
		t.Fatalf("own and shared by name: %v %+v", err, list)
	}
	if list, _ := f.r.List(b.ID, playlist.Movies, 10, 0); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
	if list, _ := f.r.List(f.adult, "", 10, 0); len(list) != 1 {
		t.Fatalf("a share lists nothing more for its owner: %+v", list)
	}
	if err := f.r.DeleteShare(p.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Share(p.ID, b.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := f.r.DeleteShare(p.ID, b.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestHouseholdShares(t *testing.T) {
	f := setup(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	accounts := account.New(f.d)
	h := &household.Household{Name: "Home", AdultID: f.adult}
	must(household.New(f.d).Create(h))
	kid := &account.Account{Kind: account.Kid, Name: "Kid", HouseholdID: &h.ID}
	late := &account.Account{Kind: account.Adult, Email: "late@example.test", Name: "Late"}
	must(accounts.Create(kid))
	must(accounts.Create(late))
	p := &playlist.Playlist{AccountID: f.adult, Name: "Home", Kind: playlist.Music}
	must(f.r.Create(p))

	sh := &playlist.HouseholdShare{PlaylistID: p.ID, HouseholdID: h.ID, Access: playlist.Write}
	if err := f.r.CreateHouseholdShare(sh); err != nil || sh.CreatedAt.IsZero() {
		t.Fatal(err, sh)
	}
	if err := f.r.CreateHouseholdShare(&playlist.HouseholdShare{PlaylistID: p.ID, HouseholdID: h.ID, Access: playlist.Read}); !errors.Is(err, db.ErrConflict) {
		t.Fatal(err)
	}
	if got, err := f.r.HouseholdShare(p.ID, h.ID); err != nil || got.Access != playlist.Write || !got.CreatedAt.Equal(sh.CreatedAt) {
		t.Fatal(err, got)
	}
	if all, err := f.r.HouseholdShares(p.ID, 10, 0); err != nil || len(all) != 1 || all[0].HouseholdID != h.ID {
		t.Fatal(err, all)
	}
	access := func(id int64, want string) {
		t.Helper()
		if got, err := f.r.Access(p.ID, id); err != nil || got != want {
			t.Fatalf("access of %d: %q, want %q (%v)", id, got, want, err)
		}
	}
	listed := func(id int64, want int) {
		t.Helper()
		if list, err := f.r.List(id, "", 10, 0); err != nil || len(list) != want {
			t.Fatalf("listed for %d: %v %+v", id, err, list)
		}
	}
	access(kid.ID, playlist.Read)
	listed(kid.ID, 1)
	access(late.ID, "")
	listed(late.ID, 0)

	// Who joins the household later is reached as well.
	_, err := f.d.Exec(`INSERT INTO household_member (household_id, account_id, created_at) VALUES (?,?,0)`, h.ID, late.ID)
	must(err)
	access(late.ID, playlist.Write)
	listed(late.ID, 1)
	must(f.r.CreateShare(&playlist.Share{PlaylistID: p.ID, AccountID: late.ID, Access: playlist.Read}))
	access(late.ID, playlist.Write)
	listed(late.ID, 1)
	if who, err := f.r.Audience(p.ID); err != nil || !slices.Equal(who, []int64{f.adult, kid.ID, late.ID}) {
		t.Fatal(err, who)
	}

	sh.Access = playlist.Read
	must(f.r.UpdateHouseholdShare(sh))
	must(f.r.CreateShare(&playlist.Share{PlaylistID: p.ID, AccountID: kid.ID, Access: playlist.Write}))
	access(kid.ID, playlist.Read)
	must(f.r.DeleteHouseholdShare(p.ID, h.ID))
	access(late.ID, playlist.Read)
	if err := f.r.DeleteHouseholdShare(p.ID, h.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

// Expanding a playlist starts from its entries and reaches each one's items
// through them, never walking every item of a kind or the whole library.
func TestExpansionStartsFromTheEntries(t *testing.T) {
	f := setup(t)
	p := &playlist.Playlist{AccountID: f.adult, Name: "P", Kind: playlist.Music}
	if err := f.r.Create(p); err != nil {
		t.Fatal(err)
	}
	e := &playlist.Entry{PlaylistID: p.ID, ItemID: f.songs[0]}
	if err := f.r.AddEntry(e, nil); err != nil {
		t.Fatal(err)
	}
	pl := &dbtest.Planner{DBTX: f.d}
	r := playlist.New(pl)
	hide := item.Audience{HideKids: true}
	if _, _, _, err := r.Totals(p.ID, hide); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.EntryTotals(e.ID, hide); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PlayOrder(p.ID, hide, 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Entries(p.ID, hide, 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Allows(p.ID, hide); err != nil {
		t.Fatal(err)
	}
	for _, plan := range pl.Plans {
		for line := range strings.SplitSeq(plan, "\n") {
			if strings.Contains(line, "item_kind_added") || line == "SCAN item" {
				t.Errorf("%s in:\n%s", line, plan)
			}
		}
	}
}

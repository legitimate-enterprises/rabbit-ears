package item_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

func TestMusicAndYouTubeItems(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	lr := library.New(d)
	music := &library.Library{Name: "Music", Kind: library.Music}
	yt := &library.Library{Name: "YouTube", Kind: library.YouTube}
	for _, l := range []*library.Library{music, yt} {
		if err := lr.Create(l); err != nil {
			t.Fatal(err)
		}
	}
	r := item.New(d)
	band := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "The Band", SortTitle: "Band"})
	guest := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "Guest"})
	lp := mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &band.ID, Kind: item.Album, ParsedTitle: "LP", ParsedYear: intp(2007)})
	song := mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &lp.ID, Kind: item.Song, Path: "/music/LP/01.flac",
		ParsedTitle: "One", DiscNumber: intp(1), TrackNumber: intp(4), ArtistID: &guest.ID})

	got, err := r.Get(song.ID)
	if err != nil || *got.DiscNumber != 1 || *got.TrackNumber != 4 || *got.ArtistID != guest.ID || got.Path != "/music/LP/01.flac" {
		t.Fatalf("%+v %v", got, err)
	}
	if a, _ := r.Get(band.ID); a.SortTitle != "Band" {
		t.Fatalf("sort title %q", a.SortTitle)
	}
	got.TrackNumber, got.ArtistID, got.SortTitle = intp(5), nil, "One"
	if err := r.Update(got); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(song.ID); *g.TrackNumber != 5 || g.ArtistID != nil || g.SortTitle != "One" {
		t.Fatalf("%+v", g)
	}
	if err := r.Create(&item.Item{LibraryID: music.ID, Kind: item.Song, ParsedTitle: "orphan"}); err == nil {
		t.Fatal("a song needs an album")
	}

	ch := mustCreate(t, r, &item.Item{LibraryID: yt.ID, Kind: item.Channel, ParsedTitle: "Channel"})
	v := mustCreate(t, r, &item.Item{LibraryID: yt.ID, ParentID: &ch.ID, Kind: item.Video, ParsedTitle: "Video"})
	now := time.Now().UTC()
	if err := r.SetMetadata(&item.Metadata{ItemID: v.ID, Provider: "youtube", ExternalID: "dQw4w9WgXcQ", Status: item.Matched,
		ReleaseDate: "2026-11-05", FetchedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if m, err := r.Metadata(v.ID); err != nil || m.ExternalID != "dQw4w9WgXcQ" || m.Provider != "youtube" || m.ProviderID != nil {
		t.Fatalf("%+v %v", m, err)
	}
	if ms, _ := r.MetadataFor([]int64{v.ID}); ms[v.ID].ExternalID != "dQw4w9WgXcQ" {
		t.Fatalf("%+v", ms[v.ID])
	}

	// Deleting a performing artist keeps the song; deleting a channel takes its videos.
	if err := r.Delete(guest.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(v.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if g, err := r.Get(song.ID); err != nil || g.ArtistID != nil {
		t.Fatalf("%+v %v", g, err)
	}

	counts, err := r.KindCounts(music.ID)
	if err != nil || counts[item.Artist] != 1 || counts[item.Album] != 1 || counts[item.Song] != 1 {
		t.Fatalf("%v %v", counts, err)
	}
}

// The item listing is the video catalog unless a kind is asked for, and the
// 4K filter keeps titles with a file at least 3200 wide or 2000 tall.
func TestCatalogScopeAndUHD(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	tv, mv, music := &library.Library{Name: "tv", Kind: library.TV}, &library.Library{Name: "mv", Kind: library.Movies},
		&library.Library{Name: "m", Kind: library.Music}
	for _, l := range []*library.Library{tv, mv, music} {
		if err := library.New(d).Create(l); err != nil {
			t.Fatal(err)
		}
	}
	r := item.New(d)
	mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "Artist"})
	uhd := mustCreate(t, r, &item.Item{LibraryID: mv.ID, Kind: item.Movie, ParsedTitle: "Scope", Path: "/m/a"})
	hd := mustCreate(t, r, &item.Item{LibraryID: mv.ID, Kind: item.Movie, ParsedTitle: "HD", Path: "/m/b"})
	show := mustCreate(t, r, &item.Item{LibraryID: tv.ID, Kind: item.Show, ParsedTitle: "Show", Path: "/tv/s"})
	season := mustCreate(t, r, &item.Item{LibraryID: tv.ID, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "S1", SeasonNumber: intp(1)})
	ep := mustCreate(t, r, &item.Item{LibraryID: tv.ID, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: "E1", EpisodeNumber: intp(1)})
	files := mediafile.New(d)
	for _, f := range []struct {
		id   int64
		w, h int
	}{{uhd.ID, 3840, 1600}, {hd.ID, 1920, 1080}, {ep.ID, 1920, 2160}} {
		mf := &mediafile.File{ItemID: f.id, Path: "/f/" + strconv.FormatInt(f.id, 10), ModifiedAt: time.Now()}
		if err := files.CreateFile(mf); err != nil {
			t.Fatal(err)
		}
		if err := files.ReplaceTracks(mf.ID, []*mediafile.Track{{StreamIndex: 0, Kind: "video", Codec: "hevc", Width: intp(f.w), Height: intp(f.h)}}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := r.List(item.Filter{Catalog: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 || slices.ContainsFunc(all, func(it *item.Item) bool { return it.Kind == item.Artist }) {
		t.Fatalf("catalog listed %d", len(all))
	}
	if n, _ := r.Count(item.Filter{}); n != 6 {
		t.Fatalf("unscoped count %d", n)
	}
	got, err := r.List(item.Filter{Catalog: true, UHD: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	slices.Sort(ids)
	if want := []int64{uhd.ID, show.ID, season.ID, ep.ID}; !slices.Equal(ids, want) {
		t.Fatalf("4K = %v, want %v", ids, want)
	}
}

// song adds a song with one probed file of this codec and duration.
func song(t *testing.T, r *item.Repo, files *mediafile.Repo, album *item.Item, title string, disc, track int, codec string, ms int64) *item.Item {
	t.Helper()
	s := mustCreate(t, r, &item.Item{LibraryID: album.LibraryID, ParentID: &album.ID, Kind: item.Song, Path: "/m/" + title,
		ParsedTitle: title, SortTitle: item.SortName(title), DiscNumber: intp(disc), TrackNumber: intp(track)})
	f := &mediafile.File{ItemID: s.ID, Path: "/m/" + title, ModifiedAt: time.Now(), DurationMs: &ms}
	if err := files.CreateFile(f); err != nil {
		t.Fatal(err)
	}
	if err := files.ReplaceTracks(f.ID, []*mediafile.Track{{StreamIndex: 0, Kind: "audio", Codec: codec}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func titles[T any](vs []T, title func(T) string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = title(v)
	}
	return out
}

// The music views count, sum and order what the scanner stored: sort names
// ignore a leading "The", letters fall back to #, an album is lossless only
// when every song is, and a song's artist is its performer or else the
// album artist.
func TestMusicViews(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	music := &library.Library{Name: "Music", Kind: library.Music}
	if err := library.New(d).Create(music); err != nil {
		t.Fatal(err)
	}
	r, files := item.New(d), mediafile.New(d)
	band := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "The Band", SortTitle: item.SortName("The Band")})
	abba := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "Abba", SortTitle: "Abba"})
	num := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "10 Years", SortTitle: "10 Years"})
	guest := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "Guest", SortTitle: "Guest"})
	lp := mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &band.ID, Kind: item.Album, ParsedTitle: "Zebra", SortTitle: "Zebra", ParsedYear: intp(2001)})
	ep := mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &abba.ID, Kind: item.Album, ParsedTitle: "The Arrival", SortTitle: item.SortName("The Arrival"), ParsedYear: intp(1976)})
	mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &num.ID, Kind: item.Album, ParsedTitle: "Numbers", SortTitle: "Numbers"})
	two := song(t, r, files, lp, "Two", 1, 2, "flac", 2000)
	song(t, r, files, lp, "One", 1, 1, "alac", 1000)
	three := song(t, r, files, lp, "Three", 2, 1, "pcm_s16le", 3000)
	three.ArtistID = &guest.ID
	if err := r.Update(three); err != nil {
		t.Fatal(err)
	}
	dancing := song(t, r, files, ep, "Dancing", 1, 1, "mp3", 4000)
	dancing.ArtistID = &abba.ID
	if err := r.Update(dancing); err != nil {
		t.Fatal(err)
	}
	song(t, r, files, ep, "Knowing", 1, 2, "flac", 5000)

	if item.SortName("The The") != "The" || item.SortName("Theory") != "Theory" || !mediafile.Lossless("pcm_s24be") || mediafile.Lossless("aac") {
		t.Fatal("sort name or lossless rule")
	}
	artists, err := r.Artists(item.MusicFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(artists, func(a *item.ArtistView) string { return a.ParsedTitle }); !slices.Equal(got, []string{"10 Years", "Abba", "The Band", "Guest"}) {
		t.Fatalf("artists %v", got)
	}
	if b := artists[2]; b.AlbumCount != 1 || b.SongCount != 3 || artists[3].SongCount != 1 || artists[3].AlbumCount != 0 || artists[1].SongCount != 2 {
		t.Fatalf("counts %+v %+v %+v", b, artists[3], artists[1])
	}
	if got, _ := r.Artists(item.MusicFilter{Letter: "B"}); len(got) != 1 || got[0].ID != band.ID {
		t.Fatalf("letter B %v", got)
	}
	if got, _ := r.Artists(item.MusicFilter{Letter: "#"}); len(got) != 1 || got[0].ID != num.ID {
		t.Fatalf("letter # %v", got)
	}
	letters, err := r.Letters(music.ID, item.Artist, false)
	if err != nil || !slices.Equal(letters, []item.LetterCount{{Letter: "A", Count: 1}, {Letter: "B", Count: 1}, {Letter: "G", Count: 1}, {Letter: "#", Count: 1}}) {
		t.Fatalf("letters %v %v", letters, err)
	}

	albums, err := r.Albums(item.MusicFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(albums, func(a *item.AlbumView) string { return a.ParsedTitle }); !slices.Equal(got, []string{"The Arrival", "Numbers", "Zebra"}) {
		t.Fatalf("albums %v", got)
	}
	z := albums[2]
	if z.ArtistName != "The Band" || z.SongCount != 3 || z.DurationMs != 6000 || !z.Lossless || albums[0].Lossless || albums[1].Lossless {
		t.Fatalf("album %+v", z)
	}
	if got, _ := r.Albums(item.MusicFilter{Sort: "year"}); got[0].ID != lp.ID || got[1].ID != ep.ID {
		t.Fatal("year sort is newest first, undated last")
	}
	if got, _ := r.Albums(item.MusicFilter{Sort: "artist"}); got[0].ParsedTitle != "Numbers" || got[1].ID != ep.ID || got[2].ID != lp.ID {
		t.Fatal("artist sort is by the album artist's sort name")
	}
	if got, _ := r.Albums(item.MusicFilter{ArtistID: band.ID}); len(got) != 1 || got[0].ID != lp.ID {
		t.Fatal("albums by artist")
	}

	songs, err := r.Songs(item.MusicFilter{AlbumID: lp.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(songs, func(s *item.SongView) string { return s.ParsedTitle }); !slices.Equal(got, []string{"One", "Two", "Three"}) {
		t.Fatalf("album order %v", got)
	}
	if s := songs[2]; s.ArtistName != "Guest" || s.PerformerID != guest.ID || s.AlbumArtistID != band.ID || s.AlbumTitle != "Zebra" ||
		s.Codec != "pcm_s16le" || *s.DurationMs != 3000 || *s.Year != 2001 {
		t.Fatalf("song %+v", s)
	}
	if songs[0].PerformerID != band.ID || songs[0].ArtistName != "The Band" {
		t.Fatalf("song artist falls back to the album artist: %+v", songs[0])
	}
	if got, _ := r.Songs(item.MusicFilter{ArtistID: guest.ID}); len(got) != 1 || got[0].ID != three.ID {
		t.Fatal("songs a guest performs")
	}
	if got, _ := r.Songs(item.MusicFilter{Q: "o"}); titles(got, func(s *item.SongView) string { return s.ParsedTitle })[0] != "One" {
		t.Fatal("a prefix match comes first")
	}
	if got, _ := r.Songs(item.MusicFilter{IDs: []int64{two.ID, three.ID}}); len(got) != 2 {
		t.Fatal("by ids")
	}
	if got, _ := r.Named(music.ID, &band.ID, item.Album, "zebra"); got == nil || got.ID != lp.ID {
		t.Fatal("named album ignores case")
	}
	if got, _ := r.Named(music.ID, nil, item.Artist, "the band"); got == nil || got.ID != band.ID {
		t.Fatal("named root")
	}
	if _, err := r.Named(music.ID, nil, item.Artist, "nobody"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

// Refresh all makes albums due too; artists and songs are filled by them.
func TestMarkAllDueIncludesAlbums(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	music := &library.Library{Name: "Music", Kind: library.Music}
	if err := library.New(d).Create(music); err != nil {
		t.Fatal(err)
	}
	r := item.New(d)
	ar := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "A"})
	al := mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &ar.ID, Kind: item.Album, ParsedTitle: "B"})
	for _, id := range []int64{ar.ID, al.ID} {
		if err := r.SetMetadata(&item.Metadata{ItemID: id, Provider: item.MusicBrainz, Status: item.Matched}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := r.MarkAllDue(time.Now()); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

// Looking music up by id, as a playlist page does per entry, finds each
// row by its key among many, never walking every artist, album or song.
func TestMusicByIDSearchesTheKey(t *testing.T) {
	d := dbtest.Open(t)
	music := &library.Library{Name: "Music", Kind: library.Music}
	if err := library.New(d).Create(music); err != nil {
		t.Fatal(err)
	}
	p := &dbtest.Planner{DBTX: d}
	r := item.New(p)
	band := mustCreate(t, r, &item.Item{LibraryID: music.ID, Kind: item.Artist, ParsedTitle: "Band"})
	lp := mustCreate(t, r, &item.Item{LibraryID: music.ID, ParentID: &band.ID, Kind: item.Album, ParsedTitle: "LP"})
	if _, err := d.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 50000)
		INSERT INTO item (library_id, parent_id, kind, parsed_title, added_at, updated_at) SELECT ?, ?, 'song', 'Song ' || i, 0, 0 FROM n`,
		music.ID, lp.ID); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := d.QueryRow(`SELECT max(id) FROM item`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	songs, err := r.Songs(item.MusicFilter{IDs: []int64{last, last - 1}})
	if err != nil || len(songs) != 2 {
		t.Fatalf("songs %d %v", len(songs), err)
	}
	albums, err := r.Albums(item.MusicFilter{IDs: []int64{lp.ID}})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums %d %v", len(albums), err)
	}
	artists, err := r.Artists(item.MusicFilter{IDs: []int64{band.ID}})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists %d %v", len(artists), err)
	}
	for i, alias := range []string{"s", "al", "a"} {
		if !strings.HasPrefix(p.Plans[i], "SEARCH "+alias+" USING INTEGER PRIMARY KEY") {
			t.Errorf("%s not found by key:\n%s", alias, p.Plans[i])
		}
	}
}

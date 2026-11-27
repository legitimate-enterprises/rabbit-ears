package scan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func intp(n int) *int { return &n }

func TestReadSong(t *testing.T) {
	root := "/music"
	cases := []struct {
		name string
		tags map[string]string
		path string
		want songInfo
	}{
		{"tags win", map[string]string{"title": "Opening", "artist": "Guest", "album_artist": "The Band", "album": "LP",
			"track": "3/12", "disc": "2/2", "date": "2007-05-01", "genre": "Rock; Pop"}, "/music/x/y/01 z.flac",
			songInfo{Artist: "Guest", AlbumArtist: "The Band", Album: "LP", Title: "Opening", Disc: intp(2), Track: intp(3), Year: intp(2007),
				Genres: []string{"Rock", "Pop"}}},
		{"vorbis names", map[string]string{"title": "T", "artist": "A", "albumartist": "AA", "album": "Al", "tracknumber": "7", "discnumber": "1", "year": "1999"},
			"/music/f.flac", songInfo{Artist: "A", AlbumArtist: "AA", Album: "Al", Title: "T", Disc: intp(1), Track: intp(7), Year: intp(1999)}},
		{"artist stands for the album artist", map[string]string{"title": "T", "artist": "Solo", "album": "Al"}, "/music/f.mp3",
			songInfo{Artist: "Solo", AlbumArtist: "Solo", Album: "Al", Title: "T"}},
		{"a compilation without an album artist", map[string]string{"title": "T", "artist": "One", "album": "Hits", "compilation": "1"}, "/music/f.mp3",
			songInfo{Artist: "One", AlbumArtist: "Various Artists", Album: "Hits", Title: "T"}},
		{"folders and the file name", nil, "/music/Folder Artist/Folder Album (2019)/01 - Untagged Song.flac",
			songInfo{Artist: "Folder Artist", AlbumArtist: "Folder Artist", Album: "Folder Album", Title: "Untagged Song", Track: intp(1), Year: intp(2019)}},
		{"a disc folder", nil, "/music/Band/Double/CD 2/1-04. Song.mp3",
			songInfo{Artist: "Band", AlbumArtist: "Band", Album: "Double", Title: "Song", Disc: intp(1), Track: intp(4)}},
		{"a disc folder alone", nil, "/music/Band/Double/Disc 2/05 Song.mp3",
			songInfo{Artist: "Band", AlbumArtist: "Band", Album: "Double", Title: "Song", Disc: intp(2), Track: intp(5)}},
		{"loose at the root", map[string]string{}, "/music/1984.mp3",
			songInfo{Artist: "Unknown Artist", AlbumArtist: "Unknown Artist", Album: "Unknown Album", Title: "1984"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := readSong(c.tags, root, c.path)
			if got.Artist != c.want.Artist || got.AlbumArtist != c.want.AlbumArtist || got.Album != c.want.Album || got.Title != c.want.Title ||
				!eqInt(got.Disc, c.want.Disc) || !eqInt(got.Track, c.want.Track) || !eqInt(got.Year, c.want.Year) || !slices.Equal(got.Genres, c.want.Genres) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func eqInt(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// fakeMusic is a MusicBrainz that knows one release and one release group;
// down fails every call and blind finds nothing by name. It records the
// ids each endpoint was asked for.
type fakeMusic struct {
	down, blind      bool
	calls            int
	releases, groups []string
}

const (
	fakeGroup    = "99999999-8888-7777-6666-555555555555"
	otherRelease = "22222222-3333-4444-5555-666666666666"
)

func (f *fakeMusic) ReleaseGroup(_ context.Context, id string) (*musicbrainz.Details, error) {
	f.calls++
	f.groups = append(f.groups, id)
	if id != fakeGroup {
		return nil, musicbrainz.ErrNotFound
	}
	return &musicbrainz.Details{ID: id, GroupID: id, Genres: []string{"Shoegaze"}}, nil
}

const fakeRelease = "11111111-2222-3333-4444-555555555555"

func (f *fakeMusic) SearchRelease(_ context.Context, artist, album string) ([]musicbrainz.Release, error) {
	f.calls++
	if f.down {
		return nil, musicbrainz.ErrUnavailable
	}
	if f.blind || album != "First Light" {
		return nil, nil
	}
	return []musicbrainz.Release{{ID: fakeRelease, Title: "First Light", Date: "2024", Score: 100, ArtistName: artist,
		ArtistID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}, nil
}

func (f *fakeMusic) Release(_ context.Context, id string) (*musicbrainz.Details, error) {
	f.calls++
	f.releases = append(f.releases, id)
	if id != fakeRelease && id != otherRelease {
		return nil, musicbrainz.ErrNotFound
	}
	return &musicbrainz.Details{ID: id, GroupID: fakeGroup, Genres: []string{"Indie Rock"}}, nil
}

func (f *fakeMusic) Artist(context.Context, string) (*musicbrainz.Details, error) {
	f.calls++
	return &musicbrainz.Details{Genres: []string{"Rock"}}, nil
}

func (f *fakeMusic) CacheCover(_ context.Context, dir, id string) (string, error) {
	f.calls++
	p := filepath.Join(dir, "caa", id+".jpg")
	return p, errors.Join(os.MkdirAll(filepath.Dir(p), 0o750), os.WriteFile(p, []byte("jpeg"), 0o600))
}

func musicEnv(t *testing.T) (*env, *fakeMusic) {
	t.Helper()
	e := newEnv(t, library.Music, "")
	mb := &fakeMusic{}
	e.s.Music = mb
	src, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", "music"))
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("music fixtures missing; run make deps")
	}
	if err := os.CopyFS(e.root, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return e, mb
}

// Scanning the music fixtures builds artists, albums and songs from tags,
// or from folders when a file has none, files artwork, and matches what
// MusicBrainz knows. A rescan of unchanged files probes nothing.
func TestScanMusic(t *testing.T) {
	e, mb := musicEnv(t)
	sum, err := e.s.Scan(context.Background(), e.lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.String(); got != "3 artists, 2 albums, 4 songs added" {
		t.Errorf("summary %q", got)
	}
	artists := e.list(item.Artist)
	names := titles(artists)
	if !slices.Equal(names, []string{"Folder Artist", "Guest Singer", "The Example Band"}) {
		t.Fatalf("artists %v", names)
	}
	band := artists[2]
	if band.SortTitle != "Example Band" {
		t.Errorf("sort title %q", band.SortTitle)
	}
	albums := e.list(item.Album)
	if len(albums) != 2 || albums[0].ParsedTitle != "First Light" || *albums[0].ParentID != band.ID || *albums[0].ParsedYear != 2024 ||
		albums[1].ParsedTitle != "Folder Album" || *albums[1].ParsedYear != 2019 {
		t.Fatalf("albums %+v %+v", albums[0], albums[1])
	}
	songs, err := e.items.Songs(item.MusicFilter{AlbumID: &albums[0].ID})
	if err != nil || len(songs) != 3 {
		t.Fatalf("songs %v %v", songs, err)
	}
	if s := songs[1]; s.ParsedTitle != "Second Wind" || s.ArtistName != "Guest Singer" || s.Codec != "mp3" || *s.TrackNumber != 2 {
		t.Errorf("guest song %+v", s)
	}
	if s := songs[0]; s.ParsedTitle != "Opening" || !s.Lossless || s.DurationMs == nil || *s.DurationMs < 2900 {
		t.Errorf("flac song %+v", s)
	}
	if s := songs[2]; s.ParsedTitle != "Last Call" || s.PerformerID != band.ID || s.Codec != "aac" {
		t.Errorf("m4a song %+v", s)
	}

	// The FLAC's embedded cover and the loose album's folder cover become
	// album art; the matched album keeps its local art over the archive's.
	for _, al := range albums {
		m, err := e.items.Metadata(al.ID)
		if err != nil || m.Provider != item.MusicBrainz || filepath.Dir(m.PosterPath) != filepath.Join(e.s.ThumbDir, "music") {
			t.Fatalf("art of %s: %+v %v", al.ParsedTitle, m, err)
		}
		if _, err := os.Stat(m.PosterPath); err != nil {
			t.Error(err)
		}
	}
	m, _ := e.items.Metadata(albums[0].ID)
	if m.Status != item.Matched || m.ExternalID != fakeRelease || m.FetchedAt == nil || m.RetryAt == nil || !m.RetryAt.After(*e.clock) {
		t.Errorf("match %+v", m)
	}
	if gs, _ := e.s.Genres.ForItem(albums[0].ID); len(gs) != 1 || gs[0].Name != "Rock" {
		t.Errorf("tag genres stand: %v", gs)
	}
	if am, err := e.items.Metadata(band.ID); err != nil || am.Status != item.Matched || am.ExternalID == "" {
		t.Errorf("artist match %+v %v", am, err)
	}
	loose, _ := e.items.Metadata(albums[1].ID)
	if loose.Status != item.Unmatched || loose.RetryAt == nil || loose.FetchedAt != nil {
		t.Errorf("no match waits for the refresh: %+v", loose)
	}

	probes, calls := *e.probes, mb.calls
	sum, err = e.s.Scan(context.Background(), e.lib, nil)
	if err != nil || *e.probes != probes || mb.calls != calls || sum.String() != "No changes; 4 songs checked" {
		t.Fatalf("rescan probed %d, looked up %d: %q %v", *e.probes-probes, mb.calls-calls, sum, err)
	}

	// Songs gone from disk are marked; they, their albums and their
	// artists all stay.
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	guest := filepath.Join(album, "02 Second Wind.mp3")
	must(t, os.Remove(guest))
	must(t, os.RemoveAll(filepath.Join(e.root, "Folder Artist")))
	if sum, err = e.s.Scan(context.Background(), e.lib, nil); err != nil {
		t.Fatal(err)
	}
	if got := titles(e.list(item.Artist)); !slices.Equal(got, names) || len(e.list(item.Album)) != 2 || len(e.list(item.Song)) != 4 ||
		!e.missing(guest) || sum.String() != "No changes; 2 songs checked; 2 files missing" {
		t.Fatalf("after removal %v %q", got, sum)
	}

	// Retagging a song onto another album moves it there, keeping the song.
	opening := filepath.Join(album, "01 Opening.flac")
	song, err := e.items.ByPath(e.lib.ID, item.Song, opening)
	must(t, err)
	_, err = e.d.Exec(`INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (?, 'adult', 'A', 0, 0)`, uid.N(1))
	must(t, err)
	_, err = e.d.Exec(`INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (?1, ?1, 'P', 'music', 0, 0)`, uid.N(1))
	must(t, err)
	_, err = e.d.Exec(`INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at, created_at, modified_at) VALUES (?1, ?1, ?2, 'a0', 0, 0, 0)`, uid.N(1), song.ID)
	must(t, err)
	retagged := filepath.Join(t.TempDir(), "x.flac")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", opening, "-map", "0", "-c", "copy", "-metadata", "album=Second Album", retagged).CombinedOutput(); err != nil { //nolint:gosec // fixture and temp paths
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(retagged) //nolint:gosec // test temp file
	must(t, err)
	must(t, os.WriteFile(opening, b, 0o600)) //nolint:gosec // test temp copy of fixtures
	later := e.clock.Add(time.Minute)
	must(t, os.Chtimes(opening, later, later))
	if _, err = e.s.Scan(context.Background(), e.lib, nil); err != nil {
		t.Fatal(err)
	}
	if got := titles(e.list(item.Album)); !slices.Equal(got, []string{"First Light", "Folder Album", "Second Album"}) {
		t.Fatalf("albums after retag %v", got)
	}
	moved, err := e.items.ByPath(e.lib.ID, item.Song, opening)
	must(t, err)
	var entries int
	must(t, e.d.QueryRow(`SELECT COUNT(*) FROM playlist_entry WHERE item_id = ?`, song.ID).Scan(&entries))
	if moved.ID != song.ID || entries != 1 {
		t.Fatalf("song %s became %s, %d playlist entries left", song.ID, moved.ID, entries)
	}
}

// retag rewrites a song file in place with other tags, as a tag editor does.
func (e *env) retag(path string, tags ...string) {
	e.t.Helper()
	out := filepath.Join(e.t.TempDir(), "x"+filepath.Ext(path))
	args := []string{"-v", "error", "-i", path, "-map", "0", "-c", "copy"}
	for _, kv := range tags {
		args = append(args, "-metadata", kv)
	}
	if b, err := exec.Command("ffmpeg", append(args, out)...).CombinedOutput(); err != nil { //nolint:gosec // fixture and temp paths
		e.t.Fatalf("%v: %s", err, b)
	}
	b, err := os.ReadFile(out) //nolint:gosec // test temp file
	must(e.t, err)
	must(e.t, os.WriteFile(path, b, 0o600)) //nolint:gosec // test temp copy of fixtures
	*e.clock = e.clock.Add(time.Minute)
	must(e.t, os.Chtimes(path, *e.clock, *e.clock))
}

// A song whose performer tag alone changes leaves the performer it had;
// one who performs nothing else is no longer listed.
func TestRetaggedPerformerIsPruned(t *testing.T) {
	e, _ := musicEnv(t)
	e.mustScan()
	e.retag(filepath.Join(e.root, "The Example Band", "First Light (2024)", "02 Second Wind.mp3"), "artist=The Example Band")
	e.mustScan()
	if got := titles(e.list(item.Artist)); !slices.Equal(got, []string{"Folder Artist", "The Example Band"}) {
		t.Fatalf("artists %v", got)
	}
	if len(e.list(item.Song)) != 4 || len(e.list(item.Album)) != 2 {
		t.Fatalf("%d songs, %d albums", len(e.list(item.Song)), len(e.list(item.Album)))
	}
}

// A song file replaced by one with another embedded picture gives its album
// that picture, which moves the artwork generation; a song added beside it
// leaves the album's art be.
func TestAlbumArtFollowsAReplacedSong(t *testing.T) {
	e, _ := musicEnv(t)
	_, err := e.s.Scan(context.Background(), e.lib, nil)
	must(t, err)
	album := e.list(item.Album)[0]
	old, err := e.items.LocalRow(album.ID)
	must(t, err)
	before := e.generation(time.Minute)

	dir := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	opening := filepath.Join(dir, "01 Opening.flac")
	repainted := filepath.Join(t.TempDir(), "x.flac")
	//nolint:gosec // fixture and temp paths
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", opening, "-f", "lavfi", "-i", "color=c=red:s=64x64", "-map", "0:a",
		"-map", "1:v", "-frames:v", "1", "-c:a", "copy", "-c:v", "mjpeg", "-disposition:v", "attached_pic", repainted).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(repainted) //nolint:gosec // test temp file
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "04 Encore.flac"), b, 0o600)) //nolint:gosec // test temp copy of fixtures
	_, err = e.s.Scan(context.Background(), e.lib, nil)
	must(t, err)
	if now, err := e.items.LocalRow(album.ID); err != nil || now.PosterPath != old.PosterPath {
		t.Fatalf("a new song took the album's art: %+v %v", now, err)
	}

	must(t, os.WriteFile(opening, b, 0o600)) //nolint:gosec // test temp copy of fixtures
	later := e.clock.Add(time.Minute)
	must(t, os.Chtimes(opening, later, later))
	_, err = e.s.Scan(context.Background(), e.lib, nil)
	must(t, err)
	now, err := e.items.LocalRow(album.ID)
	must(t, err)
	if now.PosterPath == old.PosterPath {
		t.Fatalf("the album keeps %s", old.PosterPath)
	}
	if _, err := os.Stat(now.PosterPath); err != nil {
		t.Fatal(err)
	}
	if g := e.generation(2 * time.Minute); g <= before {
		t.Errorf("the generation stays at %d", g)
	}
}

// A provider outage leaves the scan's items in place and retries later.
func TestScanMusicOutage(t *testing.T) {
	e, mb := musicEnv(t)
	mb.down = true
	sum, err := e.s.Scan(context.Background(), e.lib, nil)
	if err != nil || sum.LookupFailed != 2 || mb.calls != 1 {
		t.Fatalf("one failed call defers the rest: %v %d calls %q", err, mb.calls, sum)
	}
	albums := e.list(item.Album)
	m, _ := e.items.Metadata(albums[0].ID)
	if m.RetryAt == nil || !m.RetryAt.Equal(e.clock.Add(RetryDelay)) || m.PosterPath == "" {
		t.Fatalf("deferred %+v", m)
	}
	mb.down = false
	*e.clock = e.clock.Add(2 * RetryDelay)
	if _, err := e.s.RetryDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if m, _ = e.items.Metadata(albums[0].ID); m.Status != item.Matched {
		t.Fatalf("retried %+v", m)
	}
}

func titles(its []*item.Item) []string {
	out := make([]string, len(its))
	for i, it := range its {
		out[i] = it.ParsedTitle
	}
	return out
}

// Music libraries scan nightly like video ones.
func TestSyncSchedulesMusicScans(t *testing.T) {
	e := newEnv(t, library.Music, "")
	r, tasks := newRunner(t, e)
	if err := r.Sync(); err != nil {
		t.Fatal(err)
	}
	ts, _ := tasks.List()
	var kinds []string
	for _, x := range ts {
		kinds = append(kinds, x.Kind)
	}
	slices.Sort(kinds)
	if want := []string{"local", "metadata", "previews", "scan:" + e.lib.ID.String()}; !slices.Equal(kinds, want) {
		t.Fatalf("tasks %v, want %v", kinds, want)
	}
}

// The advisory tags as ffprobe reports them: an MP4 rtng atom is "rating",
// which only MP4 and MOV mean that way; other formats' RATING is a score.
func TestExplicitTags(t *testing.T) {
	cases := []struct {
		name      string
		tags      map[string]string
		container string
		want      bool
	}{
		{"none", map[string]string{"title": "T"}, "flac", false},
		{"rtng explicit", map[string]string{"rating": "1"}, "mp4", true},
		{"rtng old explicit", map[string]string{"rating": "4"}, "mov", true},
		{"rtng clean", map[string]string{"rating": "2"}, "mp4", false},
		{"rtng none", map[string]string{"rating": "0"}, "mp4", false},
		{"a vorbis star rating", map[string]string{"rating": "4"}, "flac", false},
		{"itunesadvisory explicit", map[string]string{"itunesadvisory": "1"}, "mp3", true},
		{"itunesadvisory clean", map[string]string{"itunesadvisory": "2"}, "flac", false},
		{"explicit flag", map[string]string{"explicit": "1"}, "ogg", true},
		{"explicit word", map[string]string{"explicit": " True "}, "flac", true},
		{"explicit off", map[string]string{"explicit": "0"}, "flac", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := explicit(c.tags, c.container); got != c.want {
				t.Errorf("explicit(%v, %q) = %v", c.tags, c.container, got)
			}
		})
	}
}

// A song's advisory tag is read at each probe, and a song scanned before
// the tag was read is probed again though its file is unchanged.
func TestScanReadsExplicit(t *testing.T) {
	e, _ := musicEnv(t)
	opening := filepath.Join(e.root, "The Example Band", "First Light (2024)", "01 Opening.flac")
	tagged := filepath.Join(t.TempDir(), "x.flac")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", opening, "-map", "0", "-c", "copy", "-metadata", "ITUNESADVISORY=1", tagged).CombinedOutput(); err != nil { //nolint:gosec // fixture and temp paths
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(tagged) //nolint:gosec // test temp file
	must(t, err)
	must(t, os.WriteFile(opening, b, 0o600)) //nolint:gosec // test temp copy of fixtures
	if _, err := e.s.Scan(context.Background(), e.lib, nil); err != nil {
		t.Fatal(err)
	}
	songs := e.list(item.Song)
	if len(songs) != 4 {
		t.Fatalf("%d songs", len(songs))
	}
	for _, s := range songs {
		if s.Explicit == nil || *s.Explicit != (s.ParsedTitle == "Opening") {
			t.Errorf("%s explicit %v", s.ParsedTitle, s.Explicit)
		}
	}

	_, err = e.d.Exec(`UPDATE item SET explicit = NULL WHERE kind = 'song'`)
	must(t, err)
	probes := *e.probes
	if _, err := e.s.Scan(context.Background(), e.lib, nil); err != nil {
		t.Fatal(err)
	}
	if *e.probes != probes+4 {
		t.Fatalf("probed %d of 4 songs never read for the tag", *e.probes-probes)
	}
	for _, s := range e.list(item.Song) {
		if s.Explicit == nil {
			t.Errorf("%s still unread", s.ParsedTitle)
		}
	}
}

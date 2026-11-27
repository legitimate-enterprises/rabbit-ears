package scan

import (
	"context"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
)

func TestLocalTags(t *testing.T) {
	cases := []struct {
		kind  string
		tags  map[string]string
		want  item.Metadata
		genre []string
	}{
		{item.Movie, map[string]string{"title": " Lamplight ", "description": "Short.", "synopsis": "Long.", "date": "2011-05-06T00:00:00Z",
			"genre": "Drama, Comedy;Noir", "itunextc": "mpaa|PG-13|300|"},
			item.Metadata{Title: "Lamplight", Overview: "Long.", ReleaseDate: "2011-05-06", ContentRating: "PG-13"}, []string{"Drama", "Comedy", "Noir"}},
		{item.Movie, map[string]string{"year": "1999", "content_rating": "TV-14", "summary": "Kept."},
			item.Metadata{Overview: "Kept.", ReleaseDate: "1999"}, nil},
		{item.Episode, map[string]string{"date_released": "2019", "date": "2020-01-02", "rating": "tv-pg", "comment": "encoder junk"},
			item.Metadata{ReleaseDate: "2019", ContentRating: "TV-PG"}, nil},
		{item.Movie, map[string]string{"date": "20110506", "rating": "Unrated"}, item.Metadata{}, nil},
		{item.Movie, map[string]string{"creation_time": "2024-01-01T00:00:00Z", "date": "199X"}, item.Metadata{}, nil},
		{item.Movie, map[string]string{"title": "Toy.Story.1995.1080p.BluRay.H264-RARBG"}, item.Metadata{}, nil},
		{item.Movie, map[string]string{"title": "toy_story_1995"}, item.Metadata{}, nil},
		{item.Movie, map[string]string{"title": "Alien"}, item.Metadata{Title: "Alien"}, nil},
		{item.Episode, map[string]string{"title": "Silicon Valley - S01E01 - Minimum Viable Product"}, item.Metadata{Title: "Minimum Viable Product"}, nil},
		{item.Episode, map[string]string{"title": "Show - s02e03-e04 - Two Parts"}, item.Metadata{Title: "Two Parts"}, nil},
		{item.Episode, map[string]string{"title": "Show - S01E01"}, item.Metadata{}, nil},
		{item.Movie, map[string]string{"title": "Dr. Strangelove"}, item.Metadata{Title: "Dr. Strangelove"}, nil},
	}
	for _, c := range cases {
		m, gs := localTags(c.tags, c.kind)
		if *m != c.want || !slices.Equal(gs, c.genre) {
			t.Errorf("%v: %+v %v", c.tags, *m, gs)
		}
	}
}

func jpegShape(t *testing.T, p string) (int, int) {
	t.Helper()
	f, err := os.Open(p) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	c, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return c.Width, c.Height
}

// tagged writes a copy of a fixture with tags and, given cover, an
// attached cover picture.
func tagged(t *testing.T, fixture, dest string, cover bool, tags ...string) {
	t.Helper()
	fixtureCopy(t, fixture, dest+".src.mp4")
	args := []string{"-v", "error", "-y", "-i", dest + ".src.mp4"}
	if cover {
		pic := dest + ".jpg"
		writeArt(t, pic)
		args = append(args, "-i", pic, "-map", "0", "-map", "1", "-disposition:v:1", "attached_pic")
	}
	for _, kv := range tags {
		args = append(args, "-metadata", kv)
	}
	//nolint:gosec // test paths
	if out, err := exec.Command("ffmpeg", append(args, "-c", "copy", dest)...).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	must(t, os.Remove(dest+".src.mp4"))
}

func writeArt(t *testing.T, p string) {
	t.Helper()
	//nolint:gosec // test path
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=orange:s=300x450:d=1", "-frames:v", "1", p).
		CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

// A library deleted while its titles' local metadata is read fails
// nothing; the titles already read stay counted.
func TestLocalMetadataOfADeletedLibraryGoesQuietly(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "B (2002)", "b.mp4"))
	e.mustScan()
	read, n := e.s.Probe, 0
	e.s.Probe = func(ctx context.Context, p string) (*probe.Result, error) {
		if n++; n == 2 {
			must(t, e.s.Libraries.Delete(e.lib.ID))
		}
		return read(ctx, p)
	}
	sum, _, err := e.s.MakeLocal(context.Background(), nil, nil)
	if err != nil || sum != "1 title" {
		t.Fatalf("%q %v", sum, err)
	}
}

// After a scan the Local metadata task reads each movie's and episode's
// tags and makes art with real ffmpeg: a tagged movie's cover is its
// poster and a frame its backdrop; an untagged movie's poster is the
// frame's 2:3 centre; an episode's poster is its frame; a season and a
// show take their first episode's. A second run reads nothing; a changed
// file is read again.
func TestLocalMetadataTask(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	tagged(t, "h264-high41.mp4", filepath.Join(e.root, "Lamplight (2011)", "Lamplight (2011).mp4"), true,
		"title=Lamplight: The Cut", "synopsis=A lamp, lit.", "date=2011-05-06", "genre=Drama, Mystery")
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Plain (2001)", "Plain (2001).mkv"))
	e.mustScan()
	tv := &library.Library{Name: "TV", Kind: library.TV}
	must(t, e.s.Libraries.Create(tv, nil))
	tvRoot := t.TempDir()
	err := e.s.Libraries.AddFolder(&library.Folder{LibraryID: tv.ID, Path: tvRoot}, nil)
	must(t, err)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(tvRoot, "Show", "Season 01", "Show - S01E02.mp4"))
	fixtureCopy(t, "hdr10-main10.mkv", filepath.Join(tvRoot, "Show", "Season 01", "Show - S01E01.mkv"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(tvRoot, "Show", "Specials", "Show - S00E01.mkv"))
	_, err = e.s.Scan(context.Background(), tv, nil)
	must(t, err)

	before := e.generation(time.Minute)
	sum, more, err := e.s.MakeLocal(context.Background(), nil, nil)
	if err != nil || more || sum != "8 titles" {
		t.Fatalf("%q %v %v", sum, more, err)
	}
	if g := e.generation(2 * time.Minute); g <= before {
		t.Errorf("local art keeps the artwork generation at %d", g)
	}
	local := func(it *item.Item) *item.Metadata {
		t.Helper()
		m, err := e.items.LocalRow(it.ID)
		must(t, err)
		return m
	}
	movies := map[string]*item.Item{}
	for _, it := range e.list(item.Movie) {
		movies[it.ParsedTitle] = it
	}
	lamp := local(movies["Lamplight"])
	if lamp.Title != "Lamplight: The Cut" || lamp.Overview != "A lamp, lit." || lamp.ReleaseDate != "2011-05-06" || lamp.RuntimeMinutes != nil {
		t.Errorf("tags %+v", lamp)
	}
	if w, h := jpegShape(t, lamp.PosterPath); w != 300 || h != 450 {
		t.Errorf("cover %dx%d", w, h)
	}
	if w, h := jpegShape(t, lamp.BackdropPath); w != 1280 || h != 720 {
		t.Errorf("backdrop %dx%d", w, h)
	}
	if gs, err := e.s.Genres.From(movies["Lamplight"].ID, genre.Local); err != nil || len(gs) != 2 {
		t.Errorf("genres %v %v", gs, err)
	}
	plain := local(movies["Plain"])
	if w, h := jpegShape(t, plain.PosterPath); w != 500 || h < 740 || h > 760 {
		t.Errorf("cropped poster %dx%d", w, h)
	}
	if plain.BackdropPath == "" || plain.Title != "" {
		t.Errorf("plain %+v", plain)
	}
	shows, err := e.items.List(item.Filter{LibraryID: &tv.ID, Kind: item.Show})
	must(t, err)
	if len(shows) != 1 {
		t.Fatalf("shows %v", shows)
	}
	s1, err := e.items.Season(shows[0].ID, 1)
	must(t, err)
	e1, err := e.items.Episode(s1.ID, 1)
	must(t, err)
	first, show, season := local(e1), local(shows[0]), local(s1)
	if w, h := jpegShape(t, first.PosterPath); w != 1280 || h != 720 || first.BackdropPath != "" {
		t.Errorf("episode %+v %dx%d", first, w, h)
	}
	if show.BackdropPath != first.PosterPath || season.BackdropPath != first.PosterPath || show.PosterPath == "" || season.PosterPath != show.PosterPath {
		t.Errorf("show %+v season %+v episode %+v", show, season, first)
	}

	if sum, _, err := e.s.MakeLocal(context.Background(), nil, nil); err != nil || sum != "0 titles" {
		t.Errorf("second run %q %v", sum, err)
	}
	p := filepath.Join(e.root, "Plain (2001)", "Plain (2001).mkv")
	later := time.Now().Add(time.Hour)
	must(t, os.Chtimes(p, later, later))
	e.mustScan()
	if sum, _, err := e.s.MakeLocal(context.Background(), nil, nil); err != nil || sum != "1 title" {
		t.Errorf("after a change %q %v", sum, err)
	}
}

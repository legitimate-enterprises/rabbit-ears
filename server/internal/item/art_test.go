package item_test

import (
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Each item's art is the address of its own image under the metadata
// settings, else its parent's, else its grandparent's; a level whose image
// is in no store stops the walk. An artist shows its newest album's, a
// kid's of the albums they may hear.
func TestArtNamesEachImageByItsAddress(t *testing.T) {
	d := dbtest.Open(t)
	dbtest.DataDir(t, "/data")
	r := item.New(d)
	libs := library.New(d)
	lib := func(kind string) uid.ID {
		l := &library.Library{Name: kind, Kind: kind}
		must(t, libs.Create(l, nil))
		return l.ID
	}
	tv, mu, yt := lib(library.TV), lib(library.Music), lib(library.YouTube)
	add := func(lib uid.ID, kind string, parent *uid.ID, year *int) *item.Item {
		return mustCreate(t, r, &item.Item{LibraryID: lib, Kind: kind, ParentID: parent, ParsedTitle: kind, ParsedYear: year})
	}
	show := add(tv, item.Show, nil, nil)
	season := add(tv, item.Season, &show.ID, nil)
	bare := add(tv, item.Episode, &season.ID, nil)
	still := add(tv, item.Episode, &season.ID, nil)
	framed := add(tv, item.Episode, &season.ID, nil)
	must(t, r.SetMetadata(&item.Metadata{ItemID: show.ID, Status: item.Matched, PosterPath: "/show.jpg", BackdropPath: "/wide.jpg"}))
	must(t, r.SetMetadata(&item.Metadata{ItemID: still.ID, Status: item.Matched, PosterPath: "/still.jpg"}))
	must(t, r.SetLocal(&item.Metadata{ItemID: framed.ID, PosterPath: "/data/thumbnails/local/0123abcd.jpg"}))

	artist := add(mu, item.Artist, nil, nil)
	older, newer := 1999, 2024
	first := add(mu, item.Album, &artist.ID, &older)
	loud := add(mu, item.Album, &artist.ID, &newer)
	yes := true
	mustCreate(t, r, &item.Item{LibraryID: mu, Kind: item.Song, ParentID: &first.ID, ParsedTitle: "calm", Explicit: new(bool)})
	song := mustCreate(t, r, &item.Item{LibraryID: mu, Kind: item.Song, ParentID: &loud.ID, ParsedTitle: "shout", Explicit: &yes})
	must(t, r.SetLocal(&item.Metadata{ItemID: first.ID, PosterPath: filepath.Join("/data", "images", "caa", "f00d.jpg")}))
	must(t, r.SetLocal(&item.Metadata{ItemID: loud.ID, PosterPath: "/data/thumbnails/music/beef.png"}))

	channel := add(yt, item.Channel, nil, nil)
	video := add(yt, item.Video, &channel.ID, nil)
	must(t, r.SetLocal(&item.Metadata{ItemID: channel.ID, PosterPath: "/data/cache/youtube/UCabc.jpg"}))
	must(t, r.SetLocal(&item.Metadata{ItemID: video.ID, PosterPath: "/library/Channel/video-thumb.jpg"}))

	dbtest.Sources(t, d, "remote", "preferred")
	ids := []uid.ID{show.ID, season.ID, bare.ID, still.ID, framed.ID, artist.ID, first.ID, song.ID, channel.ID, video.ID}
	want := map[uid.ID]item.Art{
		show.ID:    {Poster: item.ArtURL + "w500/show.jpg", Backdrop: item.ArtURL + "w1280/wide.jpg"},
		season.ID:  {Poster: item.ArtURL + "w500/show.jpg", Backdrop: item.ArtURL + "w1280/wide.jpg"},
		bare.ID:    {Poster: item.ArtURL + "w500/show.jpg", Backdrop: item.ArtURL + "w1280/wide.jpg"},
		still.ID:   {Poster: item.ArtURL + "w500/still.jpg", Backdrop: item.ArtURL + "w1280/wide.jpg"},
		framed.ID:  {Poster: item.ArtURL + "local/0123abcd.jpg", Backdrop: item.ArtURL + "w1280/wide.jpg"},
		artist.ID:  {Poster: item.ArtURL + "music/beef.png"},
		first.ID:   {Poster: item.ArtURL + "caa/f00d.jpg"},
		song.ID:    {Poster: item.ArtURL + "music/beef.png"},
		channel.ID: {Poster: item.ArtURL + "youtube/UCabc.jpg"},
		video.ID:   {},
	}
	got, err := r.Art(ids, false)
	must(t, err)
	for _, id := range ids {
		if got[id] != want[id] {
			t.Errorf("%s: %+v, want %+v", id, got[id], want[id])
		}
	}
	kid, err := r.Art([]uid.ID{artist.ID}, true)
	must(t, err)
	if p := kid[artist.ID].Poster; p != item.ArtURL+"caa/f00d.jpg" {
		t.Errorf("a kid's artist art %q", p)
	}

	// A replica carries a stored image as "store/name", and reads the same
	// address from it.
	for in, want := range map[string]string{"local/0123abcd.jpg": "local/0123abcd.jpg", "frame.jpg": ""} {
		var ref string
		must(t, d.QueryRow(`SELECT `+item.StoreRef("?1"), in).Scan(&ref))
		if ref != want {
			t.Errorf("%s: %q, want %q", in, ref, want)
		}
	}
}

package service_test

import (
	"fmt"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
)

// More rows follow the first than SQLite binds variables in one statement.
func TestNextRowOnAPlayOrderLongerThanABindLimit(t *testing.T) {
	a := newAiring(t)
	l := &library.Library{Name: "Music", Kind: library.Music}
	a.must(a.s.Libraries.Repo().Create(l))
	artist := &item.Item{LibraryID: l.ID, Kind: item.Artist, ParsedTitle: "Artist"}
	a.must(a.s.Items.Repo().Create(artist))
	album := &item.Item{LibraryID: l.ID, ParentID: &artist.ID, Kind: item.Album, ParsedTitle: "Album"}
	a.must(a.s.Items.Repo().Create(album))
	for i := range 33000 {
		n := i + 1
		a.must(a.s.Items.Repo().Create(&item.Item{LibraryID: l.ID, ParentID: &album.ID, Kind: item.Song,
			ParsedTitle: fmt.Sprintf("Song %05d", i), TrackNumber: &n}))
	}
	p := &playlist.Playlist{AccountID: a.owner.ID, Name: "Everything", Kind: playlist.Music}
	a.must(a.s.Playlists.Repo().Create(p))
	a.must(a.s.Playlists.Repo().AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: artist.ID}, nil))
	rows, err := a.s.Playlists.Repo().PlayOrder(p.ID, item.Audience{}, -1, 0)
	a.must(err)
	zero := 0
	row, at, ok, err := a.s.UpNext.NextRow(p.ID, rows[0].ItemID, &zero, item.Audience{})
	a.must(err)
	if !ok || at != 1 || row.ItemID != rows[1].ItemID {
		t.Fatalf("next = %d at %d ok %v, want %d at 1", row.ItemID, at, ok, rows[1].ItemID)
	}
}

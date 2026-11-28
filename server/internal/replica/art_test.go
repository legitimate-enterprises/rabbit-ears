package replica_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// art gives the fixture images in every store, a TMDB one, one in no store,
// albums a kid may and may not hear, and a collection's first movie with
// art; paths are as the server stores them.
const art = `
UPDATE item_metadata SET poster_path = 'thumbnails/local/aa10.jpg', backdrop_path = '/library/Tots/fanart.jpg' WHERE item_id = uid(10);
UPDATE item_metadata SET poster_path = '/two.jpg', backdrop_path = '/two-wide.jpg' WHERE item_id = uid(2) AND source = 'remote';
UPDATE item_metadata SET poster_path = 'thumbnails/local/bb04.jpg' WHERE item_id = uid(4);
UPDATE item SET parsed_year = 2010 WHERE id = uid(35);
UPDATE item SET parsed_year = 1990 WHERE id = uid(42);
INSERT INTO item_metadata (item_id, source, provider, poster_path) VALUES
  (uid(31), 'local', 'local', 'images/caa/31.jpg'), (uid(35), 'local', 'local', 'thumbnails/music/35.png'),
  (uid(42), 'local', 'local', 'thumbnails/music/42.jpg'), (uid(52), 'local', 'local', 'cache/youtube/UCgrown.jpg'),
  (uid(53), 'local', 'local', '/library/Grown/video-thumb.jpg');
INSERT INTO collection (id, source, tmdb_id, name, poster_path, created_at, modified_at) VALUES (uid(450), 'tmdb', 7, 'Saga', '/saga.jpg', 0, 0);
INSERT INTO collection_movie (collection_id, item_id) VALUES (uid(450), uid(1)), (uid(450), uid(3));
`

// The replica's art views answer every profile as the server's responses
// do, and a show's new poster reaches clients as its one metadata row.
func TestArtViewsAnswerAsTheServer(t *testing.T) {
	d := twoHouseholds(t)
	dbtest.Sources(t, d, "remote", "preferred")
	mustExec(t, d, art)
	s := syncOf(t, d)
	r, _ := snapshotOf(t, s, uid.N(201))
	items, collections, accounts := item.New(d), collection.New(d), account.New(d)

	type key struct{ profile, id uid.ID }
	read := func(q string) map[key]item.Art {
		t.Helper()
		rows, err := r.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		out := map[key]item.Art{}
		for rows.Next() {
			var k key
			var p, b sql.NullString
			if err := rows.Scan(&k.profile, &k.id, &p, &b); err != nil {
				t.Fatal(err)
			}
			out[k] = item.Art{Poster: p.String, Backdrop: b.String}
		}
		return out
	}
	itemArt := read(`SELECT account_id, item_id, poster_url, backdrop_url FROM profile_art`)
	collectionArt := read(`SELECT account_id, collection_id, poster_url, backdrop_url FROM profile_collection_art`)
	titles, sets := ids(t, r, `SELECT id FROM item`), ids(t, r, `SELECT id FROM collection`)
	var shown, kinds int
	for _, p := range ids(t, r, `SELECT id FROM account`) {
		a, err := accounts.Get(p)
		if err != nil {
			t.Fatal(err)
		}
		want, err := items.Art(titles, service.AudienceFor(a).Kid)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range titles {
			if got := itemArt[key{p, n}]; got != want[n] {
				t.Errorf("profile %s item %s: replica %+v, server %+v", p, n, got, want[n])
			}
			if want[n].Poster != "" {
				shown++
			}
		}
		for _, c := range sets {
			want, err := collections.Art(c, service.BrowseAudienceFor(a))
			if err != nil {
				t.Fatal(err)
			}
			if got := collectionArt[key{p, c}]; got != want {
				t.Errorf("profile %s collection %s: replica %+v, server %+v", p, c, got, want)
			}
			if want.Poster != "" {
				kinds++
			}
		}
	}
	if shown == 0 || kinds == 0 {
		t.Fatalf("the fixture shows no art: %d items, %d collections", shown, kinds)
	}
	if got := itemArt[key{uid.N(103), uid.N(34)}].Poster; !strings.HasSuffix(got, "music/42.jpg") {
		t.Errorf("a kid's artist art %q", got)
	}
	if got := itemArt[key{uid.N(101), uid.N(34)}].Poster; !strings.HasSuffix(got, "music/35.png") {
		t.Errorf("an adult's artist art %q", got)
	}

	v, _ := clock(t, d)
	mustExec(t, d, `UPDATE item_metadata SET poster_path = 'thumbnails/local/cc10.jpg' WHERE item_id = uid(10)`)
	got, _ := pass(t, s, uid.N(201), v, 100, nil)
	if len(got) != 1 || !strings.HasPrefix(got[0], "item_metadata:") || !strings.Contains(got[0], "local/cc10.jpg") {
		t.Errorf("a show's new poster moves %v", got)
	}
}

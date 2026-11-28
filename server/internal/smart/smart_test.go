package smart

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"modernc.org/sqlite"
)

// The fixtures' SQL names id n as uid(n), the bytes of uid.N(n).
func init() {
	err := sqlite.RegisterDeterministicScalarFunction("uid", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		n, ok := args[0].(int64)
		if !ok {
			return nil, fmt.Errorf("uid(%v)", args[0])
		}
		id := uid.N(int(n))
		return id[:], nil
	})
	if err != nil {
		panic(err)
	}
}

// library: four movies, a show of two episodes and a special, an album of
// two songs; Ada (10) owns, Bo (11) is another adult.
//
//	100 Amélie (parsed amelie) 2001, 122 min, R, Romance
//	101 Toy Story 1995, 81 min, G, Animation and Comedy, Tom Hanks, directed by
//	    John Lasseter, a 4K HDR file; Ada watched it twice
//	102 The Matrix 1999, 136 min, R, Action, Keanu Reeves; Bo watched it
//	103 Untitled, no metadata at all
//	200 Bluey (TV-Y, Animation), season 1 (201): 202 Magic Xylophone, 203
//	    Hospital; specials (204): 205 Special. Ada starred the show.
//	300 Radiohead, album 301 OK Computer 1997, Rock: 302 Airbag (flac), 303
//	    Paranoid Android (mp3, explicit). Ada starred the album.
const library = `
INSERT INTO library (id, name, kind, created_at, modified_at) VALUES (uid(1), 'Films', 'movies', 0, 0), (uid(2), 'TV', 'tv', 0, 0),
  (uid(3), 'Music', 'music', 0, 0);
INSERT INTO account (id, kind, email, name, created_at, modified_at) VALUES (uid(10), 'adult', 'ada@example.test', 'Ada', 0, 0),
  (uid(11), 'adult', 'bo@example.test', 'Bo', 0, 0);
INSERT INTO item (id, library_id, parent_id, kind, parsed_title, parsed_year, season_number, episode_number, track_number, explicit,
  added_at, created_at, modified_at) VALUES
  (uid(100), uid(1), NULL, 'movie', 'amelie', NULL, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(101), uid(1), NULL, 'movie', 'toy story', NULL, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(102), uid(1), NULL, 'movie', 'matrix', 1998, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(103), uid(1), NULL, 'movie', 'Untitled', NULL, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(200), uid(2), NULL, 'show', 'bluey', NULL, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(201), uid(2), uid(200), 'season', 'Season 1', NULL, 1, NULL, NULL, NULL, 0, 0, 0),
  (uid(202), uid(2), uid(201), 'episode', 'Magic Xylophone', NULL, NULL, 1, NULL, NULL, 0, 0, 0),
  (uid(203), uid(2), uid(201), 'episode', 'Hospital', NULL, NULL, 2, NULL, NULL, 0, 0, 0),
  (uid(204), uid(2), uid(200), 'season', 'Specials', NULL, 0, NULL, NULL, NULL, 0, 0, 0),
  (uid(205), uid(2), uid(204), 'episode', 'Special', NULL, NULL, 1, NULL, NULL, 0, 0, 0),
  (uid(300), uid(3), NULL, 'artist', 'Radiohead', NULL, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(301), uid(3), uid(300), 'album', 'OK Computer', 1997, NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(302), uid(3), uid(301), 'song', 'Airbag', NULL, NULL, NULL, 1, 0, 0, 0, 0),
  (uid(303), uid(3), uid(301), 'song', 'Paranoid Android', NULL, NULL, NULL, 2, 1, 0, 0, 0);
INSERT INTO item_metadata (item_id, source, title, release_date, runtime_minutes, content_rating) VALUES
  (uid(100), 'local', 'Amélie', '2001-04-25', 122, 'R'), (uid(101), 'local', 'Toy Story', '1995-11-22', 81, 'G'),
  (uid(102), 'local', 'The Matrix', '1999-03-31', 136, 'R'), (uid(200), 'local', 'Bluey', NULL, NULL, 'TV-Y');
INSERT INTO genre (id, name, created_at, modified_at) VALUES (uid(1), 'Romance', 0, 0), (uid(2), 'Animation', 0, 0),
  (uid(3), 'Comedy', 0, 0), (uid(4), 'Action', 0, 0), (uid(5), 'Rock', 0, 0);
INSERT INTO item_genre (item_id, genre_id, source) VALUES (uid(100), uid(1), 'local'), (uid(101), uid(2), 'local'),
  (uid(101), uid(3), 'local'), (uid(102), uid(4), 'local'), (uid(200), uid(2), 'local'), (uid(301), uid(5), 'local');
INSERT INTO person (id, name, created_at, modified_at) VALUES (uid(1), 'Tom Hanks', 0, 0), (uid(2), 'John Lasseter', 0, 0),
  (uid(3), 'Keanu Reeves', 0, 0);
INSERT INTO credit (id, item_id, person_id, kind, job, position, created_at, modified_at) VALUES
  (uid(1), uid(101), uid(1), 'cast', NULL, 0, 0, 0), (uid(2), uid(101), uid(2), 'crew', 'Director', 0, 0, 0),
  (uid(3), uid(102), uid(3), 'cast', NULL, 0, 0, 0);
INSERT INTO edition (id, item_id, created_at, modified_at) VALUES (uid(1), uid(101), 0, 0), (uid(2), uid(302), 0, 0), (uid(3), uid(303), 0, 0);
INSERT INTO media_file (id, path, size_bytes, file_modified_at, duration_ms, created_at, modified_at) VALUES
  (uid(1), '/m/toy.mkv', 1, 0, 4860000, 0, 0), (uid(2), '/s/airbag.flac', 1, 0, 284000, 0, 0), (uid(3), '/s/paranoid.mp3', 1, 0, 383000, 0, 0);
INSERT INTO edition_file (edition_id, media_file_id) VALUES (uid(1), uid(1)), (uid(2), uid(2)), (uid(3), uid(3));
INSERT INTO track (id, media_file_id, stream_index, kind, codec, width, height, hdr_format, created_at, modified_at) VALUES
  (uid(1), uid(1), 0, 'video', 'hevc', 3840, 1606, 'hdr10', 0, 0), (uid(2), uid(2), 0, 'audio', 'flac', NULL, NULL, NULL, 0, 0),
  (uid(3), uid(3), 0, 'audio', 'mp3', NULL, NULL, NULL, 0, 0);
INSERT INTO watch_state (account_id, item_id, watched, play_count, changed_at) VALUES (uid(10), uid(101), 1, 2, 0), (uid(11), uid(102), 1, 1, 0);
INSERT INTO favorite (account_id, item_id, created_at) VALUES (uid(10), uid(200), 0), (uid(10), uid(301), 0);
`

func open(t *testing.T) *sql.DB {
	t.Helper()
	d := dbtest.OpenPreferLocal(t)
	exec(t, d, library)
	return d
}

func exec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func str(s string) *string { return &s }
func num(n int64) *int64   { return &n }
func ids(ns ...int) []uid.ID {
	out := []uid.ID{}
	for _, n := range ns {
		out = append(out, uid.N(n))
	}
	return out
}

// one is the rules of a single condition.
func one(field, op string, text *string, number *int64) Rules {
	return Rules{Match: All, Groups: []Group{{Match: All, Conditions: []Condition{{Field: field, Operator: op, Text: text, Number: number}}}}}
}

// matches is what r selects for a list of kind owned by Ada, in order.
func matches(t *testing.T, d *sql.DB, kind string, r Rules) []uid.ID {
	t.Helper()
	if err := Validate(kind, &r); err != nil {
		t.Fatal(err)
	}
	q, args := compile(kind, r)
	rows, err := d.Query(q, append([]any{uid.N(10)}, args...)...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	out := []uid.ID{}
	for rows.Next() {
		var id uid.ID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every operator of every field type, with a missing value and a list
// field each way. Movies come by sort title: Amélie, The Matrix (sorting as
// Matrix), Toy Story, Untitled.
func TestOperatorsByFieldType(t *testing.T) {
	d := open(t)
	for _, c := range []struct {
		name   string
		kind   string
		r      Rules
		expect []uid.ID
	}{
		{"text is ignores ASCII case", "movies", one("title", "is", str("toy STORY"), nil), ids(101)},
		{"text is keeps other letters' case", "movies", one("title", "is", str("AMÉLIE"), nil), ids()},
		{"text is counts accents", "movies", one("title", "is", str("Amelie"), nil), ids()},
		{"text is the shown title", "movies", one("title", "is", str("amélie"), nil), ids(100)},
		{"text isNot, a missing value too", "movies", one("contentRating", "isNot", str("r"), nil), ids(101, 103)},
		{"text contains", "movies", one("title", "contains", str("MATRIX"), nil), ids(102)},
		{"text notContains", "movies", one("title", "notContains", str("a"), nil), ids(101, 103)},
		{"text startsWith", "movies", one("title", "startsWith", str("the"), nil), ids(102)},
		{"text startsWith only at the start", "movies", one("title", "startsWith", str("matrix"), nil), ids()},
		{"text endsWith", "movies", one("title", "endsWith", str("STORY"), nil), ids(101)},
		{"text endsWith longer than the title", "movies", one("title", "endsWith", str("a far longer value than Toy Story"), nil), ids()},
		{"no wildcard", "movies", one("title", "contains", str("%"), nil), ids()},
		{"no single wildcard", "movies", one("title", "contains", str("_"), nil), ids()},
		{"list is any", "movies", one("genre", "is", str("comedy"), nil), ids(101)},
		{"list isNot none, an empty list too", "movies", one("genre", "isNot", str("Comedy"), nil), ids(100, 102, 103)},
		{"list notContains", "movies", one("genre", "notContains", str("ACT"), nil), ids(100, 101, 103)},
		{"list startsWith", "movies", one("genre", "startsWith", str("anim"), nil), ids(101)},
		{"list endsWith", "movies", one("genre", "endsWith", str("tion"), nil), ids(102, 101)},
		{"cast contains", "movies", one("cast", "contains", str("hanks"), nil), ids(101)},
		{"director is", "movies", one("director", "is", str("john lasseter"), nil), ids(101)},
		{"director is not the cast", "movies", one("director", "is", str("Tom Hanks"), nil), ids()},
		{"director isNot", "movies", one("director", "isNot", str("John Lasseter"), nil), ids(100, 102, 103)},
		{"number is", "movies", one("year", "is", nil, num(1999)), ids(102)},
		{"number is the release year over the parsed one", "movies", one("year", "is", nil, num(1998)), ids()},
		{"number isNot, a missing value too", "movies", one("year", "isNot", nil, num(1999)), ids(100, 101, 103)},
		{"number lt", "movies", one("year", "lt", nil, num(2000)), ids(102, 101)},
		{"number le", "movies", one("runtime", "le", nil, num(81)), ids(101)},
		{"number gt", "movies", one("runtime", "gt", nil, num(100)), ids(100, 102)},
		{"number ge", "movies", one("year", "ge", nil, num(2001)), ids(100)},
		{"play count is the owner's", "movies", one("playCount", "gt", nil, num(0)), ids(101)},
		{"no plays is zero", "movies", one("playCount", "is", nil, num(0)), ids(100, 102, 103)},
		{"boolean isTrue, the owner's", "movies", one("watched", "isTrue", nil, nil), ids(101)},
		{"boolean isFalse, a missing value too", "movies", one("watched", "isFalse", nil, nil), ids(100, 102, 103)},
		{"hdr", "movies", one("hdr", "isTrue", nil, nil), ids(101)},
		{"4K", "movies", one("uhd", "isTrue", nil, nil), ids(101)},
		{"not 4K", "movies", one("uhd", "isFalse", nil, nil), ids(100, 102, 103)},
		{"favorite movie", "movies", one("favorite", "isTrue", nil, nil), ids()},
		{"collection field", Collection, one("title", "contains", str("t"), nil), ids(101, 102, 103)},
		{"episodes in play order, specials last", "tv", one("show", "is", str("BLUEY"), nil), ids(202, 203, 205)},
		{"episode title", "tv", one("title", "startsWith", str("hosp"), nil), ids(203)},
		{"season", "tv", one("season", "is", nil, num(1)), ids(202, 203)},
		{"episode number", "tv", one("episode", "ge", nil, num(2)), ids(203)},
		{"show's rating", "tv", one("contentRating", "is", str("tv-y"), nil), ids(202, 203, 205)},
		{"show's genre", "tv", one("genre", "is", str("animation"), nil), ids(202, 203, 205)},
		{"a starred show stars its episodes", "tv", one("favorite", "isTrue", nil, nil), ids(202, 203, 205)},
		{"unwatched episodes", "tv", one("watched", "isFalse", nil, nil), ids(202, 203, 205)},
		{"songs by track", "music", one("artist", "is", str("radiohead"), nil), ids(302, 303)},
		{"album", "music", one("album", "contains", str("computer"), nil), ids(302, 303)},
		{"song title", "music", one("title", "startsWith", str("PARA"), nil), ids(303)},
		{"album's year", "music", one("year", "is", nil, num(1997)), ids(302, 303)},
		{"album's genre", "music", one("genre", "is", str("rock"), nil), ids(302, 303)},
		{"duration", "music", one("duration", "gt", nil, num(300)), ids(303)},
		{"explicit", "music", one("explicit", "isTrue", nil, nil), ids(303)},
		{"not explicit", "music", one("explicit", "isFalse", nil, nil), ids(302)},
		{"lossless", "music", one("lossless", "isTrue", nil, nil), ids(302)},
		{"a starred album stars its songs", "music", one("favorite", "isTrue", nil, nil), ids(302, 303)},
		{"unplayed songs", "music", one("playCount", "is", nil, num(0)), ids(302, 303)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := matches(t, d, c.kind, c.r)
			if c.kind == Collection {
				slices.SortFunc(got, func(a, b uid.ID) int { return strings.Compare(a.String(), b.String()) })
			}
			if !slices.Equal(got, c.expect) {
				t.Errorf("got %v, want %v", got, c.expect)
			}
		})
	}
}

// Groups combine by the rules' match and conditions by their group's.
func TestAllAndAny(t *testing.T) {
	d := open(t)
	comedyOrMatrix := Group{Match: Any, Conditions: []Condition{{Field: "genre", Operator: "is", Text: str("Comedy")},
		{Field: "title", Operator: "contains", Text: str("matrix")}}}
	unwatched := Group{Match: All, Conditions: []Condition{{Field: "watched", Operator: "isFalse"}}}
	if got := matches(t, d, "movies", Rules{Match: All, Groups: []Group{comedyOrMatrix, unwatched}}); !slices.Equal(got, ids(102)) {
		t.Errorf("all: %v", got)
	}
	if got := matches(t, d, "movies", Rules{Match: Any, Groups: []Group{comedyOrMatrix, unwatched}}); !slices.Equal(got, ids(100, 102, 101, 103)) {
		t.Errorf("any: %v", got)
	}
	both := Group{Match: All, Conditions: comedyOrMatrix.Conditions}
	if got := matches(t, d, "movies", Rules{Match: All, Groups: []Group{both}}); !slices.Equal(got, ids()) {
		t.Errorf("group all: %v", got)
	}
}

// A value is only ever a parameter: SQL in it matches as text.
func TestAValueIsNeverSQL(t *testing.T) {
	d := open(t)
	for _, v := range []string{`'); DROP TABLE item; --`, `" OR 1=1 --`, `x' OR '1'='1`} {
		if got := matches(t, d, "movies", one("title", "contains", str(v), nil)); len(got) != 0 {
			t.Errorf("%q matched %v", v, got)
		}
		if got := matches(t, d, "movies", one("title", "notContains", str(v), nil)); len(got) != 4 {
			t.Errorf("not %q matched %v", v, got)
		}
	}
	exec(t, d, `INSERT INTO item (id, library_id, kind, parsed_title, added_at, created_at, modified_at)
		VALUES (uid(104), uid(1), 'movie', 'x'' OR ''1''=''1', 0, 0, 0)`)
	if got := matches(t, d, "movies", one("title", "is", str(`x' OR '1'='1`), nil)); !slices.Equal(got, ids(104)) {
		t.Errorf("a title of SQL: %v", got)
	}
}

func TestValidateNamesTheFault(t *testing.T) {
	cond := func(c Condition) Rules {
		return Rules{Match: All, Groups: []Group{{Match: Any, Conditions: []Condition{{Field: "title", Operator: "is", Text: str("x")}, c}}}}
	}
	many := Rules{Match: All}
	for range 3 {
		g := Group{Match: All}
		for range 20 {
			g.Conditions = append(g.Conditions, Condition{Field: "hdr", Operator: "isTrue"})
		}
		many.Groups = append(many.Groups, g)
	}
	for _, c := range []struct {
		kind, field string
		r           Rules
	}{
		{"movies", "match", Rules{Match: "most", Groups: []Group{{Match: All, Conditions: []Condition{{Field: "hdr", Operator: "isTrue"}}}}}},
		{"movies", "groups", Rules{Match: All}},
		{"movies", "groups", many},
		{"movies", "groups.0.match", Rules{Match: All, Groups: []Group{{Match: "", Conditions: []Condition{{Field: "hdr", Operator: "isTrue"}}}}}},
		{"movies", "groups.0.conditions", Rules{Match: All, Groups: []Group{{Match: All}}}},
		{"movies", "groups.0.conditions.1.field", cond(Condition{Field: "artist", Operator: "is", Text: str("x")})},
		{"music", "groups.0.conditions.1.field", cond(Condition{Field: "watched", Operator: "isTrue"})},
		{Collection, "groups.0.conditions.1.field", cond(Condition{Field: "watched", Operator: "isTrue"})},
		{Collection, "groups.0.conditions.1.field", cond(Condition{Field: "favorite", Operator: "isTrue"})},
		{Collection, "groups.0.conditions.1.field", cond(Condition{Field: "playCount", Operator: "is", Number: num(0)})},
		{"movies", "groups.0.conditions.1.operator", cond(Condition{Field: "year", Operator: "contains", Text: str("19")})},
		{"movies", "groups.0.conditions.1.operator", cond(Condition{Field: "title", Operator: "lt", Number: num(1)})},
		{"movies", "groups.0.conditions.1.operator", cond(Condition{Field: "hdr", Operator: "is", Number: num(1)})},
		{"movies", "groups.0.conditions.1.text", cond(Condition{Field: "title", Operator: "contains", Text: str("  ")})},
		{"movies", "groups.0.conditions.1.text", cond(Condition{Field: "title", Operator: "contains"})},
		{"movies", "groups.0.conditions.1.text", cond(Condition{Field: "title", Operator: "is", Text: str(strings.Repeat("é", 201))})},
		{"movies", "groups.0.conditions.1.number", cond(Condition{Field: "title", Operator: "is", Text: str("x"), Number: num(1)})},
		{"movies", "groups.0.conditions.1.number", cond(Condition{Field: "year", Operator: "is"})},
		{"movies", "groups.0.conditions.1.text", cond(Condition{Field: "year", Operator: "is", Text: str("1999")})},
		{"movies", "groups.0.conditions.1.text", cond(Condition{Field: "hdr", Operator: "isTrue", Text: str("yes")})},
		{"movies", "groups.0.conditions.1.number", cond(Condition{Field: "hdr", Operator: "isFalse", Number: num(1)})},
	} {
		err := Validate(c.kind, &c.r)
		var bad *Invalid
		if !errors.As(err, &bad) || bad.Field != c.field {
			t.Errorf("%s %+v: %v, want a fault at %s", c.kind, c.r, err, c.field)
		}
	}
	r := one("title", "is", str("  Toy Story \t"), nil)
	if err := Validate("movies", &r); err != nil || *r.Groups[0].Conditions[0].Text != "Toy Story" {
		t.Errorf("trimmed %q, %v", *r.Groups[0].Conditions[0].Text, err)
	}
	r = one("title", "is", str(strings.Repeat("é", 200)), nil)
	if err := Validate("movies", &r); err != nil {
		t.Errorf("200 characters: %v", err)
	}
}

// entries is the playlist's entries as item -> entry id and rank, in order.
func entries(t *testing.T, d *sql.DB, playlistID uid.ID) (items []uid.ID, byItem map[uid.ID][2]string) {
	t.Helper()
	rows, err := d.Query(`SELECT item_id, id, rank, edition_id IS NULL FROM playlist_entry WHERE playlist_id = ? ORDER BY rank, id`, playlistID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	byItem = map[uid.ID][2]string{}
	items = []uid.ID{}
	for rows.Next() {
		var it, id uid.ID
		var rank string
		var bare bool
		if err := rows.Scan(&it, &id, &rank, &bare); err != nil {
			t.Fatal(err)
		}
		items, byItem[it] = append(items, it), [2]string{id.String(), rank}
	}
	return items, byItem
}

func playlistOf(t *testing.T, d *sql.DB, id int, kind string) *playlist.Playlist {
	t.Helper()
	exec(t, d, `INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (uid(?), uid(10), 'Smart', ?, 0, 0)`, id, kind)
	p, err := playlist.New(d).Get(uid.N(id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Applying the rules again keeps each staying entry's id and rank, ranks a
// new title between its neighbours, drops a title that no longer matches,
// and ranks every entry anew, keeping their ids, when the order changes.
func TestApplyingAgainKeepsEntries(t *testing.T) {
	d := open(t)
	s := New(d)
	p := playlistOf(t, d, 500, "movies")
	// A manual entry with a chosen edition stays; its duplicate goes.
	exec(t, d, `INSERT INTO playlist_entry (id, playlist_id, item_id, edition_id, rank, added_at, created_at, modified_at) VALUES
		(uid(600), uid(500), uid(101), uid(1), 'a0', 7, 0, 0), (uid(601), uid(500), uid(101), NULL, 'a1', 0, 0, 0)`)
	all := one("genre", "isNot", str("Horror"), nil)
	if err := s.PutPlaylist(p, &all, &p.AccountID); err != nil {
		t.Fatal(err)
	}
	got, first := entries(t, d, p.ID)
	if !slices.Equal(got, ids(100, 102, 101, 103)) {
		t.Fatalf("entries %v", got)
	}
	if first[uid.N(101)][0] != uid.N(600).String() {
		t.Errorf("the manual entry did not stay: %v", first[uid.N(101)])
	}
	var ed uid.ID
	var added int64
	if err := d.QueryRow(`SELECT edition_id, added_at FROM playlist_entry WHERE id = uid(600)`).Scan(&ed, &added); err != nil || ed != uid.N(1) || added != 7 {
		t.Errorf("kept entry edition %v added %d: %v", ed, added, err)
	}

	exec(t, d, `INSERT INTO item (id, library_id, kind, parsed_title, added_at, created_at, modified_at) VALUES
		(uid(104), uid(1), 'movie', 'Brazil', 0, 0, 0), (uid(105), uid(1), 'movie', 'Zelig', 0, 0, 0)`)
	exec(t, d, `DELETE FROM item WHERE id = uid(102)`)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, then := entries(t, d, p.ID)
	if !slices.Equal(got, ids(100, 104, 101, 103, 105)) {
		t.Fatalf("after changes %v", got)
	}
	for _, n := range []int{100, 101, 103} {
		if then[uid.N(n)] != first[uid.N(n)] {
			t.Errorf("entry of %d moved: %v, was %v", n, then[uid.N(n)], first[uid.N(n)])
		}
	}
	if b := then[uid.N(104)][1]; b <= first[uid.N(100)][1] || b >= first[uid.N(101)][1] {
		t.Errorf("Brazil ranked %s, not between %s and %s", b, first[uid.N(100)][1], first[uid.N(101)][1])
	}

	exec(t, d, `UPDATE item_metadata SET title = 'Zzz' WHERE item_id = uid(100)`)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, last := entries(t, d, p.ID)
	if !slices.Equal(got, ids(104, 101, 103, 105, 100)) {
		t.Fatalf("after a retitle %v", got)
	}
	for _, n := range []int{100, 101, 103, 104, 105} {
		if last[uid.N(n)][0] != then[uid.N(n)][0] {
			t.Errorf("entry of %d changed id", n)
		}
	}
	if sum, err := s.Run(context.Background()); err != nil || sum != "0 of 1 smart lists changed" {
		t.Errorf("a run with nothing new: %q %v", sum, err)
	}
}

// watched, playCount and favorite read the owner's, whoever else watched.
func TestPersonalFieldsReadTheOwner(t *testing.T) {
	d := open(t)
	p := playlistOf(t, d, 500, "movies")
	exec(t, d, `INSERT INTO playlist_share (playlist_id, account_id, access, created_at) VALUES (uid(500), uid(11), 'read', 0)`)
	r := one("watched", "isFalse", nil, nil)
	if err := New(d).PutPlaylist(p, &r, &p.AccountID); err != nil {
		t.Fatal(err)
	}
	if got, _ := entries(t, d, p.ID); !slices.Equal(got, ids(100, 102, 103)) {
		t.Errorf("entries %v", got)
	}
	exec(t, d, `UPDATE playlist SET account_id = uid(11) WHERE id = uid(500)`)
	if _, err := New(d).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := entries(t, d, p.ID); !slices.Equal(got, ids(100, 101, 103)) {
		t.Errorf("entries for the new owner %v", got)
	}
}

// A smart collection holds what its rules match in release order, its hand
// order cleared.
func TestASmartCollection(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO household (id, name, adult_id, created_at, modified_at) VALUES (uid(20), 'Home', uid(10), 0, 0)`)
	exec(t, d, `INSERT INTO collection (id, source, household_id, name, created_at, modified_at) VALUES (uid(400), 'custom', uid(20), 'R', 0, 0)`)
	exec(t, d, `INSERT INTO collection_movie (collection_id, item_id, position) VALUES (uid(400), uid(101), 0), (uid(400), uid(100), 1)`)
	c := &struct{ ID uid.ID }{uid.N(400)}
	r := one("contentRating", "is", str("R"), nil)
	if err := New(d).put(list{collectionID: &c.ID, kind: Collection}, &r, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(`SELECT item_id, position IS NULL FROM collection_movie WHERE collection_id = uid(400) ORDER BY item_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var id uid.ID
		var bare bool
		if err := rows.Scan(&id, &bare); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprint(id, bare))
	}
	if want := []string{fmt.Sprint(uid.N(100), true), fmt.Sprint(uid.N(102), true)}; !slices.Equal(got, want) {
		t.Errorf("movies %v, want %v", got, want)
	}
	back, err := New(d).Rules(nil, &c.ID)
	if err != nil || back.Match != All || len(back.Groups) != 1 || *back.Groups[0].Conditions[0].Text != "R" {
		t.Errorf("rules %+v %v", back, err)
	}
}

// Rules read back as written, and a second put replaces them.
func TestRulesReadBack(t *testing.T) {
	d := open(t)
	p := playlistOf(t, d, 500, "music")
	s := New(d)
	if _, err := s.Rules(&p.ID, nil); err == nil {
		t.Fatal("rules of a manual playlist")
	}
	in := Rules{Match: Any, Groups: []Group{
		{Match: All, Conditions: []Condition{{Field: "artist", Operator: "is", Text: str("Radiohead")}, {Field: "explicit", Operator: "isTrue"}}},
		{Match: Any, Conditions: []Condition{{Field: "duration", Operator: "lt", Number: num(290)}}},
	}}
	if err := s.PutPlaylist(p, &in, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := entries(t, d, p.ID); !slices.Equal(got, ids(302, 303)) {
		t.Errorf("entries %v", got)
	}
	back, err := s.Rules(&p.ID, nil)
	if err != nil || fmt.Sprintf("%+v", deref(*back)) != fmt.Sprintf("%+v", deref(in)) {
		t.Errorf("read back %+v, %v", deref(*back), err)
	}
	again := one("title", "is", str("Airbag"), nil)
	if err := s.PutPlaylist(p, &again, nil); err != nil {
		t.Fatal(err)
	}
	if back, _ := s.Rules(&p.ID, nil); len(back.Groups) != 1 || len(back.Groups[0].Conditions) != 1 {
		t.Errorf("replaced %+v", back)
	}
	if got, _ := entries(t, d, p.ID); !slices.Equal(got, ids(302)) {
		t.Errorf("entries %v", got)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM smart_condition`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d conditions stored", n)
	}
}

// deref shows a rule's values rather than their addresses.
func deref(r Rules) string {
	var b strings.Builder
	fmt.Fprint(&b, r.Match)
	for _, g := range r.Groups {
		fmt.Fprint(&b, " [", g.Match)
		for _, c := range g.Conditions {
			fmt.Fprint(&b, " ", c.Field, " ", c.Operator)
			if c.Text != nil {
				fmt.Fprintf(&b, " %q", *c.Text)
			}
			if c.Number != nil {
				fmt.Fprint(&b, " ", *c.Number)
			}
		}
		fmt.Fprint(&b, "]")
	}
	return b.String()
}

// A list whose stored rules fail keeps its entries; the others still apply
// and the run fails naming it.
func TestARunGoesOnPastAFailingList(t *testing.T) {
	d := open(t)
	s := New(d)
	good, bad := playlistOf(t, d, 500, "movies"), playlistOf(t, d, 501, "movies")
	r := one("hdr", "isTrue", nil, nil)
	for _, p := range []*playlist.Playlist{good, bad} {
		if err := s.PutPlaylist(p, &r, nil); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, d, `UPDATE smart_condition SET field = 'gone' WHERE group_id IN (SELECT id FROM smart_group WHERE list_id =
		(SELECT id FROM smart_list WHERE playlist_id = uid(501)))`)
	exec(t, d, `INSERT INTO edition (id, item_id, created_at, modified_at) VALUES (uid(9), uid(102), 0, 0)`)
	exec(t, d, `INSERT INTO edition_file (edition_id, media_file_id) VALUES (uid(9), uid(1))`)
	_, err := s.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 smart lists failed") {
		t.Errorf("run: %v", err)
	}
	if got, _ := entries(t, d, good.ID); !slices.Equal(got, ids(102, 101)) {
		t.Errorf("good list %v", got)
	}
	if got, _ := entries(t, d, bad.ID); !slices.Equal(got, ids(101)) {
		t.Errorf("failing list %v", got)
	}
}

// Every write to what a rule reads makes the Smart lists task due, and a
// write that changes none of it does not.
func TestEveryInputMakesTheTaskDue(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (uid(500), uid(10), 'P', 'movies', 0, 0)`)
	exec(t, d, `INSERT INTO task (id, name, kind, enabled, next_run_at, created_at, modified_at) VALUES (uid(1), 'Smart lists', 'smart', 1, NULL, 0, 0)`)
	due := func(q string) bool {
		t.Helper()
		exec(t, d, `UPDATE task SET next_run_at = NULL`)
		exec(t, d, q)
		var at sql.NullInt64
		if err := d.QueryRow(`SELECT next_run_at FROM task`).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at.Valid && at.Int64 == 0
	}
	for _, q := range []string{
		`INSERT INTO item (id, library_id, kind, parsed_title, added_at, created_at, modified_at) VALUES (uid(104), uid(1), 'movie', 'New', 0, 0, 0)`,
		`UPDATE item SET parent_id = uid(204) WHERE id = uid(203)`,
		`UPDATE item SET kind = 'show' WHERE id = uid(104)`,
		`UPDATE item SET parsed_title = 'Renamed' WHERE id = uid(103)`,
		`UPDATE item SET parsed_year = 2000 WHERE id = uid(103)`,
		`UPDATE item SET season_number = 2 WHERE id = uid(201)`,
		`UPDATE item SET episode_number = 3 WHERE id = uid(203)`,
		`UPDATE item SET disc_number = 2 WHERE id = uid(302)`,
		`UPDATE item SET track_number = 3 WHERE id = uid(302)`,
		`UPDATE item SET artist_id = uid(300) WHERE id = uid(302)`,
		`UPDATE item SET sort_title = 'Z' WHERE id = uid(300)`,
		`UPDATE item SET explicit = 1 WHERE id = uid(302)`,
		`DELETE FROM item WHERE id = uid(104)`,
		`INSERT INTO item_metadata (item_id, source, title) VALUES (uid(103), 'local', 'Named')`,
		`UPDATE item_metadata SET title = 'Other' WHERE item_id = uid(103)`,
		`UPDATE item_metadata SET release_date = '2000-01-01' WHERE item_id = uid(103)`,
		`UPDATE item_metadata SET runtime_minutes = 90 WHERE item_id = uid(103)`,
		`UPDATE item_metadata SET content_rating = 'PG' WHERE item_id = uid(103)`,
		`DELETE FROM item_metadata WHERE item_id = uid(103)`,
		`INSERT INTO item_genre (item_id, genre_id, source) VALUES (uid(103), uid(3), 'local')`,
		`UPDATE item_genre SET genre_id = uid(4) WHERE item_id = uid(103)`,
		`DELETE FROM item_genre WHERE item_id = uid(103)`,
		`UPDATE genre SET name = 'Comedies' WHERE id = uid(3)`,
		`INSERT INTO credit (id, item_id, person_id, kind, position, created_at, modified_at) VALUES (uid(9), uid(103), uid(1), 'cast', 0, 0, 0)`,
		`UPDATE credit SET job = 'Director', kind = 'crew' WHERE id = uid(9)`,
		`UPDATE credit SET person_id = uid(2) WHERE id = uid(9)`,
		`DELETE FROM credit WHERE id = uid(9)`,
		`UPDATE person SET name = 'Tom' WHERE id = uid(1)`,
		`INSERT INTO edition (id, item_id, created_at, modified_at) VALUES (uid(9), uid(103), 0, 0)`,
		`UPDATE edition SET runtime_ms = 1000 WHERE id = uid(9)`,
		`INSERT INTO edition_file (edition_id, media_file_id) VALUES (uid(9), uid(1))`,
		`DELETE FROM edition_file WHERE edition_id = uid(9)`,
		`DELETE FROM edition WHERE id = uid(9)`,
		`UPDATE media_file SET missing_at = 5 WHERE id = uid(1)`,
		`UPDATE media_file SET missing_at = NULL WHERE id = uid(1)`,
		`UPDATE media_file SET duration_ms = 5 WHERE id = uid(2)`,
		`INSERT INTO track (id, media_file_id, stream_index, kind, codec, created_at, modified_at) VALUES (uid(9), uid(1), 1, 'audio', 'aac', 0, 0)`,
		`UPDATE track SET codec = 'flac' WHERE id = uid(9)`,
		`UPDATE track SET kind = 'subtitle' WHERE id = uid(9)`,
		`UPDATE track SET width = 1920 WHERE id = uid(1)`,
		`UPDATE track SET height = 1080 WHERE id = uid(1)`,
		`UPDATE track SET hdr_format = NULL WHERE id = uid(1)`,
		`DELETE FROM track WHERE id = uid(9)`,
		`UPDATE server_info SET metadata_mode = 'only'`,
		`UPDATE server_info SET metadata_source = 'remote'`,
		`INSERT INTO watch_state (account_id, item_id, changed_at) VALUES (uid(10), uid(103), 0)`,
		`UPDATE watch_state SET watched = 1 WHERE account_id = uid(10) AND item_id = uid(103)`,
		`UPDATE watch_state SET play_count = 3 WHERE account_id = uid(10) AND item_id = uid(103)`,
		`DELETE FROM watch_state WHERE account_id = uid(10) AND item_id = uid(103)`,
		`INSERT INTO favorite (account_id, item_id, created_at) VALUES (uid(10), uid(103), 0)`,
		`DELETE FROM favorite WHERE account_id = uid(10) AND item_id = uid(103)`,
		`UPDATE playlist SET account_id = uid(11) WHERE id = uid(500)`,
	} {
		if !due(q) {
			t.Errorf("not due after %s", q)
		}
	}
	for _, q := range []string{
		`UPDATE item SET modified_at = 5, kids_content = 1, path = '/x' WHERE id = uid(100)`,
		`UPDATE item SET parsed_title = parsed_title, sort_title = sort_title`,
		`UPDATE item SET sync_version = sync_version + 1`,
		`UPDATE item_metadata SET overview = 'Words', poster_path = '/p.jpg' WHERE item_id = uid(100)`,
		`UPDATE watch_state SET position_ms = 5000, last_played_at = 9, changed_at = 9`,
		`UPDATE media_file SET probed_at = 7, missing_at = NULL WHERE id = uid(1)`,
		`UPDATE track SET language = 'fre', bitrate = 9 WHERE id = uid(1)`,
		`UPDATE playlist SET name = 'Q', last_played_at = 5 WHERE id = uid(500)`,
		`UPDATE server_info SET tasks_paused = 1`,
		`INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at, created_at, modified_at) VALUES (uid(9), uid(500), uid(100), 'a0', 0, 0, 0)`,
	} {
		if due(q) {
			t.Errorf("due after %s", q)
		}
	}
}

package replica_test

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// fixture has adults with and without hide-kids, kids on ratings whose
// movie and TV ceilings rank oppositely, kids on different allow lists, a
// ratings kid with an allow list that must not count, and catalog, music
// and YouTube items on every side of each rule.
const fixture = `
INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (uid(101), 'adult', 'Ada', 0, 0);
INSERT INTO household (id, name, adult_id, created_at, modified_at) VALUES (uid(201), 'Home', uid(101), 0, 0);
UPDATE account SET household_id = uid(201) WHERE id = uid(101);
INSERT INTO account (id, kind, name, household_id, hide_kids_content, filter_mode, movie_ceiling, tv_ceiling, pin_hash, created_at, modified_at) VALUES
  (uid(102), 'adult', 'Bo', uid(201), 1, 'ratings', NULL, NULL, 'secret', 0, 0),
  (uid(103), 'kid', 'Cy', uid(201), 0, 'ratings', 'PG', 'TV-Y7', NULL, 0, 0),
  (uid(104), 'kid', 'Di', uid(201), 0, 'ratings', 'R', 'TV-Y', NULL, 0, 0),
  (uid(105), 'kid', 'Ed', uid(201), 0, 'ratings', NULL, NULL, NULL, 0, 0),
  (uid(106), 'kid', 'Fa', uid(201), 0, 'allow_list', 'NC-17', 'TV-MA', NULL, 0, 0),
  (uid(107), 'kid', 'Gu', uid(201), 1, 'allow_list', NULL, NULL, 'secret', 0, 0);
INSERT INTO library (id, name, kind, created_at, modified_at) VALUES
  (uid(1), 'Movies', 'movies', 0, 0), (uid(2), 'TV', 'tv', 0, 0), (uid(3), 'Music', 'music', 0, 0), (uid(4), 'YouTube', 'youtube', 0, 0);
INSERT INTO item (id, library_id, parent_id, kind, path, parsed_title, sort_title, kids_content, explicit, artist_id, added_at, created_at, modified_at) VALUES
  (uid(1), uid(1), NULL, 'movie', '/m/1', 'G Movie', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(2), uid(1), NULL, 'movie', '/m/2', 'PG Movie', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(3), uid(1), NULL, 'movie', '/m/3', 'PG-13 Movie', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(4), uid(1), NULL, 'movie', '/m/4', 'R Movie', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(5), uid(1), NULL, 'movie', '/m/5', 'NC-17 Movie', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(6), uid(1), NULL, 'movie', '/m/6', 'Unrated', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(7), uid(1), NULL, 'movie', '/m/7', 'Unrated Kids', NULL, 1, NULL, NULL, 0, 0, 0),
  (uid(8), uid(1), NULL, 'movie', '/m/8', 'G Not Kids', NULL, 0, NULL, NULL, 0, 0, 0),
  (uid(9), uid(1), NULL, 'movie', '/m/9', 'R Kids', NULL, 1, NULL, NULL, 0, 0, 0),
  (uid(10), uid(2), NULL, 'show', '/t/10', 'Tots', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(11), uid(2), uid(10), 'season', '/t/10/1', 'Tots', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(12), uid(2), uid(11), 'episode', NULL, 'Tots', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(13), uid(2), uid(11), 'episode', NULL, 'Tots', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(14), uid(2), NULL, 'show', '/t/14', 'Teen', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(15), uid(2), uid(14), 'season', '/t/14/1', 'Teen', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(16), uid(2), uid(15), 'episode', NULL, 'Teen', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(17), uid(2), NULL, 'show', '/t/17', 'Family', NULL, 1, NULL, NULL, 0, 0, 0),
  (uid(18), uid(2), uid(17), 'season', '/t/17/1', 'Family', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(19), uid(2), uid(18), 'episode', NULL, 'Family', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(20), uid(2), NULL, 'show', '/t/20', 'General', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(21), uid(2), uid(20), 'season', '/t/20/1', 'General', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(22), uid(2), uid(21), 'episode', NULL, 'General', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(23), uid(2), NULL, 'show', '/t/23', 'Unrated Show', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(24), uid(2), uid(23), 'season', '/t/23/1', 'Unrated Show', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(25), uid(2), uid(24), 'episode', NULL, 'Unrated Show', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(30), uid(3), NULL, 'artist', NULL, 'The Clean Band', 'Clean Band', NULL, NULL, NULL, 0, 0, 0),
  (uid(31), uid(3), uid(30), 'album', NULL, 'Mixed', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(32), uid(3), uid(31), 'song', NULL, 'Clean One', NULL, NULL, 0, NULL, 0, 0, 0),
  (uid(33), uid(3), uid(31), 'song', NULL, 'Rude One', NULL, NULL, 1, NULL, 0, 0, 0),
  (uid(34), uid(3), NULL, 'artist', NULL, 'Rude Crew', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(35), uid(3), uid(34), 'album', NULL, 'All Rude', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(36), uid(3), uid(35), 'song', NULL, 'Rude Two', NULL, NULL, 1, NULL, 0, 0, 0),
  (uid(38), uid(3), NULL, 'artist', NULL, 'Guest', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(37), uid(3), uid(31), 'song', NULL, 'Guest Clean', NULL, NULL, 0, uid(38), 0, 0, 0),
  (uid(39), uid(3), uid(35), 'song', NULL, 'Unprobed', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(40), uid(3), NULL, 'artist', NULL, 'Guest Rude', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(41), uid(3), uid(31), 'song', NULL, 'Guest Rude Song', NULL, NULL, 1, uid(40), 0, 0, 0),
  (uid(42), uid(3), uid(34), 'album', NULL, 'Lossless', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(43), uid(3), uid(42), 'song', NULL, 'Hi-Fi', NULL, NULL, 0, NULL, 0, 0, 0),
  (uid(50), uid(4), NULL, 'channel', NULL, 'Kids Channel', NULL, 1, NULL, NULL, 0, 0, 0),
  (uid(51), uid(4), uid(50), 'video', NULL, 'Kids Video', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(52), uid(4), NULL, 'channel', NULL, 'Grown Channel', NULL, 0, NULL, NULL, 0, 0, 0),
  (uid(53), uid(4), uid(52), 'video', NULL, 'Grown Video', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(54), uid(4), NULL, 'channel', NULL, 'Plain Channel', NULL, NULL, NULL, NULL, 0, 0, 0),
  (uid(55), uid(4), uid(54), 'video', NULL, 'Plain Video', NULL, NULL, NULL, NULL, 0, 0, 0);
UPDATE item SET parsed_year = 2001 WHERE id = uid(31);
UPDATE item SET parsed_year = 2004 WHERE id = uid(37);
INSERT INTO item_metadata (item_id, source, provider, content_rating) VALUES
  (uid(1), 'local', 'local', 'G'), (uid(2), 'local', 'local', 'PG'), (uid(3), 'local', 'local', 'PG-13'), (uid(4), 'local', 'local', 'R'),
  (uid(5), 'local', 'local', 'NC-17'), (uid(8), 'local', 'local', 'G'), (uid(9), 'local', 'local', 'R'), (uid(10), 'local', 'local', 'TV-Y'),
  (uid(14), 'local', 'local', 'TV-14'), (uid(17), 'local', 'local', 'TV-PG'), (uid(20), 'local', 'local', 'TV-G'), (uid(2), 'remote', 'tmdb', 'R');
INSERT INTO account_allow (account_id, item_id) VALUES (uid(106), uid(4)), (uid(106), uid(15)), (uid(106), uid(52)), (uid(107), uid(20)),
  (uid(107), uid(13)), (uid(107), uid(2)), (uid(107), uid(54)), (uid(103), uid(5));
INSERT INTO media_file (id, path, size_bytes, file_modified_at, duration_ms, missing_at, created_at, modified_at) VALUES
  (uid(1), '/a/1', 1, 0, 1000, NULL, 0, 0), (uid(2), '/a/2', 1, 0, 2000, NULL, 0, 0), (uid(3), '/a/3', 1, 0, 4000, 5, 0, 0),
  (uid(4), '/a/4', 1, 0, 8000, NULL, 0, 0), (uid(5), '/a/5', 1, 0, 16000, NULL, 0, 0), (uid(6), '/a/6', 1, 0, 32000, NULL, 0, 0),
  (uid(7), '/a/7', 1, 0, 64000, NULL, 0, 0);
INSERT INTO edition (id, item_id, created_at, modified_at) VALUES
  (uid(1), uid(32), 0, 0), (uid(2), uid(33), 0, 0), (uid(3), uid(37), 0, 0), (uid(4), uid(36), 0, 0), (uid(5), uid(41), 0, 0), (uid(6), uid(43), 0, 0);
INSERT INTO edition_file (edition_id, media_file_id) VALUES
  (uid(1), uid(1)), (uid(2), uid(2)), (uid(3), uid(3)), (uid(4), uid(4)), (uid(5), uid(5)), (uid(6), uid(6)), (uid(6), uid(7));
INSERT INTO track (id, media_file_id, stream_index, kind, codec, created_at, modified_at) VALUES
  (uid(1), uid(1), 0, 'audio', 'flac', 0, 0), (uid(2), uid(2), 0, 'audio', 'mp3', 0, 0), (uid(3), uid(3), 0, 'audio', 'flac', 0, 0),
  (uid(4), uid(4), 0, 'audio', 'alac', 0, 0), (uid(5), uid(5), 0, 'audio', 'flac', 0, 0), (uid(6), uid(6), 1, 'audio', 'pcm_s16le', 0, 0),
  (uid(7), uid(6), 0, 'video', 'mjpeg', 0, 0), (uid(8), uid(7), 0, 'audio', 'flac', 0, 0);
INSERT INTO occasion (id, name, month, day, days_before, days_after, created_at, modified_at) VALUES
  (uid(301), 'Snow', 1, 2, 3, 4, 0, 0), (uid(302), 'Sun', 7, 1, 0, 0, 0, 0);
INSERT INTO occasion_keyword (occasion_id, keyword_id) VALUES (uid(301), 900), (uid(302), 901);
INSERT INTO item_keyword (item_id, keyword_id) VALUES (uid(1), 900), (uid(10), 900), (uid(12), 900), (uid(4), 901), (uid(1), 901);
INSERT INTO occasion_exclusion (occasion_id, item_id) VALUES (uid(301), uid(10));
INSERT INTO occasion_item (occasion_id, item_id) VALUES (uid(301), uid(14)), (uid(302), uid(3));
INSERT INTO collection (id, source, household_id, name, created_at, modified_at) VALUES (uid(401), 'custom', uid(201), 'Picks', 0, 0);
INSERT INTO collection_movie (collection_id, item_id, position) VALUES (uid(401), uid(4), 0), (uid(401), uid(1), 1), (uid(401), uid(2), NULL);
INSERT INTO media_file (id, path, size_bytes, file_modified_at, duration_ms, created_at, modified_at) VALUES (uid(8), '/a/8', 1, 0, 90000, 0, 0);
INSERT INTO edition (id, item_id, runtime_ms, created_at, modified_at) VALUES
  (uid(7), uid(3), NULL, 0, 0), (uid(8), uid(5), NULL, 0, 0), (uid(9), uid(1), 7000, 0, 0);
INSERT INTO edition_file (edition_id, media_file_id) VALUES (uid(8), uid(8)), (uid(9), uid(8));
INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES
  (uid(501), uid(101), 'Mix', 'movies', 0, 0), (uid(502), uid(103), 'Tunes', 'music', 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, collection_id, edition_id, rank, added_at, created_at, modified_at) VALUES
  (uid(601), uid(501), uid(5), NULL, uid(8), 'a0', 0, 0, 0), (uid(602), uid(501), NULL, uid(401), NULL, 'a1', 0, 0, 0),
  (uid(603), uid(501), uid(3), NULL, uid(7), 'a2', 0, 0, 0), (uid(605), uid(501), uid(9), NULL, NULL, 'a2', 0, 0, 0),
  (uid(604), uid(501), uid(1), NULL, NULL, 'a2', 0, 0, 0), (uid(606), uid(501), uid(10), NULL, NULL, 'a3', 0, 0, 0),
  (uid(607), uid(501), uid(15), NULL, NULL, 'a4', 0, 0, 0), (uid(608), uid(501), uid(52), NULL, NULL, 'a5', 0, 0, 0),
  (uid(609), uid(501), uid(50), NULL, NULL, 'a6', 0, 0, 0), (uid(611), uid(502), uid(30), NULL, NULL, 'a0', 0, 0, 0),
  (uid(612), uid(502), uid(35), NULL, NULL, 'a1', 0, 0, 0), (uid(613), uid(502), uid(33), NULL, NULL, 'Zz', 0, 0, 0),
  (uid(614), uid(502), uid(38), NULL, NULL, 'a3', 0, 0, 0);
`

// replicaOf is household 201's snapshot of d, opened, so a view reading
// what a replica never holds fails.
func replicaOf(t *testing.T, d *sql.DB) *sql.DB {
	t.Helper()
	r, _ := snapshotOf(t, syncOf(t, d), uid.N(201))
	return r
}

func ids(t *testing.T, d *sql.DB, q string, args ...any) []uid.ID {
	return column[uid.ID](t, d, q, args...)
}

func column[T any](t *testing.T, d *sql.DB, q string, args ...any) []T {
	t.Helper()
	rows, err := d.Query(q, args...)
	out, err := db.Collect(rows, err, func(s db.Scanner) (T, error) {
		var v T
		return v, s.Scan(&v)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// rowsOf renders every row q answers, sorted, each as row renders values.
func rowsOf(t *testing.T, d *sql.DB, q string) []string {
	t.Helper()
	out := inOrder(t, d, q)
	slices.Sort(out)
	return out
}

// inOrder is rowsOf in the order q answers.
func inOrder(t *testing.T, d *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := d.Query(q, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, row(vals...))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// row renders values as SQLite answers them: a bool as 0 or 1, a nil
// pointer as NULL, an id, as a BLOB or a uid.ID, as its canonical string.
func row(vals ...any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case bool:
			v = 0
			if x {
				v = 1
			}
		case *int64:
			v = nil
			if x != nil {
				v = *x
			}
		case *int:
			v = nil
			if x != nil {
				v = *x
			}
		case *uid.ID:
			v = nil
			if x != nil {
				v = *x
			}
		case []byte:
			var id uid.ID
			if id.Scan(x) == nil {
				v = id
			}
		}
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, "|")
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatalf("%s: the fixture yields no rows", what)
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %v\nwant %v", what, got, want)
	}
}

func TestViewsAnswerAsTheServer(t *testing.T) {
	d := twoHouseholds(t)
	// 202's playlist of its own collection, shared with 201.
	mustExec(t, d, `INSERT INTO playlist_household_share (playlist_id, household_id, access, created_at) VALUES (uid(515), uid(201), 'read', 0)`)
	r := replicaOf(t, d)
	items, accounts := item.New(d), account.New(d)
	titles := ids(t, d, `SELECT id FROM item`)
	var profiles []*account.Account
	for _, id := range ids(t, r, `SELECT id FROM account`) {
		a, err := accounts.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, a)
	}

	t.Run("profile_audience", func(t *testing.T) {
		var want []string
		for _, a := range profiles {
			open, browse := service.AudienceFor(a).Args(), service.BrowseAudienceFor(a).Args()
			want = append(want, row(a.ID, open[0], browse[0], open[2], open[3], open[4]))
		}
		same(t, "profile_audience", rowsOf(t, r, `SELECT account_id, mode, browse_mode, allow_list, movie, tv FROM profile_audience`), want)
	})

	for view, audience := range map[string]func(*account.Account) item.Audience{
		"profile_item": service.BrowseAudienceFor, "profile_item_open": service.AudienceFor,
	} {
		t.Run(view, func(t *testing.T) {
			var want []string
			for _, a := range profiles {
				for _, n := range titles {
					ok, err := items.Visible(n, audience(a))
					if err != nil {
						t.Fatal(err)
					}
					if ok {
						want = append(want, row(a.ID, n))
					}
				}
			}
			same(t, view, rowsOf(t, r, `SELECT account_id, item_id FROM `+view), want)
		})
	}

	t.Run("kids_item", func(t *testing.T) {
		kids, err := items.List(item.Filter{KidsOnly: true, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, it := range kids {
			want = append(want, row(it.ID))
		}
		same(t, "kids_item", rowsOf(t, r, `SELECT item_id FROM kids_item`), want)
	})

	t.Run("occasion_member", func(t *testing.T) {
		var want []string
		for _, n := range ids(t, d, `SELECT id FROM occasion`) {
			members, err := items.List(item.Filter{OccasionID: &n, Catalog: true, Limit: 1000})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range members {
				want = append(want, row(n, it.ID))
			}
		}
		same(t, "occasion_member", rowsOf(t, r, `SELECT occasion_id, item_id FROM occasion_member`), want)
	})

	t.Run("playlist_row", func(t *testing.T) {
		lists := playlist.New(d)
		var got, want []string
		for _, a := range profiles {
			for _, p := range ids(t, r, `SELECT id FROM playlist`) {
				order, err := lists.PlayOrder(p, service.BrowseAudienceFor(a), -1, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range order {
					want = append(want, row(a.ID, p, v.EntryID, v.ItemID, v.EditionID, v.RuntimeMs))
				}
				got = append(got, inOrder(t, r, `SELECT p.account_id, r.playlist_id, r.entry_id, r.item_id, r.edition_id, r.runtime_ms
					FROM playlist_row r JOIN profile_item p ON p.item_id = r.item_id WHERE p.account_id = ? AND r.playlist_id = ?
					ORDER BY r.seq`, a.ID, p)...)
			}
		}
		if len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("playlist_row:\n got %v\nwant %v", got, want)
		}
	})

	t.Run("profile_station", func(t *testing.T) {
		stations := service.NewStationService(station.New(d), playlist.New(d), items)
		var want []string
		for _, a := range profiles {
			listed, _, err := stations.List(station.Viewer{ProfileID: a.ID, HouseholdID: uid.N(201), Audience: service.BrowseAudienceFor(a)}, "", 1000, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range listed {
				want = append(want, row(a.ID, st.ID))
			}
		}
		same(t, "profile_station", rowsOf(t, r, `SELECT account_id, station_id FROM profile_station`), want)
	})

	for _, clean := range []bool{false, true} {
		cols, where := "", ""
		if clean {
			cols, where = "clean_", " WHERE clean"
		}
		f := item.MusicFilter{Clean: clean, Limit: 1000}
		t.Run(fmt.Sprintf("music clean=%v", clean), func(t *testing.T) {
			artists, err := items.Artists(f)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, v := range artists {
				want = append(want, row(v.ID, v.AlbumCount, v.SongCount))
			}
			same(t, "music_artist", rowsOf(t, r, `SELECT item_id, `+cols+`album_count, `+cols+`song_count FROM music_artist`+where), want)

			albums, err := items.Albums(f)
			if err != nil {
				t.Fatal(err)
			}
			want = nil
			for _, v := range albums {
				want = append(want, row(v.ID, v.ArtistName, v.SongCount, v.DurationMs, v.Lossless))
			}
			same(t, "music_album", rowsOf(t, r, `SELECT item_id, artist_name, `+cols+`song_count, `+cols+`duration_ms, lossless
				FROM music_album`+where), want)

			songs, err := items.Songs(f)
			if err != nil {
				t.Fatal(err)
			}
			want = nil
			for _, v := range songs {
				want = append(want, row(v.ID, v.AlbumTitle, v.AlbumArtistID, v.PerformerID, v.ArtistName, v.DurationMs, v.Year,
					sqlText(v.Codec), v.Unavailable))
			}
			same(t, "music_song", rowsOf(t, r, `SELECT item_id, album_title, album_artist_id, performer_id, artist_name, duration_ms,
				year, codec, unavailable FROM music_song`+where), want)
		})
	}
}

// sqlText is a text column as rowsOf renders it: empty is NULL.
func sqlText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

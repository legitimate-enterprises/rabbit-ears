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
)

// fixture has adults with and without hide-kids, kids on ratings whose
// movie and TV ceilings rank oppositely, kids on different allow lists, a
// ratings kid with an allow list that must not count, and catalog, music
// and YouTube items on every side of each rule.
const fixture = `
INSERT INTO account (id, kind, name, created_at, updated_at) VALUES (101, 'adult', 'Ada', 0, 0);
INSERT INTO household (id, name, adult_id, created_at) VALUES (201, 'Home', 101, 0);
UPDATE account SET household_id = 201 WHERE id = 101;
INSERT INTO account (id, kind, name, household_id, hide_kids_content, filter_mode, movie_ceiling, tv_ceiling, pin_hash, created_at, updated_at) VALUES
  (102, 'adult', 'Bo', 201, 1, 'ratings', NULL, NULL, 'secret', 0, 0),
  (103, 'kid', 'Cy', 201, 0, 'ratings', 'PG', 'TV-Y7', NULL, 0, 0),
  (104, 'kid', 'Di', 201, 0, 'ratings', 'R', 'TV-Y', NULL, 0, 0),
  (105, 'kid', 'Ed', 201, 0, 'ratings', NULL, NULL, NULL, 0, 0),
  (106, 'kid', 'Fa', 201, 0, 'allow_list', 'NC-17', 'TV-MA', NULL, 0, 0),
  (107, 'kid', 'Gu', 201, 1, 'allow_list', NULL, NULL, 'secret', 0, 0);
INSERT INTO library (id, name, kind, created_at, updated_at) VALUES
  (1, 'Movies', 'movies', 0, 0), (2, 'TV', 'tv', 0, 0), (3, 'Music', 'music', 0, 0), (4, 'YouTube', 'youtube', 0, 0);
INSERT INTO item (id, library_id, parent_id, kind, path, parsed_title, sort_title, kids_content, explicit, artist_id, added_at, updated_at) VALUES
  (1, 1, NULL, 'movie', '/m/1', 'G Movie', NULL, NULL, NULL, NULL, 0, 0),
  (2, 1, NULL, 'movie', '/m/2', 'PG Movie', NULL, NULL, NULL, NULL, 0, 0),
  (3, 1, NULL, 'movie', '/m/3', 'PG-13 Movie', NULL, NULL, NULL, NULL, 0, 0),
  (4, 1, NULL, 'movie', '/m/4', 'R Movie', NULL, NULL, NULL, NULL, 0, 0),
  (5, 1, NULL, 'movie', '/m/5', 'NC-17 Movie', NULL, NULL, NULL, NULL, 0, 0),
  (6, 1, NULL, 'movie', '/m/6', 'Unrated', NULL, NULL, NULL, NULL, 0, 0),
  (7, 1, NULL, 'movie', '/m/7', 'Unrated Kids', NULL, 1, NULL, NULL, 0, 0),
  (8, 1, NULL, 'movie', '/m/8', 'G Not Kids', NULL, 0, NULL, NULL, 0, 0),
  (9, 1, NULL, 'movie', '/m/9', 'R Kids', NULL, 1, NULL, NULL, 0, 0),
  (10, 2, NULL, 'show', '/t/10', 'Tots', NULL, NULL, NULL, NULL, 0, 0),
  (11, 2, 10, 'season', '/t/10/1', 'Tots', NULL, NULL, NULL, NULL, 0, 0),
  (12, 2, 11, 'episode', NULL, 'Tots', NULL, NULL, NULL, NULL, 0, 0),
  (13, 2, 11, 'episode', NULL, 'Tots', NULL, NULL, NULL, NULL, 0, 0),
  (14, 2, NULL, 'show', '/t/14', 'Teen', NULL, NULL, NULL, NULL, 0, 0),
  (15, 2, 14, 'season', '/t/14/1', 'Teen', NULL, NULL, NULL, NULL, 0, 0),
  (16, 2, 15, 'episode', NULL, 'Teen', NULL, NULL, NULL, NULL, 0, 0),
  (17, 2, NULL, 'show', '/t/17', 'Family', NULL, 1, NULL, NULL, 0, 0),
  (18, 2, 17, 'season', '/t/17/1', 'Family', NULL, NULL, NULL, NULL, 0, 0),
  (19, 2, 18, 'episode', NULL, 'Family', NULL, NULL, NULL, NULL, 0, 0),
  (20, 2, NULL, 'show', '/t/20', 'General', NULL, NULL, NULL, NULL, 0, 0),
  (21, 2, 20, 'season', '/t/20/1', 'General', NULL, NULL, NULL, NULL, 0, 0),
  (22, 2, 21, 'episode', NULL, 'General', NULL, NULL, NULL, NULL, 0, 0),
  (23, 2, NULL, 'show', '/t/23', 'Unrated Show', NULL, NULL, NULL, NULL, 0, 0),
  (24, 2, 23, 'season', '/t/23/1', 'Unrated Show', NULL, NULL, NULL, NULL, 0, 0),
  (25, 2, 24, 'episode', NULL, 'Unrated Show', NULL, NULL, NULL, NULL, 0, 0),
  (30, 3, NULL, 'artist', NULL, 'The Clean Band', 'Clean Band', NULL, NULL, NULL, 0, 0),
  (31, 3, 30, 'album', NULL, 'Mixed', NULL, NULL, NULL, NULL, 0, 0),
  (32, 3, 31, 'song', NULL, 'Clean One', NULL, NULL, 0, NULL, 0, 0),
  (33, 3, 31, 'song', NULL, 'Rude One', NULL, NULL, 1, NULL, 0, 0),
  (34, 3, NULL, 'artist', NULL, 'Rude Crew', NULL, NULL, NULL, NULL, 0, 0),
  (35, 3, 34, 'album', NULL, 'All Rude', NULL, NULL, NULL, NULL, 0, 0),
  (36, 3, 35, 'song', NULL, 'Rude Two', NULL, NULL, 1, NULL, 0, 0),
  (38, 3, NULL, 'artist', NULL, 'Guest', NULL, NULL, NULL, NULL, 0, 0),
  (37, 3, 31, 'song', NULL, 'Guest Clean', NULL, NULL, 0, 38, 0, 0),
  (39, 3, 35, 'song', NULL, 'Unprobed', NULL, NULL, NULL, NULL, 0, 0),
  (40, 3, NULL, 'artist', NULL, 'Guest Rude', NULL, NULL, NULL, NULL, 0, 0),
  (41, 3, 31, 'song', NULL, 'Guest Rude Song', NULL, NULL, 1, 40, 0, 0),
  (42, 3, 34, 'album', NULL, 'Lossless', NULL, NULL, NULL, NULL, 0, 0),
  (43, 3, 42, 'song', NULL, 'Hi-Fi', NULL, NULL, 0, NULL, 0, 0),
  (50, 4, NULL, 'channel', NULL, 'Kids Channel', NULL, 1, NULL, NULL, 0, 0),
  (51, 4, 50, 'video', NULL, 'Kids Video', NULL, NULL, NULL, NULL, 0, 0),
  (52, 4, NULL, 'channel', NULL, 'Grown Channel', NULL, 0, NULL, NULL, 0, 0),
  (53, 4, 52, 'video', NULL, 'Grown Video', NULL, NULL, NULL, NULL, 0, 0),
  (54, 4, NULL, 'channel', NULL, 'Plain Channel', NULL, NULL, NULL, NULL, 0, 0),
  (55, 4, 54, 'video', NULL, 'Plain Video', NULL, NULL, NULL, NULL, 0, 0);
UPDATE item SET parsed_year = 2001 WHERE id = 31;
UPDATE item SET parsed_year = 2004 WHERE id = 37;
INSERT INTO item_metadata (item_id, source, provider, content_rating) VALUES
  (1, 'local', 'local', 'G'), (2, 'local', 'local', 'PG'), (3, 'local', 'local', 'PG-13'), (4, 'local', 'local', 'R'),
  (5, 'local', 'local', 'NC-17'), (8, 'local', 'local', 'G'), (9, 'local', 'local', 'R'), (10, 'local', 'local', 'TV-Y'),
  (14, 'local', 'local', 'TV-14'), (17, 'local', 'local', 'TV-PG'), (20, 'local', 'local', 'TV-G'), (2, 'remote', 'tmdb', 'R');
INSERT INTO account_allow (account_id, item_id) VALUES (106, 4), (106, 15), (106, 52), (107, 20), (107, 13), (107, 2), (107, 54), (103, 5);
INSERT INTO media_file (id, path, size_bytes, modified_at, duration_ms, missing_at) VALUES
  (1, '/a/1', 1, 0, 1000, NULL), (2, '/a/2', 1, 0, 2000, NULL), (3, '/a/3', 1, 0, 4000, 5), (4, '/a/4', 1, 0, 8000, NULL),
  (5, '/a/5', 1, 0, 16000, NULL), (6, '/a/6', 1, 0, 32000, NULL), (7, '/a/7', 1, 0, 64000, NULL);
INSERT INTO edition (id, item_id) VALUES (1, 32), (2, 33), (3, 37), (4, 36), (5, 41), (6, 43);
INSERT INTO edition_file (edition_id, media_file_id) VALUES (1, 1), (2, 2), (3, 3), (4, 4), (5, 5), (6, 6), (6, 7);
INSERT INTO track (media_file_id, stream_index, kind, codec) VALUES
  (1, 0, 'audio', 'flac'), (2, 0, 'audio', 'mp3'), (3, 0, 'audio', 'flac'), (4, 0, 'audio', 'alac'), (5, 0, 'audio', 'flac'),
  (6, 1, 'audio', 'pcm_s16le'), (6, 0, 'video', 'mjpeg'), (7, 0, 'audio', 'flac');
INSERT INTO occasion (id, name, month, day, days_before, days_after) VALUES (301, 'Snow', 1, 2, 3, 4), (302, 'Sun', 7, 1, 0, 0);
INSERT INTO occasion_keyword (occasion_id, keyword_id) VALUES (301, 900), (302, 901);
INSERT INTO item_keyword (item_id, keyword_id) VALUES (1, 900), (10, 900), (12, 900), (4, 901), (1, 901);
INSERT INTO occasion_exclusion (occasion_id, item_id) VALUES (301, 10);
INSERT INTO occasion_item (occasion_id, item_id) VALUES (301, 14), (302, 3);
INSERT INTO collection (id, source, household_id, name, created_at, updated_at) VALUES (401, 'custom', 201, 'Picks', 0, 0);
INSERT INTO collection_movie (collection_id, item_id, position) VALUES (401, 4, 0), (401, 1, 1), (401, 2, NULL);
INSERT INTO media_file (id, path, size_bytes, modified_at, duration_ms) VALUES (8, '/a/8', 1, 0, 90000);
INSERT INTO edition (id, item_id, runtime_ms) VALUES (7, 3, NULL), (8, 5, NULL), (9, 1, 7000);
INSERT INTO edition_file (edition_id, media_file_id) VALUES (8, 8), (9, 8);
INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (501, 101, 'Mix', 'movies', 0, 0), (502, 103, 'Tunes', 'music', 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, collection_id, edition_id, rank, added_at) VALUES
  (601, 501, 5, NULL, 8, 'a0', 0), (602, 501, NULL, 401, NULL, 'a1', 0), (603, 501, 3, NULL, 7, 'a2', 0),
  (605, 501, 9, NULL, NULL, 'a2', 0), (604, 501, 1, NULL, NULL, 'a2', 0), (606, 501, 10, NULL, NULL, 'a3', 0),
  (607, 501, 15, NULL, NULL, 'a4', 0), (608, 501, 52, NULL, NULL, 'a5', 0), (609, 501, 50, NULL, NULL, 'a6', 0),
  (611, 502, 30, NULL, NULL, 'a0', 0), (612, 502, 35, NULL, NULL, 'a1', 0), (613, 502, 33, NULL, NULL, 'Zz', 0),
  (614, 502, 38, NULL, NULL, 'a3', 0);
`

// replicaOf is household 201's snapshot of d, opened, so a view reading
// what a replica never holds fails.
func replicaOf(t *testing.T, d *sql.DB) *sql.DB {
	t.Helper()
	r, _ := snapshotOf(t, syncOf(t, d), 201)
	return r
}

func ints(t *testing.T, d *sql.DB, q string, args ...any) []int64 {
	return column[int64](t, d, q, args...)
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
// pointer as NULL.
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
	mustExec(t, d, `INSERT INTO playlist_household_share (playlist_id, household_id, access, created_at) VALUES (515, 201, 'read', 0)`)
	r := replicaOf(t, d)
	items, accounts := item.New(d), account.New(d)
	ids := ints(t, d, `SELECT id FROM item`)
	var profiles []*account.Account
	for _, id := range ints(t, r, `SELECT id FROM account`) {
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
				for _, n := range ids {
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
		for _, n := range ints(t, d, `SELECT id FROM occasion`) {
			members, err := items.List(item.Filter{OccasionID: n, Catalog: true, Limit: 1000})
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
			for _, p := range ints(t, r, `SELECT id FROM playlist`) {
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
			listed, _, err := stations.List(station.Viewer{ProfileID: a.ID, HouseholdID: 201, Audience: service.BrowseAudienceFor(a)}, "", 1000, 0)
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

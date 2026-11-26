package db_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func schema(t *testing.T, d *sql.DB) string {
	t.Helper()
	rows, err := d.Query(`SELECT type, name, COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+" "+b+" "+c)
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func TestMigrateFromEmpty(t *testing.T) {
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Version(d); v != db.LatestVersion() || v < 4 {
		t.Fatalf("version = %d", v)
	}
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("second Migrate must be a no-op: %v", err)
	}
}

// Every prior version upgrades to the same schema as a fresh database, and
// rows written at an old version survive.
func TestMigrateFromEveryPriorVersion(t *testing.T) {
	fresh := open(t)
	if err := db.Migrate(fresh); err != nil {
		t.Fatal(err)
	}
	want := schema(t, fresh)
	for v := 1; v < db.LatestVersion(); v++ {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			d := open(t)
			if err := db.MigrateTo(d, v); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(`INSERT INTO library (name, kind, created_at, updated_at) VALUES ('Films','movies',1,1)`); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(d); err != nil {
				t.Fatal(err)
			}
			if got := schema(t, d); got != want {
				t.Fatalf("schema at v%d -> latest differs from fresh", v)
			}
			var n int
			if err := d.QueryRow(`SELECT COUNT(*) FROM library`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("library rows = %d, %v", n, err)
			}
		})
	}
}

func TestMigrateRefusesNewerDatabase(t *testing.T) {
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO schema_migration VALUES (999,'x',0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err == nil {
		t.Fatal("want error for newer database")
	}
}

func TestTimeIsUTCInstant(t *testing.T) {
	loc := time.FixedZone("x", -7*3600)
	in := time.Date(2026, 1, 2, 3, 4, 5, 6000000, loc)
	out := db.FromMs(db.Ms(in))
	if !out.Equal(in) || out.Location() != time.UTC {
		t.Fatalf("got %v", out)
	}
}

func TestWrapConstraints(t *testing.T) {
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO library (name, kind, created_at, updated_at) VALUES ('a','movies',1,1)`
	if _, err := d.Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ins); !isErr(db.Wrap(err), db.ErrConflict) {
		t.Fatalf("unique: %v", db.Wrap(err))
	}
	_, err := d.Exec(`INSERT INTO library_folder (library_id, path) VALUES (99,'/x')`)
	if !isErr(db.Wrap(err), db.ErrReference) {
		t.Fatalf("fk: %v", db.Wrap(err))
	}
}

func isErr(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

func TestTaskRunSummaryColumn(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task (name, kind, created_at) VALUES ('t','scan:1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_run (task_id, status, started_at) VALUES (1,'succeeded',1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var s sql.NullString
	if err := d.QueryRow(`SELECT summary FROM task_run`).Scan(&s); err != nil || s.Valid {
		t.Fatalf("old runs keep a NULL summary: %v %v", s, err)
	}
}

func TestMatchedItemsAreDueForCredits(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 8); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'Films','movies',1,1)`,
		`INSERT INTO item (id, library_id, kind, parsed_title, added_at, updated_at) VALUES (1,1,'movie','A',1,1), (2,1,'movie','B',1,1)`,
		`INSERT INTO item_metadata (item_id, provider_id, status, retry_at) VALUES (1,603,'matched',9999999999999), (2,NULL,'unmatched',9999999999999)`,
		`INSERT INTO task (name, kind, next_run_at, created_at) VALUES ('Refresh metadata','metadata',9999999999999,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var matched, unmatched, task int64
	if err := d.QueryRow(`SELECT (SELECT retry_at FROM item_metadata WHERE item_id = 1), (SELECT retry_at FROM item_metadata WHERE item_id = 2),
		(SELECT next_run_at FROM task)`).Scan(&matched, &unmatched, &task); err != nil {
		t.Fatal(err)
	}
	if matched != 0 || unmatched != 9999999999999 || task != 0 {
		t.Fatalf("matched %d unmatched %d task %d", matched, unmatched, task)
	}
}

// Rebuilding item for music and YouTube kinds keeps every row, its children
// and everything that refers to it, and leaves foreign keys enforced.
func TestItemRebuildKeepsRows(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 17); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'TV','tv',1,1), (2,'Music','music',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, path, parsed_title, season_number, episode_number, added_at, updated_at)
			VALUES (1,1,NULL,'show','/tv/S','S',NULL,NULL,1,1), (2,1,1,'season','/tv/S/1','S',1,NULL,1,1), (3,1,2,'episode',NULL,'S',1,4,1,1)`,
		`INSERT INTO item_metadata (item_id, provider_id, status, title) VALUES (1,9,'matched','Show')`,
		`INSERT INTO media_file (id, item_id, path, size_bytes, modified_at) VALUES (1,3,'/tv/S/1/e4.mkv',10,1)`,
		`INSERT INTO account (id, kind, name, created_at, updated_at) VALUES (1,'adult','A',1,1)`,
		`INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (1,3,500,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var items, files, states, metas int
	if err := d.QueryRow(`SELECT (SELECT COUNT(*) FROM item), (SELECT COUNT(*) FROM media_file), (SELECT COUNT(*) FROM watch_state),
		(SELECT COUNT(*) FROM item_metadata)`).Scan(&items, &files, &states, &metas); err != nil {
		t.Fatal(err)
	}
	if items != 3 || files != 1 || states != 1 || metas != 1 {
		t.Fatalf("items %d files %d states %d metadata %d", items, files, states, metas)
	}
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	for _, q := range []string{
		`INSERT INTO item (id, library_id, kind, parsed_title, sort_title, added_at, updated_at) VALUES (10,2,'artist','The Band','Band',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, parsed_title, added_at, updated_at) VALUES (11,2,10,'album','LP',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, parsed_title, disc_number, track_number, artist_id, added_at, updated_at)
			VALUES (12,2,11,'song','One',1,1,10,1,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO item (library_id, kind, parsed_title, added_at, updated_at) VALUES (2,'song','No album',1,1)`,
		`INSERT INTO item (library_id, parent_id, kind, parsed_title, added_at, updated_at) VALUES (2,10,'artist','Nested',1,1)`,
		`INSERT INTO item (library_id, parent_id, kind, parsed_title, artist_id, added_at, updated_at) VALUES (2,10,'album','X',10,1,1)`,
		`INSERT INTO item (library_id, kind, parsed_title, added_at, updated_at) VALUES (2,'track','Bad kind',1,1)`,
	} {
		if _, err := d.Exec(q); err == nil {
			t.Fatalf("accepted %s", q)
		}
	}
	if _, err := d.Exec(`DELETE FROM item WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT (SELECT COUNT(*) FROM item WHERE library_id = 1), (SELECT COUNT(*) FROM media_file)`).Scan(&items, &files); err != nil {
		t.Fatal(err)
	}
	if items != 0 || files != 0 {
		t.Fatalf("cascade after rebuild: items %d files %d", items, files)
	}
}

// The subscription, queue and channel playlist tables go with the channel
// check and download tasks, and a channel without a folder with its videos.
func TestYouTubeBecomesScannedFolders(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 23); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'YouTube','youtube',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, path, parsed_title, added_at, updated_at)
			VALUES (1,1,NULL,'channel',NULL,'Subscribed',1,1), (2,1,1,'video',NULL,'Downloaded',1,1),
			(3,1,NULL,'channel','/yt/Folder','Folder',1,1), (4,1,3,'video','/yt/Folder/v.mp4','Scanned',1,1)`,
		`INSERT INTO channel_subscription (channel_id, address, created_at, updated_at) VALUES (1,'@example',1,1)`,
		`INSERT INTO task (id, name, kind, created_at) VALUES (1,'Check channels in YouTube','youtube:1',1),
			(2,'YouTube download','youtube-downloads',1), (3,'Scan YouTube','scan:1',1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var itemIDs, taskIDs string
	var tables int
	if err := d.QueryRow(`SELECT (SELECT group_concat(id) FROM (SELECT id FROM item ORDER BY id)), (SELECT group_concat(id) FROM (SELECT id FROM task ORDER BY id)),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN
			('youtube_settings','channel_subscription','channel_playlist','channel_playlist_video','channel_video'))`).
		Scan(&itemIDs, &taskIDs, &tables); err != nil {
		t.Fatal(err)
	}
	if itemIDs != "3,4" || taskIDs != "3" || tables != 0 {
		t.Fatalf("items %s tasks %s tables %d", itemIDs, taskIDs, tables)
	}
	if _, err := d.Exec(`UPDATE item_metadata SET published_at = 1, sidecar_stamp = 2`); err != nil {
		t.Fatal(err)
	}
}

// Video playlists split as 0025_playlist_library_kinds.sql says; music
// playlists and every kept row's id, entries and play stamp survive.
func TestPlaylistsTakeLibraryKinds(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 24); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'Films','movies',1,1), (2,'TV','tv',1,1), (3,'Music','music',1,1), (4,'YouTube','youtube',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, parsed_title, season_number, episode_number, added_at, updated_at)
			VALUES (1,1,NULL,'movie','Film',NULL,NULL,1,1), (2,2,NULL,'show','S',NULL,NULL,1,1), (3,2,2,'season','S',1,NULL,1,1),
			(4,2,3,'episode','S',1,1,1,1), (5,3,NULL,'artist','Band',NULL,NULL,1,1), (6,3,5,'album','LP',NULL,NULL,1,1),
			(7,3,6,'song','One',NULL,NULL,1,1), (8,4,NULL,'channel','Ch',NULL,NULL,1,1), (9,4,8,'video','V',NULL,NULL,1,1)`,
		`INSERT INTO account (id, kind, name, created_at, updated_at) VALUES (1,'adult','A',1,1)`,
		`INSERT INTO playlist (id, account_id, name, kind, last_played_at, created_at, updated_at) VALUES
			(1,1,'Films','video',5,1,1), (2,1,'Shows','video',NULL,1,1), (3,1,'Mixed','video',NULL,1,1),
			(4,1,'Clips','video',NULL,1,1), (5,1,'Empty','video',NULL,1,1), (6,1,'Songs','music',NULL,1,1)`,
		`INSERT INTO playlist_entry (id, playlist_id, item_id, position, added_at) VALUES
			(1,1,1,0,1), (2,1,1,1,1), (3,2,4,0,1), (4,3,1,0,1), (5,3,4,1,1), (6,4,9,0,1), (7,6,7,0,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var kinds, entries string
	var played int64
	if err := d.QueryRow(`SELECT (SELECT group_concat(id || ':' || kind) FROM (SELECT id, kind FROM playlist ORDER BY id)),
		(SELECT group_concat(id) FROM (SELECT id FROM playlist_entry ORDER BY id)), (SELECT last_played_at FROM playlist WHERE id = 1)`).
		Scan(&kinds, &entries, &played); err != nil {
		t.Fatal(err)
	}
	if kinds != "1:movies,2:tv,5:movies,6:music" || entries != "1,2,3,7" || played != 5 {
		t.Fatalf("playlists %s entries %s played %d", kinds, entries, played)
	}
	if _, err := d.Exec(`INSERT INTO playlist (account_id, name, kind, created_at, updated_at) VALUES (1,'X','video',1,1)`); err == nil {
		t.Fatal("video accepted")
	}
	if _, err := d.Exec(`DELETE FROM playlist WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM playlist_entry WHERE playlist_id = 1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("entries cascade after rebuild: %d %v", n, err)
	}
}

// Files become editions as 0027_editions.sql says, tags matched
// case-insensitively; file, track and session rows keep their ids.
func TestEditionsCarryEveryRow(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 26); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'Films','movies',1,1), (2,'TV','tv',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, parsed_title, season_number, episode_number, added_at, updated_at)
			VALUES (1,1,NULL,'movie','Film',NULL,NULL,1,1), (2,2,NULL,'show','S',NULL,NULL,1,1), (3,2,2,'season','S',1,NULL,1,1),
			(4,2,3,'episode','S',1,1,1,1), (5,1,NULL,'movie','Bare',NULL,NULL,1,1), (6,1,NULL,'movie','Cut',NULL,NULL,1,1)`,
		`INSERT INTO media_file (id, item_id, path, size_bytes, modified_at, duration_ms) VALUES
			(7,1,'/f/a.mkv',10,1,6000000), (8,1,'/f/b.mkv',20,1,6100000), (9,4,'/t/e {edition-X}.mkv',5,1,NULL),
			(10,6,'/f/Cut (2001) {Edition-Director''s Cut} [x].mkv',5,1,7000000), (11,6,'/f/Cut (2001) {edition-director''s cut}.mp4',4,1,1),
			(12,6,'/f/Cut (2001) {edition-}.mkv',3,1,5000000)`,
		`INSERT INTO track (id, media_file_id, stream_index, kind, codec) VALUES (3,8,0,'video','h264')`,
		`INSERT INTO account (id, kind, name, created_at, updated_at) VALUES (1,'adult','A',1,1)`,
		`INSERT INTO household (id, name, adult_id, created_at) VALUES (1,'H',1,1)`,
		`INSERT INTO device (id, account_id, household_id, name, platform, token_hash, created_at) VALUES (1,1,1,'D','web','h',1)`,
		`INSERT INTO playback_session (id, device_id, account_id, item_id, media_file_id, started_at, updated_at) VALUES (4,1,1,1,8,1,1)`,
		`INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (1,1,500,1), (1,4,0,1), (1,5,0,1)`,
		`INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (1,1,'P','movies',1,1), (2,1,'T','tv',1,1)`,
		`INSERT INTO playlist_entry (id, playlist_id, item_id, position, added_at) VALUES (1,1,1,0,1), (2,2,2,0,1), (3,2,4,1,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var editions, links, entries, states string
	if err := d.QueryRow(`SELECT
		(SELECT group_concat(item_id || ':' || COALESCE(tag, '-') || ':' || COALESCE(runtime_ms, '-')) FROM (SELECT * FROM edition ORDER BY item_id)),
		(SELECT group_concat(e.item_id || ':' || ef.media_file_id) FROM (SELECT * FROM edition_file ORDER BY media_file_id) ef JOIN edition e ON e.id = ef.edition_id),
		(SELECT group_concat(p.id || ':' || COALESCE(e.item_id, '-')) FROM (SELECT * FROM playlist_entry ORDER BY id) p LEFT JOIN edition e ON e.id = p.edition_id),
		(SELECT group_concat(w.item_id || ':' || COALESCE(e.item_id, '-')) FROM (SELECT * FROM watch_state ORDER BY item_id) w LEFT JOIN edition e ON e.id = w.edition_id)`).
		Scan(&editions, &links, &entries, &states); err != nil {
		t.Fatal(err)
	}
	if editions != "1:-:6100000,4:-:-,6:-:5000000,6:Director's Cut:7000000" || links != "1:7,1:8,4:9,6:10,6:11,6:12" ||
		entries != "1:1,2:-,3:4" || states != "1:1,4:4,5:-" {
		t.Fatalf("editions %s links %s entries %s states %s", editions, links, entries, states)
	}
	var sessions, tracks, cols int
	if err := d.QueryRow(`SELECT (SELECT COUNT(*) FROM playback_session WHERE media_file_id = 8), (SELECT COUNT(*) FROM track WHERE media_file_id = 8),
		(SELECT COUNT(*) FROM pragma_table_info('media_file') WHERE name = 'item_id')`).Scan(&sessions, &tracks, &cols); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || tracks != 1 || cols != 0 {
		t.Fatalf("sessions %d tracks %d item_id columns %d", sessions, tracks, cols)
	}
	// A file goes once the last title holding it does.
	if _, err := d.Exec(`DELETE FROM item WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var files int
	if err := d.QueryRow(`SELECT COUNT(*) FROM media_file`).Scan(&files); err != nil || files != 4 {
		t.Fatalf("files %d %v", files, err)
	}
}

// Every station keeps the timing its kind had: movies on the hour, tv on
// the hour or half hour, music back to back.
func TestStationsTakeTheirKindsTiming(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 36); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO account (id, kind, name, created_at, updated_at) VALUES (1,'adult','A',1,1)`,
		`INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (1,1,'F','movies',1,1), (2,1,'T','tv',1,1), (3,1,'M','music',1,1)`,
		`INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, updated_at) VALUES
			(1,1,1,'F','private',1,1), (2,1,2,'T','private',1,1), (3,1,3,'M','private',1,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var timings string
	if err := d.QueryRow(`SELECT group_concat(id || ':' || timing) FROM (SELECT id, timing FROM station ORDER BY id)`).Scan(&timings); err != nil {
		t.Fatal(err)
	}
	if timings != "1:hour,2:half_hour,3:continuous" {
		t.Fatalf("timings %s", timings)
	}
	if _, err := d.Exec(`UPDATE station SET timing = 'daily' WHERE id = 1`); err == nil {
		t.Fatal("daily accepted")
	}
}

// Provider rows and genre links split by source as
// 0041_metadata_sources.sql says; movies keep TMDB as remote, and the
// settings start Local only.
func TestMetadataSplitsBySource(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 40); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO server_info (id, name, created_at, updated_at) VALUES (1,'S',1,1)`,
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'M','movies',1,1), (2,'Mu','music',1,1), (3,'Y','youtube',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, path, parsed_title, added_at, updated_at) VALUES
			(1,1,NULL,'movie',NULL,'Film',1,1), (2,2,NULL,'artist',NULL,'Band',1,1), (3,2,2,'album',NULL,'Own',1,1),
			(4,2,2,'album',NULL,'Archive',1,1), (5,3,NULL,'channel','/y/C','C',1,1), (6,3,5,'video',NULL,'V',1,1),
			(7,3,NULL,'channel','/y/D','D',1,1), (8,2,2,'album',NULL,'Unlooked',1,1)`,
		`INSERT INTO item_metadata (item_id, provider, provider_id, status, title, poster_path, external_id, fetched_at, retry_at, published_at, sidecar_stamp) VALUES
			(1,'tmdb',9,'matched','Film!','/p.jpg',NULL,1,2,NULL,NULL),
			(3,'musicbrainz',NULL,'matched',NULL,'/data/thumbnails/music/ab.jpg','mb-own',1,2,NULL,NULL),
			(4,'musicbrainz',NULL,'matched',NULL,'/data/images/caa/mb.jpg','mb-arch',1,2,NULL,NULL),
			(5,'youtube',NULL,'matched','C','/data/cache/youtube/av.jpg','UC1',1,2,NULL,NULL),
			(6,'youtube',NULL,'matched','V','/y/C/v-thumb.jpg','vid',1,NULL,5,6),
			(7,'youtube',NULL,'matched','D','/y/D/poster.jpg','UC2',1,NULL,NULL,NULL),
			(8,'musicbrainz',NULL,'unmatched',NULL,'/data/thumbnails/music/cd.jpg',NULL,NULL,NULL,NULL,NULL)`,
		`INSERT INTO genre (id, name) VALUES (1,'Drama'), (2,'Rock')`,
		`INSERT INTO item_genre (item_id, genre_id) VALUES (1,1), (3,2)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateTo(d, 41); err != nil {
		t.Fatal(err)
	}
	var rows, genres, setting string
	if err := d.QueryRow(`SELECT group_concat(r, ' ') FROM (SELECT item_id || ':' || source || ':' || provider || ':' || COALESCE(title, '-') || ':' ||
			COALESCE(poster_path, '-') || ':' || COALESCE(external_id, '-') || ':' || COALESCE(retry_at, '-') || ':' || COALESCE(published_at, '-') AS r
			FROM item_metadata ORDER BY item_id, source)`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	want := "1:remote:tmdb:Film!:/p.jpg:-:2:- " +
		"3:local:local:-:/data/thumbnails/music/ab.jpg:-:-:- 3:remote:musicbrainz:-:-:mb-own:2:- " +
		"4:remote:musicbrainz:-:/data/images/caa/mb.jpg:mb-arch:2:- " +
		"5:local:local:C:-:UC1:-:- 5:remote:youtube:-:/data/cache/youtube/av.jpg:-:2:- " +
		"6:local:local:V:/y/C/v-thumb.jpg:vid:-:5 " +
		"7:local:local:D:/y/D/poster.jpg:UC2:-:- " +
		"8:local:local:-:/data/thumbnails/music/cd.jpg:-:-:-"
	if rows != want {
		t.Fatalf("rows\n%s\nwant\n%s", rows, want)
	}
	if err := d.QueryRow(`SELECT group_concat(item_id || ':' || genre_id || ':' || source, ' ') FROM (SELECT * FROM item_genre ORDER BY item_id)`).
		Scan(&genres); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT metadata_source || ' ' || metadata_mode FROM server_info`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if genres != "1:1:remote 3:2:local" || setting != "local only" {
		t.Fatalf("genres %s setting %s", genres, setting)
	}
}

// Paths the server made under its data folder become relative to it, found
// from the folder they were made in whatever the data folder was called; a
// provider's path and a file in a library keep theirs.
func TestDataPathsBecomeRelative(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 56); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1,'M','movies',1,1), (2,'Mu','music',1,1), (3,'Y','youtube',1,1)`,
		`INSERT INTO item (id, library_id, parent_id, kind, path, parsed_title, added_at, updated_at) VALUES
			(1,1,NULL,'movie',NULL,'Film',1,1), (2,2,NULL,'artist',NULL,'Band',1,1), (3,2,2,'album',NULL,'Own',1,1),
			(4,3,NULL,'channel','/y/C','C',1,1), (5,3,4,'video',NULL,'V',1,1), (6,1,NULL,'movie',NULL,'Odd',1,1)`,
		`INSERT INTO item_metadata (item_id, source, provider, status, poster_path, backdrop_path) VALUES
			(1,'local','local','matched','/old/data/thumbnails/local/ab.jpg','/old/data/thumbnails/local/cd.jpg'),
			(1,'remote','tmdb','matched','/p.jpg','/b.jpg'),
			(3,'local','local','matched','/old/data/thumbnails/music/ef.jpg',NULL),
			(3,'remote','musicbrainz','matched','/old/data/images/caa/mb.jpg',NULL),
			(4,'local','local','matched','/y/C/poster.jpg',NULL),
			(4,'remote','youtube','matched','/old/data/cache/youtube/UC1.jpg',NULL),
			(5,'local','local','matched','/y/C/images/v.jpg',NULL),
			(6,'local','local','matched','thumbnails/local/gh.jpg','/elsewhere/frame.jpg')`,
		`INSERT INTO media_file (id, path, size_bytes, modified_at) VALUES (1,'/m/a.mkv',1,1), (2,'/m/b.mkv',1,1)`,
		`INSERT INTO chapter (media_file_id, position, start_ms, end_ms, thumbnail_path) VALUES
			(1,0,0,1,'/old/data/thumbnails/1/0.jpg'), (1,1,1,2,'/elsewhere/1.jpg'), (2,0,0,1,NULL)`,
		`INSERT INTO seek_preview (media_file_id, dir, interval_ms, count, width, height, created_at) VALUES
			(1,'/old/data/previews/1',1,1,1,1,1), (2,'/elsewhere/9',1,1,1,1,1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	var meta, chapters, previews string
	if err := d.QueryRow(`SELECT group_concat(item_id || ':' || source || ':' || COALESCE(poster_path, '-') || ':' || COALESCE(backdrop_path, '-'), ' ')
			FROM (SELECT * FROM item_metadata ORDER BY item_id, source)`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT group_concat(COALESCE(thumbnail_path, '-'), ' ') FROM (SELECT * FROM chapter ORDER BY media_file_id, position)`).
		Scan(&chapters); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT group_concat(dir, ' ') FROM (SELECT * FROM seek_preview ORDER BY media_file_id)`).Scan(&previews); err != nil {
		t.Fatal(err)
	}
	want := "1:local:thumbnails/local/ab.jpg:thumbnails/local/cd.jpg 1:remote:/p.jpg:/b.jpg " +
		"3:local:thumbnails/music/ef.jpg:- 3:remote:images/caa/mb.jpg:- " +
		"4:local:/y/C/poster.jpg:- 4:remote:cache/youtube/UC1.jpg:- " +
		"5:local:/y/C/images/v.jpg:- 6:local:thumbnails/local/gh.jpg:/elsewhere/frame.jpg"
	if meta != want {
		t.Fatalf("metadata\n%s\nwant\n%s", meta, want)
	}
	if chapters != "thumbnails/1/0.jpg /elsewhere/1.jpg -" || previews != "previews/1 /elsewhere/9" {
		t.Fatalf("chapters %s previews %s", chapters, previews)
	}

	dbtest.DataDir(t, "/new/data")
	p, err := seekpreview.New(d).ByMediaFile(1)
	if err != nil || p.Dir != "/new/data/previews/1" {
		t.Fatalf("%v %+v", err, p)
	}
	cs, err := mediafile.New(d).Chapters(1)
	if err != nil || cs[0].ThumbnailPath != "/new/data/thumbnails/1/0.jpg" || cs[1].ThumbnailPath != "/elsewhere/1.jpg" {
		t.Fatalf("%v %+v %+v", err, cs[0], cs[1])
	}
	items := item.New(d)
	local, err := items.LocalRow(1)
	if err != nil || local.PosterPath != "/new/data/thumbnails/local/ab.jpg" || local.BackdropPath != "/new/data/thumbnails/local/cd.jpg" {
		t.Fatalf("%v %+v", err, local)
	}
	if remote, err := items.Remote(1); err != nil || remote.PosterPath != "/p.jpg" {
		t.Fatalf("%v %+v", err, remote)
	}
	if lib, err := items.LocalRow(4); err != nil || lib.PosterPath != "/y/C/poster.jpg" {
		t.Fatalf("%v %+v", err, lib)
	}
}

// copies lists the database copies beside path, oldest schema first.
func copies(t *testing.T, path string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		names[i] = filepath.Base(n)
	}
	return names
}

// A server refuses a database a newer one has migrated, so the copy from
// before a migration is the only way back to the older server.
func TestMigrateCopiesTheDatabaseBeforeChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rabbit-ears.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	latest := db.LatestVersion()
	if err := db.MigrateTo(d, latest-1); err != nil {
		t.Fatal(err)
	}
	if got := copies(t, path); len(got) != 0 {
		t.Fatalf("an empty database has nothing to copy, got %v", got)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("rabbit-ears-v%04d.db", latest-1)
	if got := copies(t, path); len(got) != 1 || got[0] != want {
		t.Fatalf("copies = %v, want [%s]", got, want)
	}
	c, err := db.Open(filepath.Join(filepath.Dir(path), "backups", want))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if v, err := db.Version(c); err != nil || v != latest-1 {
		t.Fatalf("the copy is at schema version %d (%v), want %d", v, err, latest-1)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	if got := copies(t, path); len(got) != 1 {
		t.Fatalf("a database already up to date was copied again: %v", got)
	}
}

func TestMigrateKeepsTheNewestFiveCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rabbit-ears.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	latest := db.LatestVersion()
	for v := latest - 7; v <= latest; v++ {
		if err := db.MigrateTo(d, v); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for v := latest - 5; v < latest; v++ {
		want = append(want, fmt.Sprintf("rabbit-ears-v%04d.db", v))
	}
	if got := copies(t, path); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("copies = %v, want %v", got, want)
	}
}

func TestMigrateRefusesWithoutACopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rabbit-ears.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	latest := db.LatestVersion()
	if err := db.MigrateTo(d, latest-1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "backups"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err == nil {
		t.Fatal("migrated a database it could not copy first")
	}
	if v, err := db.Version(d); err != nil || v != latest-1 {
		t.Fatalf("schema version %d (%v), want %d untouched", v, err, latest-1)
	}
}

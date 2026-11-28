package replica

import (
	"fmt"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
)

// copied are the server's views a snapshot carries as the server defines
// them, dependencies first.
var copied = []string{"item_file", "metadata_setting", "item_shown", "item_genre_shown", "credit_shown", "item_keyword_shown",
	"membership", "playlist_reach"}

// Views is the CREATE VIEW statement of every view of a snapshot,
// dependencies first: the server's own as d defines them, then those
// generated from the fragments the server's queries use.
func Views(d db.DBTX) ([]string, error) {
	out := make([]string, 0, len(copied)+len(generated))
	for _, name := range copied {
		var q string
		if err := d.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'view' AND name = ?`, name).Scan(&q); err != nil {
			return nil, fmt.Errorf("view %s: %w", name, db.Wrap(err))
		}
		out = append(out, q)
	}
	return append(out, generated...), nil
}

// ceilings is, for the ceiling column c, the ratings of scale it allows as
// Audience.Args lists them.
func ceilings(scale []string, c string) string {
	q := `CASE ` + c
	for _, r := range scale {
		q += ` WHEN '` + r + `' THEN ',` + strings.Join(account.AtOrBelow(scale, r), ",") + `,'`
	}
	return q + ` ELSE ',,' END`
}

// audience is VisibleTo for profile_audience a with mode as its mode.
func audience(mode string) item.AudienceSQL {
	return item.AudienceSQL{Mode: "a." + mode, AccountID: "a.account_id", AllowList: "a.allow_list", Movie: "a.movie", TV: "a.tv"}
}

// generated are the views built from the server's own fragments, each
// after those it reads. profile_audience holds an account's Audience.Args
// as service.AudienceFor (mode) and service.BrowseAudienceFor (browse_mode)
// derive them.
var generated = []string{
	`CREATE VIEW profile_audience AS SELECT id AS account_id, kind = '` + account.Kid + `' AS mode,
		CASE WHEN kind = '` + account.Kid + `' THEN 1 WHEN hide_kids_content THEN 2 ELSE 0 END AS browse_mode,
		kind = '` + account.Kid + `' AND filter_mode = '` + account.FilterAllowList + `' AS allow_list,
		` + ceilings(account.MovieRatings, "movie_ceiling") + ` AS movie, ` + ceilings(account.TVRatings, "tv_ceiling") + ` AS tv
	FROM account`,
	`CREATE VIEW profile_item AS SELECT a.account_id, item.id AS item_id FROM profile_audience a CROSS JOIN item
	WHERE ` + item.VisibleTo("item", audience("browse_mode")),
	`CREATE VIEW profile_item_open AS SELECT a.account_id, item.id AS item_id FROM profile_audience a CROSS JOIN item
	WHERE ` + item.VisibleTo("item", audience("mode")),
	// What each item and collection shows each profile, as the server's
	// responses carry it.
	`CREATE VIEW profile_art AS SELECT a.account_id, i.id AS item_id, ` + item.URLOf(item.PosterOf("i", "a.mode")) + ` AS poster_url,
		` + item.URLOf(item.BackdropOf("i")) + ` AS backdrop_url
	FROM profile_audience a CROSS JOIN item i`,
	`CREATE VIEW profile_collection_art AS SELECT a.account_id, c.id AS collection_id,
		` + item.URLOf(collection.ArtOf("c", item.VisibleTo("item", audience("browse_mode")), false)) + ` AS poster_url,
		` + item.URLOf(collection.ArtOf("c", item.VisibleTo("item", audience("browse_mode")), true)) + ` AS backdrop_url
	FROM profile_audience a CROSS JOIN collection c`,
	`CREATE VIEW kids_item AS SELECT id AS item_id FROM item WHERE ` + item.KidsContentOf("item"),
	`CREATE VIEW occasion_member AS SELECT o.id AS occasion_id, m.id AS item_id FROM occasion o CROSS JOIN item m
	WHERE m.id IN (` + item.OccasionMembersOf("o.id") + `)`,
	// Every playlist's play order to everyone; joined to profile_item it is
	// the order a member reads, seq numbering it before that filter.
	`CREATE VIEW playlist_row AS WITH e AS (SELECT id, rank, item_id, collection_id, edition_id FROM playlist_entry)` + playlist.Branches + `
		SELECT pe.playlist_id, ROW_NUMBER() OVER (PARTITION BY pe.playlist_id ORDER BY ` + playlist.RowOrder + `) - 1 AS seq, x.entry_id,
			x.item_id, x.edition_id, ` + playlist.Runtime + ` AS runtime_ms
		FROM x CROSS JOIN playlist_entry pe ON pe.id = x.entry_id WHERE ` + playlist.Playable,
	// The stations each profile lists on the replica's household's devices.
	`CREATE VIEW profile_station AS SELECT a.account_id, s.id AS station_id FROM profile_audience a CROSS JOIN station s
	WHERE ` + station.SeenOf("s", "s.household_id", "a.account_id", "(SELECT household_id FROM replica_meta)") + ` AND (a.browse_mode = 0 OR
		(SELECT ` + playlist.AllowsOf("a.browse_mode = 2") + ` FROM (SELECT COUNT(*) AS total, COALESCE(SUM(` +
		item.VisibleTo("item", audience("browse_mode")) + `), 0) AS seen
			FROM playlist_row x CROSS JOIN item ON item.id = x.item_id WHERE x.playlist_id = s.playlist_id)))`,
	`CREATE VIEW music_artist AS SELECT a.id AS item_id, ` + item.CleanOf("a") + ` AS clean,
		` + item.ArtistAlbumsOf("a", "0") + ` AS album_count, ` + item.ArtistSongsOf("a", "0") + ` AS song_count,
		` + item.ArtistAlbumsOf("a", "1") + ` AS clean_album_count, ` + item.ArtistSongsOf("a", "1") + ` AS clean_song_count
	FROM item a WHERE a.kind = 'artist'`,
	`CREATE VIEW music_album AS SELECT al.id AS item_id, ar.parsed_title AS artist_name, ` + item.CleanOf("al") + ` AS clean,
		` + item.AlbumSongsOf("al", "0") + ` AS song_count, ` + item.AlbumDurationOf("al", "0") + ` AS duration_ms,
		` + item.AlbumSongsOf("al", "1") + ` AS clean_song_count, ` + item.AlbumDurationOf("al", "1") + ` AS clean_duration_ms,
		` + item.AlbumLosslessOf("al") + ` AS lossless
	FROM ` + item.AlbumRows + ` WHERE al.kind = 'album'`,
	`CREATE VIEW music_song AS SELECT s.id AS item_id, ` + item.SongColumns + `, ` + item.CleanOf("s") + ` AS clean
	FROM ` + item.SongRows + ` WHERE s.kind = 'song'`,
}

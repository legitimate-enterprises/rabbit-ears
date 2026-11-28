// Package replica defines what a device's local copy of the server holds,
// the projection, and keeps every row of it versioned so a client can catch
// up from any version it holds.
package replica

import "github.com/legitimate-enterprises/rabbit-ears/server/internal/item"

// Schema is one version of the projection. The triggers, and the diffs and
// snapshots clients read, all derive from it.
type Schema struct {
	Version int
	Tables  []Table
	// Unsynced holds, per source table, the columns no replica column reads.
	// Every column of a source is read or listed here, so a new one is a
	// decision.
	Unsynced map[string][]string
	// Defines holds, per server table, the changes that alter which rows
	// some household's replica holds. A table here without a Table is
	// watched for those changes alone.
	Defines map[string]Defines
	// NotSynced is every other server table.
	NotSynced []string
}

// Table is one table of the replica, read from one server table.
type Table struct {
	Name   string
	Source string
	// Key is Source's primary key, which the replica table shares.
	Key  []string
	Cols []Col
	// Scope is the condition a Source row meets to be in a household's
	// replica, ?1 being the household's id; empty, every row is.
	Scope string
	// TombScope is the condition, over sync_tombstone's k1 to k3, a Source
	// tombstone meets to reach a household; empty, every one does.
	TombScope string
}

// Col is one replica column: Expr over the Source row, reading only the
// Source columns in Watch.
type Col struct {
	Name, Expr string
	Watch      []string
	// Type is the declared type of a column Expr derives, which no Source
	// column of its name declares.
	Type string
}

// Defines names the changes to a server table that alter a household's
// scope. A row leaving scope without being deleted leaves no tombstone, and
// one entering it may be older than the client's version, so either makes
// clients snapshot.
type Defines struct {
	Rows   bool     // inserting or deleting a row, or changing its key
	Update []string // changing one of these columns
	// When, if set, is the condition over the row, OLD or NEW, a change
	// above alters a scope only for.
	When func(row string) string
}

func plain(names ...string) []Col {
	cols := make([]Col, len(names))
	for i, n := range names {
		cols[i] = Col{Name: n, Expr: n, Watch: []string{n}}
	}
	return cols
}

// fileName is the last element of the path in column c, all a member sees of
// a path on the server.
func fileName(c string) string {
	return `substr(` + c + `, length(rtrim(` + c + `, replace(` + c + `, '/', ''))) + 1)`
}

// artPath is a metadata image as a member sees it: TMDB's path whole, since
// it names TMDB's image; one in a store as its address there, "store/name";
// any other, a file on the server, by its name.
func artPath(c string) Col {
	return Col{Name: c, Expr: `CASE WHEN source = 'remote' AND provider = 'tmdb' THEN ` + c + ` WHEN ` + item.StoreRef(c) + ` <> '' THEN ` +
		item.StoreRef(c) + ` ELSE ` + fileName(c) + ` END`,
		Watch: []string{c, "source", "provider"}}
}

// Scope fragments, each a SELECT of ids for the household ?1.
const (
	// members are the profiles a device of the household can be.
	members = `SELECT account_id FROM membership WHERE household_id = ?1`
	// owned are the playlists members own, playlists those and the ones that
	// reach them.
	owned     = `SELECT id FROM playlist WHERE account_id IN (` + members + `)`
	playlists = owned + ` UNION SELECT playlist_id FROM playlist_reach WHERE account_id IN (` + members + `)`
	// incoming are the friend requests to members, those sent to the email
	// of one other than its sender.
	incoming = `SELECT r.id FROM friend_request r JOIN account a ON a.email = r.email AND a.id <> r.from_id
		WHERE a.id IN (` + members + `)`
	// known are the accounts members see: who shares a household with one,
	// the adults linked with one, their friends, who asked one to be
	// friends, and the other side of their playlists and profile offers.
	known = `SELECT m2.account_id FROM membership m1 JOIN membership m2 ON m2.household_id = m1.household_id
			WHERE m1.account_id IN (` + members + `)
		UNION SELECT b_id FROM adult_link WHERE a_id IN (` + members + `)
		UNION SELECT a_id FROM adult_link WHERE b_id IN (` + members + `)
		UNION SELECT b_id FROM friendship WHERE a_id IN (` + members + `)
		UNION SELECT a_id FROM friendship WHERE b_id IN (` + members + `)
		UNION SELECT from_id FROM friend_request WHERE id IN (` + incoming + `)
		UNION SELECT account_id FROM playlist WHERE id IN (` + playlists + `)
		UNION SELECT account_id FROM playlist_share WHERE playlist_id IN (` + playlists + `)
		UNION SELECT from_id FROM profile_offer WHERE household_id = ?1 OR to_id IN (` + members + `)
		UNION SELECT to_id FROM profile_offer WHERE household_id = ?1 OR from_id IN (` + members + `)`
	// households are this one, those of known accounts and those members'
	// playlists are shared with.
	households = `SELECT ?1 UNION SELECT household_id FROM membership WHERE account_id IN (` + known + `)
		UNION SELECT household_id FROM playlist_household_share WHERE playlist_id IN (` + playlists + `)`
	// stations are those a member owns or may tune to, as station.SeenOf.
	stations = `SELECT s.id FROM station s JOIN account a ON a.id = s.account_id
		WHERE s.account_id IN (` + members + `) OR s.visibility = 'server' OR s.visibility = 'household' AND a.household_id = ?1`
	// aired are the playlists of those stations, which one of another
	// household brings without its owner's other playlists.
	aired = `SELECT playlist_id FROM station WHERE id IN (` + stations + `)`
	// collections are TMDB's, the household's and those an entry of a
	// playlist it holds names, without their owner's others.
	collections = `household_id IS NULL OR household_id = ?1
		OR id IN (SELECT collection_id FROM playlist_entry WHERE playlist_id IN (` + playlists + ` UNION ` + aired + `))`
)

// crossing is whether the playlist entry row names a household's own
// collection in a playlist other households may hold: one shared, aired by
// a station others may see, or of an owner in more than one household.
// Only such an entry decides whether another household holds the
// collection, so an edit of a playlist no one else holds snapshots no one.
func crossing(row string) string {
	return row + `.collection_id IN (SELECT id FROM collection WHERE household_id IS NOT NULL) AND (` + row + `.playlist_id IN (
		SELECT playlist_id FROM playlist_share UNION SELECT playlist_id FROM playlist_household_share
		UNION SELECT playlist_id FROM station WHERE visibility <> 'private')
		OR (SELECT COUNT(*) FROM membership m JOIN playlist p ON p.account_id = m.account_id WHERE p.id = ` + row + `.playlist_id) > 1)`
}

// V1 is sync schema 1. It carries only what the REST API shows a household
// member who is not an admin, and never a secret, a path on the server or
// an image's bytes. Catalog tables are whole: an adult sees every title.
var V1 = Schema{
	Version: 1,
	Tables: []Table{
		{Name: "library", Source: "library", Key: []string{"id"}, Cols: plain("id", "name", "kind")},
		{Name: "item", Source: "item", Key: []string{"id"}, Cols: plain("id", "library_id", "parent_id", "kind", "parsed_title",
			"parsed_year", "season_number", "episode_number", "disc_number", "track_number", "artist_id", "sort_title", "added_at",
			"kids_content", "explicit")},
		{Name: "item_metadata", Source: "item_metadata", Key: []string{"item_id", "source"}, Cols: append(plain("item_id", "source",
			"provider", "provider_id", "status", "title", "overview", "release_date", "runtime_minutes", "content_rating", "external_id",
			"published_at"), artPath("poster_path"), artPath("backdrop_path"),
			// item_shown reads them, and they change with every refresh.
			Col{Name: "fetched_at", Expr: "NULL"}, Col{Name: "retry_at", Expr: "NULL"})},
		{Name: "item_genre", Source: "item_genre", Key: []string{"item_id", "genre_id", "source"}, Cols: plain("item_id", "genre_id", "source")},
		{Name: "genre", Source: "genre", Key: []string{"id"}, Cols: plain("id", "name", "tmdb_id")},
		{Name: "person", Source: "person", Key: []string{"id"}, Cols: append(plain("id", "tmdb_id", "name", "profile_path"),
			Col{Name: "headshot_url", Expr: `CASE WHEN COALESCE(profile_path, '') <> '' THEN '` + item.ArtURL + `w185' || profile_path END`,
				Watch: []string{"profile_path"}, Type: "TEXT"})},
		{Name: "credit", Source: "credit", Key: []string{"id"}, Cols: plain("id", "item_id", "person_id", "kind", "character", "job",
			"department", "position")},
		{Name: "item_keyword", Source: "item_keyword", Key: []string{"item_id", "keyword_id"}, Cols: plain("item_id", "keyword_id")},
		{Name: "edition", Source: "edition", Key: []string{"id"}, Cols: plain("id", "item_id", "tag", "runtime_ms")},
		{Name: "edition_file", Source: "edition_file", Key: []string{"edition_id", "media_file_id"}, Cols: plain("edition_id", "media_file_id")},
		{Name: "media_file", Source: "media_file", Key: []string{"id"}, Cols: plain("id", "size_bytes", "container", "duration_ms",
			"bitrate", "missing_at")},
		{Name: "track", Source: "track", Key: []string{"id"}, Cols: plain("id", "media_file_id", "stream_index", "kind", "codec",
			"profile", "level", "language", "title", "is_default", "is_forced", "bitrate", "width", "height", "frame_rate", "bit_depth",
			"pixel_format", "color_primaries", "color_transfer", "hdr_format", "channels", "channel_layout", "sample_rate",
			"subtitle_is_text")},
		{Name: "chapter", Source: "chapter", Key: []string{"id"}, Cols: plain("id", "media_file_id", "position", "start_ms", "end_ms", "title")},
		{Name: "occasion", Source: "occasion", Key: []string{"id"}, Cols: plain("id", "name", "holiday", "month", "day", "days_before",
			"days_after", "color1", "color2", "color3", "created_at", "modified_at", "created_by", "modified_by")},
		{Name: "occasion_keyword", Source: "occasion_keyword", Key: []string{"occasion_id", "keyword_id"}, Cols: plain("occasion_id", "keyword_id")},
		{Name: "occasion_item", Source: "occasion_item", Key: []string{"occasion_id", "item_id"}, Cols: plain("occasion_id", "item_id")},
		{Name: "occasion_exclusion", Source: "occasion_exclusion", Key: []string{"occasion_id", "item_id"}, Cols: plain("occasion_id", "item_id")},
		{Name: "occasion_phrase", Source: "occasion_phrase", Key: []string{"occasion_id", "phrase"}, Cols: plain("occasion_id", "phrase")},
		// Admin settings, but item_shown and metadata_setting read them and
		// every item response shows their effect.
		{Name: "server_info", Source: "server_info", Key: []string{"id"}, Cols: plain("id", "name", "metadata_source", "metadata_mode")},

		{Name: "collection", Source: "collection", Key: []string{"id"}, Cols: plain("id", "source", "household_id", "tmdb_id", "name",
			"overview", "poster_path", "backdrop_path", "created_at", "modified_at", "created_by", "modified_by"), Scope: collections},
		{Name: "collection_movie", Source: "collection_movie", Key: []string{"collection_id", "item_id"},
			Cols: plain("collection_id", "item_id", "position"), Scope: `collection_id IN (SELECT id FROM collection WHERE ` + collections + `)`,
			TombScope: `k1 IN (SELECT id FROM collection WHERE ` + collections + `) OR ` + gone("k1", "collection")},

		{Name: "household", Source: "household", Key: []string{"id"}, Cols: plain("id", "name", "adult_id", "created_at", "modified_at", "created_by",
			"modified_by"),
			Scope: `id IN (` + households + `)`},
		{Name: "account", Source: "account", Key: []string{"id"}, Cols: append(plain("id", "kind", "email", "name", "is_admin",
			"household_id", "birthday", "filter_mode", "movie_ceiling", "tv_ceiling", "revoked_at", "created_at", "modified_at",
			"created_by", "modified_by", "hide_kids_content", "color", "no_autoplay"),
			Col{Name: "has_pin", Expr: `COALESCE(pin_hash, '') <> ''`, Watch: []string{"pin_hash"}}, pending),
			Scope: `id IN (` + members + `)`},
		// The other accounts members see, as a share or an offer shows them.
		{Name: "account_card", Source: "account", Key: []string{"id"}, Cols: append(plain("id", "name", "kind", "color", "household_id",
			"revoked_at"), pending), Scope: `id IN (` + known + `)`},
		{Name: "household_member", Source: "household_member", Key: []string{"household_id", "account_id"},
			Cols: plain("household_id", "account_id"), Scope: `household_id = ?1 OR account_id IN (` + members + `)`,
			TombScope: `k1 IN (` + households + `) OR k2 IN (` + known + `)`},
		{Name: "adult_link", Source: "adult_link", Key: []string{"a_id", "b_id"}, Cols: plain("a_id", "b_id", "tv", "created_at"),
			Scope:     `a_id IN (` + members + `) OR b_id IN (` + members + `)`,
			TombScope: `k1 IN (` + known + `) OR k2 IN (` + known + `)`},
		{Name: "friendship", Source: "friendship", Key: []string{"a_id", "b_id"}, Cols: plain("a_id", "b_id", "created_at"),
			Scope:     `a_id IN (` + members + `) OR b_id IN (` + members + `)`,
			TombScope: `k1 IN (` + known + `) OR k2 IN (` + known + `)`},
		// Members' own requests, as they sent them: whatever the receiver
		// does, none of these columns changes.
		{Name: "friend_request", Source: "friend_request", Key: []string{"id"}, Cols: plain("id", "from_id", "email", "created_at",
			"expires_at"), Scope: `from_id IN (` + members + `)`},
		// The requests to members, to_id being the member asked.
		{Name: "friend_request_in", Source: "friend_request", Key: []string{"id"}, Cols: append(plain("id", "from_id", "created_at",
			"expires_at"), Col{Name: "to_id", Expr: `(SELECT a.id FROM account a WHERE a.email = friend_request.email)`,
			Watch: []string{"email"}, Type: "BLOB"}), Scope: `id IN (` + incoming + `)`},
		// Its tombstones reach the decliner's households alone, so no
		// sender learns of a decline from one.
		{Name: "friend_request_decline", Source: "friend_request_decline", Key: []string{"account_id", "request_id"},
			Cols: plain("account_id", "request_id", "created_at"), Scope: `account_id IN (` + members + `)`,
			TombScope: `k1 IN (` + members + `)`},
		{Name: "profile_offer", Source: "profile_offer", Key: []string{"id"}, Cols: plain("id", "profile_id", "household_id", "from_id",
			"to_id", "created_at", "modified_at", "created_by", "modified_by"), Scope: `household_id = ?1 OR from_id IN (` + members + `) OR to_id IN (` + members + `)`},
		{Name: "account_allow", Source: "account_allow", Key: []string{"account_id", "item_id"}, Cols: plain("account_id", "item_id"),
			Scope: `account_id IN (` + members + `)`, TombScope: `k1 IN (` + members + `) OR ` + gone("k1", "account")},
		{Name: "watch_state", Source: "watch_state", Key: []string{"account_id", "item_id"}, Cols: plain("account_id", "item_id",
			"position_ms", "watched", "play_count", "last_played_at", "changed_at", "edition_id"),
			Scope: `account_id IN (` + members + `)`, TombScope: `k1 IN (` + members + `) OR ` + gone("k1", "account")},
		{Name: "favorite", Source: "favorite", Key: []string{"account_id", "item_id"}, Cols: plain("account_id", "item_id", "created_at"),
			Scope: `account_id IN (` + members + `)`, TombScope: `k1 IN (` + members + `) OR ` + gone("k1", "account")},
		// station_owned is not in a response, but which playlists the lists
		// leave out is.
		{Name: "playlist", Source: "playlist", Key: []string{"id"}, Cols: plain("id", "account_id", "name", "kind", "last_played_at",
			"created_at", "modified_at", "created_by", "modified_by", "station_owned"), Scope: `id IN (` + playlists + ` UNION ` + aired + `)`},
		{Name: "playlist_entry", Source: "playlist_entry", Key: []string{"id"}, Cols: plain("id", "playlist_id", "item_id",
			"collection_id", "rank", "added_at", "edition_id", "created_at", "modified_at", "created_by", "modified_by"), Scope: `playlist_id IN (` + playlists + ` UNION ` + aired + `)`},
		{Name: "playlist_share", Source: "playlist_share", Key: []string{"playlist_id", "account_id"},
			Cols:      plain("playlist_id", "account_id", "access", "created_at"),
			Scope:     `account_id IN (` + members + `) OR playlist_id IN (` + owned + `)`,
			TombScope: `k1 IN (` + playlists + `) OR k2 IN (` + known + `)`},
		{Name: "playlist_household_share", Source: "playlist_household_share", Key: []string{"playlist_id", "household_id"},
			Cols:      plain("playlist_id", "household_id", "access", "created_at"),
			Scope:     `household_id = ?1 OR playlist_id IN (` + owned + `)`,
			TombScope: `k1 IN (` + playlists + `) OR k2 IN (` + households + `)`},
		// household_id is the owner's, whose account another household's
		// replica lacks; a change to it changes scope, so it reaches
		// clients by snapshot.
		{Name: "station", Source: "station", Key: []string{"id"}, Cols: append(plain("id", "account_id", "playlist_id", "name", "shuffled",
			"visibility", "rating", "loop", "created_at", "modified_at", "created_by", "modified_by", "timing"),
			Col{Name: "household_id", Expr: `(SELECT o.household_id FROM account o WHERE o.id = station.account_id)`, Watch: []string{"account_id"},
				Type: "BLOB"}),
			Scope: `id IN (` + stations + `)`},
		{Name: "station_day", Source: "station_day", Key: []string{"station_id", "weekday"}, Cols: plain("station_id", "weekday"),
			Scope: `station_id IN (` + stations + `)`, TombScope: `k1 IN (` + stations + `) OR ` + gone("k1", "station")},
		{Name: "station_time", Source: "station_time", Key: []string{"id"}, Cols: plain("id", "station_id", "from_minute", "to_minute"),
			Scope: `station_id IN (` + stations + `)`},
		{Name: "station_program", Source: "station_program", Key: []string{"id"}, Cols: plain("id", "station_id", "item_id",
			"edition_id", "entry_id", "loop", "position", "starts_at", "ends_at"), Scope: `station_id IN (` + stations + `)`},
		{Name: "station_loop", Source: "station_loop", Key: []string{"station_id", "position"}, Cols: plain("station_id", "position",
			"entry_id", "item_id"), Scope: `station_id IN (` + stations + `)`,
			TombScope: `k1 IN (` + stations + `) OR ` + gone("k1", "station")},
		// item_id NULL marks an uploaded image.
		{Name: "cover", Source: "cover", Key: []string{"id"}, Cols: plain("id", "playlist_id", "station_id", "item_id", "backdrop",
			"created_at"), Scope: `playlist_id IN (` + playlists + `) OR station_id IN (` + stations + `)`},
		// The household's devices share one replica; each keeps its own.
		{Name: "download", Source: "download", Key: []string{"id"}, Cols: plain("id", "device_id", "account_id", "item_id",
			"edition_id", "media_file_id", "quality", "created_at", "modified_at", "created_by", "modified_by"),
			Scope: `device_id IN (SELECT id FROM device WHERE household_id = ?1)`},
	},
	Unsynced: map[string][]string{
		"library": {"scan_hour", "watch_files", "last_scanned_at", "created_at", "modified_at", "created_by", "modified_by"},
		// A system row's audit columns move with every scan and probe
		// (item.Update rewrites modified_at on each), and no member reads them.
		"item":            {"path", "created_at", "modified_at"},
		"item_metadata":   {"fetched_at", "retry_at", "sidecar_stamp", "identity_stamp"},
		"media_file":      {"path", "file_modified_at", "created_at", "modified_at", "probed_at", "previews_failed_for", "probe_failed_at", "probe_error", "fingerprint"},
		"genre":           {"created_at", "modified_at"},
		"person":          {"created_at", "modified_at"},
		"credit":          {"created_at", "modified_at"},
		"edition":         {"created_at", "modified_at"},
		"track":           {"dv_profile", "created_at", "modified_at"},
		"chapter":         {"thumbnail_path", "created_at", "modified_at"},
		"station_program": {"created_at", "modified_at"},
		"server_info": {"created_at", "modified_at", "tasks_paused", "tmdb_key", "gravatar", "read_ahead_seconds", "max_transcodes",
			"transcode_cache_cap_bytes"},
		"account":          {"pin_failures", "pin_locked_until"},
		"household_member": {"created_at"},
		// A station's time range and a cover show no audit field in a
		// response.
		"station_time": {"created_at", "modified_at", "created_by", "modified_by"},
		"cover":        {"jpeg", "modified_at", "created_by", "modified_by"},
		"download":     {"conversion_id"},
		// A request is never edited; who made it is its sender.
		"friend_request": {"modified_at", "created_by", "modified_by"},
	},
	Defines: map[string]Defines{
		"household_member":         {Rows: true},
		"adult_link":               {Rows: true, Update: []string{"tv"}},
		"friendship":               {Rows: true},
		"friend_request":           {Rows: true},
		"profile_offer":            {Rows: true},
		"playlist_share":           {Rows: true},
		"playlist_household_share": {Rows: true},
		"account":                  {Update: []string{"household_id", "email"}},
		"playlist":                 {Update: []string{"account_id"}},
		"station":                  {Rows: true, Update: []string{"visibility", "account_id", "playlist_id"}},
		"playlist_entry":           {Rows: true, Update: []string{"playlist_id", "collection_id"}, When: crossing},
		"device":                   {Update: []string{"household_id"}},
	},
	NotSynced: []string{
		"account_identity", "conversion", "device_link", "identity_evidence", "identity_review", "join_code",
		"library_folder", "log_entry", "occasion_seed", "playback_session", "seek_preview", "session_plan_entry", "song_release",
		"sso_provider", "task", "task_run", "throughput_sample", "watch_together", "watch_together_member",
		"schema_migration", "sync_clock", "sync_tombstone", "sync_action",
	},
}

// gone is whether the row of table that key part k names no longer
// exists. Deleting it deletes its rows elsewhere, and their tombstones go to
// every household: one scoped through it could no longer tell whether it
// held them.
func gone(k, table string) string { return k + ` NOT IN (SELECT id FROM ` + table + `)` }

// pending marks an adult invited who has not yet joined.
var pending = Col{Name: "pending", Expr: `kind = 'adult' AND COALESCE(password_hash, '') = ''`, Watch: []string{"kind", "password_hash"}}

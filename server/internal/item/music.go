package item

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// MusicBrainz is the provider of music metadata rows. Their PosterPath is a
// local image file the scanner wrote, not a provider image name.
const MusicBrainz = "musicbrainz"

// SortName is a name without a leading "The ", which music sorts and files
// letters by; a name that is only "The ..." keeps what follows.
func SortName(s string) string {
	if len(s) > 4 && strings.EqualFold(s[:4], "the ") {
		return strings.TrimSpace(s[4:])
	}
	return s
}

// losslessTrack is mediafile.Lossless over a track row named t.
const losslessTrack = `(t.codec IN ('flac','alac','wavpack') OR substr(t.codec, 1, 4) = 'pcm_')`

// MusicFilter narrows the music views. Zero values mean no constraint.
type MusicFilter struct {
	LibraryID *uid.ID
	ArtistID  *uid.ID // albums: the album artist; songs: the performer or album artist
	AlbumID   *uid.ID
	GenreID   *uid.ID
	IDs       []uid.ID
	Letter    string  // A to Z or #, of the sort name
	Q         string  // case-insensitive substring of the name or title; prefix matches first
	Sort      string  // the API's sort names; empty is the view's default
	AccountID *uid.ID // whose plays fill an album's LastPlayedAt
	// Clean keeps what a kid hears, as VisibleOf says, and counts and adds
	// up only songs not explicit.
	Clean  bool
	Limit  int // 0 means 100
	Offset int
}

// args binds f as ?1 to ?11 in the order the music queries use.
func (f MusicFilter) args() []any {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	ids, _ := json.Marshal(f.IDs)
	return []any{f.LibraryID, f.ArtistID, f.AlbumID, f.GenreID, f.Letter, f.Q, string(ids), f.AccountID, f.Limit, f.Offset, f.Clean}
}

func sortKey(a string) string {
	return `COALESCE(` + a + `.sort_title, ` + a + `.parsed_title) COLLATE NOCASE`
}

func letterOf(a string) string {
	first := `upper(substr(COALESCE(` + a + `.sort_title, ` + a + `.parsed_title), 1, 1))`
	return `CASE WHEN ` + first + ` BETWEEN 'A' AND 'Z' THEN ` + first + ` ELSE '#' END`
}

// counted keeps a song alias s that clean, an SQL truth value, lets count.
func counted(s, clean string) string { return `(` + clean + ` = 0 OR ` + cleanSong(s) + `)` }

// common is the WHERE shared by the views over item alias a: library,
// genre (on a or on g, its album), letter, query, ids and Clean.
func common(f MusicFilter, a, g string) string {
	w := `(?1 IS NULL OR ` + a + `.library_id = ?1) AND (?11 = 0 OR ` + CleanOf(a) + `)
		AND (?4 IS NULL OR EXISTS (SELECT 1 FROM item_genre_shown ig WHERE ig.item_id IN (` + a + `.id, ` + g + `.id) AND ig.genre_id = ?4))
		AND (?5 = '' OR ` + letterOf(a) + ` = ?5)
		AND (?6 = '' OR instr(lower(` + a + `.parsed_title), lower(?6)) > 0)`
	if len(f.IDs) > 0 {
		// Only a term that stands alone lets the ids drive the primary key.
		w += ` AND ` + a + `.id IN (SELECT unhex(value, '-') FROM json_each(?7))`
	}
	return w
}

// kind is alias a's kind column, kept off the kind index when f names ids:
// without statistics SQLite would rather walk every row of the kind than
// look the ids up.
func kind(f MusicFilter, a string) string {
	if len(f.IDs) > 0 {
		return `+` + a + `.kind`
	}
	return a + `.kind`
}

// order puts prefix matches of a query first unless an explicit sort was asked for.
func order(f MusicFilter, a, by string) string {
	if f.Q != "" && f.Sort == "" {
		by = `instr(lower(` + a + `.parsed_title), lower(?6)) != 1, ` + by
	}
	return ` ORDER BY ` + by + ` LIMIT ?9 OFFSET ?10`
}

// ArtistView is an artist with its counts.
type ArtistView struct {
	*Item
	AlbumCount int // albums whose album artist this is
	SongCount  int // songs on those albums or performed by the artist
}

// artistsFrom is the rows Artists pages and CountArtists counts.
func artistsFrom(f MusicFilter) string {
	return ` FROM item a WHERE ` + kind(f, "a") + ` = 'artist' AND ` + common(f, "a", "a")
}

// AlbumRows are the albums al with their album artists ar.
const AlbumRows = `item al JOIN item ar ON ar.id = al.parent_id`

// SongRows are the songs s with their albums al, album artists ar and
// performers pa.
const SongRows = `item s JOIN item al ON al.id = s.parent_id JOIN item ar ON ar.id = al.parent_id LEFT JOIN item pa ON pa.id = s.artist_id`

// albumsFrom is the rows Albums pages and CountAlbums counts.
func albumsFrom(f MusicFilter) string {
	return ` FROM ` + AlbumRows + `
		WHERE ` + kind(f, "al") + ` = 'album' AND (?2 IS NULL OR al.parent_id = ?2) AND ` + common(f, "al", "al")
}

// songsFrom is the rows Songs pages and CountSongs counts.
func songsFrom(f MusicFilter) string {
	return ` FROM ` + SongRows + `
		WHERE ` + kind(f, "s") + ` = 'song' AND (?2 IS NULL OR al.parent_id = ?2 OR s.artist_id = ?2) AND (?3 IS NULL OR s.parent_id = ?3)
			AND ` + common(f, "s", "al")
}

func (r *Repo) count(from string, f MusicFilter) (n int, err error) {
	return n, db.Wrap(r.d.QueryRow(`SELECT COUNT(*)`+from, f.args()...).Scan(&n))
}

// CountArtists is how many artists f matches, ignoring Limit and Offset.
func (r *Repo) CountArtists(f MusicFilter) (int, error) { return r.count(artistsFrom(f), f) }

// CountAlbums is how many albums f matches, ignoring Limit and Offset.
func (r *Repo) CountAlbums(f MusicFilter) (int, error) { return r.count(albumsFrom(f), f) }

// CountSongs is how many songs f matches, ignoring Limit and Offset.
func (r *Repo) CountSongs(f MusicFilter) (int, error) { return r.count(songsFrom(f), f) }

// ArtistAlbumsOf is how many albums artist a is the album artist of,
// only those a kid hears while clean, an SQL truth value, holds.
func ArtistAlbumsOf(a, clean string) string {
	return `(SELECT COUNT(*) FROM item al WHERE al.parent_id = ` + a + `.id AND al.kind = 'album' AND (` + clean + ` = 0 OR ` + CleanOf("al") + `))`
}

// ArtistSongsOf is how many songs are on artist a's albums or performed by
// a, only those not explicit while clean holds.
func ArtistSongsOf(a, clean string) string {
	// Two counts so each walks one artist's rows by index; +s.kind keeps
	// SQLite off the kind index, which walks every song.
	return `((SELECT COUNT(*) FROM item al JOIN item s ON s.parent_id = al.id WHERE al.parent_id = ` + a + `.id AND +s.kind = 'song' AND ` + counted("s", clean) + `)
		+ (SELECT COUNT(*) FROM item s JOIN item al ON al.id = s.parent_id
			WHERE s.artist_id = ` + a + `.id AND +s.kind = 'song' AND al.parent_id IS NOT ` + a + `.id AND ` + counted("s", clean) + `))`
}

// Artists pages artists A to Z by sort name.
func (r *Repo) Artists(f MusicFilter) ([]*ArtistView, error) {
	rows, err := r.d.Query(`SELECT `+prefixed("a")+`, `+ArtistAlbumsOf("a", "?11")+`, `+ArtistSongsOf("a", "?11")+`
		`+artistsFrom(f)+order(f, "a", sortKey("a")+`, a.id`), f.args()...)
	return db.Collect(rows, err, func(s db.Scanner) (*ArtistView, error) {
		v := &ArtistView{Item: &Item{}}
		return v, scanWith(s, v.Item, &v.AlbumCount, &v.SongCount)
	})
}

// AlbumView is an album with its album artist's name and its totals.
type AlbumView struct {
	*Item
	ArtistName   string
	SongCount    int
	DurationMs   int64
	Lossless     bool // every song has a lossless audio track
	LastPlayedAt *time.Time
}

// AlbumSongsOf is how many songs album al holds, only those not explicit
// while clean, an SQL truth value, holds.
func AlbumSongsOf(al, clean string) string {
	return `(SELECT COUNT(*) FROM item s WHERE s.parent_id = ` + al + `.id AND ` + counted("s", clean) + `)`
}

// AlbumDurationOf is the length of every file of album al's songs, only
// those not explicit while clean holds.
func AlbumDurationOf(al, clean string) string {
	return `(SELECT COALESCE(SUM(f.duration_ms), 0) FROM item s JOIN item_file x ON x.item_id = s.id JOIN media_file f ON f.id = x.media_file_id
		WHERE s.parent_id = ` + al + `.id AND ` + counted("s", clean) + `)`
}

// AlbumLosslessOf is whether album al has songs and each has a lossless
// audio track.
func AlbumLosslessOf(al string) string {
	return `(EXISTS (SELECT 1 FROM item s WHERE s.parent_id = ` + al + `.id) AND NOT EXISTS (SELECT 1 FROM item s WHERE s.parent_id = ` + al + `.id
		AND NOT EXISTS (SELECT 1 FROM item_file x JOIN track t ON t.media_file_id = x.media_file_id
			WHERE x.item_id = s.id AND t.kind = 'audio' AND ` + losslessTrack + `)))`
}

// Albums pages albums; sorts are title (default), artist, year and added.
func (r *Repo) Albums(f MusicFilter) ([]*AlbumView, error) {
	by := map[string]string{
		"artist": sortKey("ar") + `, al.parsed_year, ` + sortKey("al") + `, al.id`,
		"year":   `al.parsed_year IS NULL, al.parsed_year DESC, ` + sortKey("al") + `, al.id`,
		"added":  `al.added_at DESC, al.id DESC`,
	}[f.Sort]
	if by == "" {
		by = sortKey("al") + `, al.id`
	}
	rows, err := r.d.Query(`SELECT `+prefixed("al")+`, ar.parsed_title, `+AlbumSongsOf("al", "?11")+`, `+AlbumDurationOf("al", "?11")+`,
			`+AlbumLosslessOf("al")+`,
			(SELECT MAX(p.started_at) FROM playback_session p JOIN item s ON s.id = p.item_id WHERE s.parent_id = al.id AND p.account_id = ?8 AND p.station = 0)
		`+albumsFrom(f)+order(f, "al", by), f.args()...)
	return db.Collect(rows, err, func(s db.Scanner) (*AlbumView, error) {
		v := &AlbumView{Item: &Item{}}
		var played sql.NullInt64
		err := scanWith(s, v.Item, &v.ArtistName, &v.SongCount, &v.DurationMs, &v.Lossless, &played)
		v.LastPlayedAt = db.FromNullMs(played)
		return v, err
	})
}

// SongView is a song with its album, artists and file facts.
type SongView struct {
	*Item
	AlbumTitle    string
	AlbumArtistID uid.ID
	PerformerID   uid.ID // the song's own artist, else the album artist
	ArtistName    string
	DurationMs    *int64
	Year          *int // the song's, else its album's
	Codec         string
	Lossless      bool
	Unavailable   bool // it has files, all marked missing
}

// SongColumns are what SongView adds to a song, over SongRows.
var SongColumns = `al.parsed_title AS album_title, al.parent_id AS album_artist_id, COALESCE(s.artist_id, al.parent_id) AS performer_id,
	COALESCE(pa.parsed_title, ar.parsed_title) AS artist_name,
	(SELECT f.duration_ms FROM item_file x JOIN media_file f ON f.id = x.media_file_id WHERE x.item_id = s.id ORDER BY f.id LIMIT 1) AS duration_ms,
	COALESCE(s.parsed_year, al.parsed_year) AS year,
	(SELECT t.codec FROM item_file x JOIN track t ON t.media_file_id = x.media_file_id WHERE x.item_id = s.id AND t.kind = 'audio'
		ORDER BY x.media_file_id, t.stream_index LIMIT 1) AS codec, ` + mediafile.UnavailableOf("s.id") + ` AS unavailable`

// Songs pages songs. With AlbumID and no sort they come in disc then track
// order; sorts are title (the default otherwise), artist, album, added and
// popular, which is most played sessions across every profile first.
func (r *Repo) Songs(f MusicFilter) ([]*SongView, error) {
	track := `COALESCE(s.disc_number, 1), s.track_number IS NULL, s.track_number, ` + sortKey("s") + `, s.id`
	by := map[string]string{
		"title":   sortKey("s") + `, s.id`,
		"artist":  `COALESCE(pa.sort_title, pa.parsed_title, ar.sort_title, ar.parsed_title) COLLATE NOCASE, ` + sortKey("al") + `, al.id, ` + track,
		"album":   sortKey("al") + `, al.id, ` + track,
		"added":   `s.added_at DESC, s.id DESC`,
		"popular": `(SELECT COUNT(*) FROM playback_session p WHERE p.item_id = s.id AND p.station = 0) DESC, ` + sortKey("s") + `, s.id`,
	}[f.Sort]
	switch {
	case by != "":
	case f.AlbumID != nil:
		by = track
	default:
		by = sortKey("s") + `, s.id`
	}
	rows, err := r.d.Query(`SELECT `+prefixed("s")+`, `+SongColumns+`
		`+songsFrom(f)+order(f, "s", by), f.args()...)
	return db.Collect(rows, err, func(sc db.Scanner) (*SongView, error) {
		v := &SongView{Item: &Item{}}
		var dur, year sql.NullInt64
		var codec sql.NullString
		err := scanWith(sc, v.Item, &v.AlbumTitle, &v.AlbumArtistID, &v.PerformerID, &v.ArtistName, &dur, &year, &codec, &v.Unavailable)
		v.DurationMs, v.Year, v.Codec = ptr64(dur), db.FromNullInt(year), codec.String
		v.Lossless = mediafile.Lossless(v.Codec)
		return v, err
	})
}

// LetterCount is how many artists or albums file under one letter.
type LetterCount struct {
	Letter string
	Count  int
}

// Letters counts the artists or albums (kind) of a library, or of every
// library for nil, under each letter of their sort name: A to Z, then #;
// clean counts only what a kid hears.
func (r *Repo) Letters(libraryID *uid.ID, kind string, clean bool) ([]LetterCount, error) {
	rows, err := r.d.Query(`SELECT `+letterOf("i")+` AS letter, COUNT(*) FROM item i
		WHERE i.kind = ?2 AND (?1 IS NULL OR i.library_id = ?1) AND (?3 = 0 OR `+CleanOf("i")+`) GROUP BY letter`, libraryID, kind, clean)
	out, err := db.Collect(rows, err, func(s db.Scanner) (LetterCount, error) {
		var c LetterCount
		return c, db.Wrap(s.Scan(&c.Letter, &c.Count))
	})
	slices.SortFunc(out, func(a, b LetterCount) int {
		if (a.Letter == "#") != (b.Letter == "#") {
			if a.Letter == "#" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Letter, b.Letter)
	})
	return out, err
}

// Recent is one recently played album, artist or music playlist.
type Recent struct {
	Kind     string // album, artist or playlist
	ID       uid.ID
	PlayedAt time.Time
}

// RecentMusic pages what an account played last, newest first. A song's
// session counts for its album, its album artist and its performer; a music
// playlist counts from its last_played_at. kind "" mixes albums and
// playlists.
func (r *Repo) RecentMusic(accountID uid.ID, kind string, limit, offset int) ([]Recent, error) {
	parts := map[string]string{
		"album": `SELECT 'album', s.parent_id, MAX(p.started_at) FROM playback_session p JOIN item s ON s.id = p.item_id
			WHERE p.account_id = ?1 AND p.station = 0 AND s.kind = 'song' GROUP BY s.parent_id`,
		"artist": `SELECT 'artist', a, MAX(at) FROM (
				SELECT al.parent_id AS a, p.started_at AS at FROM playback_session p JOIN item s ON s.id = p.item_id
					JOIN item al ON al.id = s.parent_id WHERE p.account_id = ?1 AND p.station = 0 AND s.kind = 'song'
				UNION ALL SELECT s.artist_id, p.started_at FROM playback_session p JOIN item s ON s.id = p.item_id
					WHERE p.account_id = ?1 AND p.station = 0 AND s.kind = 'song' AND s.artist_id IS NOT NULL) GROUP BY a`,
		"playlist": `SELECT 'playlist', id, last_played_at FROM playlist WHERE account_id = ?1 AND kind = 'music' AND NOT station_owned AND last_played_at IS NOT NULL`,
	}
	q := parts[kind]
	if kind == "" {
		q = parts["album"] + ` UNION ALL ` + parts["playlist"]
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.d.Query(`SELECT * FROM (`+q+`) ORDER BY 3 DESC, 2 LIMIT ?2 OFFSET ?3`, accountID, limit, offset)
	return db.Collect(rows, err, func(s db.Scanner) (Recent, error) {
		var v Recent
		var at int64
		err := s.Scan(&v.Kind, &v.ID, &at)
		v.PlayedAt = db.FromMs(at)
		return v, db.Wrap(err)
	})
}

// Named finds an item of a kind by title, ignoring case, under parentID
// (nil for a root) in a library: the scanner's artist and album identity.
func (r *Repo) Named(libraryID uid.ID, parentID *uid.ID, kind, title string) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE library_id = ? AND kind = ? AND parent_id IS ? AND parsed_title = ? COLLATE NOCASE
		ORDER BY id LIMIT 1`, libraryID, kind, parentID, title))
}

// Performs reports whether any song names this artist as its performer.
func (r *Repo) Performs(artistID uid.ID) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM item WHERE artist_id = ?`, artistID).Scan(&n)
	return n > 0, db.Wrap(err)
}

// NeverLookedUp lists a library's albums that no provider was asked about:
// no remote row, or one never fetched.
func (r *Repo) NeverLookedUp(libraryID uid.ID, limit int) ([]*Item, error) {
	return r.query(`SELECT `+prefixed("i")+` FROM item i LEFT JOIN item_metadata m ON m.item_id = i.id AND m.source = 'remote'
		WHERE i.library_id = ? AND i.kind = 'album' AND (m.item_id IS NULL OR m.fetched_at IS NULL AND m.retry_at IS NULL)
		ORDER BY i.id LIMIT ?`, libraryID, limit)
}

// scanWith scans an item's columns into it, then the extra columns.
func scanWith(s db.Scanner, it *Item, extra ...any) error {
	var year, season, episode, disc, track sql.NullInt64
	var kids, explicit sql.NullBool
	var path, sortTitle sql.NullString
	var added, created, modified int64
	if err := s.Scan(append([]any{&it.ID, &it.LibraryID, &it.ParentID, &it.Kind, &path, &it.ParsedTitle, &year, &season, &episode,
		&disc, &track, &it.ArtistID, &sortTitle, &kids, &explicit, &added, &created, &modified}, extra...)...); err != nil {
		return db.Wrap(err)
	}
	if kids.Valid {
		it.KidsContent = &kids.Bool
	}
	if explicit.Valid {
		it.Explicit = &explicit.Bool
	}
	it.ParsedYear = db.FromNullInt(year)
	it.SeasonNumber, it.EpisodeNumber = db.FromNullInt(season), db.FromNullInt(episode)
	it.DiscNumber, it.TrackNumber = db.FromNullInt(disc), db.FromNullInt(track)
	it.Path, it.SortTitle = path.String, sortTitle.String
	it.AddedAt, it.CreatedAt, it.ModifiedAt = db.FromMs(added), db.FromMs(created), db.FromMs(modified)
	return nil
}

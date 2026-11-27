// Package item holds the library tree model (movies, shows, seasons and
// episodes; artists, albums and songs; channels and videos) and its
// repository. File-derived fields live on Item; what the files' tags, art
// and sidecars say and what a provider says live in two Metadata rows, local
// and remote, so a rescan never overwrites a match and a match never hides
// what the files said; the metadata settings pick between them per field
// when read.
package item

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Kinds of item.
const (
	Movie   = "movie"
	Show    = "show"
	Season  = "season"
	Episode = "episode"
	Artist  = "artist"
	Album   = "album"
	Song    = "song"
	Channel = "channel"
	Video   = "video"
)

// CatalogKinds are the kinds the generic item API serves; music and YouTube
// items have their own views.
var CatalogKinds = []string{Movie, Show, Season, Episode}

// Metadata match states.
const (
	Unmatched = "unmatched"
	Matched   = "matched"
)

// Item is one node of the library tree: movies, shows, artists and channels
// are roots; seasons hang off shows and episodes off seasons (specials are
// season 0); albums hang off their album artist and songs off their album;
// videos hang off their channel.
type Item struct {
	ID            uid.ID
	LibraryID     uid.ID
	ParentID      *uid.ID
	Kind          string
	Path          string // folder for shows and seasons; empty for episodes and movies
	ParsedTitle   string
	ParsedYear    *int
	SeasonNumber  *int
	EpisodeNumber *int
	DiscNumber    *int
	TrackNumber   *int
	// ArtistID is who performs a song when that is not its album artist.
	ArtistID *uid.ID
	// SortTitle is the title without a leading "The ", which music sorts
	// and files letters by; empty sorts by ParsedTitle.
	SortTitle string
	// KidsContent is, on a movie or show, an adult's override of whether
	// it is kids' content (nil leaves it to the rating), and on a channel
	// whether it is for kids (nil is not). Update leaves it; SetKidsContent
	// writes it.
	KidsContent *bool
	// Explicit is a song's advisory tag, nil until a probe read it.
	Explicit   *bool
	AddedAt    time.Time
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// KidsRatings are the ratings that make a movie or show kids' content.
var KidsRatings = map[string][]string{Movie: {"G"}, Show: {"TV-Y", "TV-Y7", "TV-G"}}

// IsKidsContent is whether a movie or show is kids' content: its override,
// else whether its rating is one of KidsRatings.
func IsKidsContent(kind, rating string, override *bool) bool {
	if override != nil {
		return *override
	}
	return slices.Contains(KidsRatings[kind], rating)
}

// Metadata is what a provider said about an item. Absent until first fetched.
type Metadata struct {
	ItemID         uid.ID
	Provider       string
	ProviderID     *int64
	Status         string
	Title          string
	Overview       string
	ReleaseDate    string // ISO date, kept as the provider gave it
	RuntimeMinutes *int
	ContentRating  string
	PosterPath     string
	BackdropPath   string
	// ExternalID is a provider's string id: a MusicBrainz MBID, or a
	// YouTube channel or video id.
	ExternalID string
	FetchedAt  *time.Time
	RetryAt    *time.Time
	// PosterFrom and BackdropFrom name the provider an image read through
	// Metadata came from, Local for a file on disk.
	PosterFrom, BackdropFrom string
}

// Local is the provider of local rows: what an item's files and sidecars
// say, beside the remote row a provider fills.
const Local = "local"

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	LibraryID *uid.ID
	Kind      string
	ParentID  *uid.ID
	Audience  Audience
	// Q is a case-insensitive substring of a movie's or show's parsed or
	// provider title, or of an episode's provider title.
	Q        string
	GenreID  *uid.ID
	PersonID *uid.ID
	// Catalog keeps only CatalogKinds.
	Catalog bool
	// KidsOnly keeps kids' content, as KidsContentOf says.
	KidsOnly bool
	// UHD keeps movies and episodes with a file whose video is at least
	// 3200 wide or 2000 tall, and shows and seasons with such an episode.
	UHD bool
	// BestFirst puts titles starting with Q before the other matches.
	BestFirst bool
	// Newest orders by when the item was added, latest first, instead of by title.
	Newest bool
	Limit  int // 0 means 100
	Offset int
	// OccasionID keeps the occasion's members.
	OccasionID *uid.ID
	// ID keeps only the item with this id.
	ID *uid.ID
}

// where binds a Filter as ?1 to ?3 and ?11 to ?17, around VisibleWhere.
var where = `(?1 IS NULL OR library_id = ?1) AND (?2 = '' OR kind = ?2) AND (?3 IS NULL OR parent_id = ?3) AND ` + VisibleWhere + `
	AND (?11 = '' OR (kind IN ('movie','show') AND instr(lower(parsed_title), lower(?11)) > 0)
		OR (kind IN ('movie','show','episode') AND instr(lower((SELECT m.title FROM item_shown m WHERE m.item_id = item.id)), lower(?11)) > 0))
	AND (?12 IS NULL OR EXISTS (SELECT 1 FROM item_genre_shown g WHERE g.item_id = item.id AND g.genre_id = ?12))
	AND (?13 IS NULL OR EXISTS (SELECT 1 FROM credit_shown c WHERE c.item_id = item.id AND c.person_id = ?13))
	AND (?14 = 0 OR kind IN ('movie','show','season','episode'))
	AND (?15 = 0 OR EXISTS (SELECT 1 FROM item_file f JOIN track t ON t.media_file_id = f.media_file_id
		WHERE t.kind = 'video' AND (t.width >= 3200 OR t.height >= 2000) AND (f.item_id = item.id
			OR f.item_id IN (SELECT e.id FROM item e WHERE e.parent_id = item.id
				OR e.parent_id IN (SELECT s.id FROM item s WHERE s.parent_id = item.id)))))
	AND (?16 IS NULL OR item.id IN (` + OccasionMembersOf("?16") + `))
	AND (?17 IS NULL OR item.id = ?17)`

// kidsOnly is the clause that keeps f to kids' content.
func (f Filter) kidsOnly() string {
	if f.KidsOnly {
		return ` AND ` + KidsContentOf("item")
	}
	return ""
}

func (f Filter) args(limit, offset any) []any {
	return append(append([]any{f.LibraryID, f.Kind, f.ParentID, limit, offset}, f.Audience.Args()...), f.Q, f.GenreID, f.PersonID, f.Catalog, f.UHD,
		f.OccasionID, f.ID)
}

// Audience limits items to what one kid may see, or keeps kids' content
// out of an adult's browsing. The zero value is an adult and sees
// everything.
type Audience struct {
	Kid       bool
	AccountID *uid.ID  // whose allow list applies
	AllowList bool     // only allowed items (or their ancestors) instead of ratings
	Movie     []string // ratings a movie may carry
	TV        []string // ratings a show may carry; seasons and episodes inherit it
	// HideKids leaves out an adult's kids' content: movies and shows that
	// are, with their seasons and episodes, and channels for kids with
	// their videos.
	HideKids bool
}

// rootOf is the movie, show, artist or channel at the top of t's tree.
func rootOf(t string) string {
	return `COALESCE((SELECT vp.parent_id FROM item vp WHERE vp.id = ` + t + `.parent_id), ` + t + `.parent_id, ` + t + `.id)`
}

// KidsContentOf is whether t's root is kids' content, as IsKidsContent
// says: a movie or show by its override or rating, a channel for kids. It
// reads item_shown by key; a LEFT JOIN onto it builds the whole view.
func KidsContentOf(t string) string {
	rating := `(SELECT vm.content_rating FROM item_shown vm WHERE vm.item_id = vr.id)`
	return `EXISTS (SELECT 1 FROM item vr WHERE vr.id = ` + rootOf(t) + `
		AND COALESCE(vr.kids_content, vr.kind = 'movie' AND ` + rating + ` IN ('` + strings.Join(KidsRatings[Movie], "','") + `')
			OR vr.kind = 'show' AND ` + rating + ` IN ('` + strings.Join(KidsRatings[Show], "','") + `')) = 1)`
}

// cleanSong holds a song alias to one read for the advisory tag and not
// explicit.
func cleanSong(s string) string { return s + `.explicit = 0` }

// CleanOf is whether a kid hears t: a song not explicit, an album or
// artist with such a song of its own or, for an artist, one it performs.
func CleanOf(t string) string {
	return `CASE ` + t + `.kind WHEN 'song' THEN ` + cleanSong(t) + `
		WHEN 'album' THEN EXISTS (SELECT 1 FROM item vs WHERE vs.parent_id = ` + t + `.id AND ` + cleanSong("vs") + `)
		WHEN 'artist' THEN EXISTS (SELECT 1 FROM item vs WHERE ` + cleanSong("vs") + ` AND +vs.kind = 'song'
			AND (vs.artist_id = ` + t + `.id OR vs.parent_id IN (SELECT va.id FROM item va WHERE va.parent_id = ` + t + `.id)))
		ELSE 0 END`
}

// AudienceSQL is the SQL of each value of an Audience's Args.
type AudienceSQL struct{ Mode, AccountID, AllowList, Movie, TV string }

// VisibleTo is the WHERE fragment over the item alias t for the audience
// a. A kid hears music but explicit songs and what holds only them; sees
// YouTube channels for kids, or on an allow list those on it, with their
// videos; and sees movies and shows by rating, an unrated one hidden, or on
// an allow list those on it. An adult hiding kids' content sees everything
// else.
func VisibleTo(t string, a AudienceSQL) string {
	return `(` + a.Mode + ` = 0 OR ` + a.Mode + ` = 2 AND NOT ` + KidsContentOf(t) + ` OR ` + a.Mode + ` = 1 AND CASE (SELECT vl.kind FROM library vl WHERE vl.id = ` + t + `.library_id)
	WHEN 'music' THEN ` + CleanOf(t) + `
	WHEN 'youtube' THEN CASE WHEN ` + a.AllowList + ` = 1
		THEN EXISTS (SELECT 1 FROM account_allow vw WHERE vw.account_id = ` + a.AccountID + ` AND vw.item_id = ` + rootOf(t) + `)
		ELSE EXISTS (SELECT 1 FROM item vc WHERE vc.id = ` + rootOf(t) + ` AND vc.kids_content = 1) END
	ELSE CASE WHEN ` + a.AllowList + ` = 1
		THEN EXISTS (SELECT 1 FROM account_allow vw WHERE vw.account_id = ` + a.AccountID + ` AND vw.item_id IN (` + t + `.id, ` + t + `.parent_id,
			(SELECT vp.parent_id FROM item vp WHERE vp.id = ` + t + `.parent_id)))
		ELSE EXISTS (SELECT 1 FROM item vr JOIN item_shown vm ON vm.item_id = vr.id WHERE vr.id = ` + rootOf(t) + `
			AND instr(CASE vr.kind WHEN 'movie' THEN ` + a.Movie + ` ELSE ` + a.TV + ` END, ',' || vm.content_rating || ',') > 0)
	END END)`
}

// VisibleOf is VisibleTo with an Audience's Args bound to ?6 to ?10.
func VisibleOf(t string) string {
	return VisibleTo(t, AudienceSQL{Mode: "?6", AccountID: "?7", AllowList: "?8", Movie: "?9", TV: "?10"})
}

// VisibleWhere is VisibleOf over a table named item.
var VisibleWhere = VisibleOf("item")

// Args binds the audience for VisibleWhere: ?6 is 0 for an adult, 1 for a
// kid and 2 for an adult hiding kids' content.
func (a Audience) Args() []any {
	mode := 0
	switch {
	case a.Kid:
		mode = 1
	case a.HideKids:
		mode = 2
	}
	return []any{mode, a.AccountID, a.AllowList, "," + strings.Join(a.Movie, ",") + ",", "," + strings.Join(a.TV, ",") + ","}
}

// Visible reports whether the item exists and the audience may see it.
func (r *Repo) Visible(id uid.ID, a Audience) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM item WHERE id = ?1 AND `+VisibleWhere, append([]any{id, nil, nil, nil, nil}, a.Args()...)...).Scan(&n)
	return n > 0, db.Wrap(err)
}

// Repo persists items and their metadata.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, library_id, parent_id, kind, path, parsed_title, parsed_year, season_number, episode_number,
	disc_number, track_number, artist_id, sort_title, kids_content, explicit, added_at, created_at, modified_at`

func scan(s db.Scanner) (*Item, error) {
	var it Item
	if err := scanWith(s, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

func ptr64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// Create inserts it and fills ID and timestamps.
func (r *Repo) Create(it *Item) error {
	if it.ID.IsZero() {
		it.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	it.AddedAt, it.CreatedAt, it.ModifiedAt = now, now, now
	_, err := r.d.Exec(`INSERT INTO item (`+cols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, it.ID, it.LibraryID, it.ParentID, it.Kind, db.NullStr(it.Path), it.ParsedTitle,
		it.ParsedYear, it.SeasonNumber, it.EpisodeNumber, it.DiscNumber, it.TrackNumber,
		it.ArtistID, db.NullStr(it.SortTitle), it.KidsContent, it.Explicit, db.Ms(now), db.Ms(now), db.Ms(now))
	return db.Wrap(err)
}

// Get returns one item or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE id = ?`, id))
}

// Update writes the file-derived fields of it.
func (r *Repo) Update(it *Item) error {
	it.ModifiedAt = time.Now().UTC().Truncate(time.Millisecond)
	return db.ExecOne(r.d, `UPDATE item SET path=?, parsed_title=?, parsed_year=?, season_number=?, episode_number=?,
		disc_number=?, track_number=?, artist_id=?, sort_title=?, explicit=?, modified_at=? WHERE id=?`,
		db.NullStr(it.Path), it.ParsedTitle, it.ParsedYear, it.SeasonNumber, it.EpisodeNumber,
		it.DiscNumber, it.TrackNumber, it.ArtistID, db.NullStr(it.SortTitle), it.Explicit, db.Ms(it.ModifiedAt), it.ID)
}

// SetKidsContent writes an item's KidsContent; nil clears it.
func (r *Repo) SetKidsContent(id uid.ID, v *bool) error {
	return db.ExecOne(r.d, `UPDATE item SET kids_content = ?, modified_at = ? WHERE id = ?`, v, db.Ms(time.Now()), id)
}

// SetParent moves an item under parentID.
func (r *Repo) SetParent(id, parentID uid.ID) error {
	return db.ExecOne(r.d, `UPDATE item SET parent_id = ?, modified_at = ? WHERE id = ?`, parentID, db.Ms(time.Now().UTC()), id)
}

// Delete removes an item; children, files, tracks, metadata, watch state and
// allow-list entries cascade. The files only it and its descendants hold go
// with them, and so do their seek preview frames and chapter thumbnails.
func (r *Repo) Delete(id uid.ID) error {
	return mediafile.DeleteMade(r.d, func() error { return db.ExecOne(r.d, `DELETE FROM item WHERE id = ?`, id) },
		`WITH RECURSIVE t(id) AS (SELECT ? UNION ALL SELECT i.id FROM item i JOIN t ON i.parent_id = t.id)
		SELECT x.media_file_id FROM item_file x WHERE x.item_id IN t
			AND NOT EXISTS (SELECT 1 FROM item_file y WHERE y.media_file_id = x.media_file_id AND y.item_id NOT IN t)`, id)
}

func (r *Repo) query(q string, args ...any) ([]*Item, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// List pages items ordered by parsed title, then season and episode number.
func (r *Repo) List(f Filter) ([]*Item, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	by := ``
	if f.BestFirst {
		by = `NOT (instr(lower(parsed_title), lower(?11)) = 1
			OR instr(lower(COALESCE((SELECT m.title FROM item_shown m WHERE m.item_id = item.id), '')), lower(?11)) = 1), `
	}
	order := `parsed_title COLLATE NOCASE, season_number, episode_number, id`
	if f.Newest {
		order = `added_at DESC, id DESC`
	}
	// The kind spelled out lets the planner walk a kind index in order and stop at the page.
	kind := ``
	if f.Kind != "" {
		kind = `kind = ?2 AND `
	}
	return r.query(`SELECT `+cols+` FROM item WHERE `+kind+where+f.kidsOnly()+`
		ORDER BY `+by+order+` LIMIT ?4 OFFSET ?5`, f.args(f.Limit, f.Offset)...)
}

// Count returns how many items match f, ignoring paging.
func (r *Repo) Count(f Filter) (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM item WHERE `+where+f.kidsOnly(), f.args(nil, nil)...).Scan(&n)
	return n, db.Wrap(err)
}

// KindCounts returns how many items of each kind a library holds; kinds it
// lacks are absent.
func (r *Repo) KindCounts(libraryID uid.ID) (map[string]int, error) {
	rows, err := r.d.Query(`SELECT kind, COUNT(*) FROM item WHERE library_id = ? GROUP BY kind`, libraryID)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, db.Wrap(err)
		}
		out[k] = n
	}
	return out, rows.Err()
}

// CountUnmatched counts the library's movies and shows (the items a provider
// is asked about) that have no remote row or one that is not matched; none
// while the settings use no remote data.
func (r *Repo) CountUnmatched(libraryID uid.ID) (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM item i LEFT JOIN item_metadata m ON m.item_id = i.id AND m.source = 'remote'
		WHERE i.library_id = ? AND i.kind IN ('movie','show') AND (m.item_id IS NULL OR m.status != 'matched')
		AND (SELECT use_remote FROM metadata_setting)`, libraryID).Scan(&n)
	return n, db.Wrap(err)
}

// ByPath finds a movie or show by its path within a library.
func (r *Repo) ByPath(libraryID uid.ID, kind, path string) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE library_id = ? AND kind = ? AND path = ?`, libraryID, kind, path))
}

// Movie finds a library's movie by its parsed title, ignoring case, and
// year, the oldest if several match.
func (r *Repo) Movie(libraryID uid.ID, title string, year int) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE library_id = ? AND kind = 'movie'
		AND parsed_title = ? COLLATE NOCASE AND parsed_year = ? ORDER BY id LIMIT 1`, libraryID, title, year))
}

// MovieInFolder finds the library's movie, the oldest if several, that has
// a file directly in dir.
func (r *Repo) MovieInFolder(libraryID uid.ID, dir string) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE library_id = ?1 AND kind = 'movie' AND id IN (
		SELECT x.item_id FROM item_file x JOIN media_file f ON f.id = x.media_file_id
		WHERE substr(f.path, 1, length(?2)) = ?2 AND instr(substr(f.path, length(?2) + 1), '/') = 0) ORDER BY id LIMIT 1`,
		libraryID, strings.TrimSuffix(dir, "/")+"/"))
}

// MovieDuplicates pairs each of the library's movies matched to the TMDB
// id of an older movie of the library with the oldest such movie, in the
// order the newer were made.
func (r *Repo) MovieDuplicates(libraryID uid.ID) ([][2]uid.ID, error) {
	rows, err := r.d.Query(`WITH m AS (SELECT i.id, m.provider_id FROM item i JOIN item_metadata m ON m.item_id = i.id AND m.source = 'remote'
			WHERE i.library_id = ? AND i.kind = 'movie' AND m.provider = 'tmdb' AND m.status = 'matched' AND m.provider_id IS NOT NULL),
		k AS (SELECT provider_id, MIN(id) AS keep FROM m GROUP BY provider_id HAVING COUNT(*) > 1)
		SELECT k.keep, m.id FROM m JOIN k ON k.provider_id = m.provider_id WHERE m.id > k.keep ORDER BY m.id`, libraryID)
	return db.Collect(rows, err,
		func(s db.Scanner) ([2]uid.ID, error) {
			var p [2]uid.ID
			return p, db.Wrap(s.Scan(&p[0], &p[1]))
		})
}

// SongTwins pairs each of the library's songs whose files are all missing
// with the one song of its album with a file not missing and the same
// disc, track and title, ignoring case; a song with more such is left out.
func (r *Repo) SongTwins(libraryID uid.ID) ([][2]uid.ID, error) {
	rows, err := r.d.Query(`WITH s AS (SELECT i.id, i.parent_id, i.disc_number, i.track_number, i.parsed_title,
			EXISTS (SELECT 1 FROM item_file x JOIN media_file f ON f.id = x.media_file_id WHERE x.item_id = i.id AND f.missing_at IS NULL) AS live
			FROM item i WHERE i.library_id = ? AND i.kind = 'song' AND EXISTS (SELECT 1 FROM item_file x WHERE x.item_id = i.id))
		SELECT MIN(l.id), g.id FROM s g JOIN s l ON l.live AND l.parent_id = g.parent_id AND l.disc_number IS g.disc_number
			AND l.track_number = g.track_number AND l.parsed_title = g.parsed_title COLLATE NOCASE
		WHERE NOT g.live GROUP BY g.id HAVING COUNT(*) = 1 ORDER BY g.id`, libraryID)
	return db.Collect(rows, err,
		func(s db.Scanner) ([2]uid.ID, error) {
			var p [2]uid.ID
			return p, db.Wrap(s.Scan(&p[0], &p[1]))
		})
}

// ShowsTitled lists the library's shows with this parsed title, ignoring
// case, oldest first.
func (r *Repo) ShowsTitled(libraryID uid.ID, title string) ([]*Item, error) {
	return r.query(`SELECT `+cols+` FROM item WHERE library_id = ? AND kind = 'show' AND parsed_title = ? COLLATE NOCASE ORDER BY id`,
		libraryID, title)
}

// ShowsNamed lists the library's shows whose folder is named exactly name
// directly under one of the library's folders, oldest first.
func (r *Repo) ShowsNamed(libraryID uid.ID, name string) ([]*Item, error) {
	return r.query(`SELECT `+cols+` FROM item WHERE library_id = ?1 AND kind = 'show' AND path IN (
		SELECT rtrim(path, '/') || '/' || ?2 FROM library_folder WHERE library_id = ?1) ORDER BY id`, libraryID, name)
}

// Season finds a show's season by number.
func (r *Repo) Season(showID uid.ID, number int) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE parent_id = ? AND kind = 'season' AND season_number = ?`, showID, number))
}

// Episode finds a season's episode by number.
func (r *Repo) Episode(seasonID uid.ID, number int) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE parent_id = ? AND kind = 'episode' AND episode_number = ?`, seasonID, number))
}

// Next is the item after id among the candidates of from, a FROM clause
// aliasing them item and ending inside its WHERE, ordered by order, with
// id bound as ?1: the first after it that the audience sees and that has a
// file on disk, wherever id itself stands. nil when none follows.
func (r *Repo) Next(from, order string, id uid.ID, a Audience) (*uid.ID, error) {
	var next uid.ID
	err := db.Wrap(r.d.QueryRow(`WITH o AS (SELECT item.id, ROW_NUMBER() OVER (ORDER BY `+order+`) AS pos `+from+`
		AND (item.id = ?1 OR `+VisibleWhere+` AND NOT `+mediafile.UnavailableOf("item.id")+`))
		SELECT id FROM o WHERE pos > (SELECT pos FROM o WHERE id = ?1) ORDER BY pos LIMIT 1`,
		append([]any{id, nil, nil, nil, nil}, a.Args()...)...).Scan(&next))
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// Neighbors are the items before and after id among the candidates of
// from, as Next takes them, ordered by order: the nearest on each side that
// the audience sees, whether or not it has a file on disk. nil for none.
func (r *Repo) Neighbors(from, order string, id uid.ID, a Audience) (prev, next *uid.ID, err error) {
	err = db.Wrap(r.d.QueryRow(`WITH o AS (SELECT item.id, LAG(item.id) OVER w AS prev, LEAD(item.id) OVER w AS next `+from+`
		AND (item.id = ?1 OR `+VisibleWhere+`) WINDOW w AS (ORDER BY `+order+`))
		SELECT prev, next FROM o WHERE id = ?1`, append([]any{id, nil, nil, nil, nil}, a.Args()...)...).Scan(&prev, &next))
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil, nil
	}
	return prev, next, err
}

// episodes and playOrder are the candidates and order of Next and
// Neighbors for an episode: its show's episodes, seasons by number with
// specials (0 or none) last, then episodes by number.
const (
	episodes = `FROM item JOIN item se ON se.id = item.parent_id WHERE +item.kind = 'episode'
		AND se.parent_id = (SELECT ps.parent_id FROM item pe JOIN item ps ON ps.id = pe.parent_id WHERE pe.id = ?1)`
	playOrder = `COALESCE(se.season_number, 0) = 0, se.season_number, se.id, item.episode_number, item.id`
)

// NextEpisode is, as Next finds it, the episode after id in its show's play
// order.
func (r *Repo) NextEpisode(id uid.ID, a Audience) (*uid.ID, error) {
	return r.Next(episodes, playOrder, id, a)
}

// EpisodeNeighbors are, as Neighbors finds them, the episodes before and
// after id in its show's play order.
func (r *Repo) EpisodeNeighbors(id uid.ID, a Audience) (prev, next *uid.ID, err error) {
	return r.Neighbors(episodes, playOrder, id, a)
}

// Episodes lists every episode of a show in season then episode order.
func (r *Repo) Episodes(showID uid.ID) ([]*Item, error) {
	return r.query(`SELECT `+prefixed("e")+` FROM item e JOIN item s ON e.parent_id = s.id
		WHERE s.parent_id = ? AND e.kind = 'episode' ORDER BY s.season_number, e.episode_number`, showID)
}

func prefixed(a string) string {
	return a + `.id, ` + a + `.library_id, ` + a + `.parent_id, ` + a + `.kind, ` + a + `.path, ` + a + `.parsed_title, ` +
		a + `.parsed_year, ` + a + `.season_number, ` + a + `.episode_number, ` + a + `.disc_number, ` + a + `.track_number, ` +
		a + `.artist_id, ` + a + `.sort_title, ` + a + `.kids_content, ` + a + `.explicit, ` + a + `.added_at, ` + a + `.created_at, ` + a + `.modified_at`
}

// SetMetadata inserts or replaces the remote row of an item.
func (r *Repo) SetMetadata(m *Metadata) error {
	if m.Provider == "" {
		m.Provider = "tmdb"
	}
	return r.put("remote", m)
}

// SetLocal inserts or replaces the local row of an item, leaving its
// YouTube publish time and sidecar stamp.
func (r *Repo) SetLocal(m *Metadata) error {
	m.Provider = Local
	return r.put(Local, m)
}

func (r *Repo) put(source string, m *Metadata) error {
	if m.Status == "" {
		m.Status = Unmatched
	}
	_, err := r.d.Exec(`INSERT INTO item_metadata (item_id, source, provider, provider_id, status, title, overview, release_date, runtime_minutes,
			content_rating, poster_path, backdrop_path, external_id, fetched_at, retry_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(item_id, source) DO UPDATE SET provider=excluded.provider, provider_id=excluded.provider_id, status=excluded.status,
			title=excluded.title, overview=excluded.overview, release_date=excluded.release_date, runtime_minutes=excluded.runtime_minutes,
			content_rating=excluded.content_rating, poster_path=excluded.poster_path, backdrop_path=excluded.backdrop_path,
			external_id=excluded.external_id, fetched_at=excluded.fetched_at, retry_at=excluded.retry_at`,
		m.ItemID, source, m.Provider, m.ProviderID, m.Status, db.NullStr(m.Title), db.NullStr(m.Overview), db.NullStr(m.ReleaseDate),
		m.RuntimeMinutes, db.NullStr(m.ContentRating), db.NullStr(db.DataRel(m.PosterPath)), db.NullStr(db.DataRel(m.BackdropPath)),
		db.NullStr(m.ExternalID), db.NullMs(m.FetchedAt), db.NullMs(m.RetryAt))
	return db.Wrap(err)
}

const mcols = `item_id, provider, provider_id, status, title, overview, release_date, runtime_minutes, content_rating, poster_path, backdrop_path,
	external_id, fetched_at, retry_at`

// shown reads item_shown, which applies the metadata settings.
const shown = `SELECT ` + mcols + `, poster_from, backdrop_from FROM item_shown`

func scanMeta(s db.Scanner, from ...any) (*Metadata, error) {
	var m Metadata
	var pid, rt, fetched, retry sql.NullInt64
	var title, overview, date, rating, poster, backdrop, external sql.NullString
	if err := s.Scan(append([]any{&m.ItemID, &m.Provider, &pid, &m.Status, &title, &overview, &date, &rt, &rating, &poster, &backdrop, &external,
		&fetched, &retry}, from...)...); err != nil {
		return nil, db.Wrap(err)
	}
	m.ProviderID, m.RuntimeMinutes = ptr64(pid), db.FromNullInt(rt)
	m.Title, m.Overview, m.ReleaseDate = title.String, overview.String, date.String
	m.ContentRating, m.PosterPath, m.BackdropPath, m.ExternalID = rating.String, db.DataAbs(poster.String), db.DataAbs(backdrop.String), external.String
	m.FetchedAt, m.RetryAt = db.FromNullMs(fetched), db.FromNullMs(retry)
	return &m, nil
}

func scanShown(s db.Scanner) (*Metadata, error) {
	var poster, backdrop string
	m, err := scanMeta(s, &poster, &backdrop)
	if err == nil {
		m.PosterFrom, m.BackdropFrom = poster, backdrop
	}
	return m, err
}

// Metadata returns what an item shows under the metadata settings, each
// field from its local or remote row, or db.ErrNotFound when the settings
// leave it none.
func (r *Repo) Metadata(itemID uid.ID) (*Metadata, error) {
	return scanShown(r.d.QueryRow(shown+` WHERE item_id = ?`, itemID))
}

// Remote returns an item's remote row, as a provider last left it, or
// db.ErrNotFound.
func (r *Repo) Remote(itemID uid.ID) (*Metadata, error) {
	return scanMeta(r.d.QueryRow(`SELECT `+mcols+` FROM item_metadata WHERE item_id = ? AND source = 'remote'`, itemID))
}

// LocalRow returns an item's local row or db.ErrNotFound.
func (r *Repo) LocalRow(itemID uid.ID) (*Metadata, error) {
	return scanMeta(r.d.QueryRow(`SELECT `+mcols+` FROM item_metadata WHERE item_id = ? AND source = 'local'`, itemID))
}

// MetadataFor returns, as Metadata does, what those ids that have any show.
func (r *Repo) MetadataFor(ids []uid.ID) (map[uid.ID]*Metadata, error) {
	out := map[uid.ID]*Metadata{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := db.In(ids)
	rows, err := r.d.Query(shown+` WHERE item_id IN `+in, args...)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		m, err := scanShown(rows)
		if err != nil {
			return nil, err
		}
		out[m.ItemID] = m
	}
	return out, rows.Err()
}

// DeleteMetadata drops an item's remote row; its local row stays.
func (r *Repo) DeleteMetadata(itemID uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM item_metadata WHERE item_id = ? AND source = 'remote'`, itemID)
}

// DeleteLocal drops the local rows of these items, so the local metadata
// task reads their files again.
func (r *Repo) DeleteLocal(ids []uid.ID) error {
	if len(ids) == 0 {
		return nil
	}
	in, args := db.In(ids)
	_, err := r.d.Exec(`DELETE FROM item_metadata WHERE source = 'local' AND item_id IN `+in, args...)
	return db.Wrap(err)
}

// DueForRetry lists items whose provider fetch should be retried by now.
func (r *Repo) DueForRetry(now time.Time, limit int) ([]*Metadata, error) {
	rows, err := r.d.Query(`SELECT `+mcols+` FROM item_metadata WHERE source = 'remote' AND retry_at IS NOT NULL AND retry_at <= ?
		ORDER BY retry_at LIMIT ?`, db.Ms(now), limit)
	return db.Collect(rows, err, func(s db.Scanner) (*Metadata, error) { return scanMeta(s) })
}

// MarkAllDue makes every movie, show and album with a remote row due for
// a re-fetch at at, returning how many. Episodes are filled by their show,
// album artists by their albums.
func (r *Repo) MarkAllDue(at time.Time) (int64, error) {
	res, err := r.d.Exec(`UPDATE item_metadata SET retry_at = ? WHERE source = 'remote' AND item_id IN (SELECT id FROM item WHERE kind IN (?, ?, ?))`, db.Ms(at), Movie, Show, Album)
	if err != nil {
		return 0, db.Wrap(err)
	}
	return res.RowsAffected()
}

// WithoutMetadata lists movies and shows of a library that have no remote
// row yet.
func (r *Repo) WithoutMetadata(libraryID uid.ID, limit int) ([]*Item, error) {
	return r.query(`SELECT `+prefixed("i")+` FROM item i LEFT JOIN item_metadata m ON m.item_id = i.id AND m.source = 'remote'
		WHERE i.library_id = ? AND i.kind IN (?, ?) AND m.item_id IS NULL ORDER BY i.id LIMIT ?`, libraryID, Movie, Show, limit)
}

// WithoutLocal lists up to limit movies, shows, seasons and episodes that
// have no local row yet, oldest first.
func (r *Repo) WithoutLocal(limit int) ([]*Item, error) {
	return r.query(`SELECT `+prefixed("i")+` FROM item i LEFT JOIN item_metadata m ON m.item_id = i.id AND m.source = 'local'
		WHERE i.kind IN (?, ?, ?, ?) AND m.item_id IS NULL ORDER BY i.id LIMIT ?`, Movie, Show, Season, Episode, limit)
}

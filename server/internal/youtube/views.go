package youtube

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Provider names the metadata rows of channels and videos. Their
// ExternalID is YouTube's id and their PosterPath an image in the library.
const Provider = "youtube"

// ChannelFilter narrows Channels. Zero values mean no constraint.
type ChannelFilter struct {
	LibraryID *uid.ID
	// ID finds one channel, with or without videos; otherwise only
	// channels with videos are listed.
	ID        *uid.ID
	Q         string
	Sort      string  // title (default), latest, or best: a title starting with Q first
	AccountID *uid.ID // whose watch state counts as unwatched
	Audience  item.Audience
	Limit     int // 0 means 100, negative means all
	Offset    int
}

// ChannelView is a channel with its counts.
type ChannelView struct {
	ID             uid.ID
	LibraryID      uid.ID
	Title          string
	Overview       string
	ExternalID     string
	ForKids        bool
	VideoCount     int
	UnwatchedCount int
	LastVideoAt    *time.Time
	AddedAt        time.Time
}

// limitOf is a filter's Limit as SQL binds it.
func limitOf(n int) int {
	if n == 0 {
		return 100
	}
	return n
}

// Channels pages channels, reading item_shown by key; a LEFT JOIN onto it
// builds the whole view.
func (r *Repo) Channels(f ChannelFilter) ([]*ChannelView, error) {
	by := `c.parsed_title COLLATE NOCASE, c.id`
	switch f.Sort {
	case "latest":
		by = `last IS NULL, last DESC, ` + by
	case "best":
		by = `instr(lower(c.parsed_title), lower(?3)) != 1, ` + by
	}
	rows, err := r.d.Query(`SELECT c.id, c.library_id, c.parsed_title, (SELECT m.overview FROM item_shown m WHERE m.item_id = c.id),
			(SELECT m.external_id FROM item_shown m WHERE m.item_id = c.id), COALESCE(c.kids_content, 0), c.added_at,
			(SELECT COUNT(*) FROM item v WHERE v.parent_id = c.id),
			(SELECT COUNT(*) FROM item v WHERE v.parent_id = c.id
				AND NOT EXISTS (SELECT 1 FROM watch_state w WHERE w.item_id = v.id AND w.account_id = ?4 AND w.watched = 1)),
			(SELECT MAX(vm.published_at) FROM item v JOIN item_metadata vm ON vm.item_id = v.id AND vm.source = 'local' WHERE v.parent_id = c.id) AS last
		FROM item c
		WHERE c.kind = 'channel' AND (?1 IS NULL OR c.library_id = ?1) AND (?3 = '' OR instr(lower(c.parsed_title), lower(?3)) > 0)
			AND (?2 IS NULL AND EXISTS (SELECT 1 FROM item v WHERE v.parent_id = c.id) OR c.id = ?2) AND `+item.VisibleOf("c")+`
		ORDER BY `+by+` LIMIT ?5 OFFSET ?11`, append(append([]any{f.LibraryID, f.ID, f.Q, f.AccountID, limitOf(f.Limit)}, f.Audience.Args()...), f.Offset)...)
	return db.Collect(rows, err, func(s db.Scanner) (*ChannelView, error) {
		var v ChannelView
		var overview, external sql.NullString
		var last sql.NullInt64
		var added int64
		if err := s.Scan(&v.ID, &v.LibraryID, &v.Title, &overview, &external, &v.ForKids, &added, &v.VideoCount, &v.UnwatchedCount, &last); err != nil {
			return nil, db.Wrap(err)
		}
		v.Overview, v.ExternalID, v.AddedAt, v.LastVideoAt = overview.String, external.String, db.FromMs(added), db.FromNullMs(last)
		return &v, nil
	})
}

// VideoFilter narrows Videos. Zero values mean no constraint.
type VideoFilter struct {
	LibraryID *uid.ID
	ChannelID *uid.ID
	ID        *uid.ID
	Unwatched bool
	// InProgress keeps videos with a resume position, watched or not.
	InProgress bool
	Q          string
	// Sort is newest (default), oldest, title, channel, length, played, or
	// best: a title starting with Q first, then by title.
	Sort      string
	AccountID *uid.ID // whose watch state the views carry
	Audience  item.Audience
	Limit     int // 0 means 100, negative means all
	Offset    int
}

// VideoView is a video with its channel, file facts and one profile's
// watch state.
type VideoView struct {
	ID           uid.ID
	LibraryID    uid.ID
	ChannelID    uid.ID
	ChannelTitle string
	Title        string
	Overview     string
	PublishedAt  *time.Time
	ExternalID   string
	DurationMs   *int64
	Height       *int
	Watched      bool
	PositionMs   int64
	LastPlayedAt *time.Time
	AddedAt      time.Time
	Unavailable  bool // it has files, all marked missing
}

const newest = `COALESCE(m.published_at, 0) DESC, v.added_at DESC, v.id DESC`

// NextVideo is, as item.Repo.Next finds it, the video after id in its
// channel in the oldest sort's order.
func (r *Repo) NextVideo(id uid.ID, a item.Audience) (*uid.ID, error) {
	return item.New(r.d).Next(`FROM item LEFT JOIN item_metadata m ON m.item_id = item.id AND m.source = 'local'
		WHERE +item.kind = 'video' AND item.parent_id = (SELECT pv.parent_id FROM item pv WHERE pv.id = ?1)`,
		`m.published_at IS NULL, m.published_at, item.added_at, item.id`, id, a)
}

// Videos pages videos, reading item_shown by key as Channels does.
func (r *Repo) Videos(f VideoFilter) ([]*VideoView, error) {
	by := map[string]string{
		"newest":  newest,
		"oldest":  `m.published_at IS NULL, m.published_at, v.added_at, v.id`,
		"title":   `v.parsed_title COLLATE NOCASE, v.id`,
		"channel": `c.parsed_title COLLATE NOCASE, c.id, ` + newest,
		"length":  `dur IS NULL, dur DESC, v.id`,
		"played":  `w.last_played_at IS NULL, w.last_played_at DESC, v.id`,
		"best":    `instr(lower(v.parsed_title), lower(?11)) != 1, v.parsed_title COLLATE NOCASE, v.id`,
	}[f.Sort]
	if by == "" {
		by = newest
	}
	rows, err := r.d.Query(`SELECT v.id, v.library_id, v.parent_id, c.parsed_title, v.parsed_title, (SELECT ms.overview FROM item_shown ms WHERE ms.item_id = v.id),
			m.published_at, (SELECT ms.external_id FROM item_shown ms WHERE ms.item_id = v.id),
			v.added_at, (SELECT f.duration_ms FROM item_file x JOIN media_file f ON f.id = x.media_file_id WHERE x.item_id = v.id ORDER BY f.id LIMIT 1) AS dur,
			(SELECT t.height FROM item_file x JOIN track t ON t.media_file_id = x.media_file_id WHERE x.item_id = v.id AND t.kind = 'video'
				ORDER BY x.media_file_id, t.stream_index LIMIT 1),
			COALESCE(w.watched, 0), COALESCE(w.position_ms, 0), w.last_played_at, `+mediafile.UnavailableOf("v.id")+`
		FROM item v JOIN item c ON c.id = v.parent_id LEFT JOIN item_metadata m ON m.item_id = v.id AND m.source = 'local'
			LEFT JOIN watch_state w ON w.item_id = v.id AND w.account_id = ?5
		WHERE v.kind = 'video' AND (?1 IS NULL OR v.library_id = ?1) AND (?2 IS NULL OR v.parent_id = ?2)
			AND (?3 IS NULL OR v.id = ?3) AND (?4 = 0 OR COALESCE(w.watched, 0) = 0) AND (?11 = '' OR instr(lower(v.parsed_title), lower(?11)) > 0)
			AND (?14 = 0 OR COALESCE(w.position_ms, 0) > 0) AND `+item.VisibleOf("v")+`
		ORDER BY `+by+` LIMIT ?12 OFFSET ?13`,
		append(append([]any{f.LibraryID, f.ChannelID, f.ID, f.Unwatched, f.AccountID}, f.Audience.Args()...),
			f.Q, limitOf(f.Limit), f.Offset, f.InProgress)...)
	return db.Collect(rows, err, func(s db.Scanner) (*VideoView, error) {
		var v VideoView
		var overview, external sql.NullString
		var published, dur, height, played sql.NullInt64
		var added int64
		if err := s.Scan(&v.ID, &v.LibraryID, &v.ChannelID, &v.ChannelTitle, &v.Title, &overview, &published, &external, &added, &dur, &height,
			&v.Watched, &v.PositionMs, &played, &v.Unavailable); err != nil {
			return nil, db.Wrap(err)
		}
		v.Overview, v.PublishedAt, v.ExternalID, v.AddedAt = overview.String, db.FromNullMs(published), external.String, db.FromMs(added)
		if dur.Valid {
			v.DurationMs = &dur.Int64
		}
		v.Height, v.LastPlayedAt = db.FromNullInt(height), db.FromNullMs(played)
		return &v, nil
	})
}

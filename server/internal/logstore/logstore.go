// Package logstore persists server log entries for the admin Logs page: a
// repository, and a slog handler that writes to the usual destination and to
// the repository, with secrets removed from both.
package logstore

import (
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Retention and MaxRows bound the table; older or surplus rows are pruned.
const (
	Retention = 30 * 24 * time.Hour
	MaxRows   = 100000
)

// Levels in ascending severity.
var levels = []string{"debug", "info", "warn", "error"}

// Entry is one log line. The optional columns carry what the Logs page
// expands: the ffmpeg argv and why a transcode was needed.
type Entry struct {
	ID              uid.ID
	At              time.Time
	Level           string
	Source          string
	Message         string
	SessionID       *uid.ID
	ItemID          *uid.ID
	FFmpegCommand   string
	TranscodeReason string
	CreatedAt       time.Time
	ModifiedAt      time.Time
}

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	MinLevel string
	Source   string
	Since    *time.Time // inclusive
	Until    *time.Time // inclusive
	Q        string     // case-insensitive substring of the message
	Limit    int        // 0 means 100
	Offset   int
}

// Repo persists log entries.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// Insert stores e, minting its ID unless set.
func (r *Repo) Insert(e *Entry) error {
	if e.ID.IsZero() {
		e.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	e.CreatedAt, e.ModifiedAt = now, now
	_, err := r.d.Exec(`INSERT INTO log_entry (id, at, level, source, message, session_id, item_id, ffmpeg_command, transcode_reason, created_at, modified_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, e.ID, db.Ms(e.At), e.Level, e.Source, e.Message, e.SessionID, e.ItemID,
		db.NullStr(e.FFmpegCommand), db.NullStr(e.TranscodeReason), db.Ms(now), db.Ms(now))
	return db.Wrap(err)
}

// List returns matching entries, newest first.
func (r *Repo) List(f Filter) ([]*Entry, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	allowed := levels
	if i := slices.Index(levels, f.MinLevel); i > 0 {
		allowed = levels[i:]
	}
	q := `SELECT id, at, level, source, message, session_id, item_id, ffmpeg_command, transcode_reason, created_at, modified_at FROM log_entry
		WHERE level IN ('` + strings.Join(allowed, `','`) + `') AND (?1 = '' OR source = ?1)
		AND (?2 IS NULL OR at >= ?2) AND (?3 IS NULL OR at <= ?3) AND (?4 = '' OR instr(lower(message), lower(?4)) > 0)
		ORDER BY at DESC, id DESC LIMIT ?5 OFFSET ?6`
	rows, err := r.d.Query(q, f.Source, db.NullMs(f.Since), db.NullMs(f.Until), f.Q, f.Limit, f.Offset)
	return db.Collect(rows, err, func(s db.Scanner) (*Entry, error) {
		var e Entry
		var at, created, modified int64
		var cmd, reason sql.NullString
		if err := s.Scan(&e.ID, &at, &e.Level, &e.Source, &e.Message, &e.SessionID, &e.ItemID, &cmd, &reason, &created, &modified); err != nil {
			return nil, db.Wrap(err)
		}
		e.At, e.FFmpegCommand, e.TranscodeReason = db.FromMs(at), cmd.String, reason.String
		e.CreatedAt, e.ModifiedAt = db.FromMs(created), db.FromMs(modified)
		return &e, nil
	})
}

// Prune deletes entries older than cutoff, then the oldest beyond maxRows.
func (r *Repo) Prune(cutoff time.Time, maxRows int) error {
	if _, err := r.d.Exec(`DELETE FROM log_entry WHERE at < ?`, db.Ms(cutoff)); err != nil {
		return db.Wrap(err)
	}
	_, err := r.d.Exec(`DELETE FROM log_entry WHERE id NOT IN (SELECT id FROM log_entry ORDER BY at DESC, id DESC LIMIT ?)`, maxRows)
	return db.Wrap(err)
}

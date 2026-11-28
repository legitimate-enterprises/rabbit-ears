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

// Retention bounds every entry's age; MaxRows the server's own entries and
// MaxClientRows those clients reported, counted apart so that neither can
// push the other's out.
const (
	Retention     = 30 * 24 * time.Hour
	MaxRows       = 100000
	MaxClientRows = 10000
)

// ClientSource is the source of every entry a client reported, and of no
// other.
const ClientSource = "client"

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
	// Client is set on an entry a client reported, and only there.
	Client     *Client
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// Client is what a client app's entry carries beyond the server's. The
// device and profile, with their names, are nil and empty once gone.
type Client struct {
	Platform    string
	AppVersion  string
	OSVersion   string
	Context     string
	Detail      string
	DeviceID    *uid.ID
	DeviceName  string
	ProfileID   *uid.ID
	ProfileName string
}

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	MinLevel string
	Source   string
	Platform string     // only client entries from it
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

// InsertClient stores e, which carries Client and its own ID, unless an
// entry with that ID is stored already.
func (r *Repo) InsertClient(e *Entry) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	e.CreatedAt, e.ModifiedAt = now, now
	return db.InTx(r.d, func(tx db.DBTX) error {
		res, err := tx.Exec(`INSERT INTO log_entry (id, at, level, source, message, created_at, modified_at) VALUES (?,?,?,?,?,?,?)
			ON CONFLICT (id) DO NOTHING`, e.ID, db.Ms(e.At), e.Level, e.Source, e.Message, db.Ms(now), db.Ms(now))
		if err != nil {
			return db.Wrap(err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return db.Wrap(err)
		}
		c := e.Client
		_, err = tx.Exec(`INSERT INTO client_log_entry (log_entry_id, platform, app_version, os_version, context, detail, device_id, profile_id)
			VALUES (?,?,?,?,?,?,?,?)`, e.ID, c.Platform, c.AppVersion, c.OSVersion, c.Context, db.NullStr(c.Detail), c.DeviceID, c.ProfileID)
		return db.Wrap(err)
	})
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
	q := `SELECT l.id, l.at, l.level, l.source, l.message, l.session_id, l.item_id, l.ffmpeg_command, l.transcode_reason, l.created_at, l.modified_at,
		c.platform, c.app_version, c.os_version, c.context, c.detail, c.device_id, d.name, c.profile_id, a.name
		FROM log_entry l LEFT JOIN client_log_entry c ON c.log_entry_id = l.id
		LEFT JOIN device d ON d.id = c.device_id LEFT JOIN account a ON a.id = c.profile_id
		WHERE l.level IN ('` + strings.Join(allowed, `','`) + `') AND (?1 = '' OR l.source = ?1) AND (?7 = '' OR c.platform = ?7)
		AND (?2 IS NULL OR l.at >= ?2) AND (?3 IS NULL OR l.at <= ?3) AND (?4 = '' OR instr(lower(l.message), lower(?4)) > 0)
		ORDER BY l.at DESC, l.id DESC LIMIT ?5 OFFSET ?6`
	rows, err := r.d.Query(q, f.Source, db.NullMs(f.Since), db.NullMs(f.Until), f.Q, f.Limit, f.Offset, f.Platform)
	return db.Collect(rows, err, func(s db.Scanner) (*Entry, error) {
		var e Entry
		var at, created, modified int64
		var cmd, reason, platform, app, os, context, detail, device, profile sql.NullString
		var c Client
		if err := s.Scan(&e.ID, &at, &e.Level, &e.Source, &e.Message, &e.SessionID, &e.ItemID, &cmd, &reason, &created, &modified,
			&platform, &app, &os, &context, &detail, &c.DeviceID, &device, &c.ProfileID, &profile); err != nil {
			return nil, db.Wrap(err)
		}
		e.At, e.FFmpegCommand, e.TranscodeReason = db.FromMs(at), cmd.String, reason.String
		e.CreatedAt, e.ModifiedAt = db.FromMs(created), db.FromMs(modified)
		if platform.Valid {
			c.Platform, c.AppVersion, c.OSVersion, c.Context, c.Detail = platform.String, app.String, os.String, context.String, detail.String
			c.DeviceName, c.ProfileName = device.String, profile.String
			e.Client = &c
		}
		return &e, nil
	})
}

// Prune deletes entries older than cutoff, then the oldest of the server's
// beyond maxRows and of clients' beyond maxClientRows.
func (r *Repo) Prune(cutoff time.Time, maxRows, maxClientRows int) error {
	if _, err := r.d.Exec(`DELETE FROM log_entry WHERE at < ?`, db.Ms(cutoff)); err != nil {
		return db.Wrap(err)
	}
	for _, c := range []struct {
		where string
		max   int
	}{{"source <> ?", maxRows}, {"source = ?", maxClientRows}} {
		//nolint:gosec // the clause is one of two constants
		_, err := r.d.Exec(`DELETE FROM log_entry WHERE `+c.where+` AND id NOT IN (SELECT id FROM log_entry WHERE `+c.where+`
			ORDER BY at DESC, id DESC LIMIT ?)`, ClientSource, ClientSource, c.max)
		if err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

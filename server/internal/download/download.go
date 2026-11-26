// Package download holds what the server keeps for offline downloads, with
// its repository: the conversions it makes, and each device's claims on a
// title's file, the file itself or a conversion, while it fetches it. What a
// device has downloaded lives on the device alone.
package download

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// Conversion states, in order.
const (
	Queued     = "queued"
	Converting = "converting"
	Ready      = "ready"
	Failed     = "failed"
)

// Conversion is one file the server makes of a source file for every device
// that needs the same one. Entry says what it makes, as a plan entry does a
// stream: TopHeight and TopBitrate for converted video (a song's AAC bitrate
// for a song), SubtitleTrackID for a subtitle burned in. Its encoder is the
// worker's choice and not kept.
type Conversion struct {
	ID               int64
	MediaFileID      int64
	SourceModifiedAt time.Time
	Entry            *session.PlanEntry
	Container        string // mp4, or m4a for a song
	Status           string
	PredictedBytes   int64
	SizeBytes        *int64 // set once ready
	Error            string
	CreatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
}

// Download is a device's claim, for one of its profiles, on one title's
// file: ConversionID's file, or with none the source file itself.
type Download struct {
	ID           int64
	DeviceID     int64
	AccountID    int64
	ItemID       int64
	EditionID    *int64
	MediaFileID  int64
	ConversionID *int64
	Quality      string // what was asked for
	CreatedAt    time.Time
}

// Repo persists conversions and downloads.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const ccols = `id, media_file_id, source_modified_at, verdict, video_action, video_codec, tone_map, height, bitrate, audio_track_id, audio_action,
	audio_channels, burn_track_id, container, status, predicted_bytes, size_bytes, error, created_at, started_at, finished_at`

func scanConversion(s db.Scanner) (*Conversion, error) {
	c := Conversion{Entry: &session.PlanEntry{SubtitleAction: session.SubtitleNone, SingleRung: true}}
	e := c.Entry
	var source, created int64
	var audio, burn int64
	var size, started, finished sql.NullInt64
	var msg sql.NullString
	if err := s.Scan(&c.ID, &c.MediaFileID, &source, &e.Verdict, &e.VideoAction, &e.VideoCodec, &e.ToneMap, &e.TopHeight, &e.TopBitrate, &audio,
		&e.AudioAction, &e.AudioChannels, &burn, &c.Container, &c.Status, &c.PredictedBytes, &size, &msg, &created, &started, &finished); err != nil {
		return nil, db.Wrap(err)
	}
	if audio != 0 {
		e.AudioTrackID = &audio
	}
	if burn != 0 {
		e.SubtitleAction, e.SubtitleTrackID = session.SubtitleBurn, &burn
	}
	if size.Valid {
		c.SizeBytes = &size.Int64
	}
	c.Error, c.SourceModifiedAt, c.CreatedAt = msg.String, db.FromMs(source), db.FromMs(created)
	c.StartedAt, c.FinishedAt = db.FromNullMs(started), db.FromNullMs(finished)
	return &c, nil
}

func idOr0(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// key is the conversion's unique columns, bound as ?1 to ?12.
func (c *Conversion) key() []any {
	e := c.Entry
	burn := int64(0)
	if e.SubtitleAction == session.SubtitleBurn {
		burn = idOr0(e.SubtitleTrackID)
	}
	return []any{c.MediaFileID, db.Ms(c.SourceModifiedAt), e.Verdict, e.VideoAction, e.VideoCodec, e.ToneMap, e.TopHeight, e.TopBitrate,
		idOr0(e.AudioTrackID), e.AudioAction, e.AudioChannels, burn}
}

const byKey = `media_file_id = ?1 AND source_modified_at = ?2 AND verdict = ?3 AND video_action = ?4 AND video_codec = ?5 AND tone_map = ?6
	AND height = ?7 AND bitrate = ?8 AND audio_track_id = ?9 AND audio_action = ?10 AND audio_channels = ?11 AND burn_track_id = ?12`

// Request finds the conversion that makes what c describes, queueing a
// failed one again, or inserts c queued; c is then the stored conversion.
func (r *Repo) Request(c *Conversion) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		_, err := d.Exec(`UPDATE conversion SET status = 'queued', error = NULL, started_at = NULL, finished_at = NULL
			WHERE status = 'failed' AND `+byKey, c.key()...)
		if err != nil {
			return db.Wrap(err)
		}
		found, err := scanConversion(d.QueryRow(`SELECT `+ccols+` FROM conversion WHERE `+byKey, c.key()...))
		if err == nil {
			*c = *found
			return nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return err
		}
		c.Status, c.CreatedAt = Queued, time.Now().UTC()
		res, err := d.Exec(`INSERT INTO conversion (media_file_id, source_modified_at, verdict, video_action, video_codec, tone_map, height, bitrate,
			audio_track_id, audio_action, audio_channels, burn_track_id, container, status, predicted_bytes, created_at)
			VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,'queued',?14,?15)`,
			append(c.key(), c.Container, c.PredictedBytes, db.Ms(c.CreatedAt))...)
		if err != nil {
			return db.Wrap(err)
		}
		c.ID, _ = res.LastInsertId()
		return nil
	})
}

// Conversion returns one conversion or db.ErrNotFound.
func (r *Repo) Conversion(id int64) (*Conversion, error) {
	return scanConversion(r.d.QueryRow(`SELECT `+ccols+` FROM conversion WHERE id = ?`, id))
}

// Conversions pages every conversion, newest first; a negative limit is all.
func (r *Repo) Conversions(limit, offset int) ([]*Conversion, error) {
	rows, err := r.d.Query(`SELECT `+ccols+` FROM conversion ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	return db.Collect(rows, err, scanConversion)
}

// Active lists the conversions queued or converting, newest first.
func (r *Repo) Active() ([]*Conversion, error) {
	rows, err := r.d.Query(`SELECT ` + ccols + ` FROM conversion WHERE status IN ('queued', 'converting') ORDER BY id DESC`)
	return db.Collect(rows, err, scanConversion)
}

// FailedConversions lists the conversions that failed, newest first.
func (r *Repo) FailedConversions() ([]*Conversion, error) {
	rows, err := r.d.Query(`SELECT ` + ccols + ` FROM conversion WHERE status = 'failed' ORDER BY id DESC`)
	return db.Collect(rows, err, scanConversion)
}

// Turn is a device's first claim, ClaimID, on a queued conversion.
type Turn struct{ DeviceID, ClaimID, ConversionID int64 }

// Turns lists, for each device claiming a queued conversion, the one it
// claimed first.
func (r *Repo) Turns() ([]Turn, error) {
	// SQLite takes a bare column from the row MIN chose.
	rows, err := r.d.Query(`SELECT d.device_id, MIN(d.id), d.conversion_id FROM download d JOIN conversion c ON c.id = d.conversion_id
		WHERE c.status = 'queued' GROUP BY d.device_id`)
	return db.Collect(rows, err, func(s db.Scanner) (Turn, error) {
		var t Turn
		return t, db.Wrap(s.Scan(&t.DeviceID, &t.ClaimID, &t.ConversionID))
	})
}

// Claimants lists the devices claiming a conversion.
func (r *Repo) Claimants(conversionID int64) ([]int64, error) {
	rows, err := r.d.Query(`SELECT DISTINCT device_id FROM download WHERE conversion_id = ?`, conversionID)
	return db.Collect(rows, err, func(s db.Scanner) (int64, error) {
		var id int64
		return id, db.Wrap(s.Scan(&id))
	})
}

// Start marks a queued conversion converting.
func (r *Repo) Start(id int64, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE conversion SET status = 'converting', started_at = ? WHERE id = ? AND status = 'queued'`, db.Ms(at), id)
}

// Finish marks a converting conversion ready at sizeBytes.
func (r *Repo) Finish(id, sizeBytes int64, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE conversion SET status = 'ready', size_bytes = ?, finished_at = ? WHERE id = ? AND status = 'converting'`,
		sizeBytes, db.Ms(at), id)
}

// Fail marks a conversion failed with why.
func (r *Repo) Fail(id int64, why string, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE conversion SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, why, db.Ms(at), id)
}

// Requeue queues again every conversion left converting, as a restart
// leaves them.
func (r *Repo) Requeue() error {
	_, err := r.d.Exec(`UPDATE conversion SET status = 'queued', started_at = NULL WHERE status = 'converting'`)
	return db.Wrap(err)
}

// DeleteConversion removes a conversion with the downloads that claim it.
func (r *Repo) DeleteConversion(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM conversion WHERE id = ?`, id)
}

// DeleteUnclaimed removes a conversion unless a download claims it, and
// reports whether it went; the claim is checked in the same statement, so
// one made after the caller looked keeps it.
func (r *Repo) DeleteUnclaimed(id int64) (bool, error) {
	res, err := r.d.Exec(`DELETE FROM conversion WHERE id = ? AND NOT EXISTS (SELECT 1 FROM download WHERE conversion_id = conversion.id)`, id)
	if err != nil {
		return false, db.Wrap(err)
	}
	n, err := res.RowsAffected()
	return n == 1, db.Wrap(err)
}

// Unneeded lists the conversions no download claims.
func (r *Repo) Unneeded() ([]*Conversion, error) {
	rows, err := r.d.Query(`SELECT ` + ccols + ` FROM conversion c WHERE NOT EXISTS (SELECT 1 FROM download d WHERE d.conversion_id = c.id) ORDER BY id`)
	return db.Collect(rows, err, scanConversion)
}

// ClaimedBy is the conversions claimed by the downloads of these accounts
// or of their devices.
func (r *Repo) ClaimedBy(accountIDs []int64) ([]int64, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	in, args := db.In(accountIDs)
	rows, err := r.d.Query(`SELECT DISTINCT d.conversion_id FROM download d JOIN device v ON v.id = d.device_id
		WHERE d.conversion_id IS NOT NULL AND (d.account_id IN `+in+` OR v.account_id IN `+in+`)`, append(args, args...)...)
	return db.Collect(rows, err, func(s db.Scanner) (int64, error) {
		var id int64
		return id, db.Wrap(s.Scan(&id))
	})
}

// Count is how many downloads claim a conversion.
func (r *Repo) Count(conversionID int64) (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM download WHERE conversion_id = ?`, conversionID).Scan(&n)
	return n, db.Wrap(err)
}

const dcols = `id, device_id, account_id, item_id, edition_id, media_file_id, conversion_id, quality, created_at`

func scanDownload(s db.Scanner) (*Download, error) {
	var v Download
	var ed, conv sql.NullInt64
	var created int64
	if err := s.Scan(&v.ID, &v.DeviceID, &v.AccountID, &v.ItemID, &ed, &v.MediaFileID, &conv, &v.Quality, &created); err != nil {
		return nil, db.Wrap(err)
	}
	if ed.Valid {
		v.EditionID = &ed.Int64
	}
	if conv.Valid {
		v.ConversionID = &conv.Int64
	}
	v.CreatedAt = db.FromMs(created)
	return &v, nil
}

// Create inserts v, stamping CreatedAt now unless set, and fills ID.
func (r *Repo) Create(v *Download) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	res, err := r.d.Exec(`INSERT INTO download (device_id, account_id, item_id, edition_id, media_file_id, conversion_id, quality, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, v.DeviceID, v.AccountID, v.ItemID, db.NullID(v.EditionID), v.MediaFileID, db.NullID(v.ConversionID), v.Quality, db.Ms(v.CreatedAt))
	if err != nil {
		return db.Wrap(err)
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

// Claim stores v, on the conversion c describes, requested as Request does,
// when c is set; one transaction, so no removal of unclaimed conversions
// runs between the request and the claim. It returns the download already
// claiming what v would, as Find does, or v.
func (r *Repo) Claim(v *Download, c *Conversion) (*Download, error) {
	out := v
	err := db.InTx(r.d, func(d db.DBTX) error {
		tx := New(d)
		if c != nil {
			if err := tx.Request(c); err != nil {
				return err
			}
			v.ConversionID = &c.ID
		}
		found, err := tx.Find(v)
		if err == nil {
			out = found
			return nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return err
		}
		return tx.Create(v)
	})
	return out, err
}

// Find returns the download already claiming what v would, the same file or
// conversion of the same title for the same device and profile, or
// db.ErrNotFound.
func (r *Repo) Find(v *Download) (*Download, error) {
	return scanDownload(r.d.QueryRow(`SELECT `+dcols+` FROM download WHERE device_id = ? AND account_id = ? AND item_id = ? AND edition_id IS ?
		AND media_file_id = ? AND conversion_id IS ? ORDER BY id LIMIT 1`, v.DeviceID, v.AccountID, v.ItemID, db.NullID(v.EditionID), v.MediaFileID,
		db.NullID(v.ConversionID)))
}

// Get returns one download or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Download, error) {
	return scanDownload(r.d.QueryRow(`SELECT `+dcols+` FROM download WHERE id = ?`, id))
}

// visibleDownloads is the downloads of device ?1 for profile ?2 whose title
// the audience bound at ?6 to ?10 may see.
var visibleDownloads = `SELECT ` + dcols + ` FROM download WHERE device_id = ?1 AND account_id = ?2 AND
	EXISTS (SELECT 1 FROM item WHERE item.id = download.item_id AND ` + item.VisibleWhere + `)`

// Of returns a download of the device for the profile whose title the
// audience may see; any other is db.ErrNotFound.
func (r *Repo) Of(id, deviceID, accountID int64, a item.Audience) (*Download, error) {
	return scanDownload(r.d.QueryRow(visibleDownloads+` AND id = ?3`, append([]any{deviceID, accountID, id, nil, nil}, a.Args()...)...))
}

// List pages, oldest first, the downloads of the device for the profile
// whose titles the audience may see.
func (r *Repo) List(deviceID, accountID int64, a item.Audience, limit, offset int) ([]*Download, error) {
	rows, err := r.d.Query(visibleDownloads+` ORDER BY id LIMIT ?3 OFFSET ?4`, append([]any{deviceID, accountID, limit, offset, nil}, a.Args()...)...)
	return db.Collect(rows, err, scanDownload)
}

// Delete removes one download.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM download WHERE id = ?`, id)
}

// Expire removes the downloads of revoked devices, and those made before
// cutoff that claim the file itself or a conversion finished before it: a
// download waiting on its conversion stays.
func (r *Repo) Expire(cutoff time.Time) error {
	_, err := r.d.Exec(`DELETE FROM download WHERE device_id IN (SELECT id FROM device WHERE revoked_at IS NOT NULL)
		OR created_at < ?1 AND (conversion_id IS NULL OR conversion_id IN (SELECT id FROM conversion WHERE status IN ('ready','failed') AND finished_at < ?1))`,
		db.Ms(cutoff))
	return db.Wrap(err)
}

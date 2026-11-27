// Package session holds playback sessions and the ordered plan each one
// carries, with their repository. The plan is what the server decided from a
// capability snapshot; a failed entry is marked bad and the session moves to
// the next one.
package session

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Verdicts, cheapest first.
const (
	DirectPlay      = "direct_play"
	Remux           = "remux"
	AudioTranscode  = "audio_transcode"
	VideoTranscode  = "video_transcode"
	ActionCopy      = "copy"
	ActionConvert   = "convert"
	SubtitleNone    = "none"
	SubtitleSidecar = "sidecar"
	SubtitleBurn    = "burn"
	EncoderHardware = "hardware"
	EncoderSoftware = "software"
)

// ServerFailure prefixes the bad reason of an entry the server failed, such
// as an ffmpeg error or a full engine. Only client refusals are kept out of
// the plan a session re-decides.
const ServerFailure = "server: "

// Session is one device playing one file for one account.
type Session struct {
	ID           uid.ID
	DeviceID     uid.ID
	AccountID    uid.ID
	ItemID       uid.ID
	MediaFileID  uid.ID
	PositionMs   int64
	CurrentEntry int    // position of the plan entry in use
	StreamKey    string // secret carried by the session's stream URLs
	// Station marks a session tuned in to a station: it marks nothing
	// watched, saves no position and counts as no play.
	Station   bool
	StartedAt time.Time
	CreatedAt time.Time
	// ModifiedAt is when the device last reported, which keeps the session
	// from being reaped: only its reports and End set it, never the server
	// changing the session's key, program or plan entry.
	ModifiedAt time.Time
	EndedAt    *time.Time
	// MovedTo is the device Move here took the session to, which ended it.
	MovedTo *uid.ID
}

// PlanEntry is one rung of the ordered fallback plan.
type PlanEntry struct {
	SessionID      uid.ID
	Position       int
	Verdict        string
	VideoAction    string
	AudioAction    string
	SubtitleAction string
	Bad            bool
	BadReason      string

	AudioTrackID    *uid.ID // nil: the file's first audio track
	AudioChannels   int     // output channels when audio converts
	SubtitleTrackID *uid.ID // nil: no subtitle
	VideoCodec      string  // output codec when video converts
	Encoder         string  // EncoderHardware or EncoderSoftware when video converts
	ToneMap         bool    // HDR source converted to SDR
	TopHeight       int     // height of the best rung
	TopBitrate      int64   // bitrate of the best rung, bits per second
	SingleRung      bool    // no lower rungs are offered
	Floor           bool    // a 240p rung sits under the standard ones
	TS              bool    // HLS segments are MPEG-TS rather than fragmented MP4
}

// Same reports whether two entries would produce the same stream, ignoring
// where they sit in a plan and whether they failed.
func (e *PlanEntry) Same(o *PlanEntry) bool {
	a, b := e, o
	return a.Verdict == b.Verdict && a.VideoAction == b.VideoAction && a.AudioAction == b.AudioAction &&
		a.SubtitleAction == b.SubtitleAction && eq(a.AudioTrackID, b.AudioTrackID) && a.AudioChannels == b.AudioChannels &&
		eq(a.SubtitleTrackID, b.SubtitleTrackID) && a.VideoCodec == b.VideoCodec && a.Encoder == b.Encoder &&
		a.ToneMap == b.ToneMap && a.TopHeight == b.TopHeight && a.TopBitrate == b.TopBitrate && a.SingleRung == b.SingleRung &&
		a.Floor == b.Floor && a.TS == b.TS
}

func eq(a, b *uid.ID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Sample is one group E telemetry report.
type Sample struct {
	ID              uid.ID
	SessionID       uid.ID
	At              time.Time
	MeasuredBitrate int64
	BufferMs        int64
	StallCount      int
	CurrentRung     *int
	RungHeight      *int   // the variant the player is on
	RungBitrate     *int64 // that variant's declared bits per second
	CreatedAt       time.Time
	ModifiedAt      time.Time
}

// Repo persists sessions and plans.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, device_id, account_id, item_id, media_file_id, position_ms, current_entry, started_at, modified_at, ended_at, stream_key, station,
	moved_to_device_id, created_at`

const scols = `s.id, s.device_id, s.account_id, s.item_id, s.media_file_id, s.position_ms, s.current_entry, s.started_at, s.modified_at,
	s.ended_at, s.stream_key, s.station, s.moved_to_device_id, s.created_at`

func scan(s db.Scanner) (*Session, error) {
	var v Session
	var started, modified, created int64
	var ended sql.NullInt64
	var key sql.NullString
	if err := s.Scan(&v.ID, &v.DeviceID, &v.AccountID, &v.ItemID, &v.MediaFileID, &v.PositionMs, &v.CurrentEntry, &started, &modified, &ended, &key, &v.Station,
		&v.MovedTo, &created); err != nil {
		return nil, db.Wrap(err)
	}
	v.StreamKey = key.String
	v.StartedAt, v.ModifiedAt, v.EndedAt, v.CreatedAt = db.FromMs(started), db.FromMs(modified), db.FromNullMs(ended), db.FromMs(created)
	return &v, nil
}

func insertPlan(d db.DBTX, id uid.ID, plan []*PlanEntry) error {
	for i, e := range plan {
		e.SessionID, e.Position = id, i
		if _, err := d.Exec(`INSERT INTO session_plan_entry (session_id, position, verdict, video_action, audio_action, subtitle_action, bad, bad_reason,
			audio_track_id, audio_channels, subtitle_track_id, video_codec, encoder, tone_map, top_height, top_bitrate, single_rung, floor_rung, ts_segments)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, i, e.Verdict, e.VideoAction, e.AudioAction, e.SubtitleAction, e.Bad, db.NullStr(e.BadReason),
			e.AudioTrackID, e.AudioChannels, e.SubtitleTrackID, e.VideoCodec, e.Encoder, e.ToneMap, e.TopHeight, e.TopBitrate, e.SingleRung,
			e.Floor, e.TS); err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

// Create inserts s and its plan atomically, minting its ID unless set; plan
// positions are assigned in order.
func (r *Repo) Create(s *Session, plan []*PlanEntry) error {
	if s.ID.IsZero() {
		s.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	s.StartedAt, s.CreatedAt, s.ModifiedAt = now, now, now
	return db.InTx(r.d, func(d db.DBTX) error {
		_, err := d.Exec(`INSERT INTO playback_session (id, device_id, account_id, item_id, media_file_id, position_ms, current_entry, started_at,
			created_at, modified_at, stream_key, station) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, s.ID, s.DeviceID, s.AccountID, s.ItemID, s.MediaFileID,
			s.PositionMs, s.CurrentEntry, db.Ms(now), db.Ms(now), db.Ms(now), db.NullStr(s.StreamKey), s.Station)
		if err != nil {
			return db.Wrap(err)
		}
		return insertPlan(d, s.ID, plan)
	})
}

// Get returns one session or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Session, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM playback_session WHERE id = ?`, id))
}

// SetStreamKey replaces the key a session's stream URLs carry, so URLs
// handed out before a new plan stop working.
func (r *Repo) SetStreamKey(id uid.ID, key string) error {
	return db.ExecOne(r.d, `UPDATE playback_session SET stream_key = ? WHERE id = ?`, key, id)
}

// SetProgram points a station's session at the program now on: its item
// and the file of it the device plays.
func (r *Repo) SetProgram(id, itemID, mediaFileID uid.ID) error {
	return db.ExecOne(r.d, `UPDATE playback_session SET item_id = ?, media_file_id = ? WHERE id = ?`, itemID, mediaFileID, id)
}

// ByStreamKey finds the open session whose stream URLs carry key, only
// while its device and that device's adult may still sign in.
func (r *Repo) ByStreamKey(key string) (*Session, error) {
	return scan(r.d.QueryRow(`SELECT `+scols+` FROM playback_session s JOIN device d ON d.id = s.device_id JOIN account a ON a.id = d.account_id
		WHERE s.stream_key = ? AND s.ended_at IS NULL AND d.revoked_at IS NULL AND a.revoked_at IS NULL`, key))
}

func (r *Repo) list(q string, args ...any) ([]*Session, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// Active lists sessions not yet ended, newest first.
func (r *Repo) Active() ([]*Session, error) {
	return r.list(`SELECT ` + cols + ` FROM playback_session WHERE ended_at IS NULL ORDER BY started_at DESC, id DESC`)
}

// ActiveByDevice lists the device's sessions still open, newest first.
func (r *Repo) ActiveByDevice(deviceID uid.ID) ([]*Session, error) {
	return r.list(`SELECT `+cols+` FROM playback_session WHERE device_id = ? AND ended_at IS NULL ORDER BY id DESC`, deviceID)
}

// Playing lists an account's open sessions that reported since the given
// time, newest first, without those tuned in to a station.
func (r *Repo) Playing(accountID uid.ID, since time.Time) ([]*Session, error) {
	return r.list(`SELECT `+cols+` FROM playback_session WHERE account_id = ? AND ended_at IS NULL AND station = 0 AND modified_at >= ?
		ORDER BY id DESC`, accountID, db.Ms(since))
}

// List pages all sessions, newest first.
func (r *Repo) List(limit, offset int) ([]*Session, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.list(`SELECT `+cols+` FROM playback_session ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
}

// SetPosition records playback progress.
func (r *Repo) SetPosition(id uid.ID, positionMs int64, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE playback_session SET position_ms = ?, modified_at = ? WHERE id = ? AND ended_at IS NULL`, positionMs, db.Ms(at), id)
}

// End closes a session and drops its telemetry, which only the live slow
// loop reads; ending twice returns db.ErrNotFound.
func (r *Repo) End(id uid.ID, at time.Time) error { return r.end(id, nil, at) }

// Move is End for a session another device took over.
func (r *Repo) Move(id, toDeviceID uid.ID, at time.Time) error { return r.end(id, &toDeviceID, at) }

func (r *Repo) end(id uid.ID, movedTo *uid.ID, at time.Time) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		if err := db.ExecOne(d, `UPDATE playback_session SET ended_at = ?1, modified_at = ?1, moved_to_device_id = ?2 WHERE id = ?3 AND ended_at IS NULL`,
			db.Ms(at), movedTo, id); err != nil {
			return err
		}
		_, err := d.Exec(`DELETE FROM throughput_sample WHERE session_id = ?`, id)
		return db.Wrap(err)
	})
}

// Delete removes a session and its plan.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM playback_session WHERE id = ?`, id)
}

const ecols = `e.session_id, e.position, e.verdict, e.video_action, e.audio_action, e.subtitle_action, e.bad, e.bad_reason,
	e.audio_track_id, e.audio_channels, e.subtitle_track_id, e.video_codec, e.encoder, e.tone_map, e.top_height, e.top_bitrate, e.single_rung,
	e.floor_rung, e.ts_segments`

func (r *Repo) entries(q string, args ...any) ([]*PlanEntry, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, func(s db.Scanner) (*PlanEntry, error) {
		var e PlanEntry
		var reason sql.NullString
		if err := s.Scan(&e.SessionID, &e.Position, &e.Verdict, &e.VideoAction, &e.AudioAction, &e.SubtitleAction, &e.Bad, &reason,
			&e.AudioTrackID, &e.AudioChannels, &e.SubtitleTrackID, &e.VideoCodec, &e.Encoder, &e.ToneMap, &e.TopHeight, &e.TopBitrate, &e.SingleRung,
			&e.Floor, &e.TS); err != nil {
			return nil, err
		}
		e.BadReason = reason.String
		return &e, nil
	})
}

// Plan lists a session's entries in order.
func (r *Repo) Plan(id uid.ID) ([]*PlanEntry, error) {
	return r.entries(`SELECT `+ecols+` FROM session_plan_entry e WHERE e.session_id = ? ORDER BY e.position`, id)
}

// ReplacePlan swaps the whole plan and points the session at its first
// entry.
func (r *Repo) ReplacePlan(id uid.ID, plan []*PlanEntry) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		if err := db.ExecOne(d, `UPDATE playback_session SET current_entry = 0 WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := d.Exec(`DELETE FROM session_plan_entry WHERE session_id = ?`, id); err != nil {
			return db.Wrap(err)
		}
		return insertPlan(d, id, plan)
	})
}

// ErrMoved means the session no longer plays the entry a failure was
// reported against; nothing was changed.
var ErrMoved = errors.New("session moved to another plan entry")

// FailEntry marks entry bad, if the session still plays it, and advances to
// the next good one. It returns the new current position; the current one
// with ErrMoved when the session had left entry; or db.ErrNotFound when the
// plan is exhausted (the session is left on the bad entry).
func (r *Repo) FailEntry(id uid.ID, entry int, reason string) (int, error) {
	next := -1
	err := db.InTx(r.d, func(d db.DBTX) error {
		var cur int
		if err := d.QueryRow(`SELECT current_entry FROM playback_session WHERE id = ?`, id).Scan(&cur); err != nil {
			return db.Wrap(err)
		}
		if cur != entry {
			next = cur
			return ErrMoved
		}
		if _, err := d.Exec(`UPDATE session_plan_entry SET bad = 1, bad_reason = ? WHERE session_id = ? AND position = ?`, db.NullStr(reason), id, cur); err != nil {
			return db.Wrap(err)
		}
		var n sql.NullInt64
		if err := d.QueryRow(`SELECT MIN(position) FROM session_plan_entry WHERE session_id = ? AND bad = 0 AND position > ?`, id, cur).Scan(&n); err != nil {
			return db.Wrap(err)
		}
		if !n.Valid {
			return nil
		}
		next = int(n.Int64)
		_, err := d.Exec(`UPDATE playback_session SET current_entry = ? WHERE id = ?`, next, id)
		return db.Wrap(err)
	})
	if err == nil && next < 0 {
		return 0, db.ErrNotFound
	}
	return next, err
}

// AddSample stores one telemetry report, minting its ID unless set, and
// marks the session modified, which keeps it from being reaped.
func (r *Repo) AddSample(v *Sample) error {
	if v.ID.IsZero() {
		v.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	v.CreatedAt, v.ModifiedAt = now, now
	return db.InTx(r.d, func(d db.DBTX) error {
		_, err := d.Exec(`INSERT INTO throughput_sample (id, session_id, at, measured_bitrate, buffer_ms, stall_count, current_rung, rung_height, rung_bitrate,
			created_at, modified_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.SessionID, db.Ms(v.At), v.MeasuredBitrate, v.BufferMs, v.StallCount,
			v.CurrentRung, v.RungHeight, v.RungBitrate, db.Ms(now), db.Ms(now))
		if err != nil {
			return db.Wrap(err)
		}
		_, err = d.Exec(`UPDATE playback_session SET modified_at = ? WHERE id = ? AND ended_at IS NULL`, db.Ms(v.At), v.SessionID)
		return db.Wrap(err)
	})
}

// Samples lists a session's last telemetry reports, at most last of them,
// oldest first.
func (r *Repo) Samples(sessionID uid.ID, last int) ([]*Sample, error) {
	rows, err := r.d.Query(`SELECT * FROM (SELECT id, session_id, at, measured_bitrate, buffer_ms, stall_count, current_rung, rung_height, rung_bitrate,
		created_at, modified_at FROM throughput_sample WHERE session_id = ? ORDER BY at DESC, id DESC LIMIT ?) ORDER BY at, id`, sessionID, last)
	return db.Collect(rows, err, func(s db.Scanner) (*Sample, error) {
		var v Sample
		var at, created, modified int64
		if err := s.Scan(&v.ID, &v.SessionID, &at, &v.MeasuredBitrate, &v.BufferMs, &v.StallCount, &v.CurrentRung, &v.RungHeight, &v.RungBitrate,
			&created, &modified); err != nil {
			return nil, err
		}
		v.At, v.CreatedAt, v.ModifiedAt = db.FromMs(at), db.FromMs(created), db.FromMs(modified)
		return &v, nil
	})
}

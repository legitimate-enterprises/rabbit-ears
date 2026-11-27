// Package mediafile holds the ffprobe facts about a file: the file itself,
// its video, audio and subtitle tracks, and its chapters.
package mediafile

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Track kinds.
const (
	Video    = "video"
	Audio    = "audio"
	Subtitle = "subtitle"
)

// File is one playable file. It belongs to items through their editions,
// one file to several when it holds several episodes.
type File struct {
	ID         uid.ID
	CreatedAt  time.Time
	ModifiedAt time.Time
	// ItemID is the first item the file belongs to, nil for none. Set on
	// create or update, it files the file under that item's untagged
	// edition and takes it from every other item's.
	ItemID         *uid.ID
	Path           string
	SizeBytes      int64
	FileModifiedAt time.Time // the file's mtime on disk
	Container      string
	DurationMs     *int64
	Bitrate        *int64
	ProbedAt       *time.Time // nil until probed
	// MissingAt is when the file was first found gone from disk, nil while
	// it is there. Only SetMissing writes it.
	MissingAt *time.Time
	// ProbeFailedAt is when ffprobe last failed to read the file and
	// ProbeError what it said; both clear once a probe succeeds.
	ProbeFailedAt *time.Time
	ProbeError    string
	// Fingerprint identifies the file by its media alone, "" until read
	// (see probe.Fingerprint).
	Fingerprint string
}

// Track is one stream. Only the fields that apply to Kind are set.
type Track struct {
	ID             uid.ID
	MediaFileID    uid.ID
	CreatedAt      time.Time
	ModifiedAt     time.Time
	StreamIndex    int
	Kind           string
	Codec          string
	Profile        string
	Level          *int
	Language       string
	Title          string
	IsDefault      bool
	IsForced       bool
	Bitrate        *int64
	Width          *int
	Height         *int
	FrameRate      *float64
	BitDepth       *int
	PixelFormat    string
	ColorPrimaries string
	ColorTransfer  string
	HDRFormat      string // "", hdr10, hdr10plus, dolby_vision, hlg
	DVProfile      *int   // Dolby Vision profile when HDRFormat is dolby_vision
	Channels       *int
	ChannelLayout  string
	SampleRate     *int
	SubtitleIsText *bool // text formats go as sidecars, bitmap formats are converted or burned
}

// Chapter is a named span of a file with an optional generated thumbnail.
type Chapter struct {
	ID            uid.ID
	MediaFileID   uid.ID
	CreatedAt     time.Time
	ModifiedAt    time.Time
	Position      int
	StartMs       int64
	EndMs         int64
	Title         string
	ThumbnailPath string
}

// Repo persists files, tracks and chapters.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// fcols reads a file aliased f.
const fcols = `f.id, (SELECT MIN(x.item_id) FROM item_file x WHERE x.media_file_id = f.id), f.path, f.size_bytes, f.file_modified_at,
	f.container, f.duration_ms, f.bitrate, f.probed_at, f.missing_at, f.probe_failed_at, f.probe_error, f.fingerprint, f.created_at, f.modified_at`

func scanFile(s db.Scanner) (*File, error) {
	var f File
	var mod, created, modified int64
	var container, probeErr, print sql.NullString
	var dur, br, probed, missing, failed sql.NullInt64
	if err := s.Scan(&f.ID, &f.ItemID, &f.Path, &f.SizeBytes, &mod, &container, &dur, &br, &probed, &missing, &failed, &probeErr, &print,
		&created, &modified); err != nil {
		return nil, db.Wrap(err)
	}
	f.FileModifiedAt, f.Container = db.FromMs(mod), container.String
	f.CreatedAt, f.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	if dur.Valid {
		f.DurationMs = &dur.Int64
	}
	if br.Valid {
		f.Bitrate = &br.Int64
	}
	f.ProbedAt, f.MissingAt = db.FromNullMs(probed), db.FromNullMs(missing)
	f.ProbeFailedAt, f.ProbeError, f.Fingerprint = db.FromNullMs(failed), probeErr.String, print.String
	return &f, nil
}

// CreateFile inserts f, filling a zero ID and the audit times.
func (r *Repo) CreateFile(f *File) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		if f.ID.IsZero() {
			f.ID = uid.New()
		}
		t := now()
		if _, err := d.Exec(`INSERT INTO media_file (id, path, size_bytes, file_modified_at, container, duration_ms, bitrate, probed_at,
			created_at, modified_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, f.ID, f.Path, f.SizeBytes, db.Ms(f.FileModifiedAt), db.NullStr(f.Container),
			f.DurationMs, f.Bitrate, db.NullMs(f.ProbedAt), db.Ms(t), db.Ms(t)); err != nil {
			return db.Wrap(err)
		}
		f.CreatedAt, f.ModifiedAt = t, t
		return belong(d, f)
	})
}

// belong files f under f.ItemID as ItemID promises, unless it is there.
func belong(d db.DBTX, f *File) error {
	if f.ItemID == nil {
		return nil
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM item_file WHERE media_file_id = ? AND item_id = ?`, f.ID, f.ItemID).Scan(&n); err != nil || n > 0 {
		return db.Wrap(err)
	}
	if _, err := edition.New(d).Place(f.ID, *f.ItemID, "", f.DurationMs, false); err != nil {
		return err
	}
	_, err := d.Exec(`DELETE FROM edition_file WHERE media_file_id = ? AND edition_id NOT IN (SELECT id FROM edition WHERE item_id = ?)`,
		f.ID, f.ItemID)
	return db.Wrap(err)
}

// File returns one file or db.ErrNotFound.
func (r *Repo) File(id uid.ID) (*File, error) {
	return scanFile(r.d.QueryRow(`SELECT `+fcols+` FROM media_file f WHERE f.id = ?`, id))
}

// FileByPath looks a file up by its absolute path.
func (r *Repo) FileByPath(path string) (*File, error) {
	return scanFile(r.d.QueryRow(`SELECT `+fcols+` FROM media_file f WHERE f.path = ?`, path))
}

func (r *Repo) files(q string, args ...any) ([]*File, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scanFile)
}

// FilesForItem lists an item's files across its editions, largest first
// (the likeliest original).
func (r *Repo) FilesForItem(itemID uid.ID) ([]*File, error) {
	return r.files(`SELECT `+fcols+` FROM media_file f WHERE EXISTS (SELECT 1 FROM item_file x WHERE x.media_file_id = f.id AND x.item_id = ?)
		ORDER BY f.size_bytes DESC, f.id`, itemID)
}

// FilesForEdition lists an edition's files, largest first.
func (r *Repo) FilesForEdition(editionID uid.ID) ([]*File, error) {
	return r.files(`SELECT `+fcols+` FROM media_file f JOIN edition_file ef ON ef.media_file_id = f.id WHERE ef.edition_id = ?
		ORDER BY f.size_bytes DESC, f.id`, editionID)
}

// inLibrary keeps files f of an item of library ?, found from the library's
// items.
const inLibrary = `f.id IN (SELECT x.media_file_id FROM item i JOIN item_file x ON x.item_id = i.id WHERE i.library_id = ?)`

// FilesInLibrary lists every file under a library.
func (r *Repo) FilesInLibrary(libraryID uid.ID) ([]*File, error) {
	return r.files(`SELECT `+fcols+` FROM media_file f WHERE `+inLibrary+` ORDER BY f.path`, libraryID)
}

// Problems a file can have.
const (
	Missing    = "missing"
	NoDuration = "no_duration"
	Unreadable = "unreadable"
)

// problems keeps files f with each problem. What a missing file's probe
// said is stale until it is back, so missing is its only problem.
var problems = map[string]string{
	Missing:    `f.missing_at IS NOT NULL`,
	NoDuration: `f.missing_at IS NULL AND f.probed_at IS NOT NULL AND COALESCE(f.duration_ms, 0) <= 0`,
	Unreadable: `f.missing_at IS NULL AND f.probe_failed_at IS NOT NULL`,
}

// Located is a file with the library and shown title of its first item,
// nil and "" for a file of no item.
type Located struct {
	*File
	LibraryID *uid.ID
	Title     string
}

// withTail scans a row's leading columns into the File and the rest into tail.
type withTail struct {
	s    db.Scanner
	tail []any
}

func (w withTail) Scan(dst ...any) error { return w.s.Scan(append(dst, w.tail...)...) }

// Problems lists the files with problem by path, of one library's items
// unless libraryID is nil.
func (r *Repo) Problems(problem string, libraryID *uid.ID) ([]*Located, error) {
	rows, err := r.d.Query(`SELECT `+fcols+`, i.library_id,
		COALESCE((SELECT m.title FROM item_shown m WHERE m.item_id = i.id), i.parsed_title, '') FROM media_file f
		LEFT JOIN item i ON i.id = (SELECT MIN(x.item_id) FROM item_file x WHERE x.media_file_id = f.id)
		WHERE `+problems[problem]+` AND (?1 IS NULL OR i.library_id = ?1) ORDER BY f.path`, libraryID)
	return db.Collect(rows, err, func(s db.Scanner) (*Located, error) {
		var l Located
		f, err := scanFile(withTail{s, []any{&l.LibraryID, &l.Title}})
		l.File = f
		return &l, err
	})
}

// ProblemCount counts the files Problems lists with libraryID nil.
func (r *Repo) ProblemCount(problem string) (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM media_file f WHERE ` + problems[problem]).Scan(&n)
	return n, db.Wrap(err)
}

// ForgetProbe marks a file unprobed, so the next scan of it probes it again.
func (r *Repo) ForgetProbe(id uid.ID) error {
	return db.ExecOne(r.d, `UPDATE media_file SET probed_at = NULL, modified_at = ? WHERE id = ?`, db.Ms(now()), id)
}

// Totals returns the number of files under a library and their combined size.
func (r *Repo) Totals(libraryID uid.ID) (files int, bytes int64, err error) {
	err = r.d.QueryRow(`SELECT COUNT(*), COALESCE(SUM(f.size_bytes), 0) FROM media_file f WHERE `+inLibrary, libraryID).Scan(&files, &bytes)
	return files, bytes, db.Wrap(err)
}

// NeedingPreviews lists probed files with a video track that have no seek
// previews, skipping a file marked missing and one whose previews failed at
// its current modification time. A scan that sees a file change removes its
// previews.
func (r *Repo) NeedingPreviews() ([]*File, error) {
	return r.files(`SELECT ` + fcols + ` FROM media_file f LEFT JOIN seek_preview p ON p.media_file_id = f.id
		WHERE f.probed_at IS NOT NULL AND f.missing_at IS NULL AND p.id IS NULL
		AND (f.previews_failed_for IS NULL OR f.previews_failed_for != f.file_modified_at)
		AND EXISTS (SELECT 1 FROM track t WHERE t.media_file_id = f.id AND t.kind = 'video') ORDER BY f.id`)
}

// PreviewsFailed records that f's previews failed at its current
// modification time.
func (r *Repo) PreviewsFailed(f *File) error {
	f.ModifiedAt = now()
	return db.ExecOne(r.d, `UPDATE media_file SET previews_failed_for = ?, modified_at = ? WHERE id = ?`, db.Ms(f.FileModifiedAt), db.Ms(f.ModifiedAt), f.ID)
}

// UnavailableOf is the SQL for whether the item whose id is the expression
// it has files, every one of them marked missing.
func UnavailableOf(it string) string {
	return `(SELECT COUNT(*) > 0 AND COUNT(*) = COUNT(uf.missing_at) FROM item_file ux
		JOIN media_file uf ON uf.id = ux.media_file_id WHERE ux.item_id = ` + it + `)`
}

// Unavailable is the subset of the items that UnavailableOf holds for.
func (r *Repo) Unavailable(itemIDs []uid.ID) (map[uid.ID]bool, error) {
	out := map[uid.ID]bool{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	in, args := db.In(itemIDs)
	rows, err := r.d.Query(`SELECT i.id FROM item i WHERE i.id IN `+in+` AND `+UnavailableOf("i.id"), args...)
	ids, err := db.Collect(rows, err, db.Column[uid.ID])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// SetMissing marks f gone from disk since at, or with nil as there. A file
// already marked keeps the instant it first went.
func (r *Repo) SetMissing(f *File, at *time.Time) error {
	if (f.MissingAt == nil) == (at == nil) {
		return nil
	}
	f.MissingAt, f.ModifiedAt = at, now()
	return db.ExecOne(r.d, `UPDATE media_file SET missing_at = ?, modified_at = ? WHERE id = ?`, db.NullMs(at), db.Ms(f.ModifiedAt), f.ID)
}

// UpdateFile writes the mutable fields of f.
func (r *Repo) UpdateFile(f *File) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		f.ModifiedAt = now()
		if err := db.ExecOne(d, `UPDATE media_file SET size_bytes=?, file_modified_at=?, container=?, duration_ms=?, bitrate=?, probed_at=?,
			probe_failed_at=?, probe_error=?, fingerprint=?, modified_at=? WHERE id=?`, f.SizeBytes, db.Ms(f.FileModifiedAt), db.NullStr(f.Container),
			f.DurationMs, f.Bitrate, db.NullMs(f.ProbedAt), db.NullMs(f.ProbeFailedAt), db.NullStr(f.ProbeError), db.NullStr(f.Fingerprint),
			db.Ms(f.ModifiedAt), f.ID); err != nil {
			return err
		}
		return belong(d, f)
	})
}

// DeleteFile removes a file; tracks and chapters cascade. Then its seek
// preview frames and chapter thumbnails are taken off disk.
func (r *Repo) DeleteFile(id uid.ID) error {
	return DeleteMade(r.d, func() error { return db.ExecOne(r.d, `DELETE FROM media_file WHERE id = ?`, id) }, `SELECT ?`, id)
}

// madePath is a seek preview folder or a chapter thumbnail the scan made.
type madePath struct {
	path  string
	thumb bool
}

// DeleteMade runs del, which deletes the files the query files selects,
// then takes off disk the seek preview frames and chapter thumbnails made
// from them, and each thumbnail's folder once empty.
func DeleteMade(d db.DBTX, del func() error, files string, args ...any) error {
	rows, err := d.Query(`WITH f(id) AS (`+files+`) SELECT dir, 0 FROM seek_preview WHERE media_file_id IN f
		UNION ALL SELECT thumbnail_path, 1 FROM chapter WHERE media_file_id IN f AND thumbnail_path IS NOT NULL`, args...)
	ps, err := db.Collect(rows, err, func(s db.Scanner) (madePath, error) {
		var p madePath
		err := s.Scan(&p.path, &p.thumb)
		p.path = db.DataAbs(p.path)
		return p, db.Wrap(err)
	})
	if err != nil {
		return err
	}
	if err := del(); err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		errs = append(errs, os.RemoveAll(p.path))
		if p.thumb {
			_ = os.Remove(filepath.Dir(p.path)) // fails while it holds the file's other thumbnails
		}
	}
	return errors.Join(errs...)
}

const tcols = `id, media_file_id, created_at, modified_at, stream_index, kind, codec, profile, level, language, title, is_default, is_forced, bitrate,
	width, height, frame_rate, bit_depth, pixel_format, color_primaries, color_transfer, hdr_format, channels, channel_layout,
	sample_rate, subtitle_is_text, dv_profile`

func scanTrack(s db.Scanner) (*Track, error) {
	var t Track
	var profile, lang, title, pix, prim, trc, hdr, layout sql.NullString
	var level, w, h, depth, ch, rate, br, text, dv sql.NullInt64
	var fps sql.NullFloat64
	var created, modified int64
	if err := s.Scan(&t.ID, &t.MediaFileID, &created, &modified, &t.StreamIndex, &t.Kind, &t.Codec, &profile, &level, &lang, &title, &t.IsDefault, &t.IsForced,
		&br, &w, &h, &fps, &depth, &pix, &prim, &trc, &hdr, &ch, &layout, &rate, &text, &dv); err != nil {
		return nil, db.Wrap(err)
	}
	t.CreatedAt, t.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	t.Profile, t.Language, t.Title, t.PixelFormat = profile.String, lang.String, title.String, pix.String
	t.ColorPrimaries, t.ColorTransfer, t.HDRFormat, t.ChannelLayout = prim.String, trc.String, hdr.String, layout.String
	t.Level, t.Width, t.Height, t.BitDepth, t.Channels, t.SampleRate = db.FromNullInt(level), db.FromNullInt(w), db.FromNullInt(h), db.FromNullInt(depth), db.FromNullInt(ch), db.FromNullInt(rate)
	t.DVProfile = db.FromNullInt(dv)
	if br.Valid {
		t.Bitrate = &br.Int64
	}
	if fps.Valid {
		t.FrameRate = &fps.Float64
	}
	if text.Valid {
		b := text.Int64 == 1
		t.SubtitleIsText = &b
	}
	return &t, nil
}

// ReplaceTracks atomically swaps a file's tracks for ts.
func (r *Repo) ReplaceTracks(fileID uid.ID, ts []*Track) error {
	t0 := now()
	return db.InTx(r.d, func(d db.DBTX) error {
		if _, err := d.Exec(`DELETE FROM track WHERE media_file_id = ?`, fileID); err != nil {
			return db.Wrap(err)
		}
		for _, t := range ts {
			var fps, text any
			if t.FrameRate != nil {
				fps = *t.FrameRate
			}
			if t.SubtitleIsText != nil {
				text = *t.SubtitleIsText
			}
			if t.ID.IsZero() {
				t.ID = uid.New()
			}
			if _, err := d.Exec(`INSERT INTO track (id, media_file_id, stream_index, kind, codec, profile, level, language, title, is_default,
				is_forced, bitrate, width, height, frame_rate, bit_depth, pixel_format, color_primaries, color_transfer, hdr_format,
				channels, channel_layout, sample_rate, subtitle_is_text, dv_profile, created_at, modified_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				t.ID, fileID, t.StreamIndex, t.Kind, t.Codec, db.NullStr(t.Profile), t.Level, db.NullStr(t.Language), db.NullStr(t.Title),
				t.IsDefault, t.IsForced, t.Bitrate, t.Width, t.Height, fps, t.BitDepth,
				db.NullStr(t.PixelFormat), db.NullStr(t.ColorPrimaries), db.NullStr(t.ColorTransfer), db.NullStr(t.HDRFormat),
				t.Channels, db.NullStr(t.ChannelLayout), t.SampleRate, text, t.DVProfile, db.Ms(t0), db.Ms(t0)); err != nil {
				return db.Wrap(err)
			}
			t.MediaFileID, t.CreatedAt, t.ModifiedAt = fileID, t0, t0
		}
		return nil
	})
}

// Tracks lists a file's tracks in stream order, optionally only one kind.
func (r *Repo) Tracks(fileID uid.ID, kind string) ([]*Track, error) {
	rows, err := r.d.Query(`SELECT `+tcols+` FROM track WHERE media_file_id = ?1 AND (?2 = '' OR kind = ?2) ORDER BY stream_index`, fileID, kind)
	return db.Collect(rows, err, scanTrack)
}

// Track returns one track or db.ErrNotFound.
func (r *Repo) Track(id uid.ID) (*Track, error) {
	return scanTrack(r.d.QueryRow(`SELECT `+tcols+` FROM track WHERE id = ?`, id))
}

// ReplaceChapters atomically swaps a file's chapters.
func (r *Repo) ReplaceChapters(fileID uid.ID, cs []*Chapter) error {
	t := now()
	return db.InTx(r.d, func(d db.DBTX) error {
		if _, err := d.Exec(`DELETE FROM chapter WHERE media_file_id = ?`, fileID); err != nil {
			return db.Wrap(err)
		}
		for _, c := range cs {
			if c.ID.IsZero() {
				c.ID = uid.New()
			}
			if _, err := d.Exec(`INSERT INTO chapter (id, media_file_id, position, start_ms, end_ms, title, thumbnail_path, created_at, modified_at)
				VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, fileID, c.Position, c.StartMs, c.EndMs, db.NullStr(c.Title), db.NullStr(db.DataRel(c.ThumbnailPath)),
				db.Ms(t), db.Ms(t)); err != nil {
				return db.Wrap(err)
			}
			c.MediaFileID, c.CreatedAt, c.ModifiedAt = fileID, t, t
		}
		return nil
	})
}

// Chapters lists a file's chapters in order.
func (r *Repo) Chapters(fileID uid.ID) ([]*Chapter, error) {
	rows, err := r.d.Query(`SELECT id, media_file_id, created_at, modified_at, position, start_ms, end_ms, title, thumbnail_path FROM chapter
		WHERE media_file_id = ? ORDER BY position`, fileID)
	return db.Collect(rows, err, func(s db.Scanner) (*Chapter, error) {
		var c Chapter
		var title, thumb sql.NullString
		var created, modified int64
		if err := s.Scan(&c.ID, &c.MediaFileID, &created, &modified, &c.Position, &c.StartMs, &c.EndMs, &title, &thumb); err != nil {
			return nil, err
		}
		c.CreatedAt, c.ModifiedAt = db.FromMs(created), db.FromMs(modified)
		c.Title, c.ThumbnailPath = title.String, db.DataAbs(thumb.String)
		return &c, nil
	})
}

// SetChapterThumbnail records the generated thumbnail path of a chapter.
func (r *Repo) SetChapterThumbnail(id uid.ID, path string) error {
	return db.ExecOne(r.d, `UPDATE chapter SET thumbnail_path = ?, modified_at = ? WHERE id = ?`, db.NullStr(db.DataRel(path)), db.Ms(now()), id)
}

// Lossless reports an ffprobe audio codec that is lossless.
func Lossless(codec string) bool {
	return codec == "flac" || codec == "alac" || codec == "wavpack" || strings.HasPrefix(codec, "pcm_")
}

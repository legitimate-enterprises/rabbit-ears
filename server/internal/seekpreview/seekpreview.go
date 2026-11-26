// Package seekpreview holds the seek preview frames made for a media file,
// and their repository.
package seekpreview

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// SeekPreview is a directory of JPEG frames, one per interval, and the BIF
// assembled from them.
type SeekPreview struct {
	ID          int64
	MediaFileID int64
	Dir         string
	IntervalMs  int
	Count       int
	Width       int
	Height      int
	CreatedAt   time.Time
}

// Repo persists seek previews.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, media_file_id, dir, interval_ms, count, width, height, created_at`

func scan(s db.Scanner) (*SeekPreview, error) {
	var p SeekPreview
	var created int64
	if err := s.Scan(&p.ID, &p.MediaFileID, &p.Dir, &p.IntervalMs, &p.Count, &p.Width, &p.Height, &created); err != nil {
		return nil, db.Wrap(err)
	}
	p.CreatedAt = db.FromMs(created)
	p.Dir = db.DataAbs(p.Dir)
	return &p, nil
}

// Put stores p as its file's previews, replacing any before, and fills ID
// and CreatedAt.
func (r *Repo) Put(p *SeekPreview) error {
	p.CreatedAt = time.Now().UTC()
	err := r.d.QueryRow(`INSERT INTO seek_preview (media_file_id, dir, interval_ms, count, width, height, created_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT (media_file_id) DO UPDATE SET dir=excluded.dir, interval_ms=excluded.interval_ms, count=excluded.count,
		width=excluded.width, height=excluded.height, created_at=excluded.created_at RETURNING id`,
		p.MediaFileID, db.DataRel(p.Dir), p.IntervalMs, p.Count, p.Width, p.Height, db.Ms(p.CreatedAt)).Scan(&p.ID)
	return db.Wrap(err)
}

// Delete removes one set or returns db.ErrNotFound.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM seek_preview WHERE id = ?`, id)
}

// Get returns one set or db.ErrNotFound.
func (r *Repo) Get(id int64) (*SeekPreview, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM seek_preview WHERE id = ?`, id))
}

// ByMediaFile returns a file's previews or db.ErrNotFound.
func (r *Repo) ByMediaFile(fileID int64) (*SeekPreview, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM seek_preview WHERE media_file_id = ?`, fileID))
}

// List returns every set newest first, or only one file's (0 for all).
func (r *Repo) List(fileID int64) ([]*SeekPreview, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM seek_preview WHERE ?1 = 0 OR media_file_id = ?1 ORDER BY created_at DESC, id DESC`, fileID)
	return db.Collect(rows, err, scan)
}

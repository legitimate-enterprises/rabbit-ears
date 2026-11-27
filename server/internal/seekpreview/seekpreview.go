// Package seekpreview holds the seek preview frames made for a media file,
// and their repository.
package seekpreview

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// SeekPreview is a directory of JPEG frames, one per interval, and the BIF
// assembled from them.
type SeekPreview struct {
	ID          uid.ID
	MediaFileID uid.ID
	Dir         string
	IntervalMs  int
	Count       int
	Width       int
	Height      int
	CreatedAt   time.Time
	ModifiedAt  time.Time // when the frames were last made
}

// Repo persists seek previews.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, media_file_id, dir, interval_ms, count, width, height, created_at, modified_at`

func scan(s db.Scanner) (*SeekPreview, error) {
	var p SeekPreview
	var created, modified int64
	if err := s.Scan(&p.ID, &p.MediaFileID, &p.Dir, &p.IntervalMs, &p.Count, &p.Width, &p.Height, &created, &modified); err != nil {
		return nil, db.Wrap(err)
	}
	p.CreatedAt, p.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	p.Dir = db.DataAbs(p.Dir)
	return &p, nil
}

// Put stores p as its file's previews, replacing any before and keeping
// their row, and fills ID and the audit times.
func (r *Repo) Put(p *SeekPreview) error {
	p.ModifiedAt = time.Now().UTC().Truncate(time.Millisecond)
	var created int64
	err := r.d.QueryRow(`INSERT INTO seek_preview (id, media_file_id, dir, interval_ms, count, width, height, created_at, modified_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT (media_file_id) DO UPDATE SET dir=excluded.dir, interval_ms=excluded.interval_ms, count=excluded.count,
		width=excluded.width, height=excluded.height, modified_at=excluded.modified_at RETURNING id, created_at`,
		uid.New(), p.MediaFileID, db.DataRel(p.Dir), p.IntervalMs, p.Count, p.Width, p.Height, db.Ms(p.ModifiedAt), db.Ms(p.ModifiedAt)).
		Scan(&p.ID, &created)
	p.CreatedAt = db.FromMs(created)
	return db.Wrap(err)
}

// Delete removes one set or returns db.ErrNotFound.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM seek_preview WHERE id = ?`, id)
}

// Get returns one set or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*SeekPreview, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM seek_preview WHERE id = ?`, id))
}

// ByMediaFile returns a file's previews or db.ErrNotFound.
func (r *Repo) ByMediaFile(fileID uid.ID) (*SeekPreview, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM seek_preview WHERE media_file_id = ?`, fileID))
}

// List returns every set most recently made first, or only one file's (nil
// for all).
func (r *Repo) List(fileID *uid.ID) ([]*SeekPreview, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM seek_preview WHERE ?1 IS NULL OR media_file_id = ?1 ORDER BY modified_at DESC, id DESC`, fileID)
	return db.Collect(rows, err, scan)
}

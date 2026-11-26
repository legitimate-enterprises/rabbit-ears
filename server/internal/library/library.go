// Package library holds the library model (a kind and its folders) and its
// repository.
package library

import (
	"database/sql"
	"path/filepath"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

// Kinds a library may hold.
const (
	Movies  = "movies"
	TV      = "tv"
	Music   = "music"
	YouTube = "youtube"
)

// Library is a set of folders of one kind with a nightly scan hour (server
// local time, 0-23) and an optional filesystem watch.
type Library struct {
	ID            int64
	Name          string
	Kind          string
	ScanHour      int
	WatchFiles    bool
	LastScannedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Folder is one root path scanned for a library; a path belongs to one library.
type Folder struct {
	ID        int64
	LibraryID int64
	Path      string
}

// Repo persists libraries and their folders.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, name, kind, scan_hour, watch_files, last_scanned_at, created_at, updated_at`

func scan(s db.Scanner) (*Library, error) {
	var l Library
	var created, updated int64
	var ns sql.NullInt64
	if err := s.Scan(&l.ID, &l.Name, &l.Kind, &l.ScanHour, &l.WatchFiles, &ns, &created, &updated); err != nil {
		return nil, db.Wrap(err)
	}
	l.LastScannedAt = db.FromNullMs(ns)
	l.CreatedAt, l.UpdatedAt = db.FromMs(created), db.FromMs(updated)
	return &l, nil
}

// Create inserts l and fills ID and timestamps.
func (r *Repo) Create(l *Library) error {
	now := time.Now().UTC()
	l.CreatedAt, l.UpdatedAt = now, now
	res, err := r.d.Exec(`INSERT INTO library (name, kind, scan_hour, watch_files, last_scanned_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, l.Name, l.Kind, l.ScanHour, l.WatchFiles, db.NullMs(l.LastScannedAt), db.Ms(now), db.Ms(now))
	if err != nil {
		return db.Wrap(err)
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one library or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Library, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM library WHERE id = ?`, id))
}

// List returns all libraries by name.
func (r *Repo) List() ([]*Library, error) {
	rows, err := r.d.Query(`SELECT ` + cols + ` FROM library ORDER BY name`)
	return db.Collect(rows, err, scan)
}

// Update writes every mutable field of l.
func (r *Repo) Update(l *Library) error {
	l.UpdatedAt = time.Now().UTC()
	return db.ExecOne(r.d, `UPDATE library SET name=?, kind=?, scan_hour=?, watch_files=?, last_scanned_at=?, updated_at=? WHERE id=?`,
		l.Name, l.Kind, l.ScanHour, l.WatchFiles, db.NullMs(l.LastScannedAt), db.Ms(l.UpdatedAt), l.ID)
}

// Delete removes a library; its folders and items cascade, as each item's
// delete does, and so do the seek preview frames and chapter thumbnails
// made from its files.
func (r *Repo) Delete(id int64) error {
	return mediafile.DeleteMade(r.d, func() error { return db.ExecOne(r.d, `DELETE FROM library WHERE id = ?`, id) },
		`SELECT x.media_file_id FROM item_file x JOIN item i ON i.id = x.item_id WHERE i.library_id = ?`, id)
}

// MarkScanned records the completion instant of a scan.
func (r *Repo) MarkScanned(id int64, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE library SET last_scanned_at = ? WHERE id = ?`, db.Ms(at), id)
}

// AddFolder attaches a root path, kept clean; a path already used anywhere
// conflicts.
func (r *Repo) AddFolder(libraryID int64, path string) (*Folder, error) {
	path = filepath.Clean(path)
	res, err := r.d.Exec(`INSERT INTO library_folder (library_id, path) VALUES (?,?)`, libraryID, path)
	if err != nil {
		return nil, db.Wrap(err)
	}
	id, _ := res.LastInsertId()
	return &Folder{id, libraryID, path}, nil
}

// Folders lists a library's root paths, clean even where saved with a
// trailing slash: scans compare them with the paths filepath yields.
func (r *Repo) Folders(libraryID int64) ([]*Folder, error) {
	rows, err := r.d.Query(`SELECT id, library_id, path FROM library_folder WHERE library_id = ? ORDER BY path`, libraryID)
	return db.Collect(rows, err, func(s db.Scanner) (*Folder, error) {
		var f Folder
		if err := s.Scan(&f.ID, &f.LibraryID, &f.Path); err != nil {
			return nil, err
		}
		f.Path = filepath.Clean(f.Path)
		return &f, nil
	})
}

// UpdateFolder writes f's path, cleaning it; a folder of another library is
// not found and a path already used anywhere conflicts.
func (r *Repo) UpdateFolder(f *Folder) error {
	f.Path = filepath.Clean(f.Path)
	return db.ExecOne(r.d, `UPDATE library_folder SET path = ? WHERE id = ? AND library_id = ?`, f.Path, f.ID, f.LibraryID)
}

// RemoveFolder detaches a root path.
func (r *Repo) RemoveFolder(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM library_folder WHERE id = ?`, id)
}

// ForPath finds the library whose folder contains path; the longest
// matching root wins.
func (r *Repo) ForPath(path string) (*Library, error) {
	rows, err := r.d.Query(`SELECT library_id, path FROM library_folder`)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	var best int64
	bestLen := -1
	for rows.Next() {
		var id int64
		var root string
		if err := rows.Scan(&id, &root); err != nil {
			return nil, err
		}
		root = strings.TrimRight(root, "/")
		if (path == root || strings.HasPrefix(path, root+"/")) && len(root) > bestLen {
			best, bestLen = id, len(root)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if bestLen < 0 {
		return nil, db.ErrNotFound
	}
	_ = rows.Close()
	return r.Get(best)
}

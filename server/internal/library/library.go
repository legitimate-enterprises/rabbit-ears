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
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
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
	ID            uid.ID
	Name          string
	Kind          string
	ScanHour      int
	WatchFiles    bool
	LastScannedAt *time.Time
	CreatedAt     time.Time
	ModifiedAt    time.Time
	CreatedBy     *uid.ID
	ModifiedBy    *uid.ID
}

// Folder is one root path scanned for a library; a path belongs to one library.
type Folder struct {
	ID         uid.ID
	LibraryID  uid.ID
	Path       string
	CreatedAt  time.Time
	ModifiedAt time.Time
	CreatedBy  *uid.ID
	ModifiedBy *uid.ID
}

// Repo persists libraries and their folders.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, name, kind, scan_hour, watch_files, last_scanned_at, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner) (*Library, error) {
	var l Library
	var created, modified int64
	var ns sql.NullInt64
	if err := s.Scan(&l.ID, &l.Name, &l.Kind, &l.ScanHour, &l.WatchFiles, &ns, &created, &modified, &l.CreatedBy, &l.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	l.LastScannedAt = db.FromNullMs(ns)
	l.CreatedAt, l.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &l, nil
}

// Create inserts l, by the acting profile by, and fills ID (unless set) and
// the audit fields.
func (r *Repo) Create(l *Library, by *uid.ID) error {
	if l.ID.IsZero() {
		l.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	l.CreatedAt, l.ModifiedAt, l.CreatedBy, l.ModifiedBy = now, now, by, by
	_, err := r.d.Exec(`INSERT INTO library (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`, l.ID, l.Name, l.Kind, l.ScanHour, l.WatchFiles,
		db.NullMs(l.LastScannedAt), db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

// Get returns one library or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Library, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM library WHERE id = ?`, id))
}

// List returns all libraries by name.
func (r *Repo) List() ([]*Library, error) {
	rows, err := r.d.Query(`SELECT ` + cols + ` FROM library ORDER BY name`)
	return db.Collect(rows, err, scan)
}

// Update writes every mutable field of l, by the acting profile by.
func (r *Repo) Update(l *Library, by *uid.ID) error {
	l.ModifiedAt, l.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE library SET name=?, kind=?, scan_hour=?, watch_files=?, last_scanned_at=?, modified_at=?, modified_by=? WHERE id=?`,
		l.Name, l.Kind, l.ScanHour, l.WatchFiles, db.NullMs(l.LastScannedAt), db.Ms(l.ModifiedAt), by, l.ID)
}

// Delete removes a library; its folders and items cascade, as each item's
// delete does, and so do the seek preview frames and chapter thumbnails
// made from its files.
func (r *Repo) Delete(id uid.ID) error {
	return mediafile.DeleteMade(r.d, func() error { return db.ExecOne(r.d, `DELETE FROM library WHERE id = ?`, id) },
		`SELECT x.media_file_id FROM item_file x JOIN item i ON i.id = x.item_id WHERE i.library_id = ?`, id)
}

// MarkScanned records the completion instant of a scan.
func (r *Repo) MarkScanned(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE library SET last_scanned_at = ? WHERE id = ?`, db.Ms(at), id)
}

// AddFolder attaches f's root path, kept clean, to f.LibraryID, by the
// acting profile by, and fills ID (unless set) and the audit fields; a path
// already used anywhere conflicts.
func (r *Repo) AddFolder(f *Folder, by *uid.ID) error {
	if f.ID.IsZero() {
		f.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.Path, f.CreatedAt, f.ModifiedAt, f.CreatedBy, f.ModifiedBy = filepath.Clean(f.Path), now, now, by, by
	_, err := r.d.Exec(`INSERT INTO library_folder (`+fcols+`) VALUES (?,?,?,?,?,?,?)`, f.ID, f.LibraryID, f.Path, db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

const fcols = `id, library_id, path, created_at, modified_at, created_by, modified_by`

// Folders lists a library's root paths, clean even where saved with a
// trailing slash: scans compare them with the paths filepath yields.
func (r *Repo) Folders(libraryID uid.ID) ([]*Folder, error) {
	rows, err := r.d.Query(`SELECT `+fcols+` FROM library_folder WHERE library_id = ? ORDER BY path`, libraryID)
	return db.Collect(rows, err, func(s db.Scanner) (*Folder, error) {
		var f Folder
		var created, modified int64
		if err := s.Scan(&f.ID, &f.LibraryID, &f.Path, &created, &modified, &f.CreatedBy, &f.ModifiedBy); err != nil {
			return nil, err
		}
		f.Path, f.CreatedAt, f.ModifiedAt = filepath.Clean(f.Path), db.FromMs(created), db.FromMs(modified)
		return &f, nil
	})
}

// UpdateFolder writes f's path, cleaning it, by the acting profile by; a
// folder of another library is not found and a path already used anywhere
// conflicts.
func (r *Repo) UpdateFolder(f *Folder, by *uid.ID) error {
	f.Path, f.ModifiedAt, f.ModifiedBy = filepath.Clean(f.Path), time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE library_folder SET path = ?, modified_at = ?, modified_by = ? WHERE id = ? AND library_id = ?`,
		f.Path, db.Ms(f.ModifiedAt), by, f.ID, f.LibraryID)
}

// RemoveFolder detaches a root path.
func (r *Repo) RemoveFolder(id uid.ID) error {
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
	var best uid.ID
	bestLen := -1
	for rows.Next() {
		var id uid.ID
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

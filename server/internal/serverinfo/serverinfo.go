// Package serverinfo holds the server's own identity, set at first run, and
// its repository.
package serverinfo

import (
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Info is the single server row.
type Info struct {
	Name string
	// TasksPaused holds scheduled and file-change task runs; Run now still runs.
	TasksPaused bool
	// Gravatar shows adults' Gravatar pictures.
	Gravatar bool
	// TMDBKey is the saved TMDB API key, empty when none is saved. It is a
	// secret: never served or logged.
	TMDBKey string
	// Metadata is where titles' metadata and artwork come from.
	Metadata Sources
	// ReadAheadSeconds is how far past the newest segment a player fetched
	// a stream is converted, and the forward buffer players are asked to hold.
	ReadAheadSeconds int
	// MaxTranscodes is how many video transcodes run at once.
	MaxTranscodes int
	// TranscodeCacheCapBytes is how much the segment cache holds before idle
	// sessions' segments are pruned.
	TranscodeCacheCapBytes int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Repo persists the server row.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// Get returns the server row, or db.ErrNotFound before first run.
func (r *Repo) Get() (*Info, error) {
	var i Info
	var created, updated int64
	if err := r.d.QueryRow(`SELECT name, tasks_paused, gravatar, COALESCE(tmdb_key, ''), metadata_source, metadata_mode, read_ahead_seconds, max_transcodes,
		transcode_cache_cap_bytes, created_at, updated_at FROM server_info WHERE id = 1`).
		Scan(&i.Name, &i.TasksPaused, &i.Gravatar, &i.TMDBKey, &i.Metadata.Source, &i.Metadata.Mode, &i.ReadAheadSeconds, &i.MaxTranscodes,
			&i.TranscodeCacheCapBytes, &created, &updated); err != nil {
		return nil, db.Wrap(err)
	}
	i.CreatedAt, i.UpdatedAt = db.FromMs(created), db.FromMs(updated)
	return &i, nil
}

// Save creates or renames the server row.
func (r *Repo) Save(name string) error {
	now := db.Ms(time.Now().UTC())
	_, err := r.d.Exec(`INSERT INTO server_info (id, name, created_at, updated_at) VALUES (1, ?1, ?2, ?2)
		ON CONFLICT (id) DO UPDATE SET name = ?1, updated_at = ?2`, name, now)
	return db.Wrap(err)
}

// Update writes the editable fields of the server row; db.ErrNotFound
// before first run.
func (r *Repo) Update(i *Info) error {
	return db.ExecOne(r.d, `UPDATE server_info SET name = ?, tasks_paused = ?, gravatar = ?, updated_at = ? WHERE id = 1`,
		i.Name, i.TasksPaused, i.Gravatar, db.Ms(time.Now().UTC()))
}

// SetTMDBKey saves the TMDB API key, or removes it when key is empty;
// db.ErrNotFound before first run.
func (r *Repo) SetTMDBKey(key string) error {
	return db.ExecOne(r.d, `UPDATE server_info SET tmdb_key = NULLIF(?, ''), updated_at = ? WHERE id = 1`, key, db.Ms(time.Now().UTC()))
}

// Metadata sources and modes.
const (
	Local     = "local"
	Remote    = "remote"
	Only      = "only"
	Preferred = "preferred"
)

// Sources is the pair of metadata settings: Source is Local (the files'
// tags, art and sidecars) or Remote (TMDB, MusicBrainz, the Cover Art
// Archive, YouTube avatars); Mode Only uses no other, Preferred fills each
// field Source lacks from the other.
type Sources struct{ Source, Mode string }

// Default is every server's setting until an admin changes it.
var Default = Sources{Local, Only}

// UseRemote reports whether remote data is shown and fetched.
func (s Sources) UseRemote() bool { return s.Source == Remote || s.Mode == Preferred }

// Valid reports whether both settings are known values.
func (s Sources) Valid() bool {
	return (s.Source == Local || s.Source == Remote) && (s.Mode == Only || s.Mode == Preferred)
}

// Sources returns the metadata settings, Default before first run.
func (r *Repo) Sources() (Sources, error) {
	i, err := r.Get()
	switch {
	case errors.Is(err, db.ErrNotFound):
		return Default, nil
	case err != nil:
		return Sources{}, err
	}
	return i.Metadata, nil
}

// SetSources saves the metadata settings; db.ErrNotFound before first run.
func (r *Repo) SetSources(s Sources) error {
	return db.ExecOne(r.d, `UPDATE server_info SET metadata_source = ?, metadata_mode = ?, updated_at = ? WHERE id = 1`,
		s.Source, s.Mode, db.Ms(time.Now().UTC()))
}

// DefaultReadAhead is every server's read-ahead, in seconds, until an admin
// changes it.
const DefaultReadAhead = 300

// ReadAhead returns the read-ahead in seconds, DefaultReadAhead before
// first run.
func (r *Repo) ReadAhead() (int, error) {
	i, err := r.Get()
	switch {
	case errors.Is(err, db.ErrNotFound):
		return DefaultReadAhead, nil
	case err != nil:
		return 0, err
	}
	return i.ReadAheadSeconds, nil
}

// SetReadAhead saves the read-ahead in seconds, which the row keeps within
// 30 to 600; db.ErrNotFound before first run.
func (r *Repo) SetReadAhead(seconds int) error {
	return db.ExecOne(r.d, `UPDATE server_info SET read_ahead_seconds = ?, updated_at = ? WHERE id = 1`, seconds, db.Ms(time.Now().UTC()))
}

// Every server's transcode limits until an admin changes them.
const (
	DefaultMaxTranscodes          = 3
	DefaultTranscodeCacheCapBytes = 20_000_000_000
)

// TranscodeLimits returns the transcode limit and the cache cap in bytes,
// the defaults before first run.
func (r *Repo) TranscodeLimits() (int, int64, error) {
	i, err := r.Get()
	switch {
	case errors.Is(err, db.ErrNotFound):
		return DefaultMaxTranscodes, DefaultTranscodeCacheCapBytes, nil
	case err != nil:
		return 0, 0, err
	}
	return i.MaxTranscodes, i.TranscodeCacheCapBytes, nil
}

// SetTranscodeLimits saves the transcode limit and the cache cap in bytes,
// which the row keeps within 1 to 8 and 1 GB to 1 TB; db.ErrNotFound
// before first run.
func (r *Repo) SetTranscodeLimits(maxTranscodes int, cacheCapBytes int64) error {
	return db.ExecOne(r.d, `UPDATE server_info SET max_transcodes = ?, transcode_cache_cap_bytes = ?, updated_at = ? WHERE id = 1`,
		maxTranscodes, cacheCapBytes, db.Ms(time.Now().UTC()))
}

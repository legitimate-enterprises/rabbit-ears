package item

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// IdentityStamps maps each of the library's movies, shows and albums that
// has a remote row to that row's identity stamp: what the title's names and
// tags stated when its match was made from them or last agreed with them.
// "" is a match made by hand or before stamps were kept.
func (r *Repo) IdentityStamps(libraryID int64) (map[int64]string, error) {
	rows, err := r.d.Query(`SELECT m.item_id, COALESCE(m.identity_stamp, '') FROM item_metadata m JOIN item i ON i.id = m.item_id
		WHERE m.source = 'remote' AND i.library_id = ? AND i.kind IN (?, ?, ?)`, libraryID, Movie, Show, Album)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var stamp string
		if err := rows.Scan(&id, &stamp); err != nil {
			return nil, db.Wrap(err)
		}
		out[id] = stamp
	}
	return out, db.Wrap(rows.Err())
}

// IdentityStamp returns one item's stamp as IdentityStamps gives it, ""
// also when it has no remote row.
func (r *Repo) IdentityStamp(itemID int64) (string, error) {
	var stamp sql.NullString
	err := r.d.QueryRow(`SELECT identity_stamp FROM item_metadata WHERE item_id = ? AND source = 'remote'`, itemID).Scan(&stamp)
	if err == sql.ErrNoRows { //nolint:errorlint // database/sql returns it unwrapped
		return "", nil
	}
	return stamp.String, db.Wrap(err)
}

// SetIdentityStamp writes the stamp of an item's remote row, "" clearing
// it; an item without the row has nothing to stamp.
func (r *Repo) SetIdentityStamp(itemID int64, stamp string) error {
	_, err := r.d.Exec(`UPDATE item_metadata SET identity_stamp = ? WHERE item_id = ? AND source = 'remote'`, db.NullStr(stamp), itemID)
	return db.Wrap(err)
}

// MatchByHand stores m as the item's remote row, a match an admin made. It
// carries no identity stamp, so scans check it against the item's names
// and never replace it with another title; an album may take another
// edition of the release group it is. It reports whether m names another
// provider title than the item had: that title's details are then due at
// now, and a show's episodes lose the remote rows that described the other
// title's.
func (r *Repo) MatchByHand(m *Metadata, now time.Time) (rematched bool, err error) {
	err = db.InTx(r.d, func(d db.DBTX) error {
		tx := &Repo{d}
		prev, err := tx.Remote(m.ItemID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
		rematched = m.ProviderID != nil && (prev == nil || prev.ProviderID == nil || *prev.ProviderID != *m.ProviderID)
		if rematched {
			m.RetryAt = &now
			if err := tx.DeleteEpisodeMetadata(m.ItemID); err != nil {
				return err
			}
		}
		if err := tx.SetMetadata(m); err != nil {
			return err
		}
		return tx.SetIdentityStamp(m.ItemID, "")
	})
	return rematched, err
}

// HomeOf returns the show an episode file's titles are under, or the
// channel of a video file's; db.ErrNotFound for a file under neither.
func (r *Repo) HomeOf(fileID int64, kind string) (*Item, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM item WHERE kind = ?2 AND id IN (
		SELECT COALESCE((SELECT p.parent_id FROM item p WHERE p.id = i.parent_id), i.parent_id)
		FROM item_file x JOIN item i ON i.id = x.item_id WHERE x.media_file_id = ?1) ORDER BY id LIMIT 1`, fileID, kind))
}

// EpisodePaths lists the paths of the files on disk of a show's episodes.
func (r *Repo) EpisodePaths(showID int64) ([]string, error) {
	rows, err := r.d.Query(`SELECT DISTINCT f.path FROM item s JOIN item e ON e.parent_id = s.id
		JOIN item_file x ON x.item_id = e.id JOIN media_file f ON f.id = x.media_file_id
		WHERE s.parent_id = ? AND f.missing_at IS NULL ORDER BY f.path`, showID)
	return db.Collect(rows, err, db.Column[string])
}

// DeleteEpisodeMetadata drops the remote rows of a show's episodes, which
// describe the episodes of the title the show was matched to.
func (r *Repo) DeleteEpisodeMetadata(showID int64) error {
	_, err := r.d.Exec(`DELETE FROM item_metadata WHERE source = 'remote' AND item_id IN (
		SELECT e.id FROM item s JOIN item e ON e.parent_id = s.id WHERE s.parent_id = ?)`, showID)
	return db.Wrap(err)
}

// SongRelease is a song's path with the MusicBrainz release and release
// group its tags name.
type SongRelease struct {
	Path, Release, Group string
	// Read is whether the song's tags were read for the ids.
	Read bool
}

// SetSongRelease records the ids a song's tags name, "" for none.
func (r *Repo) SetSongRelease(songID int64, release, group string) error {
	_, err := r.d.Exec(`INSERT INTO song_release (item_id, release_id, release_group_id) VALUES (?1, ?2, ?3)
		ON CONFLICT(item_id) DO UPDATE SET release_id = ?2, release_group_id = ?3`, songID, db.NullStr(release), db.NullStr(group))
	return db.Wrap(err)
}

// SongReleaseRead reports whether a song's tags were read for the ids.
func (r *Repo) SongReleaseRead(songID int64) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM song_release WHERE item_id = ?`, songID).Scan(&n)
	return n > 0, db.Wrap(err)
}

// AlbumReleases lists an album's songs with the ids their tags name.
func (r *Repo) AlbumReleases(albumID int64) ([]SongRelease, error) {
	rows, err := r.d.Query(`SELECT COALESCE(s.path, ''), COALESCE(t.release_id, ''), COALESCE(t.release_group_id, ''), t.item_id IS NOT NULL
		FROM item s LEFT JOIN song_release t ON t.item_id = s.id WHERE s.parent_id = ? AND s.kind = 'song' ORDER BY s.path`, albumID)
	return db.Collect(rows, err, func(s db.Scanner) (SongRelease, error) {
		var x SongRelease
		return x, db.Wrap(s.Scan(&x.Path, &x.Release, &x.Group, &x.Read))
	})
}

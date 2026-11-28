// Package favorite holds the items each profile starred and its
// repository.
package favorite

import (
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Kinds are the kinds of item a profile favorites.
var Kinds = []string{item.Movie, item.Show, item.Season, item.Episode, item.Artist, item.Album, item.Song, item.Video}

// Takes reports whether an item of kind may be a favorite.
func Takes(kind string) bool { return slices.Contains(Kinds, kind) }

// Favorite is one item an account starred, and when.
type Favorite struct {
	AccountID uid.ID
	ItemID    uid.ID
	Kind      string
	CreatedAt time.Time
}

// Repo persists favorites.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// Put stars the item as of instant at; one already starred keeps the
// instant it was first starred at.
func (r *Repo) Put(accountID, itemID uid.ID, at time.Time) error {
	_, err := r.d.Exec(`INSERT INTO favorite (account_id, item_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (account_id, item_id) DO NOTHING`, accountID, itemID, db.Ms(at))
	return db.Wrap(err)
}

// Delete unstars the item; with at, only a star made no later than at.
// Unstarring what is not starred is no error.
func (r *Repo) Delete(accountID, itemID uid.ID, at *time.Time) error {
	var until *int64
	if at != nil {
		ms := db.Ms(*at)
		until = &ms
	}
	_, err := r.d.Exec(`DELETE FROM favorite WHERE account_id = ? AND item_id = ? AND (?3 IS NULL OR created_at <= ?3)`,
		accountID, itemID, until)
	return db.Wrap(err)
}

// List pages an account's favorites the audience sees, of kind unless it
// is empty, most recently starred first.
func (r *Repo) List(accountID uid.ID, kind string, a item.Audience, limit, offset int) ([]*Favorite, error) {
	rows, err := r.d.Query(`SELECT f.account_id, f.item_id, item.kind, f.created_at
		FROM favorite f JOIN item ON item.id = f.item_id
		WHERE f.account_id = ?1 AND (?2 = '' OR item.kind = ?2) AND `+item.VisibleWhere+`
		ORDER BY f.created_at DESC, f.item_id LIMIT ?4 OFFSET ?5`,
		append([]any{accountID, kind, nil, limit, offset}, a.Args()...)...)
	return db.Collect(rows, err, func(s db.Scanner) (*Favorite, error) {
		var f Favorite
		var at int64
		if err := s.Scan(&f.AccountID, &f.ItemID, &f.Kind, &at); err != nil {
			return nil, db.Wrap(err)
		}
		f.CreatedAt = db.FromMs(at)
		return &f, nil
	})
}

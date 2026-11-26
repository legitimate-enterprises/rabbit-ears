// Package review holds the identity reviews a scan raises: the titles, and
// the files no title could be made for, whose identity it would not decide
// alone, each with the evidence an admin needs to decide it.
package review

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Reasons a review is raised for.
const (
	// ConflictingIdentity: the ids a title's names and tags state name
	// different titles, or another title than its match.
	ConflictingIdentity = "conflicting_identity"
	// InvalidIdentity: a stated id is malformed or unknown to its provider.
	InvalidIdentity = "invalid_identity"
	// TiedCandidates: several provider titles fit the name equally well.
	TiedCandidates = "tied_candidates"
	// ConflictingYears: a show's episode files name different premiere years.
	ConflictingYears = "conflicting_years"
	// AmbiguousMove: a file gone from disk looks like several new ones, or
	// several gone files like one new one.
	AmbiguousMove = "ambiguous_move"
	// NoMatch: the provider has no title of this name.
	NoMatch = "no_match"
	// Unrecognized: a file whose name places it under no title.
	Unrecognized = "unrecognized"
)

// OfIdentity are the reasons that question which title an item is; a
// match made by hand answers them.
var OfIdentity = []string{ConflictingIdentity, InvalidIdentity, TiedCandidates, ConflictingYears, NoMatch}

// Reasons lists every reason.
var Reasons = []string{ConflictingIdentity, InvalidIdentity, TiedCandidates, ConflictingYears, AmbiguousMove, NoMatch, Unrecognized}

// Evidence roles.
const (
	Current   = "current"   // the match the title has
	Stated    = "stated"    // what a name or tag states
	Candidate = "candidate" // a title or file it could be
)

// Review is one title, or one file without a title, that needs an admin.
type Review struct {
	ID        int64
	LibraryID int64
	// ItemID is the title; nil for a file no title was made for, which
	// Path then names.
	ItemID      *int64
	Path        string
	Reason      string
	RaisedAt    time.Time
	DismissedAt *time.Time
	Evidence    []Evidence
	// ItemKind and Title describe the title on every read.
	ItemKind, Title string
}

// Evidence is one fact a review rests on. Only the fields that apply are
// set: a provider id, what the provider calls it, or the path stating it.
type Evidence struct {
	Role       string
	Provider   string // tmdb, tvdb, imdb or musicbrainz
	ExternalID string
	Title      string
	Year       *int
	Country    string
	Path       string
}

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	LibraryID int64
	Reason    string
	// Dismissed keeps only dismissed reviews, or only open ones; nil keeps both.
	Dismissed *bool
}

// Repo persists reviews and their evidence.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `r.id, r.library_id, r.item_id, r.path, r.reason, r.raised_at, r.dismissed_at, COALESCE(i.kind, ''),
	COALESCE((SELECT m.title FROM item_shown m WHERE m.item_id = i.id), i.parsed_title, '')`

const from = ` FROM identity_review r LEFT JOIN item i ON i.id = r.item_id `

func scan(s db.Scanner) (*Review, error) {
	var r Review
	var item, dismissed sql.NullInt64
	var path sql.NullString
	var raised int64
	if err := s.Scan(&r.ID, &r.LibraryID, &item, &path, &r.Reason, &raised, &dismissed, &r.ItemKind, &r.Title); err != nil {
		return nil, db.Wrap(err)
	}
	if item.Valid {
		r.ItemID = &item.Int64
	}
	r.Path, r.RaisedAt, r.DismissedAt = path.String, db.FromMs(raised), db.FromNullMs(dismissed)
	return &r, nil
}

func (r *Repo) evidence(rv *Review) error {
	rows, err := r.d.Query(`SELECT role, provider, external_id, title, year, country, path FROM identity_evidence
		WHERE review_id = ? ORDER BY position`, rv.ID)
	rv.Evidence, err = db.Collect(rows, err, func(s db.Scanner) (Evidence, error) {
		var e Evidence
		var provider, external, title, country, path sql.NullString
		var year sql.NullInt64
		err := s.Scan(&e.Role, &provider, &external, &title, &year, &country, &path)
		e.Provider, e.ExternalID, e.Title, e.Country, e.Path = provider.String, external.String, title.String, country.String, path.String
		if year.Valid {
			y := int(year.Int64)
			e.Year = &y
		}
		return e, db.Wrap(err)
	})
	return err
}

// Get returns one review with its evidence, or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Review, error) {
	rv, err := scan(r.d.QueryRow(`SELECT `+cols+from+`WHERE r.id = ?`, id))
	if err != nil {
		return nil, err
	}
	return rv, r.evidence(rv)
}

// kept is the condition for the reviews a Filter keeps, its fields in order.
const kept = `WHERE (?1 = 0 OR r.library_id = ?1) AND (?2 = '' OR r.reason = ?2) AND (?3 IS NULL OR (r.dismissed_at IS NOT NULL) = ?3)`

// List returns the reviews f keeps with their evidence, newest first.
func (r *Repo) List(f Filter) ([]*Review, error) {
	rows, err := r.d.Query(`SELECT `+cols+from+kept+` ORDER BY r.raised_at DESC, r.id DESC`, f.LibraryID, f.Reason, f.Dismissed)
	out, err := db.Collect(rows, err, scan)
	for _, rv := range out {
		if err == nil {
			err = r.evidence(rv)
		}
	}
	return out, err
}

// Count counts the reviews List returns.
func (r *Repo) Count(f Filter) (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*)`+from+kept, f.LibraryID, f.Reason, f.Dismissed).Scan(&n)
	return n, db.Wrap(err)
}

// SetDismissed dismisses a review at at, or with nil opens it again.
func (r *Repo) SetDismissed(id int64, at *time.Time) error {
	return db.ExecOne(r.d, `UPDATE identity_review SET dismissed_at = ? WHERE id = ?`, db.NullMs(at), id)
}

// Raise records rv, of an item or else of a path, with its evidence. A
// review of the same subject and reason is kept, with when it was raised
// and whether it was dismissed, and takes the new evidence.
func (r *Repo) Raise(rv *Review) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		err := d.QueryRow(`SELECT id FROM identity_review WHERE reason = ?1 AND (item_id = ?2 OR ?2 IS NULL AND item_id IS NULL
			AND library_id = ?3 AND path = ?4)`, rv.Reason, rv.ItemID, rv.LibraryID, rv.Path).Scan(&rv.ID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			var path any
			if rv.ItemID == nil {
				path = rv.Path
			}
			res, err := d.Exec(`INSERT INTO identity_review (library_id, item_id, path, reason, raised_at) VALUES (?,?,?,?,?)`,
				rv.LibraryID, rv.ItemID, path, rv.Reason, db.Ms(rv.RaisedAt))
			if err != nil {
				return db.Wrap(err)
			}
			rv.ID, _ = res.LastInsertId()
		case err != nil:
			return db.Wrap(err)
		}
		if _, err := d.Exec(`DELETE FROM identity_evidence WHERE review_id = ?`, rv.ID); err != nil {
			return db.Wrap(err)
		}
		for i, e := range rv.Evidence {
			if _, err := d.Exec(`INSERT INTO identity_evidence (review_id, position, role, provider, external_id, title, year, country, path)
				VALUES (?,?,?,?,?,?,?,?,?)`, rv.ID, i, e.Role, db.NullStr(e.Provider), db.NullStr(e.ExternalID), db.NullStr(e.Title),
				e.Year, db.NullStr(e.Country), db.NullStr(e.Path)); err != nil {
				return db.Wrap(err)
			}
		}
		return nil
	})
}

// Settle deletes the item's reviews of these reasons but the one of
// reason keep; "" keeps none.
func (r *Repo) Settle(itemID int64, keep string, reasons ...string) error {
	for _, reason := range reasons {
		if reason == keep {
			continue
		}
		if _, err := r.d.Exec(`DELETE FROM identity_review WHERE item_id = ? AND reason = ?`, itemID, reason); err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

// Sweep deletes the library's reviews of reason that are not among keep.
func (r *Repo) Sweep(libraryID int64, reason string, keep []int64) error {
	in, args := db.In(keep)
	_, err := r.d.Exec(`DELETE FROM identity_review WHERE library_id = ? AND reason = ? AND id NOT IN `+in,
		append([]any{libraryID, reason}, args...)...)
	return db.Wrap(err)
}

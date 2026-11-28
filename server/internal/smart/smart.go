// Package smart holds the rules of smart playlists and collections, and
// applies them: it writes the titles they match into the list's entries or
// movies, so everything else reads a smart list as any other.
package smart

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Collection is the kind of list a custom collection is, beside a
// playlist's kinds.
const Collection = "collection"

// Matches: All needs every part to match, Any one.
const (
	All = "all"
	Any = "any"
)

// Limits on a list's rules.
const (
	MaxGroups     = 20
	MaxConditions = 50
	MaxText       = 200
)

// Rules are what a smart list holds: the titles matching Match of Groups.
type Rules struct {
	Match  string
	Groups []Group
}

// Group matches Match of its Conditions.
type Group struct {
	Match      string
	Conditions []Condition
}

// Condition tests one field. Text is set for a text operator, Number for a
// number operator, neither for isTrue and isFalse.
type Condition struct {
	Field    string
	Operator string
	Text     *string
	Number   *int64
}

// Invalid is a rule that cannot stand, naming where it is as the API's
// field paths do: "groups", "groups.0.conditions.2.text".
type Invalid struct{ Field, Message string }

func (e *Invalid) Error() string { return e.Field + ": " + e.Message }

// operators by the field type they take.
var operators = map[int][]string{
	text:    {"is", "isNot", "contains", "notContains", "startsWith", "endsWith"},
	number:  {"is", "isNot", "lt", "le", "gt", "ge"},
	boolean: {"isTrue", "isFalse"},
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Validate checks r for a list of kind and trims its texts. It returns an
// *Invalid naming the first fault.
func Validate(kind string, r *Rules) error {
	fields, ok := catalog[kind]
	if !ok {
		return fmt.Errorf("no smart lists of kind %q", kind)
	}
	if r.Match != All && r.Match != Any {
		return &Invalid{"match", "must be all or any"}
	}
	if len(r.Groups) == 0 || len(r.Groups) > MaxGroups {
		return &Invalid{"groups", "from 1 to " + strconv.Itoa(MaxGroups) + " groups"}
	}
	n := 0
	for i := range r.Groups {
		g := &r.Groups[i]
		at := "groups." + strconv.Itoa(i)
		if g.Match != All && g.Match != Any {
			return &Invalid{at + ".match", "must be all or any"}
		}
		if len(g.Conditions) == 0 {
			return &Invalid{at + ".conditions", "at least one condition"}
		}
		for j := range g.Conditions {
			c := &g.Conditions[j]
			at := at + ".conditions." + strconv.Itoa(j)
			if n++; n > MaxConditions {
				return &Invalid{"groups", "at most " + strconv.Itoa(MaxConditions) + " conditions in all"}
			}
			f, ok := fields[c.Field]
			if !ok {
				what := "a " + kind + " playlist"
				if kind == Collection {
					what = "a collection"
				}
				return &Invalid{at + ".field", "not a field of " + what}
			}
			if !has(operators[f.typ], c.Operator) {
				return &Invalid{at + ".operator", "not an operator of " + c.Field}
			}
			switch f.typ {
			case text:
				if c.Number != nil {
					return &Invalid{at + ".number", c.Field + " takes text"}
				}
				if c.Text == nil || strings.TrimSpace(*c.Text) == "" {
					return &Invalid{at + ".text", "must not be blank"}
				}
				t := strings.TrimSpace(*c.Text)
				if len([]rune(t)) > MaxText {
					return &Invalid{at + ".text", "at most " + strconv.Itoa(MaxText) + " characters"}
				}
				c.Text = &t
			case number:
				if c.Text != nil {
					return &Invalid{at + ".text", c.Field + " takes a number"}
				}
				if c.Number == nil {
					return &Invalid{at + ".number", "a number is needed"}
				}
			case boolean:
				if c.Text != nil {
					return &Invalid{at + ".text", c.Operator + " takes no value"}
				}
				if c.Number != nil {
					return &Invalid{at + ".number", c.Operator + " takes no value"}
				}
			}
		}
	}
	return nil
}

// positive is the test of operator op on value x against the bound value v,
// for an operator and its negation alike; negated reports which.
func positive(typ int, op, x, v string) (test string, negated bool) {
	if typ == text {
		x, v = `lower(`+x+`)`, `lower(`+v+`)`
	}
	switch op {
	case "is":
		return x + ` = ` + v, false
	case "isNot":
		return x + ` = ` + v, true
	case "contains":
		return `instr(` + x + `, ` + v + `) > 0`, false
	case "notContains":
		return `instr(` + x + `, ` + v + `) > 0`, true
	case "startsWith":
		return `instr(` + x + `, ` + v + `) = 1`, false
	case "endsWith":
		return `substr(` + x + `, -length(` + v + `)) = ` + v, false
	case "lt":
		return x + ` < ` + v, false
	case "le":
		return x + ` <= ` + v, false
	case "gt":
		return x + ` > ` + v, false
	case "ge":
		return x + ` >= ` + v, false
	case "isTrue":
		return `COALESCE(` + x + `, 0) = 1`, false
	}
	return `COALESCE(` + x + `, 0) = 0`, false
}

// compile is the query of the titles a list of kind holds under r, which
// Validate passed, with its arguments after the owner's at ?1. Every field
// and operator comes from the catalog and every value is bound, so nothing
// a profile wrote reaches the SQL but as a parameter.
func compile(kind string, r Rules) (string, []any) {
	fields := catalog[kind]
	var args []any
	bind := func(v any) string {
		args = append(args, v)
		return "?" + strconv.Itoa(len(args)+1)
	}
	join := map[string]string{All: " AND ", Any: " OR "}
	groups := make([]string, len(r.Groups))
	for i, g := range r.Groups {
		conds := make([]string, len(g.Conditions))
		for j, c := range g.Conditions {
			f := fields[c.Field]
			v := ""
			switch {
			case c.Text != nil:
				v = bind(*c.Text)
			case c.Number != nil:
				v = bind(*c.Number)
			}
			test, negated := positive(f.typ, c.Operator, f.expr, v)
			switch {
			case f.from != "" && negated:
				conds[j] = `NOT EXISTS (SELECT 1 ` + f.from + ` AND ` + test + `)`
			case f.from != "":
				conds[j] = `EXISTS (SELECT 1 ` + f.from + ` AND ` + test + `)`
			case negated:
				conds[j] = `NOT COALESCE(` + test + `, 0)`
			default:
				conds[j] = `COALESCE(` + test + `, 0)`
			}
		}
		groups[i] = `(` + strings.Join(conds, join[g.Match]) + `)`
	}
	return fmt.Sprintf(leaves[kind], strings.Join(groups, join[r.Match])), args
}

// Service stores and applies the rules.
type Service struct {
	d db.DBTX
	// Events, when set, hears of each playlist a run changed.
	Events *live.Hub
}

// New returns a Service over d.
func New(d db.DBTX) *Service { return &Service{d: d} }

// list is one smart list as a run applies it.
type list struct {
	id                       uid.ID
	playlistID, collectionID *uid.ID
	kind                     string
	owner                    *uid.ID
}

// listOf reads smart lists with the kind each holds and, for a playlist,
// its owner.
const listOf = `SELECT l.id, l.playlist_id, l.collection_id, COALESCE(p.kind, 'collection'), p.account_id
	FROM smart_list l LEFT JOIN playlist p ON p.id = l.playlist_id`

func scanList(s db.Scanner) (list, error) {
	var l list
	return l, db.Wrap(s.Scan(&l.id, &l.playlistID, &l.collectionID, &l.kind, &l.owner))
}

// Rules returns the rules of the playlist, or else of the collection, or
// db.ErrNotFound when it has none.
func (s *Service) Rules(playlistID, collectionID *uid.ID) (*Rules, error) {
	var id uid.ID
	r := &Rules{}
	err := s.d.QueryRow(`SELECT id, match FROM smart_list WHERE playlist_id = ? OR collection_id = ?`, playlistID, collectionID).Scan(&id, &r.Match)
	if err != nil {
		return nil, db.Wrap(err)
	}
	rows, err := s.d.Query(`SELECT g.position, g.match, c.field, c.operator, c.text_value, c.number_value FROM smart_group g
		JOIN smart_condition c ON c.group_id = g.id WHERE g.list_id = ? ORDER BY g.position, c.position`, id)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	last := -1
	for rows.Next() {
		var pos int
		var match string
		var c Condition
		var t sql.NullString
		var n sql.NullInt64
		if err := rows.Scan(&pos, &match, &c.Field, &c.Operator, &t, &n); err != nil {
			return nil, db.Wrap(err)
		}
		if t.Valid {
			c.Text = &t.String
		}
		if n.Valid {
			c.Number = &n.Int64
		}
		if pos != last {
			r.Groups, last = append(r.Groups, Group{Match: match}), pos
		}
		g := &r.Groups[len(r.Groups)-1]
		g.Conditions = append(g.Conditions, c)
	}
	return r, db.Wrap(rows.Err())
}

// PutPlaylist validates r for p, stores it as p's rules, replacing any, and
// applies them, all or nothing.
func (s *Service) PutPlaylist(p *playlist.Playlist, r *Rules, by *uid.ID) error {
	return s.put(list{playlistID: &p.ID, kind: p.Kind, owner: &p.AccountID}, r, by)
}

// PutCollection is PutPlaylist for a custom collection.
func (s *Service) PutCollection(c *collection.Collection, r *Rules, by *uid.ID) error {
	return s.put(list{collectionID: &c.ID, kind: Collection}, r, by)
}

func (s *Service) put(l list, r *Rules, by *uid.ID) error {
	if err := Validate(l.kind, r); err != nil {
		return err
	}
	return db.InTx(s.d, func(d db.DBTX) error {
		now := db.Ms(time.Now())
		err := d.QueryRow(`SELECT id FROM smart_list WHERE playlist_id = ? OR collection_id = ?`, l.playlistID, l.collectionID).Scan(&l.id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			l.id = uid.New()
			_, err = d.Exec(`INSERT INTO smart_list (id, playlist_id, collection_id, match, created_at, modified_at, created_by, modified_by)
				VALUES (?,?,?,?,?,?,?,?)`, l.id, l.playlistID, l.collectionID, r.Match, now, now, by, by)
		case err == nil:
			if _, err = d.Exec(`UPDATE smart_list SET match = ?, modified_at = ?, modified_by = ? WHERE id = ?`, r.Match, now, by, l.id); err == nil {
				_, err = d.Exec(`DELETE FROM smart_group WHERE list_id = ?`, l.id)
			}
		}
		if err != nil {
			return db.Wrap(err)
		}
		for i, g := range r.Groups {
			gid := uid.New()
			if _, err := d.Exec(`INSERT INTO smart_group (id, list_id, position, match, created_at, modified_at) VALUES (?,?,?,?,?,?)`,
				gid, l.id, i, g.Match, now, now); err != nil {
				return db.Wrap(err)
			}
			for j, c := range g.Conditions {
				if _, err := d.Exec(`INSERT INTO smart_condition (id, group_id, position, field, operator, text_value, number_value, created_at,
					modified_at) VALUES (?,?,?,?,?,?,?,?,?)`, uid.New(), gid, j, c.Field, c.Operator, c.Text, c.Number, now, now); err != nil {
					return db.Wrap(err)
				}
			}
		}
		_, err = apply(d, l, *r)
		return err
	})
}

// apply writes the titles r matches into l, reporting whether it changed.
func apply(d db.DBTX, l list, r Rules) (bool, error) {
	q, args := compile(l.kind, r)
	rows, err := d.Query(q, append([]any{l.owner}, args...)...)
	ids, err := db.Collect(rows, err, db.Column[uid.ID])
	if err != nil {
		return false, err
	}
	if l.playlistID != nil {
		return playlist.New(d).SetItems(*l.playlistID, ids)
	}
	return collection.New(d).SetMovies(*l.collectionID, ids)
}

// Run applies every smart list's rules again, each list alone, and tells
// those who see a playlist it changed. A list that fails keeps what it
// held; the run goes on and fails naming them.
func (s *Service) Run(ctx context.Context) (string, error) {
	rows, err := s.d.Query(listOf + ` ORDER BY l.id`)
	lists, err := db.Collect(rows, err, scanList)
	if err != nil {
		return "", err
	}
	var failed []error
	changed := 0
	for _, l := range lists {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r, err := s.Rules(l.playlistID, l.collectionID)
		var moved bool
		if err == nil {
			err = Validate(l.kind, r)
		}
		if err == nil {
			err = db.InTx(s.d, func(d db.DBTX) error {
				var err error
				moved, err = apply(d, l, *r)
				return err
			})
		}
		if err != nil {
			failed = append(failed, fmt.Errorf("list %s: %w", l.id, err))
			continue
		}
		if !moved {
			continue
		}
		changed++
		if s.Events != nil && l.playlistID != nil {
			if to, err := playlist.New(s.d).Audience(*l.playlistID); err == nil && len(to) > 0 {
				s.Events.Publish(live.Event{Kind: live.Playlist, PlaylistID: l.playlistID, AccountIDs: to})
			}
		}
	}
	sum := fmt.Sprintf("%d of %d smart lists changed", changed, len(lists))
	if len(failed) > 0 {
		return sum, fmt.Errorf("%d smart lists failed: %w", len(failed), errors.Join(failed...))
	}
	return sum, nil
}

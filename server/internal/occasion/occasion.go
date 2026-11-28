// Package occasion holds occasions: seasonal labels, each with a window of
// days around an anchor date and the movies and shows that surface in it.
package occasion

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Occasion is anchored to a Holiday key, or to Month and Day when Holiday
// is empty. Its window runs from DaysBefore before the anchor to DaysAfter
// after it. Keywords are TMDB keyword ids. Phrases title its Home row in
// place of Name; Colors are none or three #RRGGBB, washed behind its
// screens as Wash says.
type Occasion struct {
	ID                    uid.ID
	CreatedAt, ModifiedAt time.Time
	CreatedBy, ModifiedBy *uid.ID
	Name                  string
	Holiday               string
	Month, Day            int
	DaysBefore, DaysAfter int
	Keywords              []int64
	Phrases               []string
	Colors                []string
	// Wash is always read; written nil, a create takes the schema's
	// defaults and an update keeps what is stored.
	Wash *Wash
}

// Wash is how the colors are drawn, each in percent: the glows' strength,
// how far each color is brightened toward white, how much of a glow's
// radius is at full strength, and the glows' radius.
type Wash struct {
	Strength, Brightness, Sharpness, Reach int
}

// MaxDays bounds DaysBefore and DaysAfter, so a date is in at most one
// year's window and On need look only one year either side.
const MaxDays = 90

// Holiday is one of the server's built-in holidays, its date in each year
// the table covers, and the colors an occasion anchored to it starts with.
type Holiday struct {
	Key    string
	Name   string
	Colors []string
	Dates  []time.Time
}

//go:embed holidays.json
var holidaysJSON []byte

var holidays = func() []Holiday {
	var raw []struct {
		Key    string   `json:"key"`
		Name   string   `json:"name"`
		Colors []string `json:"colors"`
		Dates  []string `json:"dates"`
	}
	if err := json.Unmarshal(holidaysJSON, &raw); err != nil {
		panic(err)
	}
	out := make([]Holiday, len(raw))
	for i, h := range raw {
		out[i] = Holiday{Key: h.Key, Name: h.Name, Colors: h.Colors}
		for _, s := range h.Dates {
			d, err := time.Parse(time.DateOnly, s)
			if err != nil {
				panic(err)
			}
			out[i].Dates = append(out[i].Dates, d)
		}
	}
	return out
}()

//go:embed phrases.json
var phrasesJSON []byte

var builtInPhrases = func() map[string][]string {
	var out map[string][]string
	if err := json.Unmarshal(phrasesJSON, &out); err != nil {
		panic(err)
	}
	return out
}()

// BuiltInPhrases are the phrases an occasion anchored to the holiday key
// starts with; nil for none.
func BuiltInPhrases(key string) []string { return slices.Clone(builtInPhrases[key]) }

// Holidays lists the built-in holidays in calendar order.
func Holidays() []Holiday { return holidays }

// HolidayByKey returns the built-in holiday with key.
func HolidayByKey(key string) (Holiday, bool) {
	for _, h := range holidays {
		if h.Key == key {
			return h, true
		}
	}
	return Holiday{}, false
}

// AnchorIn is the occasion's date in year, at midnight UTC; false when it has
// none: a holiday the table has no date for that year, or Feb 29 outside a
// leap year.
func (o *Occasion) AnchorIn(year int) (time.Time, bool) {
	if o.Holiday == "" {
		t := time.Date(year, time.Month(o.Month), o.Day, 0, 0, 0, 0, time.UTC)
		return t, t.Day() == o.Day
	}
	h, _ := HolidayByKey(o.Holiday)
	for _, d := range h.Dates {
		if d.Year() == year {
			return d, true
		}
	}
	return time.Time{}, false
}

// Activation is one year's window of an occasion: its anchor, first and
// last days, and how many days a date is from the anchor.
type Activation struct {
	Anchor, First, Last time.Time
	Distance            int
}

// On returns the window containing the calendar date d, given at midnight
// UTC, if any; a window may start or end in the year next to its anchor's.
func (o *Occasion) On(d time.Time) (Activation, bool) {
	for y := d.Year() - 1; y <= d.Year()+1; y++ {
		a, ok := o.AnchorIn(y)
		if !ok {
			continue
		}
		first, last := a.AddDate(0, 0, -o.DaysBefore), a.AddDate(0, 0, o.DaysAfter)
		if !d.Before(first) && !d.After(last) {
			return Activation{Anchor: a, First: first, Last: last, Distance: int(max(d.Sub(a), a.Sub(d)).Hours()) / 24}, true
		}
	}
	return Activation{}, false
}

// Repo persists occasions, their keywords, phrases and hand-picked members.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const (
	washCols = `wash_strength, wash_brightness, wash_sharpness, wash_reach`
	cols     = `id, name, holiday, month, day, days_before, days_after, color1, color2, color3, created_at, modified_at, created_by, modified_by, ` +
		washCols
)

func scan(s db.Scanner) (*Occasion, error) {
	o := Occasion{Wash: &Wash{}}
	var holiday, c1, c2, c3 sql.NullString
	var month, day sql.NullInt64
	var created, modified int64
	if err := s.Scan(&o.ID, &o.Name, &holiday, &month, &day, &o.DaysBefore, &o.DaysAfter, &c1, &c2, &c3,
		&created, &modified, &o.CreatedBy, &o.ModifiedBy, &o.Wash.Strength, &o.Wash.Brightness, &o.Wash.Sharpness, &o.Wash.Reach); err != nil {
		return nil, db.Wrap(err)
	}
	o.CreatedAt, o.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	o.Holiday, o.Month, o.Day = holiday.String, int(month.Int64), int(day.Int64)
	if c1.Valid {
		o.Colors = []string{c1.String, c2.String, c3.String}
	}
	return &o, nil
}

// fields binds o's columns after id in cols order, a color NULL unless o
// has three.
func fields(o *Occasion) []any {
	c := []any{nil, nil, nil}
	if len(o.Colors) == 3 {
		c = []any{o.Colors[0], o.Colors[1], o.Colors[2]}
	}
	return append([]any{o.Name, db.NullStr(o.Holiday), nullZero(o.Month), nullZero(o.Day), o.DaysBefore, o.DaysAfter}, c...)
}

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// Create inserts o with its keywords and phrases, filling a zero ID and the
// audit fields.
func (r *Repo) Create(o *Occasion, by *uid.ID) error {
	if o.ID.IsZero() {
		o.ID = uid.New()
	}
	o.CreatedAt = now()
	o.ModifiedAt, o.CreatedBy, o.ModifiedBy = o.CreatedAt, by, by
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`INSERT INTO occasion (name, holiday, month, day, days_before, days_after, color1, color2, color3,
			id, created_at, modified_at, created_by, modified_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append(fields(o), o.ID, db.Ms(o.CreatedAt), db.Ms(o.ModifiedAt), by, by)...); err != nil {
			return db.Wrap(err)
		}
		if err := setWash(tx, o); err != nil {
			return err
		}
		return setLists(tx, o)
	})
}

// setWash writes o's wash, or reads the stored one into o when it has none.
func setWash(tx db.DBTX, o *Occasion) error {
	if o.Wash == nil {
		o.Wash = &Wash{}
		return db.Wrap(tx.QueryRow(`SELECT `+washCols+` FROM occasion WHERE id = ?`, o.ID).
			Scan(&o.Wash.Strength, &o.Wash.Brightness, &o.Wash.Sharpness, &o.Wash.Reach))
	}
	_, err := tx.Exec(`UPDATE occasion SET wash_strength = ?, wash_brightness = ?, wash_sharpness = ?, wash_reach = ? WHERE id = ?`,
		o.Wash.Strength, o.Wash.Brightness, o.Wash.Sharpness, o.Wash.Reach, o.ID)
	return db.Wrap(err)
}

// Update rewrites an occasion and replaces its keywords and phrases.
func (r *Repo) Update(o *Occasion, by *uid.ID) error {
	o.ModifiedAt, o.ModifiedBy = now(), by
	return db.InTx(r.d, func(tx db.DBTX) error {
		if err := db.ExecOne(tx, `UPDATE occasion SET name = ?, holiday = ?, month = ?, day = ?, days_before = ?, days_after = ?,
			color1 = ?, color2 = ?, color3 = ?, modified_at = ?, modified_by = ? WHERE id = ?`,
			append(fields(o), db.Ms(o.ModifiedAt), by, o.ID)...); err != nil {
			return err
		}
		if err := setWash(tx, o); err != nil {
			return err
		}
		return setLists(tx, o)
	})
}

// SeedBuiltIns gives each holiday occasion the schema seeds waiting in
// occasion_seed its holiday's built-in phrases and colors, once.
func (r *Repo) SeedBuiltIns() error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		rows, err := tx.Query(`SELECT ` + cols + ` FROM occasion WHERE id IN (SELECT occasion_id FROM occasion_seed)`)
		waiting, err := db.Collect(rows, err, scan)
		if err != nil {
			return err
		}
		for _, o := range waiting {
			h, _ := HolidayByKey(o.Holiday)
			o.Colors, o.Phrases = h.Colors, BuiltInPhrases(o.Holiday)
			if err := db.ExecOne(tx, `UPDATE occasion SET color1 = ?, color2 = ?, color3 = ?, modified_at = ?, modified_by = NULL WHERE id = ?`,
				append(fields(o)[6:], db.Ms(now()), o.ID)...); err != nil {
				return err
			}
			if err := setPhrases(tx, o); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`DELETE FROM occasion_seed`)
		return db.Wrap(err)
	})
}

func nullZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func setLists(tx db.DBTX, o *Occasion) error {
	if err := setKeywords(tx, o); err != nil {
		return err
	}
	return setPhrases(tx, o)
}

// setPhrases writes the phrases in order, so rowid order is theirs; a
// phrase equal to an earlier one ignoring case is dropped.
func setPhrases(tx db.DBTX, o *Occasion) error {
	if _, err := tx.Exec(`DELETE FROM occasion_phrase WHERE occasion_id = ?`, o.ID); err != nil {
		return db.Wrap(err)
	}
	for _, p := range o.Phrases {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO occasion_phrase (occasion_id, phrase) VALUES (?, ?)`, o.ID, p); err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

func setKeywords(tx db.DBTX, o *Occasion) error {
	if _, err := tx.Exec(`DELETE FROM occasion_keyword WHERE occasion_id = ?`, o.ID); err != nil {
		return db.Wrap(err)
	}
	for _, k := range o.Keywords {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO occasion_keyword (occasion_id, keyword_id) VALUES (?, ?)`, o.ID, k); err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

// Delete removes an occasion with its keywords, phrases and hand-picked
// members.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM occasion WHERE id = ?`, id)
}

// Get returns one occasion with its keywords and phrases, or
// db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Occasion, error) {
	o, err := scan(r.d.QueryRow(`SELECT `+cols+` FROM occasion WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return o, r.withLists([]*Occasion{o})
}

// List pages occasions with their keywords and phrases by name; a negative
// limit is none.
func (r *Repo) List(limit, offset int) ([]*Occasion, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM occasion ORDER BY name, id LIMIT ? OFFSET ?`, limit, offset)
	os, err := db.Collect(rows, err, scan)
	if err != nil {
		return nil, err
	}
	return os, r.withLists(os)
}

func (r *Repo) withLists(os []*Occasion) error {
	if len(os) == 0 {
		return nil
	}
	byID := map[uid.ID]*Occasion{}
	ids := make([]uid.ID, len(os))
	for i, o := range os {
		byID[o.ID], ids[i], o.Keywords, o.Phrases = o, o.ID, nil, nil
	}
	in, args := db.In(ids)
	phrases, err := r.d.Query(`SELECT occasion_id, phrase FROM occasion_phrase WHERE occasion_id IN `+in+` ORDER BY rowid`, args...)
	if err != nil {
		return db.Wrap(err)
	}
	defer func() { _ = phrases.Close() }()
	for phrases.Next() {
		var id uid.ID
		var p string
		if err := phrases.Scan(&id, &p); err != nil {
			return db.Wrap(err)
		}
		byID[id].Phrases = append(byID[id].Phrases, p)
	}
	if err := phrases.Err(); err != nil {
		return err
	}
	rows, err := r.d.Query(`SELECT occasion_id, keyword_id FROM occasion_keyword WHERE occasion_id IN `+in+` ORDER BY keyword_id`, args...)
	if err != nil {
		return db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uid.ID
		var k int64
		if err := rows.Scan(&id, &k); err != nil {
			return db.Wrap(err)
		}
		byID[id].Keywords = append(byID[id].Keywords, k)
	}
	return rows.Err()
}

// AddMember adds a movie or show to an occasion by hand, clearing any hand
// removal of it.
func (r *Repo) AddMember(occasionID, itemID uid.ID) error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`DELETE FROM occasion_exclusion WHERE occasion_id = ? AND item_id = ?`, occasionID, itemID); err != nil {
			return db.Wrap(err)
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO occasion_item (occasion_id, item_id) VALUES (?, ?)`, occasionID, itemID)
		return db.Wrap(err)
	})
}

// RemoveMember removes an item from an occasion by hand: a hand addition
// is dropped, and the item stays out whatever keywords it later carries.
func (r *Repo) RemoveMember(occasionID, itemID uid.ID) error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`DELETE FROM occasion_item WHERE occasion_id = ? AND item_id = ?`, occasionID, itemID); err != nil {
			return db.Wrap(err)
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO occasion_exclusion (occasion_id, item_id) VALUES (?, ?)`, occasionID, itemID)
		return db.Wrap(err)
	})
}

// Keyworded reports which of the ids carry one of the occasion's keywords
// the metadata settings show.
func (r *Repo) Keyworded(occasionID uid.ID, ids []uid.ID) (map[uid.ID]bool, error) {
	out := map[uid.ID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := db.In(ids)
	rows, err := r.d.Query(`SELECT DISTINCT ik.item_id FROM item_keyword_shown ik JOIN occasion_keyword ok ON ok.keyword_id = ik.keyword_id
		WHERE ok.occasion_id = ? AND ik.item_id IN `+in, append([]any{occasionID}, args...)...)
	keyworded, err := db.Collect(rows, err, db.Column[uid.ID])
	if err != nil {
		return nil, err
	}
	for _, id := range keyworded {
		out[id] = true
	}
	return out, nil
}

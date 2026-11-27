package occasion_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestPhrases(t *testing.T) {
	r := occasion.New(dbtest.Open(t))
	o := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7, Phrases: []string{"Lucky", "Seven Up", "lucky"}}
	must(t, r.Create(o, nil))
	if got, err := r.Get(o.ID); err != nil || !reflect.DeepEqual(got.Phrases, []string{"Lucky", "Seven Up"}) {
		t.Fatalf("in the order given, once each ignoring case: %+v %v", got, err)
	}
	o.Phrases = []string{"Jackpot", "Lucky"}
	must(t, r.Update(o, nil))
	all, err := r.List(-1, 0)
	must(t, err)
	for _, x := range all {
		if x.ID == o.ID && !reflect.DeepEqual(x.Phrases, []string{"Jackpot", "Lucky"}) {
			t.Fatalf("an update replaces the phrases: %+v", x)
		}
	}
	o.Phrases = nil
	must(t, r.Update(o, nil))
	if got, _ := r.Get(o.ID); got.Phrases != nil {
		t.Fatalf("%+v", got)
	}
}

// Every key of phrases.json is a holiday, and every phrase one an admin
// could give: trimmed, 1 to 60 characters, at most 200 per holiday, each
// once ignoring case.
func TestBuiltInPhrases(t *testing.T) {
	raw, err := os.ReadFile("phrases.json")
	must(t, err)
	var file map[string][]string
	must(t, json.Unmarshal(raw, &file))
	for key, ps := range file {
		if _, ok := occasion.HolidayByKey(key); !ok {
			t.Errorf("%s is not a holiday", key)
		}
		if !reflect.DeepEqual(occasion.BuiltInPhrases(key), ps) || len(ps) > 200 {
			t.Errorf("%s: %d phrases", key, len(ps))
		}
		seen := map[string]bool{}
		for _, p := range ps {
			if n := utf8.RuneCountInString(p); n < 1 || n > 60 || strings.TrimSpace(p) != p || seen[strings.ToLower(p)] {
				t.Errorf("%s: %q", key, p)
			}
			seen[strings.ToLower(p)] = true
		}
	}
	if got := occasion.BuiltInPhrases("festivus"); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestColors(t *testing.T) {
	r := occasion.New(dbtest.Open(t))
	o := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7, Colors: []string{"#000000", "#ABCDEF", "#123456"}}
	must(t, r.Create(o, nil))
	if got, err := r.Get(o.ID); err != nil || !reflect.DeepEqual(got.Colors, o.Colors) {
		t.Fatalf("%+v %v", got, err)
	}
	o.Colors = nil
	must(t, r.Update(o, nil))
	if got, _ := r.Get(o.ID); got.Colors != nil {
		t.Fatalf("an update clears the colors: %+v", got)
	}
	christmas, _ := occasion.HolidayByKey("christmas")
	if !reflect.DeepEqual(christmas.Colors, []string{"#1F6B45", "#B3263E", "#E0B252"}) {
		t.Fatalf("%v", christmas.Colors)
	}
	for _, h := range occasion.Holidays() {
		if len(h.Colors) != 3 {
			t.Errorf("%s: %v", h.Key, h.Colors)
		}
	}
}

// Holiday occasions waiting in occasion_seed get their holiday's built-ins
// once; the others get none, and a list emptied later stays empty.
func TestSeedBuiltIns(t *testing.T) {
	d := dbtest.Open(t)
	_, err := d.Exec(`INSERT INTO occasion (id, name, holiday, month, day, days_before, days_after, created_at, modified_at)
		VALUES (?1, 'Eggs', 'easter', NULL, NULL, 1, 1, 0, 0), (?2, 'Sevens', NULL, 7, 7, 0, 0, 0, 0);
		INSERT INTO occasion_seed (occasion_id) VALUES (?1)`, uid.N(1), uid.N(2))
	must(t, err)
	r := occasion.New(d)
	must(t, r.SeedBuiltIns())
	all, err := r.List(-1, 0)
	must(t, err)
	phrases, colors := map[string][]string{}, map[string][]string{}
	for _, o := range all {
		phrases[o.Name], colors[o.Name] = o.Phrases, o.Colors
	}
	builtIn := func(key string) ([]string, []string) {
		h, _ := occasion.HolidayByKey(key)
		return occasion.BuiltInPhrases(key), h.Colors
	}
	for name, key := range map[string]string{"Christmas": "christmas", "Halloween": "halloween", "Eggs": "easter"} {
		p, c := builtIn(key)
		if !reflect.DeepEqual(phrases[name], p) || !reflect.DeepEqual(colors[name], c) {
			t.Errorf("%s: %v %v", name, phrases[name], colors[name])
		}
	}
	if len(phrases["Christmas"]) == 0 || phrases["Sevens"] != nil || colors["Sevens"] != nil {
		t.Fatalf("%v %v", phrases, colors)
	}
	christmas := all[0]
	christmas.Phrases, christmas.Colors = nil, nil
	must(t, r.Update(christmas, nil))
	must(t, r.SeedBuiltIns())
	if o, _ := r.Get(christmas.ID); o.Phrases != nil || o.Colors != nil {
		t.Fatalf("seeding is once: %+v", o)
	}
}

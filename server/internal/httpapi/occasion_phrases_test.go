package httpapi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
)

func occasionBody(kv ...any) map[string]any {
	in := map[string]any{"daysBefore": 0, "daysAfter": 0, "keywordIds": []int64{}}
	for i := 0; i+1 < len(kv); i += 2 {
		in[kv[i].(string)] = kv[i+1]
	}
	return in
}

func TestOccasionPhrasesAndColors(t *testing.T) {
	e := newEnv(t)
	christmas, _ := occasion.HolidayByKey("christmas")
	var o api.Occasion
	e.expect(201, "POST", "/occasions", e.admin.token, occasionBody("name", "Yule", "holiday", "christmas"), &o)
	if !reflect.DeepEqual(o.Phrases, occasion.BuiltInPhrases("christmas")) || o.Colors == nil || !reflect.DeepEqual(*o.Colors, christmas.Colors) {
		t.Fatalf("a holiday occasion without phrases or colors starts with its holiday's: %+v", o)
	}
	e.expect(201, "POST", "/occasions", e.admin.token, occasionBody("name", "Bare", "holiday", "christmas", "phrases", []string{}, "colors", []string{}), &o)
	if len(o.Phrases) != 0 || o.Phrases == nil || o.Colors != nil {
		t.Fatalf("empty arrays mean none: %+v", o)
	}
	e.expect(201, "POST", "/occasions", e.admin.token, occasionBody("name", "Sevens", "month", 7, "day", 7), &o)
	if len(o.Phrases) != 0 || o.Colors != nil {
		t.Fatalf("a dated occasion starts with none: %+v", o)
	}

	path := "/occasions/" + itoa(o.Id)
	colors := []string{"#112233", "#aabbcc", "#DDEEFF"}
	e.expect(200, "PUT", path, e.admin.token, occasionBody("name", "Sevens", "month", 7, "day", 7,
		"phrases", []string{"  Lucky  ", "Seven Up", "LUCKY"}, "colors", colors), &o)
	var got api.Occasion
	e.expect(200, "GET", path, e.plain.token, nil, &got)
	if !reflect.DeepEqual(got.Phrases, []string{"Lucky", "Seven Up"}) || !reflect.DeepEqual(o.Phrases, got.Phrases) || got.Colors == nil ||
		!reflect.DeepEqual(*got.Colors, colors) {
		t.Fatalf("trimmed, once each ignoring case, in order: %+v %+v", o, got)
	}

	many := make([]string, 201)
	for i := range many {
		many[i] = "p" + itoa(int64(i))
	}
	for name, bad := range map[string]map[string]any{
		"blank phrase":     {"phrases": []string{"ok", "   "}},
		"empty phrase":     {"phrases": []string{""}},
		"long phrase":      {"phrases": []string{strings.Repeat("x", 61)}},
		"too many phrases": {"phrases": many},
		"one color":        {"colors": []string{"#112233"}},
		"two colors":       {"colors": []string{"#112233", "#445566"}},
		"four colors":      {"colors": []string{"#112233", "#445566", "#778899", "#000000"}},
		"not a color":      {"colors": []string{"#112233", "#445566", "red"}},
	} {
		in := occasionBody("name", "Sevens", "month", 7, "day", 7)
		for k, v := range bad {
			in[k] = v
		}
		if raw := e.expect(400, "PUT", path, e.admin.token, in, nil); errorCode(t, raw) != "bad_request" {
			t.Errorf("%s: %s", name, raw)
		}
	}
	e.expect(200, "PUT", path, e.admin.token, occasionBody("name", "Lucky Sevens", "month", 7, "day", 7), &o)
	e.expect(200, "GET", path, e.plain.token, nil, &got)
	if o.Name != "Lucky Sevens" || !reflect.DeepEqual(got.Phrases, []string{"Lucky", "Seven Up"}) || got.Colors == nil || !reflect.DeepEqual(*got.Colors, colors) {
		t.Fatalf("a replace without phrases or colors keeps them: %+v %+v", o, got)
	}
	e.expect(200, "PUT", path, e.admin.token, occasionBody("name", "Sevens", "month", 7, "day", 7, "phrases", []string{strings.Repeat("x", 60)},
		"colors", []string{}), &o)
	e.expect(200, "GET", path, e.plain.token, nil, &got)
	if len(got.Phrases) != 1 || got.Colors != nil || len(o.Phrases) != 1 || o.Colors != nil {
		t.Fatalf("empty arrays clear them: %+v %+v", o, got)
	}
	e.expect(404, "PUT", "/occasions/999", e.admin.token, occasionBody("name", "Gone", "month", 7, "day", 7), nil)
}

func TestHolidaysCarryTheirColors(t *testing.T) {
	e := newEnv(t)
	var hs api.HolidayList
	e.expect(200, "GET", "/holidays", e.plain.token, nil, &hs)
	for _, h := range hs.Items {
		want, _ := occasion.HolidayByKey(h.Key)
		if h.Colors == nil || !reflect.DeepEqual(*h.Colors, want.Colors) || len(want.Colors) != 3 {
			t.Errorf("%s: %v", h.Key, h.Colors)
		}
	}
}

func TestActiveOccasionHeadlineAndItem(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	e.expect(204, "PUT", "/occasions/1/members/"+itoa(c.g), e.admin.token, nil, nil)
	colors := []string{"#112233", "#445566", "#778899"}
	var o api.Occasion
	e.expect(201, "POST", "/occasions", e.admin.token, occasionBody("name", "Advent", "month", 12, "day", 1, "daysAfter", 30,
		"phrases", []string{"One", "Two", "Three"}, "colors", colors), &o)
	e.expect(204, "PUT", "/occasions/"+itoa(o.Id)+"/members/"+itoa(c.r), e.admin.token, nil, nil)
	picks := []int{2}
	e.svc.Occasions.Pick = func(n int) int {
		i := picks[0] % n
		picks = picks[1:]
		return i
	}
	season := "/profiles/" + itoa(e.plain.account.ID) + "/occasions?date=2026-12-20"
	var active api.ActiveOccasionList
	e.expect(200, "GET", season, e.plain.token, nil, &active)
	if len(active.Items) != 2 || active.Items[0].Name != "Christmas" || active.Items[0].Headline != nil || active.Items[0].Colors != nil ||
		active.Items[1].Headline == nil || *active.Items[1].Headline != "Three" || !reflect.DeepEqual(*active.Items[1].Colors, colors) {
		t.Fatalf("a headline only from an occasion's phrases, colors only when it has them: %+v", active)
	}
	picks = []int{0}
	e.expect(200, "GET", season, e.plain.token, nil, &active)
	if *active.Items[1].Headline != "One" {
		t.Fatalf("picked afresh per request: %+v", active.Items[1])
	}

	picks = []int{0, 0}
	e.expect(200, "GET", season+"&itemId="+itoa(c.r), e.plain.token, nil, &active)
	if len(active.Items) != 1 || active.Items[0].Name != "Advent" {
		t.Fatalf("only occasions the item belongs to: %+v", active)
	}
	e.expect(200, "GET", season+"&itemId="+itoa(c.pg), e.plain.token, nil, &active)
	if len(active.Items) != 0 {
		t.Fatalf("%+v", active)
	}
	e.expect(400, "GET", season+"&itemId=x", e.plain.token, nil, nil)
}

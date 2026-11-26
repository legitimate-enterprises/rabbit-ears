package httpapi_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

func TestOccasionsCRUDAndValidation(t *testing.T) {
	e := newEnv(t)
	var page api.OccasionPage
	e.expect(200, "GET", "/occasions", e.plain.token, nil, &page)
	if len(page.Items) != 2 || page.Items[0].Name != "Christmas" || *page.Items[0].Holiday != "christmas" || page.Items[0].KeywordIds[0] != 207317 ||
		page.Items[1].Name != "Halloween" || page.Items[1].DaysBefore != 30 || page.Items[1].DaysAfter != 1 {
		t.Fatalf("seeded: %+v", page)
	}
	body := map[string]any{"name": "Leap Day", "month": 2, "day": 29, "daysBefore": 0, "daysAfter": 0, "keywordIds": []int64{}}
	var leap api.Occasion
	e.expect(403, "POST", "/occasions", e.plain.token, body, nil)
	e.expect(201, "POST", "/occasions", e.admin.token, body, &leap)
	if leap.Month == nil || *leap.Day != 29 || leap.Holiday != nil || leap.KeywordIds == nil {
		t.Fatalf("%+v", leap)
	}
	e.expect(409, "POST", "/occasions", e.admin.token, map[string]any{"name": "leap day", "holiday": "easter", "daysBefore": 0, "daysAfter": 0,
		"keywordIds": []int64{}}, nil)
	for _, bad := range []map[string]any{
		{"name": "Both", "holiday": "easter", "month": 1, "day": 1},
		{"name": "Neither"},
		{"name": "No day", "month": 4},
		{"name": "No such day", "month": 4, "day": 31},
		{"name": "Month 13", "month": 13, "day": 1},
		{"name": "Unknown", "holiday": "festivus"},
		{"name": "Negative", "holiday": "easter", "daysBefore": -1},
		{"name": "Too long", "holiday": "easter", "daysAfter": 91},
	} {
		in := map[string]any{"daysBefore": 0, "daysAfter": 0, "keywordIds": []int64{}}
		for k, v := range bad {
			in[k] = v
		}
		e.expect(400, "POST", "/occasions", e.admin.token, in, nil)
	}

	path := "/occasions/" + itoa(leap.Id)
	body = map[string]any{"name": "Easter", "holiday": "easter", "daysBefore": 7, "daysAfter": 1, "keywordIds": []int64{12, 4}, "phrases": []string{},
		"colors": []string{}}
	e.expect(403, "PUT", path, e.plain.token, body, nil)
	e.expect(200, "PUT", path, e.admin.token, body, &leap)
	var got api.Occasion
	e.expect(200, "GET", path, e.plain.token, nil, &got)
	if got.Name != "Easter" || got.Month != nil || got.Day != nil || *got.Holiday != "easter" || len(got.KeywordIds) != 2 || got.KeywordIds[0] != 4 {
		t.Fatalf("%+v", got)
	}
	e.expect(404, "PUT", "/occasions/999", e.admin.token, body, nil)
	e.expect(403, "DELETE", path, e.plain.token, nil, nil)
	e.expect(204, "DELETE", path, e.admin.token, nil, nil)
	e.expect(404, "GET", path, e.plain.token, nil, nil)

	var hs api.HolidayList
	e.expect(200, "GET", "/holidays", e.plain.token, nil, &hs)
	if len(hs.Items) != 14 || hs.Items[3].Key != "easter" || hs.Items[3].Dates[1].Format("2006-01-02") != "2027-03-28" {
		t.Fatalf("%+v", hs.Items[3])
	}
}

func TestOccasionMembersAndSeason(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	must(t, e.svc.Items.Repo().SetKeywords(c.r, []int64{207317}))
	must(t, e.svc.Items.Repo().SetKeywords(c.adultShow, []int64{207317}))
	must(t, e.svc.Items.Repo().SetKeywords(c.adultEp, []int64{207317}))
	christmas := "/occasions/1"
	members := christmas + "/members"

	e.expect(403, "GET", members, e.plain.token, nil, nil)
	e.expect(403, "PUT", members+"/"+itoa(c.g), e.plain.token, nil, nil)
	e.expect(403, "DELETE", members+"/"+itoa(c.r), e.plain.token, nil, nil)
	e.expect(204, "PUT", members+"/"+itoa(c.g), e.admin.token, nil, nil)
	e.expect(400, "PUT", members+"/"+itoa(c.kidEp), e.admin.token, nil, nil)
	e.expect(404, "PUT", members+"/999", e.admin.token, nil, nil)
	e.expect(404, "PUT", "/occasions/999/members/"+itoa(c.g), e.admin.token, nil, nil)
	var mp api.OccasionMemberPage
	e.expect(200, "GET", members, e.admin.token, nil, &mp)
	via := map[int64]api.OccasionMemberVia{}
	for _, m := range mp.Items {
		via[m.Item.Id] = m.Via
	}
	if len(via) != 3 || via[c.g] != api.OccasionMemberViaAdded || via[c.r] != api.OccasionMemberViaKeyword || via[c.adultShow] != api.OccasionMemberViaKeyword {
		t.Fatalf("every member, with how it got there: %+v", via)
	}
	e.expect(200, "GET", members+"?limit=1", e.admin.token, nil, &mp)
	if len(mp.Items) != 1 || mp.NextOffset == nil {
		t.Fatalf("paged: %+v", mp)
	}
	e.expect(204, "DELETE", members+"/"+itoa(c.adultShow), e.admin.token, nil, nil)

	var items api.ItemPage
	e.expect(200, "GET", "/items?occasionId=1", e.plain.token, nil, &items)
	if got := ids(items.Items); len(got) != 2 || !got[c.g] || !got[c.r] {
		t.Fatalf("listItems by occasion: %v", got)
	}
	inSeason := "/profiles/" + itoa(e.plain.account.ID) + "/occasions?date=2026-12-20"
	var active api.ActiveOccasionList
	e.expect(200, "GET", inSeason, e.plain.token, nil, &active)
	if len(active.Items) != 1 || active.Items[0].Name != "Christmas" || active.Items[0].AnchorDate.Format("2006-01-02") != "2026-12-25" ||
		active.Items[0].FirstDate.Format("2006-01-02") != "2026-11-25" || active.Items[0].LastDate.Format("2006-01-02") != "2026-12-30" {
		t.Fatalf("%+v", active)
	}
	e.expect(400, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/occasions", e.plain.token, nil, nil)
	e.expect(400, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/occasions?date=2026-13-01", e.plain.token, nil, nil)
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/occasions?date=2026-07-04", e.plain.token, nil, &active)
	if len(active.Items) != 0 {
		t.Fatalf("out of season: %+v", active)
	}

	me := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(200, "PUT", me+"/preferences", e.plain.token, map[string]any{"hideKidsContent": true}, nil)
	e.expect(200, "GET", "/items?occasionId=1", e.plain.token, nil, &items)
	if got := ids(items.Items); len(got) != 1 || !got[c.r] {
		t.Fatalf("an adult hiding kids' content lists no G film: %v", got)
	}
	e.expect(200, "PUT", me+"/preferences", e.plain.token, map[string]any{"hideKidsContent": false}, nil)

	k := e.kid(e.plain, "Kid") // PG and TV-Y7
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	kidSeason := "/profiles/" + itoa(k.Id) + "/occasions?date=2026-12-20"
	e.expect(200, "GET", "/items?occasionId=1", e.plain.token, nil, &items)
	if got := ids(items.Items); len(got) != 1 || !got[c.g] {
		t.Fatalf("a kid lists only the members they see: %v", got)
	}
	e.expect(200, "GET", kidSeason, e.plain.token, nil, &active)
	if len(active.Items) != 1 {
		t.Fatalf("%+v", active)
	}
	e.expect(403, "PUT", members+"/"+itoa(c.g), e.plain.token, nil, nil)
	// Without the G film the kid sees no member, and no occasion.
	must(t, e.svc.Occasions.Repo().RemoveMember(1, c.g))
	e.expect(200, "GET", kidSeason, e.plain.token, nil, &active)
	if len(active.Items) != 0 {
		t.Fatalf("a kid sees an occasion only with a member they see: %+v", active)
	}
}

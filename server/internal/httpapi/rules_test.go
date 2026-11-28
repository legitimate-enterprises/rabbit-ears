package httpapi_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// rated is rules matching the content rating.
func rated(rating string) map[string]any {
	return map[string]any{"match": "all", "groups": []any{map[string]any{"match": "all",
		"conditions": []any{map[string]any{"field": "contentRating", "operator": "is", "text": rating}}}}}
}

// A smart playlist holds what its rules match; its entries and kind are the
// rules' to change, and only its owner changes them.
func TestSmartPlaylist(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	tok, rules := e.plain.token, s.owner+"/rules"
	var all api.PlaylistEntryPage
	e.expect(200, "GET", s.owner+"/entries", tok, nil, &all)
	manual := all.Items[0].Id

	e.expect(404, "GET", rules, tok, nil, nil)
	var got api.SmartRules
	e.expect(200, "PUT", rules, tok, map[string]any{"match": "any", "groups": []any{map[string]any{"match": "all",
		"conditions": []any{map[string]any{"field": "title", "operator": "contains", "text": "  gent "}}}}}, &got)
	if got.Match != api.Any || len(got.Groups) != 1 || *got.Groups[0].Conditions[0].Text != "gent" {
		t.Fatalf("answers the rules as stored: %+v", got)
	}
	var back api.SmartRules
	e.expect(200, "GET", rules, tok, nil, &back)
	if !slices.EqualFunc(back.Groups, got.Groups, func(a, b api.SmartGroup) bool { return *a.Conditions[0].Text == *b.Conditions[0].Text }) {
		t.Fatalf("%+v", back)
	}
	var page api.PlaylistEntryPage
	e.expect(200, "GET", s.owner+"/entries", tok, nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{s.g}) || page.Items[0].Id != manual {
		t.Fatalf("the matching entry stays as it was, the other goes: %+v", page)
	}
	var pl api.Playlist
	e.expect(200, "GET", s.owner, tok, nil, &pl)
	if pl.Smart == nil || !*pl.Smart || pl.ItemCount != 1 {
		t.Fatalf("%+v", pl)
	}

	// Entries are the rules'.
	raw := e.expect(409, "POST", s.owner+"/entries", tok, map[string]any{"itemId": s.r}, nil)
	if errorCode(t, raw) != "conflict" {
		t.Fatalf("%s", raw)
	}
	e.expect(409, "POST", s.owner+"/entries/batch", tok, map[string]any{"entries": []any{map[string]any{"itemId": s.r}}}, nil)
	e.expect(409, "PUT", s.owner+"/entries/"+itoa(manual), tok, map[string]any{"itemId": s.g, "position": 0}, nil)
	e.expect(409, "DELETE", s.owner+"/entries/"+itoa(manual), tok, nil, nil)
	e.expect(409, "PUT", s.owner, tok, map[string]any{"name": "Family", "kind": "tv"}, nil)
	e.expect(200, "PUT", s.owner, tok, map[string]any{"name": "Kind films", "kind": "movies"}, &pl)

	// Rules the kind cannot hold name where they fail.
	for path, body := range map[string]map[string]any{
		"groups": {"match": "all", "groups": []any{}},
		"groups.0.conditions.0.field": {"match": "all", "groups": []any{map[string]any{"match": "all",
			"conditions": []any{map[string]any{"field": "artist", "operator": "is", "text": "x"}}}}},
		"groups.0.conditions.0.operator": {"match": "all", "groups": []any{map[string]any{"match": "all",
			"conditions": []any{map[string]any{"field": "year", "operator": "contains", "text": "19"}}}}},
		"groups.0.conditions.0.text": {"match": "all", "groups": []any{map[string]any{"match": "all",
			"conditions": []any{map[string]any{"field": "title", "operator": "is", "text": "   "}}}}},
		"groups.0.conditions.0.number": {"match": "all", "groups": []any{map[string]any{"match": "all",
			"conditions": []any{map[string]any{"field": "year", "operator": "gt"}}}}},
	} {
		if got := fieldOf(t, e.expect(400, "PUT", rules, tok, body, nil)); got != path {
			t.Errorf("named %s, want %s", got, path)
		}
	}
	e.expect(200, "GET", rules, tok, nil, &back)
	if *back.Groups[0].Conditions[0].Text != "gent" {
		t.Fatalf("a refused put changes nothing: %+v", back)
	}

	// Who else may see it reads the rules; only the owner writes them.
	other := e.admin.account.ID
	e.link(e.plain.account.ID, other, false)
	e.share(s, other, "write")
	e.expect(200, "GET", s.under(other)+"/rules", e.admin.token, nil, &back)
	e.expect(403, "PUT", s.under(other)+"/rules", e.admin.token, rated("R"), nil)
	e.expect(409, "POST", s.under(other)+"/entries", e.admin.token, map[string]any{"itemId": s.r}, nil)
	e.expect(404, "GET", "/profiles/"+itoa(other)+"/playlists/"+itoa(uid.N(999999))+"/rules", e.admin.token, nil, nil)

	// A kid it is shared with sees only what their ceiling allows.
	e.expect(200, "PUT", rules, tok, map[string]any{"match": "all", "groups": []any{map[string]any{"match": "all",
		"conditions": []any{map[string]any{"field": "hdr", "operator": "isFalse"}}}}}, nil)
	e.expect(200, "GET", s.owner+"/entries", tok, nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{s.g, s.r}) {
		t.Fatalf("%v", entryIDs(page))
	}
	k := e.kid(e.plain, "Kid")
	e.share(s, k.Id, "read")
	e.expect(200, "GET", s.under(k.Id)+"/entries", e.kidDevice(e.plain, k.Id), nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{s.g}) {
		t.Fatalf("a kid sees only allowed titles: %v", entryIDs(page))
	}
}

// watched reads the owner's, whoever the playlist is shared with.
func TestSmartPlaylistReadsTheOwner(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	other := e.admin.account.ID
	e.link(e.plain.account.ID, other, false)
	e.share(s, other, "read")
	e.expect(200, "PUT", "/profiles/"+itoa(other)+"/watch-state/"+itoa(s.g), e.admin.token, map[string]any{"watched": true}, nil)
	e.expect(200, "PUT", s.owner+"/rules", e.plain.token, map[string]any{"match": "all", "groups": []any{map[string]any{"match": "all",
		"conditions": []any{map[string]any{"field": "watched", "operator": "isFalse"}}}}}, nil)
	var page api.PlaylistEntryPage
	e.expect(200, "GET", s.under(other)+"/entries", e.admin.token, nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{s.g, s.r}) {
		t.Fatalf("another's watch does not count: %v", entryIDs(page))
	}
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(s.g), e.plain.token, map[string]any{"watched": true}, nil)
	if _, err := e.svc.Smart.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.expect(200, "GET", s.under(other)+"/entries", e.admin.token, nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{s.r}) {
		t.Fatalf("the owner's watch does: %v", entryIDs(page))
	}
}

// A smart collection holds the movies its rules match; its movies and
// order are the rules', and a TMDB collection takes none.
func TestSmartCollection(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	g, r := e.film(films, "Gentle", "2001-01-01", "G"), e.film(films, "Rough", "2002-01-01", "R")
	tok := e.plain.token
	var c api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Rough ones"}, &c)
	one := "/collections/" + itoa(c.Id)
	e.expect(204, "PUT", one+"/movies/"+itoa(g), tok, nil, nil)
	e.expect(404, "GET", one+"/rules", tok, nil, nil)
	e.expect(200, "PUT", one+"/rules", tok, rated("R"), nil)
	e.expect(200, "GET", one, tok, nil, &c)
	if c.Smart == nil || !*c.Smart || c.MovieCount != 1 {
		t.Fatalf("%+v", c)
	}
	var page api.ItemPage
	e.expect(200, "GET", one+"/movies", tok, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != r {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", one+"/rules", tok, nil, nil)

	for _, call := range []struct{ method, path string }{
		{"PUT", one + "/movies/" + itoa(g)}, {"DELETE", one + "/movies/" + itoa(r)},
		{"PUT", one + "/movies/" + itoa(r) + "/position"}, {"DELETE", one + "/order"},
	} {
		var body any
		if call.method == "PUT" && call.path != one+"/movies/"+itoa(g) {
			body = map[string]any{"position": 0}
		}
		if raw := e.expect(403, call.method, call.path, tok, body, nil); errorCode(t, raw) != "forbidden" {
			t.Errorf("%s %s: %s", call.method, call.path, raw)
		}
	}
	e.expect(200, "PUT", one, tok, map[string]any{"name": "Renamed"}, nil)

	if got := fieldOf(t, e.expect(400, "PUT", one+"/rules", tok, map[string]any{"match": "all", "groups": []any{map[string]any{"match": "all",
		"conditions": []any{map[string]any{"field": "watched", "operator": "isTrue"}}}}}, nil)); got != "groups.0.conditions.0.field" {
		t.Errorf("a personal field: %s", got)
	}
	saga := e.tmdbCollection(10, "Saga", g, r)
	e.expect(403, "PUT", "/collections/"+itoa(saga)+"/rules", tok, rated("R"), nil)
	e.expect(404, "GET", "/collections/"+itoa(saga)+"/rules", tok, nil, nil)

	// A kid sees it once it holds what they may watch, and still may not
	// write its rules.
	e.expect(200, "PUT", one+"/rules", tok, rated("G"), nil)
	k := e.kid(e.plain, "Kid")
	e.expect(403, "PUT", one+"/rules", e.kidDevice(e.plain, k.Id), rated("R"), nil)
	var theirs api.Collection
	e.expect(201, "POST", "/collections", e.admin.token, map[string]any{"name": "Theirs"}, &theirs)
	e.expect(404, "PUT", "/collections/"+itoa(theirs.Id)+"/rules", tok, rated("G"), nil)

	e.expect(204, "DELETE", one, tok, nil, nil)
	var n int
	must(t, e.db.QueryRow(`SELECT COUNT(*) FROM smart_list`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d smart lists outlive their collection", n)
	}
}

// A station on its own smart playlist plays the new titles once
// rescheduled, and its kind is the rules'.
func TestSmartStation(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	films := e.library("movies")
	meadow, storm := e.film(films, "Meadow", "2001-01-01", "G"), e.film(films, "Storm", "2002-01-01", "R")
	e.file(meadow, 90*60000)
	e.file(storm, 90*60000)
	var st, other api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Films", "kind": "movies"}, &st)
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Others", "kind": "movies"}, &other)
	if st.Smart == nil || *st.Smart {
		t.Fatalf("a new station is not smart: %+v", st.Smart)
	}
	pl := "/profiles/" + itoa(e.plain.account.ID) + "/playlists/" + itoa(st.PlaylistId)
	e.expect(201, "POST", pl+"/entries", e.plain.token, map[string]any{"itemId": meadow}, nil)
	e.expect(200, "POST", "/stations/"+itoa(st.Id)+"/reschedule", e.plain.token, nil, &st)
	if st.OnAir.Next == nil || st.OnAir.Next.ItemId != meadow {
		t.Fatalf("%+v", st.OnAir)
	}
	e.expect(200, "PUT", pl+"/rules", e.plain.token, rated("R"), nil)
	e.expect(200, "POST", "/stations/"+itoa(st.Id)+"/reschedule", e.plain.token, nil, &st)
	if st.OnAir.Next == nil || st.OnAir.Next.ItemId != storm || st.Smart == nil || !*st.Smart {
		t.Fatalf("it plays what the rules match: %+v %v", st.OnAir, st.Smart)
	}
	var page api.StationPage
	e.expect(200, "GET", "/stations", e.plain.token, nil, &page)
	for _, s := range page.Items {
		if s.Smart == nil || *s.Smart != (s.Id == st.Id) {
			t.Errorf("%s smart %v", s.Name, s.Smart)
		}
	}
	e.expect(409, "PUT", "/stations/"+itoa(st.Id), e.plain.token, map[string]any{"name": "Films", "kind": "tv"}, nil)
}

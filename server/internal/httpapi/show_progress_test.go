package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (e *env) child(lib, parent uid.ID, kind string, number int) uid.ID {
	e.t.Helper()
	body := map[string]any{"libraryId": lib, "parentId": parent, "kind": kind, "parsedTitle": kind}
	if kind == "season" {
		body["seasonNumber"] = number
	} else {
		body["episodeNumber"] = number
	}
	var it api.Item
	e.expect(201, "POST", "/items", e.admin.token, body, &it)
	return it.Id
}

func (e *env) showProgress(profile uid.ID, query string) api.ShowProgressPage {
	e.t.Helper()
	var page api.ShowProgressPage
	e.expect(200, "GET", "/profiles/"+itoa(profile)+"/show-progress"+query, e.plain.token, nil, &page)
	return page
}

// mark sets one episode's state for the plain adult, a moment after the
// last so the latest played is unambiguous.
func (e *env) mark(ep uid.ID, watched bool, positionMs int64) {
	e.t.Helper()
	time.Sleep(2 * time.Millisecond)
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(ep), e.plain.token,
		map[string]any{"watched": watched, "positionMs": positionMs}, nil)
}

func TestShowProgress(t *testing.T) {
	e := newEnv(t)
	tv := e.library("tv")
	alpha, _ := e.show(tv, "Alpha", "TV-Y7")
	var s1 uid.ID
	{
		var page api.ItemPage
		e.expect(200, "GET", "/items?parentId="+itoa(alpha), e.admin.token, nil, &page)
		s1 = page.Items[0].Id
	}
	var s1e1 uid.ID
	{
		var page api.ItemPage
		e.expect(200, "GET", "/items?parentId="+itoa(s1), e.admin.token, nil, &page)
		s1e1 = page.Items[0].Id
	}
	specials := e.child(tv, alpha, "season", 0)
	s0e1 := e.child(tv, specials, "episode", 1)
	s2 := e.child(tv, alpha, "season", 2)
	s2e1 := e.child(tv, s2, "episode", 1)
	s1e2 := e.child(tv, s1, "episode", 2)
	e.show(tv, "Beta", "TV-MA")
	var empty api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "kind": "show", "parsedTitle": "Empty"}, &empty)
	e.rate(empty.Id, "TV-Y")
	e.child(tv, empty.Id, "season", 1)

	me := e.plain.account.ID
	at := func(page api.ShowProgressPage, i int) api.ShowProgress {
		t.Helper()
		if len(page.Items) <= i {
			t.Fatalf("%+v", page)
		}
		return page.Items[i]
	}
	next := func(want *uid.ID) {
		t.Helper()
		got := at(e.showProgress(me, ""), 0)
		if (got.NextEpisodeId == nil) != (want == nil) || want != nil && *got.NextEpisodeId != *want {
			t.Fatalf("next episode %v, want %v: %+v", got.NextEpisodeId, want, got)
		}
	}

	page := e.showProgress(me, "")
	if len(page.Items) != 3 || page.Items[1].ShowId == alpha || page.NextOffset != nil {
		t.Fatalf("every show the adult sees, by title: %+v", page)
	}
	a := at(page, 0)
	if a.ShowId != alpha || a.Seasons != 3 || a.Episodes != 4 || a.Unwatched != 4 || a.InProgress || a.LastPlayedAt != nil || *a.NextEpisodeId != s1e1 {
		t.Fatalf("nothing played starts at the first episode: %+v", a)
	}
	if z := at(page, 2); z.ShowId != empty.Id || z.Seasons != 1 || z.Episodes != 0 || z.Unwatched != 0 || z.NextEpisodeId != nil || z.LastPlayedAt != nil {
		t.Fatalf("a show with no episodes: %+v", z)
	}

	e.mark(s1e1, true, 0)
	next(&s1e2)
	e.mark(s2e1, false, 5000)
	next(&s2e1)
	if a := at(e.showProgress(me, ""), 0); !a.InProgress || a.Unwatched != 3 || a.LastPlayedAt == nil {
		t.Fatalf("%+v", a)
	}
	e.mark(s2e1, true, 0)
	next(&s0e1)
	e.mark(s0e1, true, 0)
	next(nil)
	if a := at(e.showProgress(me, ""), 0); a.Unwatched != 1 || a.InProgress {
		t.Fatalf("an earlier unwatched episode is not next: %+v", a)
	}
	e.mark(s1e1, true, 3000)
	next(&s1e1)
	if a := at(e.showProgress(me, ""), 0); !a.InProgress {
		t.Fatalf("a watched episode played again partway is in progress: %+v", a)
	}
	e.mark(s1e1, true, 0)

	paged := e.showProgress(me, "?limit=1&offset=1")
	if len(paged.Items) != 1 || paged.Items[0].ShowId == alpha || paged.NextOffset == nil || *paged.NextOffset != 2 {
		t.Fatalf("%+v", paged)
	}

	e.expect(404, "GET", "/profiles/"+itoa(e.admin.account.ID)+"/show-progress", e.plain.token, nil, nil)

	k := e.kid(e.plain, "Kid")
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	kid := e.showProgress(k.Id, "")
	if len(kid.Items) != 2 || kid.Items[0].ShowId != alpha || kid.Items[1].ShowId != empty.Id {
		t.Fatalf("a kid sees only shows within the filter: %+v", kid)
	}
	if a := kid.Items[0]; a.Unwatched != 4 || a.LastPlayedAt != nil || *a.NextEpisodeId != s1e1 {
		t.Fatalf("watch state is per profile: %+v", a)
	}
	e.expect(403, "GET", "/profiles/"+itoa(me)+"/show-progress", e.plain.token, nil, nil)
}

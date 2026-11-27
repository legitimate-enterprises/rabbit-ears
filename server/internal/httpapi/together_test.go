package httpapi_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

// pair is two adults of one household on their own devices, Heidi first
// (the household owner) and Claire, with a movie to watch together.
type pair struct {
	e             *env
	heidi, claire string
	lib, movie    uid.ID
	move          func(time.Duration)
}

func newPair(t *testing.T) pair {
	e := newEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	e.svc.Together.Now = func() time.Time { return now }
	claire := e.householdAdult(e.plain, "Claire", "claire@example.test")
	lib := e.library("movies")
	return pair{e, e.plain.token, claire.Token, lib, e.movie(lib, "Heat"), func(d time.Duration) { now = now.Add(d) }}
}

func (p pair) open(token string, item uid.ID) api.WatchTogether {
	p.e.t.Helper()
	var w api.WatchTogether
	p.e.expect(201, "POST", "/watch-together", token, map[string]any{"itemId": item}, &w)
	return w
}

func (p pair) poll(token string, w api.WatchTogether) api.WatchTogether {
	p.e.t.Helper()
	var got api.WatchTogether
	p.e.expect(200, "GET", togetherPath(w), token, nil, &got)
	return got
}

func (p pair) state(want int, token string, w api.WatchTogether, playing bool, pos int64) api.WatchTogether {
	p.e.t.Helper()
	var got api.WatchTogether
	p.e.expect(want, "PUT", togetherPath(w)+"/state", token, map[string]any{"playing": playing, "positionMs": pos}, &got)
	return got
}

func (p pair) list(token string, item ...uid.ID) []api.WatchTogether {
	p.e.t.Helper()
	var page api.WatchTogetherPage
	path := "/watch-together"
	if len(item) > 0 {
		path += "?itemId=" + itoa(item[0])
	}
	p.e.expect(200, "GET", path, token, nil, &page)
	return page.Items
}

func togetherPath(w api.WatchTogether) string { return "/watch-together/" + itoa(w.Id) }

func TestWatchTogetherTwoClients(t *testing.T) {
	p := newPair(t)
	e := p.e
	w := p.open(p.heidi, p.movie)
	if w.Status != api.Waiting || w.ItemId != p.movie || len(w.Members) != 1 || w.Members[0].Role != api.Host ||
		w.Members[0].AccountId != e.plain.account.ID || w.Members[0].DeviceName != "Laptop" || w.Members[0].Platform != api.Web ||
		!w.Members[0].Present || w.JoinMsLeft != 300_000 || w.Playing || w.PositionMs != 0 || w.Version != 1 ||
		!w.JoinBy.Equal(w.OpenedAt.Add(5*time.Minute)) || w.EndedAt != nil {
		t.Fatalf("opened %+v", w)
	}
	e.expect(409, "POST", togetherPath(w)+"/join", p.heidi, nil, nil)
	p.state(409, p.heidi, w, true, 0)

	p.move(48 * time.Second)
	got := p.list(p.claire, p.movie)
	if len(got) != 1 || got[0].Id != w.Id || got[0].JoinMsLeft != 252_000 || got[0].Members[0].Name != e.plain.account.Name {
		t.Fatalf("Join Heidi · 4:12: %+v", got)
	}
	if other := p.list(p.claire, e.movie(p.lib, "Ran")); len(other) != 0 {
		t.Fatalf("another title lists none: %+v", other)
	}
	if g := p.poll(p.claire, w); len(g.Members) != 1 || g.Version != 1 {
		t.Fatalf("a household adult reads it before joining: %+v", g)
	}

	var joined api.WatchTogether
	e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, &joined)
	if joined.Status != api.Joined || len(joined.Members) != 2 || joined.Members[1].Role != api.Guest || joined.Members[1].Name != "Claire" ||
		joined.Version != 2 || joined.JoinMsLeft != 0 {
		t.Fatalf("joined %+v", joined)
	}
	e.expect(409, "POST", togetherPath(w)+"/join", p.claire, nil, nil)

	if s := p.state(200, p.claire, w, true, 0); !s.Playing || s.Version != 3 || s.PausedById != nil ||
		s.EffectiveAtMs != s.ServerTimeMs+1000 || s.EffectivePositionMs != 0 {
		t.Fatalf("play %+v", s)
	}
	p.move(11 * time.Second)
	if s := p.poll(p.heidi, w); !s.Playing || s.PositionMs != 10_000 || !s.ServerTime.After(w.ServerTime) {
		t.Fatalf("the shared position advances while playing: %+v", s)
	}
	s := p.state(200, p.heidi, w, false, 10_700)
	if s.Playing || s.PositionMs != 10_700 || s.Version != 4 || s.PausedById == nil || *s.PausedById != e.plain.account.ID ||
		s.PausedByName == nil || *s.PausedByName != e.plain.account.Name {
		t.Fatalf("Heidi paused: %+v", s)
	}
	p.move(5 * time.Second)
	if s := p.poll(p.claire, w); s.PositionMs != 10_700 || s.Playing {
		t.Fatalf("paused holds still: %+v", s)
	}
	if s := p.state(200, p.claire, w, false, 60_000); s.PositionMs != 60_000 || s.Version != 5 || *s.PausedById != e.plain.account.ID {
		t.Fatalf("a seek while paused keeps who paused: %+v", s)
	}
	if s := p.state(200, p.claire, w, true, 60_000); !s.Playing || s.Version != 6 || s.PausedById != nil || s.PausedByName != nil {
		t.Fatalf("Play resumes for both: %+v", s)
	}

	p.move(31 * time.Second)
	s = p.poll(p.heidi, w)
	if s.PositionMs != 90_000 || !s.Members[0].Present || s.Members[1].Present {
		t.Fatalf("Claire unseen for 31 s is not present: %+v", s)
	}

	e.expect(204, "DELETE", togetherPath(w), p.heidi, nil, nil)
	s = p.poll(p.claire, w)
	if s.Status != api.Ended || s.EndedAt == nil || s.Version != 7 || s.PositionMs != 90_000 {
		t.Fatalf("Leave ends it for both: %+v", s)
	}
	p.state(409, p.claire, w, false, 90_000)
	e.expect(409, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
	e.expect(204, "DELETE", togetherPath(w), p.claire, nil, nil)
	if l := p.list(p.claire, p.movie); len(l) != 0 {
		t.Fatalf("ended ones are not listed: %+v", l)
	}
}

// Each member holds a poll open with the version it has; a play from
// either side answers both at once, with one instant to start at.
func TestWatchTogetherPushesEachChange(t *testing.T) {
	p := newPair(t)
	e := p.e
	w := p.open(p.heidi, p.movie)
	e.svc.Together.Hold = 5 * time.Second
	wait := func(token string, after int) chan api.WatchTogether {
		out := make(chan api.WatchTogether, 1)
		go func() {
			var got api.WatchTogether
			e.do("GET", togetherPath(w)+"?after="+itoa(int64(after)), token, nil, &got)
			out <- got
		}()
		return out
	}
	host := wait(p.heidi, w.Version)
	time.Sleep(50 * time.Millisecond)
	e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
	if s := <-host; s.Status != api.Joined || s.Version != 2 {
		t.Fatalf("the host hears the join: %+v", s)
	}

	host, guest := wait(p.heidi, 2), wait(p.claire, 2)
	time.Sleep(50 * time.Millisecond)
	began := time.Now()
	played := p.state(200, p.heidi, w, true, 0)
	for _, ch := range []chan api.WatchTogether{host, guest} {
		s := <-ch
		if s.Version != 3 || !s.Playing || s.EffectiveAtMs != played.EffectiveAtMs || s.EffectivePositionMs != 0 ||
			s.EffectiveAtMs != s.ServerTimeMs+1000 {
			t.Fatalf("both hear one play at one instant: %+v", s)
		}
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("pushed, not polled: %v", d)
	}

	e.svc.Together.Hold = 50 * time.Millisecond
	if s := <-wait(p.claire, 3); s.Version != 3 || !s.Members[1].Present {
		t.Fatalf("an unchanged state answers when the hold runs out: %+v", s)
	}
}

func TestWatchTogetherWindowAndQuiet(t *testing.T) {
	p := newPair(t)
	e := p.e

	w := p.open(p.heidi, p.movie)
	for range 5 {
		p.move(50 * time.Second)
		p.poll(p.heidi, w)
	}
	p.move(50 * time.Second)
	if s := p.poll(p.heidi, w); s.Status != api.Ended || !s.EndedAt.Equal(s.JoinBy) || s.JoinMsLeft != 0 {
		t.Fatalf("the window passed unjoined: %+v", s)
	}
	e.expect(409, "POST", togetherPath(w)+"/join", p.claire, nil, nil)

	w = p.open(p.heidi, p.movie)
	e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
	for range 10 {
		p.move(30 * time.Second)
		if s := p.poll(p.heidi, w); s.Status != api.Joined {
			t.Fatalf("one side polling keeps it: %+v", s)
		}
	}
	p.move(time.Minute)
	if s := p.poll(p.claire, w); s.Status != api.Ended {
		t.Fatalf("no poll for 60 s ends it: %+v", s)
	}

	w = p.open(p.heidi, p.movie)
	p.move(30 * time.Second)
	next := p.open(p.heidi, p.movie)
	if s := p.poll(p.heidi, w); s.Status != api.Ended {
		t.Fatalf("opening another ends the previous: %+v", s)
	}
	mine := p.open(p.claire, e.movie(p.lib, "Ran"))
	e.expect(200, "POST", togetherPath(next)+"/join", p.claire, nil, nil)
	if s := p.poll(p.claire, mine); s.Status != api.Ended {
		t.Fatalf("joining ends the one the guest hosted: %+v", s)
	}
}

func TestWatchTogetherWhoMayTakePart(t *testing.T) {
	p := newPair(t)
	e := p.e
	bea := e.householdAdult(e.plain, "Bea", "bea@example.test").Token
	tb := e.youtubeLibrary()
	show, ep := e.show(e.library("tv"), "Sitcom", "TV-PG")

	e.expect(400, "POST", "/watch-together", p.heidi, map[string]any{"itemId": show}, nil)
	e.expect(400, "POST", "/watch-together", p.heidi, map[string]any{"itemId": tb.channel}, nil)
	e.expect(404, "POST", "/watch-together", p.heidi, map[string]any{"itemId": uid.N(999_999)}, nil)
	p.open(p.heidi, ep)
	video := p.open(p.heidi, tb.newer)

	w := p.open(p.heidi, p.movie)
	if l := p.list(bea, p.movie); len(l) != 1 {
		t.Fatalf("a third adult sees it waiting: %+v", l)
	}
	e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
	if l := p.list(bea, p.movie); len(l) != 0 {
		t.Fatalf("a third adult does not see a joined one: %+v", l)
	}
	if l := p.list(p.claire); len(l) != 1 || l[0].Id != w.Id {
		t.Fatalf("a member sees theirs: %+v", l)
	}
	e.expect(409, "POST", togetherPath(w)+"/join", bea, nil, nil)
	p.state(403, bea, w, true, 0)
	e.expect(403, "DELETE", togetherPath(w), bea, nil, nil)
	if s := p.poll(bea, w); s.Version != 2 {
		t.Fatalf("an outsider's poll changes nothing: %+v", s)
	}
	if s := p.poll(p.heidi, video); s.Status != api.Ended {
		t.Fatalf("one open per adult: %+v", s)
	}

	// Another household reads nothing and changes nothing.
	other := e.admin.token
	e.expect(404, "GET", togetherPath(w), other, nil, nil)
	e.expect(404, "POST", togetherPath(w)+"/join", other, nil, nil)
	p.state(404, other, w, false, 0)
	e.expect(404, "DELETE", togetherPath(w), other, nil, nil)
	if l := p.list(other); len(l) != 0 {
		t.Fatalf("another household lists %+v", l)
	}

	e.expect(404, "GET", "/watch-together/"+uid.N(999_999).String(), p.heidi, nil, nil)
}

// A Watch together reaches beyond the household those who see its opener:
// an adult an admin linked them with, on a TV only by a link set for TVs,
// and a shared kid on a device of their other household.
func TestWatchTogetherReachesWhoSeesItsOpener(t *testing.T) {
	p := newPair(t)
	e := p.e
	admin := e.admin.token
	tv, _ := e.secondDevice(e.admin, "TV", "roku")
	e.expect(201, "POST", "/adult-links", admin, map[string]any{"accountId": e.plain.account.ID, "otherAccountId": e.admin.account.ID}, nil)
	c := e.cursor(admin)
	w := p.open(p.heidi, p.movie)
	if evs := e.heard(admin, &c); !slices.ContainsFunc(evs, func(ev api.Event) bool { return is(ev, api.EventKindWatchTogether, "opened") }) {
		t.Fatalf("a linked adult does not hear of it: %+v", evs)
	}
	if l := p.list(admin); len(l) != 1 || l[0].Id != w.Id {
		t.Fatalf("a linked adult lists %+v", l)
	}
	if l := p.list(tv); len(l) != 0 {
		t.Fatalf("a link off TVs lists on a TV: %+v", l)
	}
	e.expect(404, "POST", togetherPath(w)+"/join", tv, nil, nil)
	third := e.adult("third@example.test", false)
	e.expect(404, "GET", togetherPath(w), third.token, nil, nil)

	e.expect(200, "POST", togetherPath(w)+"/join", admin, nil, nil)
	if got := p.state(200, admin, w, true, 0); !got.Playing {
		t.Fatalf("the linked guest plays it: %+v", got)
	}
	e.expect(204, "DELETE", "/adult-links/"+itoa(e.plain.account.ID)+"/"+itoa(e.admin.account.ID), admin, nil, nil)
	if got := p.poll(admin, w); got.Status != api.Joined {
		t.Fatalf("a member keeps it: %+v", got)
	}

	k := e.kid(e.plain, "Mia")
	e.putIn(k.Id, e.admin.household)
	e.rate(p.movie, "G")
	w = p.open(p.heidi, p.movie)
	if l := p.list(e.kidDevice(e.admin, k.Id)); len(l) != 1 || l[0].Id != w.Id {
		t.Fatalf("a shared kid lists %+v", l)
	}
}

// Any two profiles of a household watch together, a kid among them, on a
// title every member may see. One on a title a kid may not see is, to that
// kid, not there.
func TestWatchTogetherWithKids(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	p := pair{e: e, heidi: e.plain.token, claire: e.householdAdult(e.plain, "Claire", "claire@example.test").Token}
	tb := e.youtubeLibrary()
	rated := e.kid(e.plain, "Rae")
	var listed api.Account
	e.expect(201, "POST", "/profiles", p.heidi, map[string]any{"name": "Lee", "birthday": "2018-05-01", "filterMode": "allow_list"}, &listed)
	for _, id := range []uid.ID{c.r, c.pg} {
		e.expect(204, "PUT", "/profiles/"+itoa(listed.Id)+"/allowed-items/"+itoa(id), p.heidi, nil, nil)
	}
	rae, lee := e.kidDevice(e.plain, rated.Id), e.kidDevice(e.plain, listed.Id)
	join := func(want int, token string, w api.WatchTogether) api.WatchTogether {
		t.Helper()
		var got api.WatchTogether
		e.expect(want, "POST", togetherPath(w)+"/join", token, nil, &got)
		return got
	}
	refused := func(token string, item uid.ID) {
		t.Helper()
		e.expect(404, "POST", "/watch-together", token, map[string]any{"itemId": item}, nil)
	}
	// hidden is a Watch together the profile cannot tell from none.
	hidden := func(token string, w api.WatchTogether) {
		t.Helper()
		for _, l := range append(p.list(token, w.ItemId), p.list(token)...) {
			if l.Id == w.Id || l.ItemId == w.ItemId {
				t.Fatalf("listed %+v", l)
			}
		}
		e.expect(404, "GET", togetherPath(w), token, nil, nil)
		e.expect(404, "GET", togetherPath(w)+"?after=0", token, nil, nil)
		join(404, token, w)
		p.state(404, token, w, true, 0)
		e.expect(404, "DELETE", togetherPath(w), token, nil, nil)
		if s := p.poll(p.claire, w); s.Status != api.Waiting || s.Version != w.Version || len(s.Members) != 1 {
			t.Fatalf("it stands as it was: %+v", s)
		}
	}

	// A kid opens one on a title under their ceilings and an adult joins.
	w := p.open(rae, c.pg)
	if w.Members[0].AccountId != rated.Id || w.Members[0].Name != "Rae" || w.Members[0].DeviceName != "Tablet" {
		t.Fatalf("the kid hosts: %+v", w)
	}
	if l := p.list(p.heidi, c.pg); len(l) != 1 || l[0].Id != w.Id {
		t.Fatalf("an adult sees a kid's waiting: %+v", l)
	}
	join(200, p.heidi, w)
	p.state(200, rae, w, true, 0)
	if s := p.state(200, rae, w, false, 5_000); s.PausedById == nil || *s.PausedById != rated.Id || *s.PausedByName != "Rae" {
		t.Fatalf("the kid paused: %+v", s)
	}
	e.expect(204, "DELETE", togetherPath(w), rae, nil, nil)
	for _, id := range []uid.ID{c.r, c.unrated, c.adultEp, tb.newer} {
		refused(rae, id)
	}
	p.open(rae, c.kidEp)

	// A kid joins one an adult opened on a title the kid may see.
	w = p.open(p.heidi, c.g)
	if l := p.list(rae); len(l) != 2 || l[0].Id != w.Id || l[0].Members[0].Name != e.plain.account.Name {
		t.Fatalf("a kid sees it waiting, above their own: %+v", l)
	}
	if s := p.poll(rae, w); s.Status != api.Waiting {
		t.Fatalf("a kid reads it before joining: %+v", s)
	}
	if s := join(200, rae, w); s.Status != api.Joined || s.Members[1].AccountId != rated.Id {
		t.Fatalf("the kid joined: %+v", s)
	}
	p.state(200, rae, w, true, 0)

	// An adult's title above a kid's ceilings is not there for that kid,
	// and is for the kid whose allow list holds it.
	w = p.open(p.claire, c.r)
	hidden(rae, w)
	if l := p.list(lee, c.r); len(l) != 1 || l[0].Id != w.Id {
		t.Fatalf("on the allow list: %+v", l)
	}
	join(200, lee, w)
	p.state(403, rae, p.open(p.claire, c.pg), true, 0)

	// An allow list is only what is on it, whatever the rating.
	refused(lee, c.g)
	hidden(lee, p.open(p.claire, c.g))
	hidden(lee, p.open(p.claire, c.kidEp))
	w = p.open(lee, c.pg)
	if s := join(200, rae, w); s.Members[0].Name != "Lee" || s.Members[1].Name != "Rae" {
		t.Fatalf("two kids: %+v", s)
	}

	// A video follows its channel: on the allow list, or marked for kids.
	e.expect(204, "PUT", "/profiles/"+itoa(listed.Id)+"/allowed-items/"+itoa(tb.channel), p.heidi, nil, nil)
	w = p.open(p.claire, tb.newer)
	hidden(rae, w)
	join(200, lee, w)
	e.expect(200, "PUT", "/youtube/channels/"+itoa(tb.channel)+"/for-kids", p.heidi, map[string]any{"forKids": true}, nil)
	e.expect(204, "DELETE", "/profiles/"+itoa(listed.Id)+"/allowed-items/"+itoa(tb.channel), p.heidi, nil, nil)
	w = p.open(rae, tb.older)
	hidden(lee, w)
	refused(lee, tb.older)
	join(200, p.claire, w)

	// A member who may no longer see the title has left.
	w = p.open(rae, c.pg)
	join(200, p.heidi, w)
	e.rate(c.pg, "R")
	e.expect(404, "GET", togetherPath(w), rae, nil, nil)
	if s := p.poll(p.heidi, w); s.Status != api.Ended {
		t.Fatalf("it ended for the adult left: %+v", s)
	}

	// A deleted kid has left.
	w = p.open(lee, c.pg)
	join(200, p.heidi, w)
	e.expect(204, "DELETE", "/profiles/"+itoa(listed.Id), p.heidi, nil, nil)
	if s := p.poll(p.heidi, w); s.Status != api.Ended {
		t.Fatalf("a deleted kid leaves it: %+v", s)
	}
}

// race runs each call at once and returns their statuses in order.
func race(calls ...func() int) []int {
	out := make([]int, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Go(func() { out[i] = c() })
	}
	wg.Wait()
	return out
}

func TestWatchTogetherRaces(t *testing.T) {
	p := newPair(t)
	e := p.e
	bea := e.householdAdult(e.plain, "Bea", "bea@example.test").Token
	stateBody := func(playing bool, pos int64) map[string]any {
		return map[string]any{"playing": playing, "positionMs": pos}
	}

	// Two adults press Join at once: one gets in.
	w := p.open(p.heidi, p.movie)
	joins := race(
		func() int { c, _ := e.do("POST", togetherPath(w)+"/join", p.claire, nil, nil); return c },
		func() int { c, _ := e.do("POST", togetherPath(w)+"/join", bea, nil, nil); return c })
	if joins[0]+joins[1] != 200+409 || joins[0] != 200 && joins[0] != 409 {
		t.Fatalf("joins %v", joins)
	}
	guest := p.claire
	if joins[1] == 200 {
		guest = bea
	}
	p.state(200, guest, w, true, 0)
	p.move(20 * time.Second)

	// Both press pause a moment apart: one pause, one version step.
	before := p.poll(p.heidi, w).Version
	pauses := race(
		func() int {
			c, _ := e.do("PUT", togetherPath(w)+"/state", p.heidi, stateBody(false, 20_100), nil)
			return c
		},
		func() int {
			c, _ := e.do("PUT", togetherPath(w)+"/state", guest, stateBody(false, 19_900), nil)
			return c
		})
	s := p.poll(p.heidi, w)
	if pauses[0] != 200 || pauses[1] != 200 || s.Playing || s.Version != before+1 || s.PausedById == nil ||
		(s.PositionMs != 20_100 && s.PositionMs != 19_900) {
		t.Fatalf("both paused: %v %+v", pauses, s)
	}

	// One leaves while the other seeks: it ends, and the seek either landed
	// first or found it ended.
	outcome := race(
		func() int {
			c, _ := e.do("PUT", togetherPath(w)+"/state", guest, stateBody(false, 600_000), nil)
			return c
		},
		func() int { c, _ := e.do("DELETE", togetherPath(w), p.heidi, nil, nil); return c })
	s = p.poll(guest, w)
	if outcome[1] != 204 || s.Status != api.Ended {
		t.Fatalf("leave mid-seek: %v %+v", outcome, s)
	}
	switch outcome[0] {
	case 200:
		if s.PositionMs != 600_000 || s.Version != before+3 {
			t.Fatalf("seek then leave: %+v", s)
		}
	case 409:
		if s.PositionMs == 600_000 || s.Version != before+2 {
			t.Fatalf("leave then seek: %+v", s)
		}
	default:
		t.Fatalf("seek answered %d", outcome[0])
	}
	p.state(409, guest, w, true, 0)
}

// A member's device that starts another title has left; the same title is
// the Watch together's own playback session on that device.
func TestWatchTogetherBesidePlayback(t *testing.T) {
	p := newPair(t)
	e := p.e
	file := func(m uid.ID) {
		e.expect(201, "POST", "/media-files", e.admin.token, map[string]any{"itemId": m, "path": e.onDisk("/films/" + itoa(m) + ".mp4"), "sizeBytes": 1,
			"fileModifiedAt": "2026-01-01T00:00:00Z"}, nil)
	}
	m, other := p.movie, e.movie(p.lib, "Ran")
	bea := e.householdAdult(e.plain, "Bea", "bea@example.test")
	dan := e.householdAdult(e.plain, "Dan", "dan@example.test")
	file(m)
	file(other)
	w := p.open(p.claire, m)
	e.expect(200, "POST", togetherPath(w)+"/join", p.heidi, nil, nil)
	p.state(200, p.heidi, w, true, 0)
	e.start(m, snapshot())
	if s := p.poll(p.claire, w); s.Status != api.Joined {
		t.Fatalf("playing the title keeps it: %+v", s)
	}
	e.start(other, snapshot())
	if s := p.poll(p.claire, w); s.Status != api.Ended {
		t.Fatalf("playing another title leaves it: %+v", s)
	}

	w = p.open(p.claire, m)
	e.expect(200, "POST", togetherPath(w)+"/join", p.heidi, nil, nil)
	e.expect(204, "POST", "/auth/sign-out", p.heidi, nil, nil)
	if s := p.poll(p.claire, w); s.Status != api.Ended {
		t.Fatalf("signing out leaves it: %+v", s)
	}

	// Revoking an adult's account revokes their devices, which leave.
	w = p.open(p.claire, m)
	e.expect(200, "POST", togetherPath(w)+"/join", bea.Token, nil, nil)
	e.expect(200, "PUT", "/accounts/"+itoa(bea.Me.Account.Id), e.admin.token, map[string]any{"kind": "adult", "name": "Bea",
		"email": "bea@example.test", "revoked": true}, nil)
	if s := p.poll(p.claire, w); s.Status != api.Ended {
		t.Fatalf("a revoked account leaves it: %+v", s)
	}
	w = p.open(p.claire, m)
	e.expect(200, "POST", togetherPath(w)+"/join", dan.Token, nil, nil)
	e.expect(204, "DELETE", "/accounts/"+itoa(dan.Me.Account.Id), e.admin.token, nil, nil)
	if s := p.poll(p.claire, w); s.Status != api.Ended {
		t.Fatalf("a deleted account leaves it: %+v", s)
	}
}

// An adult listening to music can open a Watch together and wait: the next
// song starting on the device, from a queue or a music station, is not
// another title.
func TestWatchTogetherOutlastsASong(t *testing.T) {
	p := newPair(t)
	e := p.e
	e.stationClock()
	song := e.song(e.library("music"), "Opening")
	e.file(song, 180000)
	profile := "/profiles/" + itoa(e.plain.account.ID)
	var songs api.Playlist
	e.expect(201, "POST", profile+"/playlists", p.heidi, map[string]any{"name": "Songs", "kind": "music"}, &songs)
	e.expect(201, "POST", profile+"/playlists/"+itoa(songs.Id)+"/entries", p.heidi, map[string]any{"itemId": song}, nil)
	var st api.Station
	e.expect(201, "POST", "/stations", p.heidi, map[string]any{"name": "Radio", "playlistId": songs.Id}, &st)
	w := p.open(p.heidi, p.movie)
	e.start(song, snapshot())
	if s := p.poll(p.heidi, w); s.Status == api.Ended {
		t.Fatalf("a song starting leaves no Watch together: %+v", s)
	}
	e.expect(201, "POST", "/stations/"+itoa(st.Id)+"/playback", p.heidi, map[string]any{"snapshot": snapshot()}, nil)
	if s := p.poll(p.heidi, w); s.Status == api.Ended {
		t.Fatalf("a music station's song leaves no Watch together: %+v", s)
	}
	e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
}

// A watched title with a resume position opens where its host would resume it.
func TestWatchTogetherOpensAtAWatchedTitlesPosition(t *testing.T) {
	p := newPair(t)
	must(t, p.e.svc.WatchStates.Repo().Save(&watchstate.State{AccountID: p.e.plain.account.ID, ItemID: p.movie, PositionMs: 42_000,
		Watched: true, ChangedAt: time.Now().UTC()}))
	if w := p.open(p.heidi, p.movie); w.PositionMs != 42_000 {
		t.Fatalf("opened at %d", w.PositionMs)
	}
}

// Members tell the server what they need on their polls and actions; the
// instant an action takes effect follows the slowest, an older client that
// says nothing still gets the second, and a report out of range is refused.
func TestWatchTogetherLeadFollowsReportedNeeds(t *testing.T) {
	p := newPair(t)
	w := p.open(p.heidi, p.movie)
	p.e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
	lead := func(s api.WatchTogether) int64 { return s.EffectiveAtMs - s.ServerTimeMs }
	put := func(playing bool) (s api.WatchTogether) {
		p.e.expect(200, "PUT", togetherPath(w)+"/state?rttMs=20", p.heidi, map[string]any{"playing": playing, "positionMs": 0}, &s)
		return s
	}
	var got api.WatchTogether

	p.e.expect(200, "GET", togetherPath(w)+"?rttMs=30", p.claire, nil, &got)
	if s := put(true); lead(s) != 300 {
		t.Fatalf("close members need the least: %d", lead(s))
	}
	p.e.expect(200, "GET", togetherPath(w)+"?rttMs=400&floorMs=900", p.claire, nil, &got)
	if s := put(false); lead(s) != 900 {
		t.Fatalf("a platform floor holds: %d", lead(s))
	}
	p.e.expect(200, "GET", togetherPath(w), p.claire, nil, &got)
	if s := put(true); lead(s) != 1000 {
		t.Fatalf("a poll that reports nothing needs the second: %d", lead(s))
	}
	p.e.expect(400, "GET", togetherPath(w)+"?rttMs=-1", p.claire, nil, nil)
}

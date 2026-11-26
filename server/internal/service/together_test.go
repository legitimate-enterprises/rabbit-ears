package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/together"
)

// joinedPair is a Watch together two adults have joined, on a clock that
// stands at now until moved.
type joinedPair struct {
	s           *service.WatchTogetherService
	household   service.Where
	id          int64
	host, guest together.Member
	now         *time.Time
}

func newJoinedPair(t *testing.T, start time.Time) joinedPair {
	t.Helper()
	d := dbtest.Open(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	heidi := &account.Account{Kind: account.Adult, Email: "h@example.test", Name: "Heidi"}
	must(account.New(d).Create(heidi))
	h := &household.Household{Name: "Home", AdultID: heidi.ID}
	must(household.New(d).Create(h))
	claire := &account.Account{Kind: account.Adult, Email: "c@example.test", Name: "Claire", HouseholdID: &h.ID}
	must(account.New(d).Create(claire))
	var members []together.Member
	for i, a := range []*account.Account{heidi, claire} {
		dev := &device.Device{TokenHash: "t" + a.Email, Name: []string{"TV", "Mac"}[i], Platform: "tvos", AccountID: a.ID, HouseholdID: h.ID}
		must(device.New(d).Create(dev))
		members = append(members, together.Member{AccountID: a.ID, DeviceID: dev.ID})
	}
	l := &library.Library{Name: "Movies", Kind: library.Movies}
	must(library.New(d).Create(l))
	movie := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "Dune"}
	must(item.New(d).Create(movie))

	s := service.NewWatchTogetherService(together.New(d), item.New(d), account.New(d))
	now := start
	s.Now = func() time.Time { return now }
	v, err := s.Open(h.ID, movie.ID, members[0], 0, 0)
	must(err)
	_, err = s.Join(service.Where{HouseholdID: h.ID}, v.W.ID, members[1], item.Audience{})
	must(err)
	return joinedPair{s, service.Where{HouseholdID: h.ID}, v.W.ID, members[0], members[1], &now}
}

// report is one poll by who, carrying a round trip and a floor, or nothing.
func (p joinedPair) report(r [2]*int64, who together.Member) {
	p.s.Report(p.id, who.DeviceID, r[0], r[1])
	if _, err := p.s.Poll(p.household, who.AccountID, p.id, item.Audience{}); err != nil {
		panic(err)
	}
}

func (p joinedPair) set(t *testing.T, who together.Member, playing bool, pos int64) service.Shared {
	t.Helper()
	v, err := p.s.SetState(p.household, who.AccountID, p.id, playing, pos, item.Audience{})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Every action takes effect on both at one instant, a lead after it was
// sent, with no rounding to a whole second. Members that report nothing
// need the full second.
func TestWatchTogetherSchedulesActionsALeadAway(t *testing.T) {
	start := time.Date(2026, 11, 17, 20, 0, 0, 300_000_000, time.UTC)
	p := newJoinedPair(t, start)

	v := p.set(t, p.guest, true, 5_000)
	if want := start.Add(service.Lead); !v.W.PositionAt.Equal(want) || !v.W.Playing || v.W.PositionMs != 5_000 {
		t.Fatalf("play from paused starts at %v, there: %+v", want, v.W)
	}
	if v.PositionMs() != 5_000 {
		t.Fatalf("held until it starts: %d", v.PositionMs())
	}

	*p.now = start.Add(10*time.Second + 250*time.Millisecond)
	v = p.set(t, p.host, true, 60_000)
	at := p.now.Add(service.Lead)
	if !v.W.PositionAt.Equal(at) || v.W.PositionMs != 60_000+service.Lead.Milliseconds() {
		t.Fatalf("a seek while playing lands where the seeker will be: %+v", v.W)
	}
	if got := v.W.SharedAt(at); got != 60_000+service.Lead.Milliseconds() {
		t.Fatalf("both are at the seeker's position at the instant: %d", got)
	}

	*p.now = start.Add(20 * time.Second)
	v = p.set(t, p.host, false, 70_000)
	if !v.W.PositionAt.Equal(p.now.Add(service.Lead)) || v.W.Playing || v.W.PositionMs != 70_000 {
		t.Fatalf("a pause holds where the pauser stopped: %+v", v.W)
	}
}

func ms(n int64) *int64 { return &n }

// The lead is what the slowest present member needs: its round trip and a
// margin, never below its platform floor, never below the server's least,
// never above a second; a member that reports no round trip needs the
// second.
func TestWatchTogetherLeadFollowsThePresentMembers(t *testing.T) {
	start := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		host, guest [2]*int64 // round trip, floor
		want        time.Duration
	}{
		{"neither reports", [2]*int64{}, [2]*int64{}, service.Lead},
		{"both close", [2]*int64{ms(20)}, [2]*int64{ms(30)}, service.MinLead},
		{"one reports", [2]*int64{ms(20)}, [2]*int64{}, service.Lead},
		{"one slow", [2]*int64{ms(20)}, [2]*int64{ms(400)}, 400*time.Millisecond + service.LinkMargin},
		{"too slow", [2]*int64{ms(20)}, [2]*int64{ms(5_000)}, service.Lead},
		{"a platform floor", [2]*int64{ms(20)}, [2]*int64{ms(20), ms(800)}, 800 * time.Millisecond},
		{"a floor above the second", [2]*int64{ms(20)}, [2]*int64{ms(20), ms(9_000)}, service.Lead},
		{"a floor with no round trip", [2]*int64{ms(20)}, [2]*int64{nil, ms(500)}, service.Lead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newJoinedPair(t, start)
			p.report(c.host, p.host)
			p.report(c.guest, p.guest)
			if v := p.set(t, p.guest, true, 0); v.W.PositionAt.Sub(*p.now) != c.want {
				t.Fatalf("lead %v, want %v", v.W.PositionAt.Sub(*p.now), c.want)
			}
		})
	}
}

// A member silent for longer than Present is not waited for; one that
// reports again is, and so is one whose report is dropped by a poll
// reporting nothing, as an older client's is.
func TestWatchTogetherLeadIgnoresAbsentMembersAndForgetsReports(t *testing.T) {
	p := newJoinedPair(t, time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC))
	p.report([2]*int64{ms(20)}, p.host)
	p.report([2]*int64{ms(20)}, p.guest)
	*p.now = p.now.Add(10 * time.Second)
	p.report([2]*int64{ms(900)}, p.guest)
	*p.now = p.now.Add(service.Present - time.Second)
	p.report([2]*int64{ms(20)}, p.host)
	*p.now = p.now.Add(2 * time.Second)
	p.report([2]*int64{ms(20)}, p.host)
	if v := p.set(t, p.host, true, 0); v.W.PositionAt.Sub(*p.now) != service.MinLead {
		t.Fatalf("the silent slow member is not waited for: %v", v.W.PositionAt.Sub(*p.now))
	}
	p.report([2]*int64{}, p.guest)
	if v := p.set(t, p.host, false, 0); v.W.PositionAt.Sub(*p.now) != service.Lead {
		t.Fatalf("a poll that reports nothing needs the second: %v", v.W.PositionAt.Sub(*p.now))
	}
}

func TestWatchTogetherVersionsChangesOnly(t *testing.T) {
	start := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	p := newJoinedPair(t, start)
	v := p.set(t, p.guest, true, 0)
	if v.W.Version != 3 {
		t.Fatalf("open, join, play: %d", v.W.Version)
	}
	if again := p.set(t, p.host, true, 300); again.W.Version != 3 || !again.W.PositionAt.Equal(v.W.PositionAt) {
		t.Fatalf("both press play: one change, %+v", again.W)
	}
	*p.now = start.Add(10 * time.Second)
	if v := p.set(t, p.host, true, 9_600); v.W.Version != 4 {
		t.Fatalf("a seek of 600 ms is a change: %+v", v.W)
	}
}

// A member waiting with the version it has hears the next change at once,
// and otherwise the same state when the hold runs out.
func TestWatchTogetherWaitWakesOnChange(t *testing.T) {
	p := newJoinedPair(t, time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC))
	p.s.Hold = 5 * time.Second
	ctx := context.Background()

	v, err := p.s.Wait(ctx, p.household, p.host.AccountID, p.id, 1, item.Audience{})
	if err != nil || v.W.Version != 2 {
		t.Fatalf("an older version answers at once: %+v %v", v.W, err)
	}

	got := make(chan service.Shared)
	began := time.Now()
	go func() {
		v, _ := p.s.Wait(ctx, p.household, p.host.AccountID, p.id, 2, item.Audience{})
		got <- v
	}()
	time.Sleep(50 * time.Millisecond)
	p.set(t, p.guest, true, 0)
	select {
	case v := <-got:
		if v.W.Version != 3 || !v.W.Playing || time.Since(began) > time.Second {
			t.Fatalf("woken by the change: %+v after %v", v.W, time.Since(began))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the change did not wake the waiter")
	}

	p.s.Hold = 100 * time.Millisecond
	if v, err := p.s.Wait(ctx, p.household, p.host.AccountID, p.id, 3, item.Audience{}); err != nil || v.W.Version != 3 {
		t.Fatalf("the hold runs out with the same state: %+v %v", v.W, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.s.Wait(cancelled, p.household, p.guest.AccountID, p.id, 3, item.Audience{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a gone caller stops waiting: %v", err)
	}

	p.s.Hold = 5 * time.Second
	go func() {
		v, _ := p.s.Wait(ctx, p.household, p.guest.AccountID, p.id, 3, item.Audience{})
		got <- v
	}()
	time.Sleep(50 * time.Millisecond)
	if err := p.s.Leave(p.household, p.host.AccountID, p.id, item.Audience{}); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v.Status() != "ended" {
		t.Fatalf("leaving wakes the other: %+v", v.W)
	}
}

// Waiting on one that has ended, or on one that is not there, answers at
// once and leaves nothing to wake.
func TestWatchTogetherWaitOnAnEndedOrUnknownOneKeepsNothing(t *testing.T) {
	p := newJoinedPair(t, time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC))
	if err := p.s.Leave(p.household, p.host.AccountID, p.id, item.Audience{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{p.id, p.id + 100} {
		_, _ = p.s.Wait(context.Background(), p.household, p.guest.AccountID, id, 9, item.Audience{})
	}
	if n := p.s.Waited(); n != 0 {
		t.Fatalf("%d left to wake", n)
	}
}

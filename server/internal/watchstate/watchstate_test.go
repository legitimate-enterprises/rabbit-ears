package watchstate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

func setup(t *testing.T) (*watchstate.Repo, uid.ID, uid.ID, []uid.ID) {
	d := dbtest.Open(t)
	a, b := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A"}, &account.Account{Kind: account.Adult, Email: "b@example.test", Name: "B"}
	ar := account.New(d)
	_ = ar.Create(a, nil)
	_ = ar.Create(b, nil)
	l := &library.Library{Name: "tv", Kind: library.TV}
	_ = library.New(d).Create(l, nil)
	ir := item.New(d)
	show := &item.Item{LibraryID: l.ID, Kind: item.Show, ParsedTitle: "S"}
	_ = ir.Create(show)
	one := 1
	season := &item.Item{LibraryID: l.ID, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "1", SeasonNumber: &one}
	_ = ir.Create(season)
	var eps []uid.ID
	for n := 1; n <= 3; n++ {
		n := n
		e := &item.Item{LibraryID: l.ID, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: "e", EpisodeNumber: &n}
		if err := ir.Create(e); err != nil {
			t.Fatal(err)
		}
		eps = append(eps, e.ID)
	}
	return watchstate.New(d), a.ID, b.ID, eps
}

func TestSaveGetDelete(t *testing.T) {
	r, a, b, eps := setup(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := r.Get(a, eps[0]); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Save(&watchstate.State{AccountID: a, ItemID: eps[0], PositionMs: 5000, LastPlayedAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(&watchstate.State{AccountID: a, ItemID: eps[0], PositionMs: 9000, PlayCount: 1, Watched: true, LastPlayedAt: &at}); err != nil {
		t.Fatalf("save must upsert: %v", err)
	}
	g, _ := r.Get(a, eps[0])
	if g.PositionMs != 9000 || !g.Watched || g.PlayCount != 1 || !g.LastPlayedAt.Equal(at) {
		t.Fatalf("%+v", g)
	}
	if _, err := r.Get(b, eps[0]); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("state must be per account")
	}
	if err := r.Save(&watchstate.State{AccountID: uid.N(999), ItemID: eps[0]}); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	if err := r.Delete(a, eps[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(a, eps[0]); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

// A write that says when it happened applies only over an older one, so a
// device syncing what it played offline neither undoes a later change nor
// is undone by an earlier one.
func TestSaveAtKeepsTheNewer(t *testing.T) {
	r, a, _, eps := setup(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if ok, err := r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[0], PositionMs: 1000}, now.Add(-time.Hour)); err != nil || !ok {
		t.Fatalf("the first write: %v %v", ok, err)
	}
	if ok, err := r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[0], Watched: true}, now); err != nil || !ok {
		t.Fatalf("a newer write: %v %v", ok, err)
	}
	if ok, err := r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[0], PositionMs: 2000}, now.Add(-time.Minute)); err != nil || ok {
		t.Fatalf("an older write is ignored: %v %v", ok, err)
	}
	g, err := r.Get(a, eps[0])
	if err != nil || !g.Watched || g.PositionMs != 0 || !g.ChangedAt.Equal(now) {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestListPagesMostRecentFirst(t *testing.T) {
	r, a, _, eps := setup(t)
	base := time.Now().UTC()
	for i, id := range eps {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := r.Save(&watchstate.State{AccountID: a, ItemID: id, LastPlayedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := r.List(a, nil, item.Audience{}, 2, 0)
	if err != nil || len(first) != 2 || first[0].ItemID != eps[2] {
		t.Fatalf("%v %v", first, err)
	}
	rest, _ := r.List(a, nil, item.Audience{}, 2, 2)
	if len(rest) != 1 || rest[0].ItemID != eps[0] {
		t.Fatalf("%v", rest)
	}
}

func TestDismissAtForgetsOnlyThePosition(t *testing.T) {
	r, a, _, eps := setup(t)
	t0 := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	played := at(0)
	// A second watch in progress: watched, counted once, at 40s.
	if _, err := r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[0], PositionMs: 40_000, Watched: true, PlayCount: 1, LastPlayedAt: &played}, at(0)); err != nil {
		t.Fatal(err)
	}
	if err := r.DismissAt(a, eps[0], at(1)); err != nil {
		t.Fatal(err)
	}
	g, _ := r.Get(a, eps[0])
	if g.PositionMs != 0 || !g.Watched || g.PlayCount != 1 || !g.LastPlayedAt.Equal(played) || !g.ChangedAt.Equal(at(1)) {
		t.Fatalf("dismiss must keep all but the position: %+v", g)
	}
	// Already out of progress: a no-op that keeps changedAt.
	if err := r.DismissAt(a, eps[0], at(2)); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(a, eps[0]); !g.ChangedAt.Equal(at(1)) {
		t.Fatalf("a dismiss of nothing in progress must not touch the state: %+v", g)
	}
	if err := r.DismissAt(a, eps[1], at(2)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("never played: %v", err)
	}
}

func TestDismissAtOrdersByInstant(t *testing.T) {
	r, a, _, eps := setup(t)
	t0 := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	// Progress later than the dismiss wins: playing again un-dismisses.
	_, _ = r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[0], PositionMs: 9000}, at(5))
	if err := r.DismissAt(a, eps[0], at(4)); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(a, eps[0]); g.PositionMs != 9000 {
		t.Fatalf("an older dismiss must lose: %+v", g)
	}
	// A dismiss later than a mark keeps the mark: never unwatched.
	_, _ = r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[1], PositionMs: 9000, Watched: true, PlayCount: 1}, at(1))
	if err := r.DismissAt(a, eps[1], at(2)); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(a, eps[1]); g.PositionMs != 0 || !g.Watched || g.PlayCount != 1 {
		t.Fatalf("%+v", g)
	}
	// A play the server did not see, later than the dismiss, counts on top of it.
	_, _ = r.SaveAt(&watchstate.State{AccountID: a, ItemID: eps[2], PositionMs: 9000}, at(1))
	_ = r.DismissAt(a, eps[2], at(2))
	g, err := r.RecordPlay(a, eps[2], nil, at(3))
	if err != nil || !g.Watched || g.PlayCount != 1 || g.PositionMs != 0 {
		t.Fatalf("%+v %v", g, err)
	}
}

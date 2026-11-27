package review_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A review raised again keeps its id, its age and its dismissal and takes
// the new evidence; reviews go with their item, and a file's review is its
// own subject.
func TestRaiseSettleAndSweep(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	l := &library.Library{Name: "L", Kind: library.TV, ScanHour: 3}
	must(t, library.New(d).Create(l, nil))
	items := item.New(d)
	show := &item.Item{LibraryID: l.ID, Kind: item.Show, Path: "/tv/The Office", ParsedTitle: "The Office"}
	must(t, items.Create(show))
	r := review.New(d)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	year := 2001
	rv := &review.Review{LibraryID: l.ID, ItemID: &show.ID, Reason: review.TiedCandidates, RaisedAt: t0, Evidence: []review.Evidence{
		{Role: review.Candidate, Provider: "tmdb", ExternalID: "2316", Title: "The Office", Country: "US"},
		{Role: review.Candidate, Provider: "tmdb", ExternalID: "2996", Title: "The Office", Year: &year, Country: "GB"}}}
	must(t, r.Raise(rv))
	loose := &review.Review{LibraryID: l.ID, Path: "/tv/loose.mkv", Reason: review.Unrecognized, RaisedAt: t0.Add(time.Minute)}
	must(t, r.Raise(loose))
	must(t, r.SetDismissed(rv.ID, &t0))

	again := &review.Review{LibraryID: l.ID, ItemID: &show.ID, Reason: review.TiedCandidates, RaisedAt: t0.Add(time.Hour),
		Evidence: []review.Evidence{{Role: review.Stated, Provider: "tvdb", ExternalID: "78107", Path: "/tv/The Office"}}}
	must(t, r.Raise(again))
	got, err := r.Get(rv.ID)
	must(t, err)
	if again.ID != rv.ID || !got.RaisedAt.Equal(t0) || got.DismissedAt == nil || len(got.Evidence) != 1 || got.Evidence[0].ExternalID != "78107" ||
		got.ItemKind != item.Show || got.Title != "The Office" {
		t.Fatalf("raised again: %+v", got)
	}

	yes, no := true, false
	all, err := r.List(review.Filter{LibraryID: &l.ID})
	must(t, err)
	open, err := r.List(review.Filter{Dismissed: &no})
	must(t, err)
	dismissed, err := r.List(review.Filter{Dismissed: &yes, Reason: review.TiedCandidates})
	must(t, err)
	if len(all) != 2 || all[0].ID != loose.ID || all[0].Path != "/tv/loose.mkv" || all[0].ItemID != nil ||
		len(open) != 1 || open[0].ID != loose.ID || len(dismissed) != 1 || dismissed[0].ID != rv.ID {
		t.Fatalf("all %+v open %+v dismissed %+v", all, open, dismissed)
	}

	must(t, r.Settle(show.ID, review.TiedCandidates, review.TiedCandidates, review.NoMatch))
	if _, err := r.Get(rv.ID); err != nil {
		t.Fatalf("the kept reason goes: %v", err)
	}
	must(t, r.Settle(show.ID, "", review.TiedCandidates, review.NoMatch))
	if _, err := r.Get(rv.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("settled review stays: %v", err)
	}
	must(t, r.Sweep(l.ID, review.Unrecognized, []uid.ID{loose.ID}))
	if _, err := r.Get(loose.ID); err != nil {
		t.Fatalf("a kept review is swept: %v", err)
	}
	must(t, r.Sweep(l.ID, review.Unrecognized, nil))
	must(t, r.Raise(rv))
	must(t, items.Delete(show.ID))
	if left, err := r.List(review.Filter{}); err != nil || len(left) != 0 {
		t.Fatalf("left %+v %v", left, err)
	}
}

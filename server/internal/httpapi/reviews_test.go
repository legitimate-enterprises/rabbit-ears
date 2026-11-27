package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// show makes a show with one episode matched to TMDB id as a scan leaves
// them: remote rows on both.
func (e *env) matchedShow(lib uid.ID, tmdbID int64) (show, ep uid.ID) {
	e.t.Helper()
	show, ep = e.show(lib, "The Office", "TV-14")
	now := time.Now().UTC()
	must(e.t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: show, Status: item.Matched, ProviderID: &tmdbID, Title: "The Office", FetchedAt: &now}))
	must(e.t, e.svc.Items.Repo().SetIdentityStamp(show, "the office"))
	must(e.t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: ep, Status: item.Matched, Title: "Pilot", FetchedAt: &now}))
	return show, ep
}

func TestIdentityReviewsAdminReadAndDismiss(t *testing.T) {
	e := newEnv(t)
	lib := e.library("tv")
	show, _ := e.matchedShow(lib, 2316)
	year := 2001
	raised := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	of := &review.Review{LibraryID: lib, ItemID: &show, Reason: review.ConflictingIdentity, RaisedAt: raised, Evidence: []review.Evidence{
		{Role: review.Current, Provider: "tmdb", ExternalID: "2316", Title: "The Office", Year: &year},
		{Role: review.Stated, Provider: "tvdb", ExternalID: "78107", Path: "/tv/The Office (2001) {tvdb-78107}"},
		{Role: review.Candidate, Provider: "tmdb", ExternalID: "2996", Title: "The Office", Year: &year, Country: "GB"}}}
	must(t, e.svc.Reviews.Repo().Raise(of))
	loose := &review.Review{LibraryID: lib, Path: "/tv/stray.mkv", Reason: review.Unrecognized, RaisedAt: raised.Add(time.Hour)}
	must(t, e.svc.Reviews.Repo().Raise(loose))

	var page api.IdentityReviewPage
	e.expect(200, "GET", "/identity-reviews", e.admin.token, nil, &page)
	if len(page.Items) != 2 || page.Items[0].Id != loose.ID || page.Items[0].ItemId != nil || *page.Items[0].Path != "/tv/stray.mkv" || page.Items[1].Id != of.ID {
		t.Fatalf("newest first: %+v", page)
	}
	page = api.IdentityReviewPage{}
	e.expect(200, "GET", "/identity-reviews?reason=conflicting_identity&libraryId="+itoa(lib), e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != of.ID {
		t.Fatalf("%+v", page)
	}
	page = api.IdentityReviewPage{}
	e.expect(200, "GET", "/identity-reviews?libraryId="+itoa(uid.N(999)), e.admin.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("%+v", page)
	}
	page = api.IdentityReviewPage{}
	e.expect(200, "GET", "/identity-reviews?limit=1", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("%+v", page)
	}
	e.expect(400, "GET", "/identity-reviews?reason=whim", e.admin.token, nil, nil)

	one := "/identity-reviews/" + itoa(of.ID)
	var got api.IdentityReview
	e.expect(200, "GET", one, e.admin.token, nil, &got)
	if *got.ItemId != show || *got.ItemKind != "show" || *got.Title != "The Office" || got.Path != nil || !got.RaisedAt.Equal(raised) || got.DismissedAt != nil || len(got.Evidence) != 3 {
		t.Fatalf("%+v", got)
	}
	if ev := got.Evidence[2]; ev.Role != api.Candidate || *ev.Provider != api.IdentityEvidenceProviderTmdb || *ev.ExternalId != "2996" || *ev.Year != 2001 || *ev.Country != "GB" || ev.Path != nil {
		t.Fatalf("%+v", ev)
	}

	got = api.IdentityReview{}
	e.expect(200, "PUT", one, e.admin.token, map[string]any{"dismissed": true}, &got)
	if got.DismissedAt == nil {
		t.Fatalf("%+v", got)
	}
	// A scan raising it again leaves it dismissed.
	must(t, e.svc.Reviews.Repo().Raise(of))
	page = api.IdentityReviewPage{}
	e.expect(200, "GET", "/identity-reviews?dismissed=false", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != loose.ID {
		t.Fatalf("open: %+v", page)
	}
	page = api.IdentityReviewPage{}
	e.expect(200, "GET", "/identity-reviews?dismissed=true", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != of.ID {
		t.Fatalf("dismissed: %+v", page)
	}
	got = api.IdentityReview{}
	e.expect(200, "PUT", one, e.admin.token, map[string]any{"dismissed": false}, &got)
	if got.DismissedAt != nil {
		t.Fatalf("%+v", got)
	}
	e.expect(400, "PUT", one, e.admin.token, map[string]any{}, nil)
	e.expect(404, "GET", "/identity-reviews/"+itoa(uid.N(999)), e.admin.token, nil, nil)
	e.expect(404, "PUT", "/identity-reviews/"+itoa(uid.N(999)), e.admin.token, map[string]any{"dismissed": true}, nil)
	e.expect(403, "GET", "/identity-reviews", e.plain.token, nil, nil)
	e.expect(403, "GET", one, e.plain.token, nil, nil)
	e.expect(403, "PUT", one, e.plain.token, map[string]any{"dismissed": true}, nil)
}

// Matching a show by hand to another title ends the reviews of its
// identity, drops what described the other title's episodes and makes the
// new title's details due; editing the match it has does none of that.
func TestRematchByHandRefreshesDependents(t *testing.T) {
	e := newEnv(t)
	lib := e.library("tv")
	show, ep := e.matchedShow(lib, 2316)
	raise := func() {
		must(t, e.svc.Reviews.Repo().Raise(&review.Review{LibraryID: lib, ItemID: &show, Reason: review.ConflictingIdentity, RaisedAt: time.Now().UTC()}))
	}
	raise()
	stray := &review.Review{LibraryID: lib, Path: "/tv/stray.mkv", Reason: review.Unrecognized, RaisedAt: time.Now().UTC()}
	must(t, e.svc.Reviews.Repo().Raise(stray))
	due := func() int {
		ms, err := e.svc.Items.Repo().DueForRetry(time.Now().Add(time.Minute), 10)
		must(t, err)
		return len(ms)
	}
	reviews := func() int {
		rs, err := e.svc.Reviews.Repo().List(review.Filter{})
		must(t, err)
		return len(rs)
	}
	id := "/items/" + itoa(show) + "/metadata"

	e.expect(200, "PUT", id, e.admin.token, map[string]any{"status": "matched", "providerId": 2316, "title": "The Office (edited)"}, nil)
	if _, err := e.svc.Items.Repo().Remote(ep); err != nil || due() != 0 || reviews() != 2 {
		t.Fatalf("an edit of the same match: episode %v, %d due, %d reviews", err, due(), reviews())
	}

	e.expect(200, "PUT", id, e.admin.token, map[string]any{"status": "matched", "providerId": 2996, "title": "The Office"}, nil)
	stamp, err := e.svc.Items.Repo().IdentityStamp(show)
	must(t, err)
	if _, err := e.svc.Items.Repo().Remote(ep); err == nil || due() != 1 || reviews() != 1 || stamp != "" {
		t.Fatalf("a rematch: episode row %v, %d due, %d reviews, stamp %q", err, due(), reviews(), stamp)
	}

	raise()
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: ep, Status: item.Matched, Title: "Downsize"}))
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	if _, err := e.svc.Items.Repo().Remote(ep); err == nil || reviews() != 1 {
		t.Fatalf("an unmatch: episode row %v, %d reviews", err, reviews())
	}
}

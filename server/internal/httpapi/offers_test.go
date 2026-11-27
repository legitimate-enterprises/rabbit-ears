package httpapi_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (e *env) offers(token string) api.ProfileOfferList {
	e.t.Helper()
	var l api.ProfileOfferList
	e.expect(200, "GET", "/household/offers", token, nil, &l)
	return l
}

func (e *env) offer(want int, token string, profile, to uid.ID) api.ProfileOffer {
	e.t.Helper()
	var o api.ProfileOffer
	e.expect(want, "POST", "/household/offers", token, map[string]any{"profileId": profile, "toProfileId": to}, &o)
	return o
}

func offerIDs(os []api.ProfileOffer) []uid.ID {
	var out []uid.ID
	for _, o := range os {
		out = append(out, o.Id)
	}
	return out
}

func heardHousehold(evs []api.Event) bool {
	return slices.ContainsFunc(evs, func(ev api.Event) bool { return ev.Kind == api.EventKindHousehold })
}

// A household's devices hear of a profile put into it or taken out.
func TestAHouseholdHearsOfProfilesComingAndGoing(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.admin, "Rex")
	c := e.cursor(e.plain.token)
	e.putIn(k.Id, e.plain.household)
	if !heardHousehold(e.heard(e.plain.token, &c, "&kinds=household")) {
		t.Fatal("not told of a profile put in")
	}
	e.expect(204, "DELETE", "/profiles/"+itoa(k.Id), e.plain.token, nil, nil)
	if !heardHousehold(e.heard(e.plain.token, &c, "&kinds=household")) {
		t.Fatal("not told of a profile gone")
	}
}

// An offer goes when its kid leaves the offering household or is made an
// adult.
func TestAnOfferGoesWithTheKidsPlaceInTheHousehold(t *testing.T) {
	e := newEnv(t)
	e.expect(201, "POST", "/adult-links", e.admin.token, map[string]any{"accountId": e.plain.account.ID, "otherAccountId": e.admin.account.ID}, nil)
	third := e.adult("third@example.test", false)
	zed, ada := e.kid(e.plain, "Zed"), e.kid(e.plain, "Ada")
	e.putIn(zed.Id, third.household)
	e.offer(201, e.plain.token, zed.Id, e.admin.account.ID)
	e.offer(201, e.plain.token, ada.Id, e.admin.account.ID)
	e.expect(204, "DELETE", memberPath(e.plain.household, zed.Id), e.admin.token, nil, nil)
	e.expect(201, "POST", "/profiles/"+itoa(ada.Id)+"/make-adult", e.plain.token, map[string]any{"email": "ada@example.test"}, nil)
	if got := e.offers(e.admin.token); len(got.Incoming) != 0 {
		t.Fatalf("offers outlived the kid's place: %+v", got.Incoming)
	}
}

// An adult offers a kid of their household to an adult an admin linked them
// with, who alone accepts it into their own household; both hear of it.
func TestAnAdultOffersAKidToALinkedAdult(t *testing.T) {
	e := newEnv(t)
	plain, admin := e.plain.account.ID, e.admin.account.ID
	mia := e.kid(e.plain, "Mia")
	e.offer(403, e.plain.token, mia.Id, admin)
	e.expect(201, "POST", "/adult-links", e.admin.token, map[string]any{"accountId": plain, "otherAccountId": admin}, nil)

	var linked api.AccountPage
	e.expect(200, "GET", "/household/linked-adults", e.plain.token, nil, &linked)
	if len(linked.Items) != 1 || linked.Items[0].Id != admin || linked.Items[0].Email != nil || linked.Items[0].HouseholdId != nil {
		t.Fatalf("the linked adult, without email or household: %+v", linked.Items)
	}
	tv, _ := e.secondDevice(e.plain, "TV", "roku")
	e.expect(200, "GET", "/household/linked-adults", tv, nil, &linked)
	if len(linked.Items) != 0 {
		t.Fatalf("a link off TVs lists on a TV: %+v", linked.Items)
	}
	e.offer(403, tv, mia.Id, admin)
	e.expect(403, "GET", "/household/offers", e.kidDevice(e.plain, mia.Id), nil, nil)
	cy := e.member(e.plain, "Cy", "cy@example.test")
	guest := e.adult("guest@example.test", false)
	e.putIn(guest.account.ID, e.plain.household)
	e.offer(403, e.plain.token, guest.account.ID, admin)
	var inv api.HouseholdInvite
	e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Dee", "email": "dee@example.test"}, &inv)
	e.offer(409, e.plain.token, inv.Account.Id, admin)
	e.offer(404, e.plain.token, admin, admin)

	theirs, ours := e.cursor(e.admin.token), e.cursor(cy.token)
	o := e.offer(201, e.plain.token, mia.Id, admin)
	if o.ProfileName != "Mia" || o.HouseholdId != e.plain.household.ID || o.FromProfileId != plain || o.ToProfileId != admin {
		t.Fatalf("%+v", o)
	}
	if !heardHousehold(e.heard(e.admin.token, &theirs, "&kinds=household")) || !heardHousehold(e.heard(cy.token, &ours, "&kinds=household")) {
		t.Fatal("both sides hear of an offer")
	}
	e.offer(409, e.plain.token, mia.Id, admin)
	if got := e.offers(e.admin.token); !slices.Equal(offerIDs(got.Incoming), []uid.ID{o.Id}) || len(got.Outgoing) != 0 {
		t.Fatalf("incoming to the adult offered to: %+v", got)
	}
	if got := e.offers(cy.token); len(got.Incoming) != 0 || !slices.Equal(offerIDs(got.Outgoing), []uid.ID{o.Id}) {
		t.Fatalf("outgoing to the offering household's adults: %+v", got)
	}
	e.expect(403, "POST", "/household/offers/"+itoa(o.Id)+"/accept", e.plain.token, nil, nil)
	e.expect(204, "POST", "/household/offers/"+itoa(o.Id)+"/accept", e.admin.token, nil, nil)
	if !slices.Contains(profileIDs(e, e.admin.token), mia.Id) || !slices.Contains(profileIDs(e, e.plain.token), mia.Id) {
		t.Fatal("an accepted kid stands in both households")
	}
	e.expect(404, "POST", "/household/offers/"+itoa(o.Id)+"/accept", e.admin.token, nil, nil)
	e.offer(409, e.plain.token, mia.Id, admin)

	self := e.offer(201, e.plain.token, plain, admin)
	e.expect(204, "DELETE", "/household/offers/"+itoa(self.Id), e.admin.token, nil, nil)
	self = e.offer(201, e.plain.token, plain, admin)
	e.expect(204, "DELETE", "/household/offers/"+itoa(self.Id), cy.token, nil, nil)
	self = e.offer(201, e.plain.token, plain, admin)
	e.expect(204, "POST", "/household/offers/"+itoa(self.Id)+"/accept", e.admin.token, nil, nil)
	var p api.Account
	e.expect(200, "GET", "/profiles/"+itoa(plain), e.admin.token, nil, &p)
	if p.Present == nil || !*p.Present {
		t.Fatalf("an accepted adult is present: %+v", p)
	}

	e.offer(201, e.plain.token, cy.account.ID, admin)
	ivy := e.kid(e.plain, "Ivy")
	e.offer(201, e.plain.token, ivy.Id, admin)
	e.expect(204, "DELETE", "/adult-links/"+itoa(plain)+"/"+itoa(admin), e.admin.token, nil, nil)
	if got := e.offers(e.plain.token); len(got.Outgoing) != 0 {
		t.Fatalf("unlinking leaves offers: %+v", got)
	}
}

// A household's devices hear of a kid added or edited, as a lowered
// ceiling, so a kid's open device reads its profile again.
func TestAHouseholdHearsOfAKidAddedOrEdited(t *testing.T) {
	e := newEnv(t)
	c := e.cursor(e.plain.token)
	k := e.kid(e.plain, "Rex")
	if !heardHousehold(e.heard(e.plain.token, &c, "&kinds=household")) {
		t.Fatal("not told of a kid added")
	}
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id), e.plain.token, map[string]any{"name": "Rex", "birthday": "2018-05-01", "movieCeiling": "G", "tvCeiling": "TV-Y"}, nil)
	if !heardHousehold(e.heard(e.plain.token, &c, "&kinds=household")) {
		t.Fatal("not told of a kid edited")
	}
	e.expect(200, "PUT", "/accounts/"+itoa(k.Id), e.admin.token, map[string]any{"name": "Rex", "kind": "kid", "movieCeiling": "PG", "tvCeiling": "TV-Y"}, nil)
	if !heardHousehold(e.heard(e.plain.token, &c, "&kinds=household")) {
		t.Fatal("not told of an admin's edit of a kid")
	}
}

// A profile's other devices hear that it now hides kids' content, a preference kept per profile.
func TestAHouseholdHearsOfAProfileHidingKidsContent(t *testing.T) {
	e := newEnv(t)
	c := e.cursor(e.plain.token)
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/preferences", e.plain.token, map[string]any{"hideKidsContent": true}, nil)
	if !heardHousehold(e.heard(e.plain.token, &c, "&kinds=household")) {
		t.Fatal("not told of the preference")
	}
}

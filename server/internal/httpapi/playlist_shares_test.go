package httpapi_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

// sharedSetup is plain's movies playlist holding a G and an R film, each
// with a file, and the routes for it under the owner and under another
// profile.
type sharedSetup struct {
	pl         api.Playlist
	g, r       int64
	owner      string
	under      func(profile int64) string
	sharesPath string
}

func (e *env) sharedSetup() sharedSetup {
	e.t.Helper()
	films := e.library("movies")
	s := sharedSetup{g: e.film(films, "Gentle", "2001-01-01", "G"), r: e.film(films, "Rough", "2002-01-01", "R")}
	e.file(s.g, 60000)
	e.file(s.r, 60000)
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Family", "kind": "movies"}, &s.pl)
	s.under = func(profile int64) string { return "/profiles/" + itoa(profile) + "/playlists/" + itoa(s.pl.Id) }
	s.owner = s.under(e.plain.account.ID)
	s.sharesPath = s.owner + "/shares"
	for _, id := range []int64{s.g, s.r} {
		e.expect(201, "POST", s.owner+"/entries", e.plain.token, map[string]any{"itemId": id}, nil)
	}
	return s
}

func (e *env) share(s sharedSetup, profile int64, access string) {
	e.t.Helper()
	e.expect(201, "POST", s.sharesPath, e.plain.token, map[string]any{"profileId": profile, "access": access}, nil)
}

func listed(t *testing.T, page api.PlaylistPage, id int64) *api.Playlist {
	t.Helper()
	for i := range page.Items {
		if page.Items[i].Id == id {
			return &page.Items[i]
		}
	}
	return nil
}

func TestPlaylistSharesCRUD(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	tok, other := e.plain.token, e.admin.account.ID
	e.link(e.plain.account.ID, other, false)

	var sh api.PlaylistShare
	e.expect(201, "POST", s.sharesPath, tok, map[string]any{"profileId": other, "access": "read"}, &sh)
	if sh.PlaylistId != s.pl.Id || sh.ProfileId != other || sh.Name != e.admin.account.Name || sh.Kind != api.Adult ||
		sh.Access != api.PlaylistShareAccessRead || sh.CreatedAt.IsZero() {
		t.Fatalf("%+v", sh)
	}
	e.expect(409, "POST", s.sharesPath, tok, map[string]any{"profileId": other, "access": "write"}, nil)
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": e.plain.account.ID, "access": "read"}, nil)
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": 999999, "access": "read"}, nil)
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": other, "access": "owner"}, nil)

	var page api.PlaylistSharePage
	e.expect(200, "GET", s.sharesPath, tok, nil, &page)
	if len(page.Items) != 1 || page.Items[0].ProfileId != other {
		t.Fatalf("%+v", page)
	}
	one := s.sharesPath + "/" + itoa(other)
	e.expect(200, "GET", one, tok, nil, &sh)
	e.expect(200, "PUT", one, tok, map[string]any{"access": "write"}, &sh)
	if sh.Access != api.PlaylistShareAccessWrite {
		t.Fatalf("%+v", sh)
	}
	e.expect(200, "GET", one, tok, nil, &sh)
	if sh.Access != api.PlaylistShareAccessWrite {
		t.Fatalf("not stored %+v", sh)
	}

	// Only the owner reads or changes the shares, write access or not.
	theirs := s.under(other) + "/shares"
	e.expect(403, "GET", theirs, e.admin.token, nil, nil)
	e.expect(403, "GET", theirs+"/"+itoa(other), e.admin.token, nil, nil)
	e.expect(403, "POST", theirs, e.admin.token, map[string]any{"profileId": e.plain.account.ID, "access": "read"}, nil)
	e.expect(403, "PUT", theirs+"/"+itoa(other), e.admin.token, map[string]any{"access": "read"}, nil)
	e.expect(403, "DELETE", theirs+"/"+itoa(other), e.admin.token, nil, nil)

	e.expect(204, "DELETE", one, tok, nil, nil)
	e.expect(404, "GET", one, tok, nil, nil)
	e.expect(404, "PUT", one, tok, map[string]any{"access": "read"}, nil)
	e.expect(404, "DELETE", one, tok, nil, nil)
	e.expect(404, "GET", s.under(other), e.admin.token, nil, nil)

	// A station's own playlist is listed nowhere and shared with no one.
	var st api.Station
	e.expect(201, "POST", "/stations", tok, map[string]any{"name": "Own", "kind": "movies"}, &st)
	e.expect(409, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(st.PlaylistId)+"/shares", tok,
		map[string]any{"profileId": other, "access": "read"}, nil)
}

func TestSharedPlaylistReadOnly(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	reader := e.admin // another household
	e.link(e.plain.account.ID, reader.account.ID, false)
	e.share(s, reader.account.ID, "read")
	mine := s.under(reader.account.ID)
	tok := reader.token

	var page api.PlaylistPage
	e.expect(200, "GET", "/profiles/"+itoa(reader.account.ID)+"/playlists", tok, nil, &page)
	got := listed(t, page, s.pl.Id)
	if got == nil || got.Access != api.PlaylistAccessRead || got.OwnerName == nil || *got.OwnerName != e.plain.account.Name ||
		got.ProfileId != e.plain.account.ID || got.ItemCount != 2 {
		t.Fatalf("listed as shared by its owner: %+v", page)
	}
	e.expect(200, "GET", "/profiles/"+itoa(reader.account.ID)+"/playlists?kind=tv", tok, nil, &page)
	if listed(t, page, s.pl.Id) != nil {
		t.Fatal("the kind filter applies to shared playlists")
	}
	e.expect(200, "GET", mine, tok, nil, nil)
	var entries api.PlaylistEntryPage
	e.expect(200, "GET", mine+"/entries", tok, nil, &entries)
	if len(entries.Items) != 2 {
		t.Fatalf("%+v", entries)
	}
	e.expect(200, "GET", mine+"/entries/"+itoa(entries.Items[0].Id), tok, nil, nil)
	e.expect(200, "GET", mine+"/play-order", tok, nil, nil)

	e.expect(403, "POST", mine+"/entries", tok, map[string]any{"itemId": s.g}, nil)
	e.expect(403, "PUT", mine+"/entries/"+itoa(entries.Items[0].Id), tok, map[string]any{"itemId": s.g, "position": 1}, nil)
	e.expect(403, "DELETE", mine+"/entries/"+itoa(entries.Items[0].Id), tok, nil, nil)
	e.expect(403, "PUT", mine, tok, map[string]any{"name": "Mine now", "kind": "movies"}, nil)
	e.expect(403, "DELETE", mine, tok, nil, nil)

	// Read access plays it and makes it a station's source.
	e.expect(201, "POST", "/playback/sessions", tok, map[string]any{"profileId": reader.account.ID, "itemId": s.g,
		"playlistId": s.pl.Id, "snapshot": snapshot()}, nil)
	var st api.Station
	e.expect(201, "POST", "/stations", tok, map[string]any{"name": "Borrowed", "playlistId": s.pl.Id}, &st)
	if st.PlaylistId != s.pl.Id || st.OwnerId != reader.account.ID {
		t.Fatalf("%+v", st)
	}

	// The owner's view is unchanged.
	var own api.Playlist
	e.expect(200, "GET", s.owner, e.plain.token, nil, &own)
	if own.Access != api.PlaylistAccessOwner || own.OwnerName != nil {
		t.Fatalf("%+v", own)
	}

	// Unsharing takes the playlist away, and the station built on it.
	e.expect(204, "DELETE", s.sharesPath+"/"+itoa(reader.account.ID), e.plain.token, nil, nil)
	e.expect(404, "GET", mine, tok, nil, nil)
	e.expect(404, "GET", "/stations/"+itoa(st.Id), tok, nil, nil)
	e.expect(404, "POST", "/playback/sessions", tok, map[string]any{"profileId": reader.account.ID, "itemId": s.g,
		"playlistId": s.pl.Id, "snapshot": snapshot()}, nil)
}

func TestSharedPlaylistReadWrite(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	writer := e.householdAdult(e.plain, "Partner", "partner@example.test")
	e.share(s, writer.Me.Account.Id, "write")
	mine, tok := s.under(writer.Me.Account.Id), writer.Token

	var en api.PlaylistEntry
	e.expect(201, "POST", mine+"/entries", tok, map[string]any{"itemId": s.g, "position": 0}, &en)
	e.expect(200, "PUT", mine+"/entries/"+itoa(en.Id), tok, map[string]any{"itemId": s.g, "position": 2}, &en)
	if en.Position != 2 {
		t.Fatalf("%+v", en)
	}
	var page api.PlaylistEntryPage
	e.expect(200, "GET", s.owner+"/entries", e.plain.token, nil, &page)
	if !slices.Equal(entryIDs(page), []int64{s.g, s.r, s.g}) {
		t.Fatalf("the owner sees the writer's edits: %v", entryIDs(page))
	}
	e.expect(204, "DELETE", mine+"/entries/"+itoa(en.Id), tok, nil, nil)

	var got api.Playlist
	e.expect(200, "GET", mine, tok, nil, &got)
	if got.Access != api.PlaylistAccessWrite {
		t.Fatalf("%+v", got)
	}
	e.expect(403, "PUT", mine, tok, map[string]any{"name": "Renamed", "kind": "movies"}, nil)
	e.expect(403, "DELETE", mine, tok, nil, nil)
	e.expect(403, "POST", mine+"/shares", tok, map[string]any{"profileId": e.admin.account.ID, "access": "read"}, nil)
	e.expect(200, "GET", s.owner, e.plain.token, nil, &got)
	if got.Name != "Family" {
		t.Fatalf("%+v", got)
	}
}

func TestPlaylistNotSharedIsNotFound(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	e.link(e.plain.account.ID, e.admin.account.ID, false)
	e.share(s, e.admin.account.ID, "write")
	stranger := e.adult("stranger@example.test", false)
	mine, tok := s.under(stranger.account.ID), stranger.token

	var page api.PlaylistPage
	e.expect(200, "GET", "/profiles/"+itoa(stranger.account.ID)+"/playlists", tok, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("%+v", page)
	}
	e.expect(404, "GET", mine, tok, nil, nil)
	e.expect(404, "GET", mine+"/entries", tok, nil, nil)
	e.expect(404, "GET", mine+"/play-order", tok, nil, nil)
	e.expect(404, "POST", mine+"/entries", tok, map[string]any{"itemId": s.g}, nil)
	e.expect(404, "PUT", mine, tok, map[string]any{"name": "X", "kind": "movies"}, nil)
	e.expect(404, "DELETE", mine, tok, nil, nil)
	e.expect(404, "GET", mine+"/shares", tok, nil, nil)
	e.expect(400, "POST", "/stations", tok, map[string]any{"name": "X", "playlistId": s.pl.Id}, nil)
	// A share is the recipient's alone: the owner's route under the
	// recipient's profile is another household's profile.
	e.expect(404, "GET", s.under(e.admin.account.ID), tok, nil, nil)
}

func TestPlaylistSharedWithKids(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	k := e.kid(e.plain, "Kid") // PG ceiling
	theirKid := e.kid(e.admin, "Neighbor kid")
	e.expect(400, "POST", s.sharesPath, e.plain.token, map[string]any{"profileId": theirKid.Id, "access": "read"}, nil)
	e.expect(400, "POST", s.sharesPath, e.plain.token, map[string]any{"profileId": k.Id, "access": "write"}, nil)
	e.share(s, k.Id, "read")
	e.expect(400, "PUT", s.sharesPath+"/"+itoa(k.Id), e.plain.token, map[string]any{"access": "write"}, nil)

	var unshared api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Grown-ups", "kind": "movies"}, &unshared)

	tok := e.kidDevice(e.plain, k.Id)
	mine := s.under(k.Id)
	var page api.PlaylistPage
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/playlists", tok, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != s.pl.Id || page.Items[0].ItemCount != 1 || page.Items[0].Access != api.PlaylistAccessRead {
		t.Fatalf("a kid lists only what is shared with them, counted as they may see it: %+v", page)
	}
	var entries api.PlaylistEntryPage
	e.expect(200, "GET", mine+"/entries", tok, nil, &entries)
	if !slices.Equal(entryIDs(entries), []int64{s.g}) {
		t.Fatalf("a kid gets only the entries their filters allow: %v", entryIDs(entries))
	}
	var all api.PlaylistEntryPage
	e.expect(200, "GET", s.owner+"/entries", e.plain.token, nil, &all)
	e.expect(404, "GET", mine+"/entries/"+itoa(all.Items[1].Id), tok, nil, nil)
	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", mine+"/play-order", tok, nil, &order)
	if items, _ := playIDs(order); !slices.Equal(items, []int64{s.g}) {
		t.Fatalf("%v", items)
	}
	e.expect(403, "POST", mine+"/entries", tok, map[string]any{"itemId": s.g}, nil)
	e.expect(403, "PUT", mine, tok, map[string]any{"name": "X", "kind": "movies"}, nil)
	e.expect(403, "GET", mine+"/shares", tok, nil, nil)
	e.expect(404, "GET", "/profiles/"+itoa(k.Id)+"/playlists/"+itoa(unshared.Id), tok, nil, nil)
}

// A kid's poster for a shared playlist is of the first entry they may see,
// never of a title their filter hides.
func TestPlaylistArtItemFollowsTheKidsFilter(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	k := e.kid(e.plain, "Kid") // PG ceiling
	var first api.PlaylistEntryPage
	e.expect(200, "GET", s.owner+"/entries", e.plain.token, nil, &first)
	e.expect(200, "PUT", s.owner+"/entries/"+itoa(first.Items[1].Id), e.plain.token, map[string]any{"itemId": s.r, "position": 0}, nil)
	e.share(s, k.Id, "read")

	var mine api.Playlist
	e.expect(200, "GET", s.owner, e.plain.token, nil, &mine)
	if mine.ArtItemId == nil || *mine.ArtItemId != s.r {
		t.Fatalf("the owner's poster is the first entry's: %v", mine.ArtItemId)
	}
	var theirs api.Playlist
	e.expect(200, "GET", s.under(k.Id), e.kidDevice(e.plain, k.Id), nil, &theirs)
	if theirs.ArtItemId == nil || *theirs.ArtItemId != s.g {
		t.Fatalf("a kid's poster is the first entry they may see: %v", theirs.ArtItemId)
	}
}

// labeled checks that a share sheet row of profile id is labeled with
// household want and shows the profile's color.
func (e *env) labeled(id int64, household *int64, color *string, want int64) {
	e.t.Helper()
	a, err := e.svc.Accounts.Repo().Get(id)
	must(e.t, err)
	if household == nil || *household != want || color == nil || *color != a.ShownColor() {
		e.t.Fatalf("profile %d labeled %v in %v, want %d in %s", id, household, color, want, a.ShownColor())
	}
}

func TestShareCandidates(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	theirKid := e.kid(e.admin, "Neighbor kid")
	partner := e.householdAdult(e.plain, "Partner", "partner@example.test")
	e.addAdult("pending@example.test", 7)
	stranger := e.adult("stranger@example.test", false)
	tv, _ := e.secondDevice(e.plain, "TV", "tvos")
	candidates := func(token string) map[int64]api.ShareCandidate {
		t.Helper()
		var page api.ShareCandidatePage
		e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/share-candidates", token, nil, &page)
		got := map[int64]api.ShareCandidate{}
		for _, c := range page.Items {
			got[c.ProfileId] = c
		}
		return got
	}

	got := candidates(e.plain.token)
	if len(got) != 2 || got[k.Id].Kind != api.Kid || got[partner.Me.Account.Id].HouseholdName != e.plain.household.Name {
		t.Fatalf("the household's joined members: %+v", got)
	}
	// An adult an admin linked, on a TV only when the link says so.
	e.link(e.plain.account.ID, e.admin.account.ID, false)
	if got := candidates(e.plain.token); len(got) != 3 || got[e.admin.account.ID].HouseholdName != e.admin.household.Name {
		t.Fatalf("linked: %+v", got)
	}
	if got := candidates(tv); len(got) != 2 {
		t.Fatalf("linked, not for TVs: %+v", got)
	}
	// Everyone in a household the owner was shared into.
	e.joins(e.admin.household.ID, e.plain.account.ID)
	got = candidates(tv)
	if _, ok := got[stranger.account.ID]; ok || len(got) != 4 || got[theirKid.Id].HouseholdName != e.admin.household.Name {
		t.Fatalf("another household of theirs: %+v", got)
	}
	// Each is labeled with the first of the owner's households, their own
	// first, that it is in, else its own.
	pid := partner.Me.Account.Id
	e.joins(e.admin.household.ID, pid)
	got = candidates(e.plain.token)
	names := map[int64]string{e.plain.household.ID: e.plain.household.Name, e.admin.household.ID: e.admin.household.Name}
	for id, want := range map[int64]int64{pid: e.plain.household.ID, k.Id: e.plain.household.ID, theirKid.Id: e.admin.household.ID,
		e.admin.account.ID: e.admin.household.ID} {
		c := got[id]
		e.labeled(id, c.HouseholdId, c.Color, want)
		if c.HouseholdName != names[want] {
			t.Fatalf("%+v", c)
		}
	}

	var page api.ShareCandidatePage
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/share-candidates?limit=1", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.NextOffset == nil {
		t.Fatalf("%+v", page)
	}
	e.expect(403, "GET", "/profiles/"+itoa(k.Id)+"/share-candidates", e.plain.token, nil, nil)
	e.expect(403, "GET", "/profiles/"+itoa(k.Id)+"/share-candidates", e.kidDevice(e.plain, k.Id), nil, nil)
}

func TestDeletingASharedPlaylist(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	e.link(e.plain.account.ID, e.admin.account.ID, false)
	e.share(s, e.admin.account.ID, "read")
	var theirs api.Station
	e.expect(201, "POST", "/stations", e.admin.token, map[string]any{"name": "Borrowed", "playlistId": s.pl.Id}, &theirs)

	var own api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Mine", "playlistId": s.pl.Id}, &own)
	e.expect(409, "DELETE", s.owner, e.plain.token, nil, nil)
	e.expect(204, "DELETE", "/stations/"+itoa(own.Id), e.plain.token, nil, nil)

	// A recipient's station never keeps the owner from deleting.
	e.expect(204, "DELETE", s.owner, e.plain.token, nil, nil)
	e.expect(404, "GET", "/stations/"+itoa(theirs.Id), e.admin.token, nil, nil)
	e.expect(404, "GET", s.under(e.admin.account.ID), e.admin.token, nil, nil)
}

// link is an admin letting two adults of different households see each
// other, on TVs too when tv is set.
func (e *env) link(a, b int64, tv bool) {
	e.t.Helper()
	_, err := e.db.Exec(`INSERT INTO adult_link (a_id, b_id, tv, created_at) VALUES (?,?,?,0)`, min(a, b), max(a, b), tv)
	must(e.t, err)
}

func TestShareTargetsAreThoseTheOwnerSees(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	tok, admin := e.plain.token, e.admin.account.ID
	tv, _ := e.secondDevice(e.plain, "TV", "tvos")
	theirKid := e.kid(e.admin, "Neighbor kid")
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": admin, "access": "read"}, nil)
	e.link(e.plain.account.ID, admin, false)
	e.expect(400, "POST", s.sharesPath, tv, map[string]any{"profileId": admin, "access": "read"}, nil)
	var sh api.PlaylistShare
	e.expect(201, "POST", s.sharesPath, tok, map[string]any{"profileId": admin, "access": "read"}, &sh)
	e.labeled(admin, sh.HouseholdId, sh.Color, e.admin.household.ID)
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": theirKid.Id, "access": "read"}, nil)
	e.joins(e.admin.household.ID, e.plain.account.ID)
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": theirKid.Id, "access": "write"}, nil)
	e.expect(201, "POST", s.sharesPath, tv, map[string]any{"profileId": theirKid.Id, "access": "read"}, nil)
	partner := e.householdAdult(e.plain, "Partner", "partner@example.test")
	pid := partner.Me.Account.Id
	e.joins(e.admin.household.ID, pid)
	e.share(s, pid, "read")
	e.expect(200, "GET", s.sharesPath+"/"+itoa(pid), tok, nil, &sh)
	e.labeled(pid, sh.HouseholdId, sh.Color, e.plain.household.ID)

	// A share with someone the owner no longer sees stays, to change or
	// remove, but is not made again.
	_, err := e.db.Exec(`DELETE FROM adult_link`)
	must(t, err)
	_, err = e.db.Exec(`DELETE FROM household_member`)
	must(t, err)
	var page api.PlaylistSharePage
	e.expect(200, "GET", s.sharesPath, tok, nil, &page)
	if len(page.Items) != 3 {
		t.Fatalf("%+v", page)
	}
	for _, v := range page.Items {
		want := e.admin.household.ID
		if v.ProfileId == pid {
			want = e.plain.household.ID
		}
		e.labeled(v.ProfileId, v.HouseholdId, v.Color, want)
	}
	e.expect(200, "PUT", s.sharesPath+"/"+itoa(admin), tok, map[string]any{"access": "write"}, &sh)
	e.labeled(admin, sh.HouseholdId, sh.Color, e.admin.household.ID)
	e.expect(200, "GET", s.under(admin), e.admin.token, nil, nil)
	e.expect(204, "DELETE", s.sharesPath+"/"+itoa(admin), tok, nil, nil)
	e.expect(400, "POST", s.sharesPath, tok, map[string]any{"profileId": admin, "access": "read"}, nil)
}

func TestShareHouseholds(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Pending", "email": "pending@example.test"}, nil)
	gone := e.householdAdult(e.plain, "Gone", "gone@example.test")
	_, err := e.db.Exec(`UPDATE account SET revoked_at = 0 WHERE id = ?`, gone.Me.Account.Id)
	must(t, err)
	path := "/profiles/" + itoa(e.plain.account.ID) + "/share-households"
	var list api.ShareHouseholdList
	e.expect(200, "GET", path, e.plain.token, nil, &list)
	if len(list.Items) != 1 || list.Items[0].Id != e.plain.household.ID || list.Items[0].Name != e.plain.household.Name || list.Items[0].Profiles != 2 {
		t.Fatalf("the owner and the kid, not the pending or revoked adult: %+v", list)
	}
	e.joins(e.admin.household.ID, e.plain.account.ID)
	e.expect(200, "GET", path, e.plain.token, nil, &list)
	if len(list.Items) != 2 || list.Items[0].Id != e.plain.household.ID || list.Items[1].Id != e.admin.household.ID ||
		list.Items[1].Name != e.admin.household.Name || list.Items[1].Profiles != 2 {
		t.Fatalf("their own first: %+v", list)
	}
	e.expect(403, "GET", "/profiles/"+itoa(k.Id)+"/share-households", e.plain.token, nil, nil)
	e.expect(403, "GET", "/profiles/"+itoa(k.Id)+"/share-households", e.kidDevice(e.plain, k.Id), nil, nil)
}

func TestPlaylistSaysWhetherItIsShared(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	partner := e.householdAdult(e.plain, "Partner", "partner@example.test")
	pid, ptok := partner.Me.Account.Id, partner.Token
	shared := func(profile int64, token string, want bool) {
		t.Helper()
		var p api.Playlist
		e.expect(200, "GET", s.under(profile), token, nil, &p)
		var page api.PlaylistPage
		e.expect(200, "GET", "/profiles/"+itoa(profile)+"/playlists", token, nil, &page)
		got := listed(t, page, s.pl.Id)
		if p.Shared == nil || *p.Shared != want || got == nil || got.Shared == nil || *got.Shared != want {
			t.Fatalf("shared for %d: %v, listed %+v; want %v, always sent", profile, p.Shared, got, want)
		}
	}
	if s.pl.Shared == nil || *s.pl.Shared {
		t.Fatalf("made: %+v", s.pl)
	}
	shared(e.plain.account.ID, e.plain.token, false)
	e.share(s, pid, "read")
	shared(e.plain.account.ID, e.plain.token, true)
	shared(pid, ptok, true)
	e.expect(204, "DELETE", s.sharesPath+"/"+itoa(pid), e.plain.token, nil, nil)
	shared(e.plain.account.ID, e.plain.token, false)
	e.expect(201, "POST", s.owner+"/household-shares", e.plain.token, map[string]any{"householdId": e.plain.household.ID, "access": "read"}, nil)
	shared(e.plain.account.ID, e.plain.token, true)
	shared(pid, ptok, true)
}

// joins puts the profile into another household as well.
func (e *env) joins(household, profile int64) {
	e.t.Helper()
	_, err := e.db.Exec(`INSERT INTO household_member (household_id, account_id, created_at) VALUES (?,?,0)`, household, profile)
	must(e.t, err)
}

func TestPlaylistHouseholdSharesCRUD(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	tok, home := e.plain.token, e.plain.household
	path := s.owner + "/household-shares"

	var sh api.PlaylistHouseholdShare
	e.expect(201, "POST", path, tok, map[string]any{"householdId": home.ID, "access": "read"}, &sh)
	if sh.PlaylistId != s.pl.Id || sh.HouseholdId != home.ID || sh.Name != home.Name || sh.Access != api.PlaylistShareAccessRead || sh.CreatedAt.IsZero() {
		t.Fatalf("%+v", sh)
	}
	e.expect(409, "POST", path, tok, map[string]any{"householdId": home.ID, "access": "write"}, nil)
	e.expect(400, "POST", path, tok, map[string]any{"householdId": e.admin.household.ID, "access": "read"}, nil)
	e.expect(400, "POST", path, tok, map[string]any{"householdId": 999999, "access": "read"}, nil)
	e.expect(400, "POST", path, tok, map[string]any{"householdId": home.ID, "access": "owner"}, nil)
	// A household the owner was shared into is theirs to share with too.
	e.joins(e.admin.household.ID, e.plain.account.ID)
	e.expect(201, "POST", path, tok, map[string]any{"householdId": e.admin.household.ID, "access": "write"}, nil)

	var page api.PlaylistHouseholdSharePage
	e.expect(200, "GET", path, tok, nil, &page)
	if len(page.Items) != 2 || page.Items[0].HouseholdId != e.admin.household.ID || page.Items[1].HouseholdId != home.ID {
		t.Fatalf("by name: %+v", page)
	}
	e.expect(200, "GET", path+"?limit=1", tok, nil, &page)
	if len(page.Items) != 1 || page.NextOffset == nil {
		t.Fatalf("%+v", page)
	}
	one := path + "/" + itoa(home.ID)
	e.expect(200, "GET", one, tok, nil, &sh)
	e.expect(200, "PUT", one, tok, map[string]any{"access": "write"}, &sh)
	if sh.Access != api.PlaylistShareAccessWrite {
		t.Fatalf("%+v", sh)
	}
	e.expect(200, "GET", one, tok, nil, &sh)
	if sh.Access != api.PlaylistShareAccessWrite {
		t.Fatalf("not stored %+v", sh)
	}

	// Only the owner reads or changes them, even for one it reaches with
	// write access.
	theirs := s.under(e.admin.account.ID) + "/household-shares"
	e.expect(403, "GET", theirs, e.admin.token, nil, nil)
	e.expect(403, "GET", theirs+"/"+itoa(home.ID), e.admin.token, nil, nil)
	e.expect(403, "POST", theirs, e.admin.token, map[string]any{"householdId": e.admin.household.ID, "access": "read"}, nil)
	e.expect(403, "PUT", theirs+"/"+itoa(home.ID), e.admin.token, map[string]any{"access": "read"}, nil)
	e.expect(403, "DELETE", theirs+"/"+itoa(home.ID), e.admin.token, nil, nil)

	e.expect(204, "DELETE", one, tok, nil, nil)
	e.expect(404, "GET", one, tok, nil, nil)
	e.expect(404, "PUT", one, tok, map[string]any{"access": "read"}, nil)
	e.expect(404, "DELETE", one, tok, nil, nil)

	var st api.Station
	e.expect(201, "POST", "/stations", tok, map[string]any{"name": "Own", "kind": "movies"}, &st)
	e.expect(409, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(st.PlaylistId)+"/household-shares", tok,
		map[string]any{"householdId": home.ID, "access": "read"}, nil)
}

func TestPlaylistSharedWithAHousehold(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	partner := e.householdAdult(e.plain, "Partner", "partner@example.test")
	pid, ptok := partner.Me.Account.Id, partner.Token
	k := e.kid(e.plain, "Kid") // PG ceiling
	ktok := e.kidDevice(e.plain, k.Id)
	home := s.owner + "/household-shares/" + itoa(e.plain.household.ID)
	e.expect(201, "POST", s.owner+"/household-shares", e.plain.token, map[string]any{"householdId": e.plain.household.ID, "access": "write"}, nil)

	// A kid in it only reads, and only what their filters allow.
	var page api.PlaylistPage
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/playlists", ktok, nil, &page)
	if got := listed(t, page, s.pl.Id); got == nil || got.Access != api.PlaylistAccessRead || got.ItemCount != 1 {
		t.Fatalf("%+v", page)
	}
	var entries api.PlaylistEntryPage
	e.expect(200, "GET", s.under(k.Id)+"/entries", ktok, nil, &entries)
	if !slices.Equal(entryIDs(entries), []int64{s.g}) {
		t.Fatalf("%v", entryIDs(entries))
	}

	e.expect(200, "GET", "/profiles/"+itoa(pid)+"/playlists", ptok, nil, &page)
	if got := listed(t, page, s.pl.Id); got == nil || got.Access != api.PlaylistAccessWrite || got.OwnerName == nil || *got.OwnerName != e.plain.account.Name {
		t.Fatalf("%+v", page)
	}
	e.expect(201, "POST", s.under(pid)+"/entries", ptok, map[string]any{"itemId": s.g}, nil)

	// Who joins later sees it too.
	e.expect(404, "GET", s.under(e.admin.account.ID), e.admin.token, nil, nil)
	e.joins(e.plain.household.ID, e.admin.account.ID)
	e.expect(200, "GET", s.under(e.admin.account.ID), e.admin.token, nil, nil)
	var theirs, partners api.Station
	e.expect(201, "POST", "/stations", e.admin.token, map[string]any{"name": "Borrowed", "playlistId": s.pl.Id}, &theirs)
	e.expect(201, "POST", "/stations", ptok, map[string]any{"name": "Kept", "playlistId": s.pl.Id}, &partners)

	// A station goes only with the last way its owner had to the playlist.
	e.share(s, pid, "read")
	e.expect(204, "DELETE", s.sharesPath+"/"+itoa(pid), e.plain.token, nil, nil)
	e.expect(200, "GET", "/stations/"+itoa(partners.Id), ptok, nil, nil)
	e.share(s, pid, "read")
	e.expect(204, "DELETE", home, e.plain.token, nil, nil)
	var p api.Playlist
	e.expect(200, "GET", s.under(pid), ptok, nil, &p)
	if p.Access != api.PlaylistAccessRead {
		t.Fatalf("%+v", p)
	}
	e.expect(200, "GET", "/stations/"+itoa(partners.Id), ptok, nil, nil)
	e.expect(404, "GET", s.under(e.admin.account.ID), e.admin.token, nil, nil)
	e.expect(404, "GET", "/stations/"+itoa(theirs.Id), e.admin.token, nil, nil)
	e.expect(404, "GET", s.under(k.Id), ktok, nil, nil)
}

func TestHouseholdShareEventsReachItsMembers(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	s := e.sharedSetup()
	partner := e.householdAdult(e.plain, "Partner", "partner@example.test")
	hers, theirs := e.cursor(partner.Token), e.cursor(e.admin.token)
	heard := func(got []api.Event) bool {
		return len(got) > 0 && is(got[0], api.EventKindPlaylist, "") && *got[0].PlaylistId == s.pl.Id
	}

	e.expect(201, "POST", s.owner+"/household-shares", e.plain.token, map[string]any{"householdId": e.plain.household.ID, "access": "read"}, nil)
	if got := e.heard(partner.Token, &hers); !heard(got) {
		t.Fatalf("shared: %+v", got)
	}
	if got := e.heard(e.admin.token, &theirs); len(got) != 0 {
		t.Fatalf("another household: %+v", got)
	}
	e.expect(204, "DELETE", s.owner+"/household-shares/"+itoa(e.plain.household.ID), e.plain.token, nil, nil)
	if got := e.heard(partner.Token, &hers); !heard(got) {
		t.Fatalf("unshared: %+v", got)
	}
}

// A profile that stops being in a household loses the stations that play
// what was shared with that household, however the membership ends.
func TestLeavingAHouseholdDropsStationsOfItsPlaylists(t *testing.T) {
	for name, end := range map[string]func(e *env){
		"taken out": func(e *env) {
			e.expect(204, "DELETE", memberPath(e.plain.household, e.admin.account.ID), e.admin.token, nil, nil)
		},
		"household deleted": func(e *env) {
			e.expect(204, "DELETE", "/households/"+itoa(e.plain.household.ID), e.admin.token, nil, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := e.sharedSetup()
			e.expect(201, "POST", s.owner+"/household-shares", e.plain.token, map[string]any{"householdId": e.plain.household.ID, "access": "read"}, nil)
			e.expect(204, "PUT", memberPath(e.plain.household, e.admin.account.ID), e.admin.token, nil, nil)
			var lost, own api.Station
			e.expect(201, "POST", "/stations", e.admin.token, map[string]any{"name": "Borrowed", "playlistId": s.pl.Id}, &lost)
			e.expect(201, "POST", "/stations", e.admin.token, map[string]any{"name": "Own", "kind": "movies"}, &own)
			end(e)
			var n int
			must(t, e.db.QueryRow(`SELECT COUNT(*) FROM station WHERE id = ?`, lost.Id).Scan(&n))
			if n != 0 {
				t.Fatal("a station still plays a playlist its owner no longer sees")
			}
			e.expect(200, "GET", "/stations/"+itoa(own.Id), e.admin.token, nil, nil)
		})
	}
}

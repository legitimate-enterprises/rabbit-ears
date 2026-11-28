package httpapi_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/gravatar"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/sso"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

const password = "longenough"

// member is an adult the owner adds who joins on their own device, with
// password as theirs.
func (e *env) member(owner user, name, email string) user {
	e.t.Helper()
	res := e.householdAdult(owner, name, email)
	a, err := e.svc.Accounts.Repo().Get(res.Me.Account.Id)
	must(e.t, err)
	return user{res.Token, a, owner.household, res.Me.Device.Id}
}

// withPassword gives an adult made by the env password as theirs.
func (e *env) withPassword(u user) user {
	e.t.Helper()
	_, err := e.svc.Auth.SetPassword(u.account, password, &u.deviceID, nil)
	must(e.t, err)
	return u
}

func (e *env) deletion(token string) api.AccountDeletion {
	e.t.Helper()
	var d api.AccountDeletion
	e.expect(200, "GET", "/me/deletion", token, nil, &d)
	return d
}

func (e *env) deleteSelf(want int, token, pw string) []byte {
	e.t.Helper()
	return e.expect(want, "POST", "/me/deletion", token, map[string]any{"password": pw}, nil)
}

// accountGone reports an account no longer stored.
func (e *env) accountGone(id uid.ID) bool {
	e.t.Helper()
	_, err := e.svc.Accounts.Repo().Get(id)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		e.t.Fatal(err)
	}
	return err != nil
}

// Deleting an account deletes its playlists as deleting each would: the
// stations of those it shared them with that play them go too, whichever
// delete takes the account.
func TestDeletingAnAccountDeletesTheStationsPlayingItsPlaylists(t *testing.T) {
	for name, del := range map[string]func(e *env, cy user){
		"an admin deletes it": func(e *env, cy user) {
			e.expect(204, "DELETE", "/accounts/"+itoa(cy.account.ID), e.admin.token, nil, nil)
		},
		"the owner removes them": func(e *env, cy user) {
			e.expect(204, "DELETE", "/profiles/"+itoa(cy.account.ID), e.plain.token, nil, nil)
		},
		"an admin deletes their household": func(e *env, _ user) {
			e.expect(204, "DELETE", "/households/"+itoa(e.plain.household.ID), e.admin.token, nil, nil)
		},
		"they delete it themselves": func(e *env, cy user) { e.deleteSelf(204, cy.token, password) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			cy := e.member(e.plain, "Cy", "cy@example.test")
			var pl api.Playlist
			e.expect(201, "POST", "/profiles/"+itoa(cy.account.ID)+"/playlists", cy.token, map[string]any{"name": "Cy's", "kind": "movies"}, &pl)
			e.link(cy.account.ID, e.admin.account.ID, false)
			e.expect(201, "POST", "/profiles/"+itoa(cy.account.ID)+"/playlists/"+itoa(pl.Id)+"/shares", cy.token,
				map[string]any{"profileId": e.admin.account.ID, "access": "read"}, nil)
			var theirs api.Station
			e.expect(201, "POST", "/stations", e.admin.token, map[string]any{"name": "Borrowed", "playlistId": pl.Id}, &theirs)

			del(e, cy)
			e.expect(404, "GET", "/stations/"+itoa(theirs.Id), e.admin.token, nil, nil)
		})
	}
}

// Who owns the household afterwards, and who goes with the account,
// follows from the account's place in it; the server keeps an admin.
func TestDeletingOwnAccount(t *testing.T) {
	type outcome struct {
		api.AccountDeletion
		deleted []uid.ID // accounts gone afterwards
		kept    []uid.ID // accounts still there
		owner   *uid.ID  // the household's owner afterwards; nil when it is gone
	}
	for name, c := range map[string]func(e *env) (deleter user, want outcome){
		"an adult who does not own the household": func(e *env) (user, outcome) {
			cy := e.member(e.plain, "Cy", "cy@example.test")
			k := e.kid(e.plain, "Mia")
			return cy, outcome{api.AccountDeletion{Outcome: api.AccountDeletionOutcomeAccount, KidNames: []string{}},
				[]uid.ID{cy.account.ID}, []uid.ID{e.plain.account.ID, k.Id}, &e.plain.account.ID}
		},
		"the owner, with other adults": func(e *env) (user, outcome) {
			// Cy is invited first and Bea joins first: the household goes to
			// whoever joined earliest.
			var cyInv, beaInv api.HouseholdInvite
			e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Cy", "email": "cy@example.test"}, &cyInv)
			e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Bea", "email": "bea@example.test"}, &beaInv)
			e.expect(201, "POST", beaInv.JoinCode.Link, "", device("name", "Bea", "password", password), nil)
			time.Sleep(5 * time.Millisecond)
			e.expect(201, "POST", cyInv.JoinCode.Link, "", device("name", "Cy", "password", password), nil)
			k := e.kid(e.plain, "Mia")
			bea := "Bea"
			return e.withPassword(e.plain), outcome{api.AccountDeletion{Outcome: api.AccountDeletionOutcomeTransfer, NewOwnerName: &bea, KidNames: []string{}},
				[]uid.ID{e.plain.account.ID}, []uid.ID{beaInv.Account.Id, cyInv.Account.Id, k.Id}, &beaInv.Account.Id}
		},
		"the owner as its only adult": func(e *env) (user, outcome) {
			k := e.kid(e.plain, "Mia")
			var inv api.HouseholdInvite
			e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Dee", "email": "dee@example.test"}, &inv)
			return e.withPassword(e.plain), outcome{api.AccountDeletion{Outcome: api.AccountDeletionOutcomeHousehold, KidNames: []string{"Mia"}},
				[]uid.ID{e.plain.account.ID, k.Id, inv.Account.Id}, []uid.ID{e.admin.account.ID}, nil}
		},
		"an admin who is not the only one": func(e *env) (user, outcome) {
			e.plain.account.IsAdmin = true
			must(e.t, e.svc.Accounts.Repo().Update(e.plain.account, nil))
			return e.withPassword(e.admin), outcome{api.AccountDeletion{Outcome: api.AccountDeletionOutcomeHousehold, KidNames: []string{}},
				[]uid.ID{e.admin.account.ID}, []uid.ID{e.plain.account.ID}, nil}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			u, want := c(e)
			named := func(s *string) string {
				if s == nil {
					return ""
				}
				return *s
			}
			if got := e.deletion(u.token); got.Outcome != want.Outcome || !slices.Equal(got.KidNames, want.KidNames) ||
				named(got.NewOwnerName) != named(want.NewOwnerName) {
				t.Fatalf("told %+v, want %+v", got, want.AccountDeletion)
			}
			e.deleteSelf(204, u.token, password)
			e.expect(401, "GET", "/me", u.token, nil, nil)
			for _, id := range want.deleted {
				if !e.accountGone(id) {
					t.Errorf("account %v is still there", id)
				}
			}
			for _, id := range want.kept {
				if e.accountGone(id) {
					t.Errorf("account %v went too", id)
				}
			}
			h, err := e.svc.Households.Repo().Get(u.household.ID)
			if want.owner == nil && err == nil || want.owner != nil && (err != nil || h.AdultID != *want.owner) {
				t.Fatalf("household afterwards %+v %v, want owner %v", h, err, want.owner)
			}
		})
	}
}

// The server's only admin is told before confirming, and refused, so the
// server is never left with nobody to manage it.
func TestTheOnlyAdminCannotDeleteTheirAccount(t *testing.T) {
	e := newEnv(t)
	e.withPassword(e.admin)
	if got := e.deletion(e.admin.token); got.Outcome != api.AccountDeletionOutcomeRefused {
		t.Fatalf("%+v", got)
	}
	raw := e.deleteSelf(409, e.admin.token, password)
	if !strings.Contains(string(raw), "make another account an admin") {
		t.Fatalf("%s", raw)
	}
	if e.accountGone(e.admin.account.ID) {
		t.Fatal("deleted")
	}
}

// Deleting asks the password again, and wrong ones count against sign-in's
// limit; a kid has none and deletes nobody.
func TestDeletingOwnAccountNeedsThePassword(t *testing.T) {
	e := newEnv(t)
	e.withPassword(e.plain)
	k := e.kid(e.plain, "Mia")
	kid := e.kidDevice(e.plain, k.Id)
	e.expect(403, "GET", "/me/deletion", kid, nil, nil)
	e.deleteSelf(403, kid, password)
	e.expect(400, "POST", "/me/deletion", e.plain.token, map[string]any{}, nil)

	for range 10 {
		e.deleteSelf(403, e.plain.token, "wrongwrong")
	}
	e.deleteSelf(429, e.plain.token, password)
	e.expect(429, "POST", "/auth/sign-in", "", device("email", e.plain.account.Email, "password", password), nil)
	if e.accountGone(e.plain.account.ID) || e.accountGone(k.Id) {
		t.Fatal("deleted without the password")
	}

	e.svc.Auth.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	e.deleteSelf(204, e.plain.token, password)
}

// Every device of the account, or acting as it, is signed out at once: its
// stream stops, its held poll answers 401, its local check is forgotten,
// and a Watch together it is in ends for the other member.
func TestDeletedAccountIsSignedOutEverywhere(t *testing.T) {
	e := newEnv(t)
	e.svc.EventHold = 10 * time.Second
	e.svc.Avatars = gravatar.New(t.TempDir())
	m, _ := e.playable()
	cy := e.member(e.plain, "Cy", "cy@example.test")
	phone, phoneID := e.secondDevice(cy, "Phone", "ios")
	st := e.startOn(phone, cy.account.ID, m)

	// The owner's tablet, switched to Cy.
	tablet, _ := e.secondDevice(e.plain, "Tablet", "web")
	e.expect(204, "POST", "/profiles/"+itoa(cy.account.ID)+"/unlock", tablet, map[string]any{}, nil)

	var w api.WatchTogether
	e.expect(201, "POST", "/watch-together", cy.token, map[string]any{"itemId": m}, &w)
	e.expect(200, "POST", togetherPath(w)+"/join", e.plain.token, nil, &w)

	c, err := e.svc.Local.Start(phoneID)
	must(t, err)
	_, err = e.svc.Local.Confirm(c.ID, "n", proof(t, c.Key, "client", "n"), true)
	must(t, err)
	picture := filepath.Join(e.svc.Avatars.Dir, gravatar.Hash(cy.account.Email))
	must(t, os.MkdirAll(e.svc.Avatars.Dir, 0o750))
	must(t, os.WriteFile(picture, []byte("jpeg"), 0o600))

	cursor := e.cursor(phone)
	polled := make(chan int, 1)
	go func() {
		code, _ := e.do("GET", "/events?after="+cursor, phone, nil, nil)
		polled <- code
	}()
	time.Sleep(50 * time.Millisecond)

	e.deleteSelf(204, cy.token, password)
	select {
	case code := <-polled:
		// Cy leaves the Watch together before the account goes, and a poll
		// that wakes to that event first may answer it; the next is refused.
		if code == 200 {
			code, _ = e.do("GET", "/events?after="+cursor, phone, nil, nil)
		}
		if code != 401 {
			t.Fatalf("held poll answered %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held poll was not answered")
	}
	for _, token := range []string{cy.token, phone, tablet} {
		e.expect(401, "GET", "/me", token, nil, nil)
	}
	if _, _, live := e.svc.Playback.Live(st.SessionId); live {
		t.Fatal("the stream is still playing")
	}
	if e.svc.Local.Home(phoneID) {
		t.Fatal("the device's local check stands")
	}
	if _, err := os.Stat(picture); !os.IsNotExist(err) {
		t.Fatalf("the Gravatar copy is kept: %v", err)
	}
	e.expect(200, "GET", togetherPath(w), e.plain.token, nil, &w)
	if w.Status != api.Ended {
		t.Fatalf("%+v", w)
	}
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)
}

// ref is a column naming an account or a household.
type ref struct{ table, column, target string }

// refs lists every column naming an account or a household: those with a
// foreign key to one, and any named for one without.
func refs(t *testing.T, e *env) []ref {
	t.Helper()
	rows, err := e.db.Query(`SELECT m.name, p."from", p."table" FROM sqlite_master m, pragma_foreign_key_list(m.name) p
		WHERE m.type = 'table' AND p."table" IN ('account', 'household')
		UNION SELECT m.name, c.name, CASE WHEN c.name LIKE '%household_id' THEN 'household' ELSE 'account' END
		FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND (c.name LIKE '%account_id' OR c.name LIKE '%profile_id' OR c.name LIKE '%household_id'
			OR c.name IN ('adult_id', 'paused_by'))
		ORDER BY 1, 2`)
	must(t, err)
	defer func() { _ = rows.Close() }()
	var out []ref
	for rows.Next() {
		var r ref
		must(t, rows.Scan(&r.table, &r.column, &r.target))
		out = append(out, r)
	}
	must(t, rows.Err())
	return out
}

// naming counts, per column, the rows naming one of the accounts or
// households.
func naming(t *testing.T, e *env, rs []ref, accounts []uid.ID, household uid.ID) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, r := range rs {
		ids := accounts
		if r.target == "household" {
			ids = []uid.ID{household}
		}
		var n int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %q WHERE %q IN (%s)`, r.table, r.column, strings.Trim(strings.Repeat("?,", len(ids)), ","))
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		must(t, e.db.QueryRow(q, args...).Scan(&n))
		out[r.table+"."+r.column] = n
	}
	return out
}

// Deleting the household's only adult leaves no row naming it, its
// household or its members, in any table. Every such column is seeded
// first, so a table added later fails here until this test covers it.
func TestDeletingOwnAccountLeavesNoOrphans(t *testing.T) {
	e := newEnv(t)
	e.withPassword(e.plain)
	m, f := e.playable()
	owner, owned := e.plain.account.ID, "/profiles/"+itoa(e.plain.account.ID)

	k := e.kid(e.plain, "Mia")
	e.kidDevice(e.plain, k.Id)
	e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(m), e.plain.token, nil, nil)
	// A kid the admin's household holds too outlives the household.
	ava := e.kid(e.plain, "Ava")
	e.putIn(ava.Id, e.admin.household)
	e.kidDevice(e.plain, ava.Id)
	e.expect(204, "PUT", "/profiles/"+itoa(ava.Id)+"/allowed-items/"+itoa(m), e.plain.token, nil, nil)
	var inv api.HouseholdInvite
	e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Dee", "email": "dee@example.test"}, &inv)

	p := &sso.Provider{Name: "Pocket", Issuer: "https://id.example.test", ClientID: "c", ClientSecret: "s", Enabled: true}
	must(t, e.svc.SSO.Repo().Create(p, nil))
	must(t, e.svc.SSO.Repo().CreateIdentity(&sso.Identity{AccountID: owner, ProviderID: p.ID, Subject: "sub"}))

	link := e.newLink()
	e.expect(200, "POST", "/link/"+link.Code+"/approve", e.plain.token, nil, nil)

	e.expect(201, "POST", "/collections", e.plain.token, map[string]any{"name": "Kites"}, nil)
	e.startOn(e.plain.token, owner, m)
	e.expect(200, "PUT", owned+"/watch-state/"+itoa(m), e.plain.token, map[string]any{"watched": true}, nil)

	var pl api.Playlist
	e.expect(201, "POST", owned+"/playlists", e.plain.token, map[string]any{"name": "Mine", "kind": "movies"}, &pl)
	var st api.Station
	e.expect(201, "POST", owned+"/playlists/"+itoa(pl.Id)+"/entries", e.plain.token, map[string]any{"itemId": m}, nil)
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl.Id,
		"times": []map[string]string{{"from": "16:00", "to": "02:00"}}}, &st)
	e.expect(201, "POST", "/libraries/"+itoa(itemOf(t, e, m).LibraryId)+"/folders", e.admin.token, map[string]any{"path": "/media/films"}, nil)
	for _, o := range []cover.Owner{{PlaylistID: &pl.Id}, {StationID: &st.Id}} {
		must(t, e.svc.Covers.Repo().Set(&cover.Cover{Owner: o, JPEG: []byte{0xff, 0xd8}}, &owner))
	}
	var given api.Playlist
	admins := "/profiles/" + itoa(e.admin.account.ID) + "/playlists"
	e.expect(201, "POST", admins, e.admin.token, map[string]any{"name": "Given", "kind": "movies"}, &given)

	// Two conversions of the owner's: one only they claim, which goes with
	// them, and one the admin claims too, which stays.
	downloads := e.svc.Downloads.Repo()
	conversion := func(height int) *download.Conversion {
		c := &download.Conversion{MediaFileID: f, SourceModifiedAt: time.Unix(0, 0), Container: "mp4", PredictedBytes: 1,
			Entry: &session.PlanEntry{Verdict: session.VideoTranscode, VideoAction: "convert", AudioAction: "copy", TopHeight: height}}
		must(t, downloads.Request(c))
		return c
	}
	alone, shared := conversion(720), conversion(480)
	for _, d := range []download.Download{
		{DeviceID: e.plain.deviceID, AccountID: owner, ConversionID: &alone.ID},
		{DeviceID: e.plain.deviceID, AccountID: k.Id, ConversionID: &shared.ID},
		{DeviceID: e.admin.deviceID, AccountID: e.admin.account.ID, ConversionID: &shared.ID},
	} {
		d.ItemID, d.MediaFileID, d.Quality = m, f, "720p"
		must(t, downloads.Create(&d, &owner))
	}

	var w api.WatchTogether
	e.expect(201, "POST", "/watch-together", e.plain.token, map[string]any{"itemId": m}, &w)
	other, _ := e.secondDevice(e.plain, "Tablet", "web")
	e.expect(200, "POST", togetherPath(w)+"/join", other, nil, nil)
	e.expect(200, "PUT", togetherPath(w)+"/state", e.plain.token, map[string]any{"playing": true, "positionMs": 0}, nil)
	e.expect(200, "PUT", togetherPath(w)+"/state", e.plain.token, map[string]any{"playing": false, "positionMs": 60_000}, nil)

	for _, q := range []struct {
		sql  string
		a, b uid.ID
	}{
		{`INSERT INTO household_member (household_id, account_id, created_at) VALUES (?,?,0)`, e.plain.household.ID, e.admin.account.ID},
		{`INSERT INTO household_member (household_id, account_id, created_at) VALUES (?,?,0)`, e.admin.household.ID, owner},
		{`INSERT INTO playlist_household_share (playlist_id, household_id, access, created_at) VALUES (?,?,'read',0)`, given.Id, e.plain.household.ID},
		{`INSERT INTO favorite (account_id, item_id, created_at) VALUES (?,?,0)`, owner, m},
		{`INSERT INTO smart_list (id, playlist_id, match, created_at, modified_at) VALUES (?,?,'all',0,0)`, uid.New(), pl.Id},
	} {
		_, err := e.db.Exec(q.sql, q.a, q.b)
		must(t, err)
	}
	for _, other := range []uid.ID{e.admin.account.ID, inv.Account.Id} {
		a, b := ordered(owner, other)
		_, err := e.db.Exec(`INSERT INTO adult_link (a_id, b_id, created_at) VALUES (?,?,0)`, a, b)
		must(t, err)
		_, err = e.db.Exec(`INSERT INTO friendship (a_id, b_id, created_at) VALUES (?,?,0)`, a, b)
		must(t, err)
	}
	e.expect(202, "POST", "/friend-requests", e.plain.token, map[string]any{"email": e.admin.account.Email}, nil)
	var asked api.OutgoingFriendRequest
	e.expect(202, "POST", "/friend-requests", e.admin.token, map[string]any{"email": e.plain.account.Email}, &asked)
	e.expect(204, "DELETE", "/friend-requests/"+itoa(asked.Id), e.plain.token, nil, nil)
	e.expect(204, "POST", "/client-errors", e.plain.token, clientErrors(clientError(uid.New(), "Home failed")), nil)
	e.offer(201, e.plain.token, k.Id, e.admin.account.ID)
	e.offer(201, e.admin.token, e.kid(e.admin, "Rex").Id, owner)
	e.expect(201, "POST", admins+"/"+itoa(given.Id)+"/shares", e.admin.token, map[string]any{"profileId": owner, "access": "read"}, nil)

	doomed := []uid.ID{owner, k.Id, inv.Account.Id}
	rs := refs(t, e)
	// Whoever made or last changed a row is named by it: the owner is, on
	// a row of every such table.
	for _, r := range rs {
		if r.column == "created_by" || r.column == "modified_by" {
			_, err := e.db.Exec(fmt.Sprintf(`UPDATE %q SET %q = ? WHERE rowid = (SELECT MIN(rowid) FROM %q)`, r.table, r.column, r.table), owner)
			must(t, err)
		}
	}
	for col, n := range naming(t, e, rs, doomed, e.plain.household.ID) {
		if n == 0 {
			t.Errorf("%s names none of what goes; seed a row of it", col)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	e.deleteSelf(204, e.plain.token, password)
	for col, n := range naming(t, e, rs, doomed, e.plain.household.ID) {
		if n != 0 {
			t.Errorf("%s still names %d", col, n)
		}
	}
	var broken int
	must(t, e.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken))
	if broken != 0 {
		t.Fatalf("%d rows break a foreign key", broken)
	}
	var covers, kept int
	must(t, e.db.QueryRow(`SELECT COUNT(*) FROM cover`).Scan(&covers))
	must(t, e.db.QueryRow(`SELECT COUNT(*) FROM download WHERE device_id = ?`, e.plain.deviceID).Scan(&kept))
	if covers != 0 || kept != 0 {
		t.Fatalf("%d images and %d downloads of the devices stay", covers, kept)
	}
	if _, err := downloads.Conversion(alone.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a conversion only they claimed stays: %v", err)
	}
	if _, err := downloads.Conversion(shared.ID); err != nil {
		t.Fatalf("a conversion another claims goes: %v", err)
	}
	e.expect(200, "GET", admins+"/"+itoa(given.Id), e.admin.token, nil, nil)
	var al api.AllowList
	e.expect(200, "GET", "/profiles/"+itoa(ava.Id)+"/allowed-items", e.admin.token, nil, &al)
	if !slices.Equal(al.ItemIds, []uid.ID{m}) {
		t.Fatalf("the shared kid lost their allow list: %+v", al)
	}
}

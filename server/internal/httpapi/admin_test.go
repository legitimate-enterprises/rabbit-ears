package httpapi_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestAccountsCRUD(t *testing.T) {
	e := newEnv(t)
	var a api.Account
	e.expect(201, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "email": "new@example.test", "name": "New"}, &a)
	if !a.Pending || a.IsAdmin || a.HouseholdId != nil {
		t.Fatalf("new adult must be pending with no household: %+v", a)
	}
	e.expect(409, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "email": "NEW@example.test", "name": "Dup"}, nil)
	e.expect(400, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "name": "No email"}, nil)
	e.expect(400, "POST", "/accounts", e.admin.token, map[string]any{"kind": "kid", "name": "Kid"}, nil)
	e.expect(400, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "email": "not-an-email", "name": "X"}, nil)

	var page api.AccountPage
	e.expect(200, "GET", "/accounts?kind=adult&limit=2", e.admin.token, nil, &page)
	if len(page.Items) != 2 || page.NextOffset == nil {
		t.Fatalf("%+v", page)
	}

	id := "/accounts/" + itoa(a.Id)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "adult", "email": "new@example.test", "name": "Renamed", "isAdmin": true}, &a)
	if a.Name != "Renamed" || !a.IsAdmin {
		t.Fatalf("%+v", a)
	}
	e.expect(400, "PUT", id, e.admin.token, map[string]any{"kind": "kid", "name": "Renamed"}, nil)
	e.expect(200, "GET", id, e.admin.token, nil, &a)
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
}

func TestRevokingAnAccountRevokesItsDevices(t *testing.T) {
	e := newEnv(t)
	id := "/accounts/" + itoa(e.plain.account.ID)
	// A revoke the save refuses signs nothing out.
	e.expect(409, "PUT", id, e.admin.token, map[string]any{"kind": "adult", "email": e.admin.account.Email, "name": "P", "revoked": true}, nil)
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)

	// Another adult's device acting as the revoked one is signed out too.
	m, _ := e.playable()
	cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
	if st, raw := e.unlock(user{token: cy.Token}, e.plain.account.ID, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	own := e.startOn(e.plain.token, e.plain.account.ID, m)
	acting := e.startOn(cy.Token, e.plain.account.ID, m)
	var a api.Account
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "adult", "email": "plain@example.test", "name": "P", "revoked": true}, &a)
	if a.RevokedAt == nil {
		t.Fatal("revokedAt not set")
	}
	e.expect(401, "GET", "/me", e.plain.token, nil, nil)
	e.expect(401, "GET", "/me", cy.Token, nil, nil)
	for _, st := range []api.PlaybackStream{own, acting} {
		e.signedOutOf(st)
	}
	for _, d := range []uid.ID{e.plain.deviceID, cy.Me.Device.Id} {
		var got api.Device
		e.expect(200, "GET", "/devices/"+itoa(d), e.admin.token, nil, &got)
		if got.RevokedAt == nil {
			t.Fatalf("device %v not revoked", d)
		}
	}

	// An absent revoked keeps the account as it is; false restores it.
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "adult", "email": "plain@example.test", "name": "Renamed"}, &a)
	if a.Name != "Renamed" || a.RevokedAt == nil {
		t.Fatalf("a rename restored the account: %+v", a)
	}
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "adult", "email": "plain@example.test", "name": "P", "revoked": false}, &a)
	if a.RevokedAt != nil {
		t.Fatalf("false did not restore the account: %+v", a)
	}
}

// A kid has no devices of their own: revoking them ends what plays as them
// on their adult's devices.
func TestRevokingAKidEndsTheirPlayback(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	k := e.kid(e.plain, "Kid")
	st := e.startOn(e.kidDevice(e.plain, k.Id), k.Id, m)
	if res, _ := e.get(st.StreamUrl); res.StatusCode != 200 {
		t.Fatalf("stream before revoking: %d", res.StatusCode)
	}
	e.expect(200, "PUT", "/accounts/"+itoa(k.Id), e.admin.token, map[string]any{"kind": "kid", "name": "Kid", "birthday": "2018-05-01", "revoked": true}, nil)
	e.signedOutOf(st)
}

// signedOutOf fails unless st's session is over and its stream URL no
// longer serves.
func (e *env) signedOutOf(st api.PlaybackStream) {
	e.t.Helper()
	if _, _, live := e.svc.Playback.Live(st.SessionId); live {
		e.t.Fatalf("session %s is still playing", st.SessionId)
	}
	if res, _ := e.get(st.StreamUrl); res.StatusCode == 200 {
		e.t.Fatalf("session %s still streams", st.SessionId)
	}
}

func TestHouseholdsCRUD(t *testing.T) {
	e := newEnv(t)
	var adult api.Account
	e.expect(201, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "email": "h@example.test", "name": "H"}, &adult)
	var h api.Household
	e.expect(201, "POST", "/households", e.admin.token, map[string]any{"name": "Home", "adultId": adult.Id}, &h)
	e.expect(409, "POST", "/households", e.admin.token, map[string]any{"name": "Second", "adultId": adult.Id}, nil)
	cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
	e.expect(409, "POST", "/households", e.admin.token, map[string]any{"name": "Cy's", "adultId": cy.Me.Account.Id}, nil)
	e.expect(400, "POST", "/households", e.admin.token, map[string]any{"name": "Ghost", "adultId": uid.N(999)}, nil)
	var page api.HouseholdPage
	e.expect(200, "GET", "/households", e.admin.token, nil, &page)
	if len(page.Items) != 3 {
		t.Fatalf("%+v", page)
	}
	id := "/households/" + itoa(h.Id)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Renamed", "adultId": adult.Id}, &h)
	if h.Name != "Renamed" {
		t.Fatalf("%+v", h)
	}
	e.expect(400, "PUT", id, e.admin.token, map[string]any{"name": "Renamed", "adultId": e.plain.account.ID}, nil)
	e.expect(200, "GET", id, e.admin.token, nil, &h)
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
}

func TestDevicesCRUD(t *testing.T) {
	e := newEnv(t)
	var page api.DevicePage
	e.expect(200, "GET", "/devices?accountId="+itoa(e.plain.account.ID), e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != e.plain.deviceID {
		t.Fatalf("%+v", page)
	}
	page = api.DevicePage{}
	e.expect(200, "GET", "/devices?limit=1", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.NextOffset == nil {
		t.Fatalf("%+v", page)
	}
	id := "/devices/" + itoa(e.plain.deviceID)
	var d api.Device
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Kitchen", "revoked": true}, &d)
	if d.Name != "Kitchen" || d.RevokedAt == nil {
		t.Fatalf("%+v", d)
	}
	e.expect(401, "GET", "/me", e.plain.token, nil, nil)
	// An absent revoked keeps the device as it is.
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Old phone"}, &d)
	if d.Name != "Old phone" || d.RevokedAt == nil {
		t.Fatalf("a rename restored the device: %+v", d)
	}
	e.expect(401, "GET", "/me", e.plain.token, nil, nil)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Kitchen", "revoked": false}, &d)
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Kitchen"}, &d)
	if d.RevokedAt != nil {
		t.Fatalf("a rename revoked the device: %+v", d)
	}
	m, _ := e.playable()
	st := e.startOn(e.plain.token, e.plain.account.ID, m)
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
	e.expect(401, "GET", "/me", e.plain.token, nil, nil)
	e.signedOutOf(st)
}

var joinCodeShape = regexp.MustCompile(`^[0-9A-Z]{4}-[0-9A-Z]{4}$`)

func TestJoinCodesCRUD(t *testing.T) {
	e := newEnv(t)
	var a api.Account
	e.expect(201, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "email": "j@example.test", "name": "J"}, &a)
	var j api.JoinCode
	before := time.Now()
	e.expect(201, "POST", "/join-codes", e.admin.token, map[string]any{"accountId": a.Id}, &j)
	if !joinCodeShape.MatchString(j.Code) || j.Link != "/join/"+j.Code {
		t.Fatalf("%+v", j)
	}
	if d := j.ExpiresAt.Sub(before); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour+time.Minute {
		t.Fatalf("default expiry must be 7 days, got %v", d)
	}
	e.expect(201, "POST", "/join-codes", e.admin.token, map[string]any{"accountId": a.Id, "expiresInDays": 30}, nil)
	e.expect(400, "POST", "/join-codes", e.admin.token, map[string]any{"accountId": a.Id, "expiresInDays": 3}, nil)
	e.expect(404, "POST", "/join-codes", e.admin.token, map[string]any{"accountId": uid.N(999)}, nil)
	e.expect(409, "POST", "/join-codes", e.admin.token, map[string]any{"accountId": e.plain.account.ID}, nil)

	var page api.JoinCodePage
	e.expect(200, "GET", "/join-codes?accountId="+itoa(a.Id), e.admin.token, nil, &page)
	if len(page.Items) != 2 {
		t.Fatalf("%+v", page)
	}
	id := "/join-codes/" + itoa(j.Id)
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(200, "GET", id, e.admin.token, nil, &j)
	if j.RevokedAt == nil {
		t.Fatal("revoke must keep the record and stamp it")
	}
	e.expect(404, "GET", "/join-codes/"+itoa(uid.N(999)), e.admin.token, nil, nil)
}

func TestTasksCRUDAndRuns(t *testing.T) {
	e := newEnv(t)
	var task api.Task
	e.expect(201, "POST", "/tasks", e.admin.token, map[string]any{"name": "Scan Films", "kind": "scan", "intervalSeconds": 86400}, &task)
	if !task.Enabled || task.LastRun != nil {
		t.Fatalf("%+v", task)
	}
	e.expect(409, "POST", "/tasks", e.admin.token, map[string]any{"name": "Scan Films", "kind": "scan"}, nil)
	e.expect(400, "POST", "/tasks", e.admin.token, map[string]any{"name": "Bad", "kind": "scan", "intervalSeconds": 0}, nil)

	run, err := e.svc.Tasks.Repo().StartRun(task.Id, time.Now())
	must(t, err)
	must(t, e.svc.Tasks.Repo().SetSummary(run.ID, "3 movies added"))
	must(t, e.svc.Tasks.Repo().FinishRun(run.ID, "boom", time.Now()))
	id := "/tasks/" + itoa(task.Id)
	e.expect(200, "GET", id, e.admin.token, nil, &task)
	if task.LastRun == nil || task.LastRun.Status != api.TaskRunStatusFailed || task.LastRun.Error == nil {
		t.Fatalf("%+v", task.LastRun)
	}
	var runs api.TaskRunPage
	e.expect(200, "GET", id+"/runs", e.admin.token, nil, &runs)
	if len(runs.Items) != 1 || runs.Items[0].Summary == nil || *runs.Items[0].Summary != "3 movies added" {
		t.Fatalf("%+v", runs)
	}
	e.expect(404, "GET", "/tasks/"+itoa(uid.N(999))+"/runs", e.admin.token, nil, nil)

	var page api.TaskPage
	e.expect(200, "GET", "/tasks", e.admin.token, nil, &page)
	if len(page.Items) != 1 {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Scan Films", "kind": "scan", "enabled": false}, &task)
	if task.Enabled || task.IntervalSeconds != nil {
		t.Fatalf("%+v", task)
	}
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
}

// An edit that leaves filterMode out keeps the kid's mode, so an incomplete
// request never widens what the kid sees. Under ratings a ceiling left out
// is the one suggested for the kid's age, and a birthday left out is the
// stored one.
func TestAdminEditOfAKidSuggestsMissingCeilings(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	k := e.kid(e.plain, "Sam")
	kidToken := e.kidDevice(e.plain, k.Id)
	born := time.Now().AddDate(-3, 0, 0).Format(time.DateOnly)
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id), e.plain.token, map[string]any{"name": "Sam", "birthday": born, "filterMode": "allow_list"}, &k)
	e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(c.r), e.plain.token, nil, nil)
	id := "/accounts/" + itoa(k.Id)
	onlyTheAllowed := func() {
		t.Helper()
		e.expect(200, "GET", "/items/"+itoa(c.r), kidToken, nil, nil)
		e.expect(404, "GET", "/items/"+itoa(c.g), kidToken, nil, nil)
	}
	onlyTheAllowed()

	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "kid", "name": "Sam"}, &k)
	if k.FilterMode != api.FilterModeAllowList || k.MovieCeiling != nil || k.TvCeiling != nil || k.Birthday == nil {
		t.Fatalf("a bare edit changes the mode, sets ceilings or drops the birthday: %+v", k)
	}
	onlyTheAllowed()
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id), e.plain.token, map[string]any{"name": "Sam", "birthday": born}, &k)
	if k.FilterMode != api.FilterModeAllowList || k.MovieCeiling != nil || k.TvCeiling != nil {
		t.Fatalf("a profile edit without filterMode changes the mode: %+v", k)
	}
	onlyTheAllowed()

	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "kid", "name": "Sam", "filterMode": "ratings"}, &k)
	if k.FilterMode != api.FilterModeRatings || k.MovieCeiling == nil || *k.MovieCeiling != api.G || k.TvCeiling == nil || *k.TvCeiling != api.TVY {
		t.Fatalf("ratings without ceilings leaves them blank: %+v", k)
	}
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "kid", "name": "Sam", "movieCeiling": "PG-13"}, &k)
	if k.FilterMode != api.FilterModeRatings || *k.MovieCeiling != api.PG13 || *k.TvCeiling != api.TVY {
		t.Fatalf("a given ceiling is kept, the other is suggested: %+v", k)
	}
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"kind": "kid", "name": "Sam", "filterMode": "allow_list"}, &k)
	if k.FilterMode != api.FilterModeAllowList || k.MovieCeiling != nil || k.TvCeiling != nil {
		t.Fatalf("allow list suggests nothing: %+v", k)
	}
}

package httpapi_test

import (
	"bytes"
	"image/png"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

var tv = map[string]any{"deviceName": "Living room TV", "platform": "roku"}

func (e *env) newLink() api.DeviceLinkCreated {
	e.t.Helper()
	var c api.DeviceLinkCreated
	e.expect(201, "POST", "/device-links", "", tv, &c)
	return c
}

func poll(c api.DeviceLinkCreated) string {
	return "/device-links/" + itoa(c.Id) + "?token=" + c.PollToken
}

// clock freezes the device-link clock at now and returns a way to move it.
func (e *env) clock() func(time.Duration) {
	now := time.Now().UTC()
	e.svc.DeviceLinks.Now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

// raw sends a bodiless request and returns the response with its body read.
func (e *env) raw(method, path, token string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, nil)
	must(e.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	defer func() { _ = res.Body.Close() }()
	var b bytes.Buffer
	_, _ = b.ReadFrom(res.Body)
	return res, b.Bytes()
}

func TestDeviceLinkSignsTheTVInOnce(t *testing.T) {
	e := newEnv(t)
	c := e.newLink()
	if !regexp.MustCompile(`^[2-9A-HJ-NP-Z]{4}-[2-9A-HJ-NP-Z]{4}$`).MatchString(c.Code) || c.Link != "/link?code="+c.Code || len(c.PollToken) < 32 {
		t.Fatalf("%+v", c)
	}
	if d := time.Until(c.ExpiresAt); d < 9*time.Minute || d > 10*time.Minute {
		t.Fatalf("life %v", d)
	}
	var p api.DeviceLinkPoll
	e.expect(200, "GET", poll(c), "", nil, &p)
	if p.Status != api.Pending || p.SignIn != nil || !p.ExpiresAt.Equal(c.ExpiresAt) {
		t.Fatalf("%+v", p)
	}
	e.expect(404, "GET", "/device-links/"+itoa(c.Id)+"?token=wrong", "", nil, nil)
	e.expect(404, "GET", "/device-links/"+itoa(uid.N(999))+"?token="+c.PollToken, "", nil, nil)

	res, b := e.raw("GET", "/device-links/"+itoa(c.Id)+"/qr?token="+c.PollToken, "")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("qr %d %s", res.StatusCode, b)
	}
	if img, err := png.Decode(bytes.NewReader(b)); err != nil || img.Bounds().Dx() < 100 {
		t.Fatalf("qr image %v", err)
	}
	e.expect(404, "GET", "/device-links/"+itoa(c.Id)+"/qr?token=wrong", "", nil, nil)

	var pv api.DeviceLinkPreview
	e.expect(200, "GET", "/link/"+c.Code, e.plain.token, nil, &pv)
	if pv.DeviceName != "Living room TV" || pv.Platform != api.Roku || !pv.ExpiresAt.Equal(c.ExpiresAt) {
		t.Fatalf("%+v", pv)
	}
	var l api.DeviceLink
	e.expect(200, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, &l)
	if l.Status != api.Approved || *l.AccountId != e.plain.account.ID || *l.HouseholdId != e.plain.household.ID || l.DecidedAt == nil || l.DeviceId != nil {
		t.Fatalf("%+v", l)
	}
	e.expect(404, "POST", "/link/"+c.Code+"/approve", e.admin.token, nil, nil)
	e.expect(404, "POST", "/link/"+c.Code+"/deny", e.plain.token, nil, nil)
	e.expect(404, "GET", "/link/"+c.Code, e.plain.token, nil, nil)

	e.expect(200, "GET", poll(c), "", nil, &p)
	if p.Status != api.Approved || p.SignIn == nil || p.SignIn.Token == "" {
		t.Fatalf("%+v", p)
	}
	me := p.SignIn.Me
	if me.Account.Id != e.plain.account.ID || me.Household == nil || me.Household.Id != e.plain.household.ID || me.Device.Name != "Living room TV" || me.Device.Platform != api.Roku {
		t.Fatalf("%+v", me)
	}
	token := p.SignIn.Token
	e.expect(200, "GET", "/me", token, nil, nil)
	e.expect(404, "GET", poll(c), "", nil, nil)
	e.expect(404, "GET", "/device-links/"+itoa(c.Id)+"/qr?token="+c.PollToken, "", nil, nil)

	var page api.DeviceLinkPage
	e.expect(200, "GET", "/household/device-links", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].DeviceId == nil || *page.Items[0].DeviceId != me.Device.Id || page.Items[0].Status != api.Approved {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", "/household/device-links", e.admin.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("another household's link: %+v", page)
	}
	e.expect(200, "GET", "/device-links", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Code != c.Code {
		t.Fatalf("%+v", page)
	}
	e.expect(404, "DELETE", "/household/device-links/"+itoa(c.Id), e.admin.token, nil, nil)
	e.expect(204, "DELETE", "/household/device-links/"+itoa(c.Id), e.plain.token, nil, nil)
	e.expect(200, "GET", "/me", token, nil, nil)
	e.expect(404, "DELETE", "/device-links/"+itoa(c.Id), e.admin.token, nil, nil)
}

func TestDeviceLinkExpiresAndDenies(t *testing.T) {
	e := newEnv(t)
	advance := e.clock()
	late := e.newLink()
	collectLate := e.newLink()
	e.expect(200, "POST", "/link/"+collectLate.Code+"/approve", e.plain.token, nil, nil)
	advance(10 * time.Minute)
	var p api.DeviceLinkPoll
	for _, c := range []api.DeviceLinkCreated{late, collectLate} {
		e.expect(200, "GET", poll(c), "", nil, &p)
		if p.Status != api.Expired || p.SignIn != nil {
			t.Fatalf("%s: %+v", c.Code, p)
		}
	}
	e.expect(404, "GET", "/link/"+late.Code, e.plain.token, nil, nil)
	e.expect(404, "POST", "/link/"+late.Code+"/approve", e.plain.token, nil, nil)

	c := e.newLink()
	e.expect(204, "POST", "/link/"+c.Code+"/deny", e.plain.token, nil, nil)
	e.expect(404, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, nil)
	advance(time.Hour)
	e.expect(200, "GET", poll(c), "", nil, &p)
	if p.Status != api.Denied || p.SignIn != nil {
		t.Fatalf("%+v", p)
	}
	// An admin delete stops a pending link.
	c = e.newLink()
	e.expect(204, "DELETE", "/device-links/"+itoa(c.Id), e.admin.token, nil, nil)
	e.expect(404, "GET", poll(c), "", nil, nil)
	e.expect(404, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, nil)
}

func TestKidCannotApproveALink(t *testing.T) {
	e := newEnv(t)
	c := e.newLink()
	k := e.kid(e.plain, "Kid")
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(403, "GET", "/link/"+c.Code, e.plain.token, nil, nil)
	e.expect(403, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, nil)
	e.expect(403, "POST", "/link/"+c.Code+"/deny", e.plain.token, nil, nil)
	e.expect(403, "GET", "/household/device-links", e.plain.token, nil, nil)
	e.expect(403, "DELETE", "/household/device-links/"+itoa(c.Id), e.plain.token, nil, nil)
	var p api.DeviceLinkPoll
	e.expect(200, "GET", poll(c), "", nil, &p)
	if p.Status != api.Pending {
		t.Fatalf("%+v", p)
	}
}

// Ten misses in ten minutes lock the adult out of every code, the right
// one included, until the window passes; other adults are unaffected.
func TestGuessingCodesIsRateLimited(t *testing.T) {
	e := newEnv(t)
	advance := e.clock()
	for i := range 10 {
		path := "/link/ZZZZ-ZZZ" + strconv.Itoa(2+i%8)
		switch i % 3 {
		case 1:
			e.expect(404, "POST", path+"/approve", e.plain.token, nil, nil)
		case 2:
			e.expect(404, "POST", path+"/deny", e.plain.token, nil, nil)
		default:
			e.expect(404, "GET", path, e.plain.token, nil, nil)
		}
		advance(10 * time.Second)
	}
	c := e.newLink()
	res, b := e.raw("POST", "/link/"+c.Code+"/approve", e.plain.token)
	retry, _ := strconv.Atoi(res.Header.Get("Retry-After"))
	if res.StatusCode != 429 || errorCode(t, b) != "too_many_requests" || retry != 600-100 {
		t.Fatalf("%d %q %s", res.StatusCode, res.Header.Get("Retry-After"), b)
	}
	e.expect(429, "GET", "/link/"+c.Code, e.plain.token, nil, nil)
	e.expect(200, "GET", "/link/"+c.Code, e.admin.token, nil, nil)
	advance(time.Duration(retry) * time.Second)
	e.expect(200, "GET", "/link/"+c.Code, e.plain.token, nil, nil)
}

func TestPendingLinksAreLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	advance := e.clock()
	for range 5 {
		e.newLink()
	}
	res, b := e.raw("POST", "/device-links", "")
	if res.StatusCode != 400 {
		t.Fatalf("no body: %d %s", res.StatusCode, b)
	}
	got, raw := e.do("POST", "/device-links", "", tv, nil)
	if got != 429 || errorCode(t, raw) != "too_many_requests" {
		t.Fatalf("%d %s", got, raw)
	}
	advance(10 * time.Minute)
	e.newLink()
}

// A device switched to another adult's profile sees, approves and denies
// no link, for the reason ApproveDeviceLink gives.
func TestOnlyTheSignedInAdultDecidesALink(t *testing.T) {
	e := newEnv(t)
	bea := e.householdAdult(e.plain, "Bea", "bea@example.test")
	c := e.newLink()
	if st, raw := e.unlock(e.plain, bea.Me.Account.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(403, "GET", "/link/"+c.Code, e.plain.token, nil, nil)
	e.expect(403, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, nil)
	e.expect(403, "POST", "/link/"+c.Code+"/deny", e.plain.token, nil, nil)
	if st, raw := e.unlock(e.plain, e.plain.account.ID, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	var l api.DeviceLink
	e.expect(200, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, &l)
	if l.AccountId == nil || *l.AccountId != e.plain.account.ID {
		t.Fatalf("%+v", l)
	}
}

// A link nobody collected is deleted a day past its expiry, the next time a
// device asks for one; a link that signed a device in stays on record.
func TestUncollectedLinksArePruned(t *testing.T) {
	e := newEnv(t)
	advance := e.clock()
	stale, denied, used := e.newLink(), e.newLink(), e.newLink()
	e.expect(204, "POST", "/link/"+denied.Code+"/deny", e.plain.token, nil, nil)
	e.expect(200, "POST", "/link/"+used.Code+"/approve", e.plain.token, nil, nil)
	e.expect(200, "GET", poll(used), "", nil, nil)
	advance(10*time.Minute + 24*time.Hour + time.Second)
	fresh := e.newLink()
	var page api.DeviceLinkPage
	e.expect(200, "GET", "/device-links", e.admin.token, nil, &page)
	var ids []uid.ID
	for _, l := range page.Items {
		ids = append(ids, l.Id)
	}
	if len(ids) != 2 || ids[0] != fresh.Id || ids[1] != used.Id {
		t.Fatalf("kept %v; stale %v denied %v", ids, stale.Id, denied.Id)
	}
}

// Both listings page in the database: a page holds limit links and says
// where the next starts.
func TestDeviceLinkListingsPage(t *testing.T) {
	e := newEnv(t)
	var codes []string
	for range 3 {
		c := e.newLink()
		e.expect(200, "POST", "/link/"+c.Code+"/approve", e.plain.token, nil, nil)
		codes = append(codes, c.Code)
	}
	for _, path := range []string{"/device-links", "/household/device-links"} {
		token := e.admin.token
		if path == "/household/device-links" {
			token = e.plain.token
		}
		var page api.DeviceLinkPage
		e.expect(200, "GET", path+"?limit=2", token, nil, &page)
		if len(page.Items) != 2 || page.NextOffset == nil || *page.NextOffset != 2 || page.Items[0].Code != codes[2] {
			t.Fatalf("%s: %+v", path, page)
		}
		e.expect(200, "GET", path+"?limit=2&offset=2", token, nil, &page)
		if len(page.Items) != 1 || page.NextOffset != nil || page.Items[0].Code != codes[0] {
			t.Fatalf("%s: %+v", path, page)
		}
	}
}

// A TV signed in by a link is never an admin device, whoever approved it:
// the admin UI is web only, and a code can be phished.
func TestLinkedDeviceIsNeverAdmin(t *testing.T) {
	e := newEnv(t)
	c := e.newLink()
	e.expect(200, "POST", "/link/"+c.Code+"/approve", e.admin.token, nil, nil)
	var p api.DeviceLinkPoll
	e.expect(200, "GET", poll(c), "", nil, &p)
	tv := p.SignIn.Token
	e.expect(200, "GET", "/me", tv, nil, nil)
	e.expect(403, "GET", "/device-links", tv, nil, nil)
	e.expect(403, "GET", "/sessions", tv, nil, nil)
	e.expect(200, "GET", "/device-links", e.admin.token, nil, nil)
}

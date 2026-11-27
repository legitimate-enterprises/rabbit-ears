package httpapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// provider adds a provider at p through the admin API; extra overrides
// the input's fields.
func (e *env) provider(p *idp, extra map[string]any) api.SsoProvider {
	e.t.Helper()
	in := map[string]any{"name": "Keyhole", "issuer": p.srv.URL, "clientId": p.client, "clientSecret": p.secret, "linksByEmail": true}
	for k, v := range extra {
		in[k] = v
	}
	var out api.SsoProvider
	raw := e.expect(201, "POST", "/admin/sso-providers", e.admin.token, in, &out)
	if bytes.Contains(raw, []byte(p.secret)) {
		e.t.Fatalf("the secret came back: %s", raw)
	}
	return out
}

// ssoFlow runs one sign-in the way a browser does: start, the provider,
// the callback. It answers the sign-in page the callback sends it to.
func (e *env) ssoFlow(b *browser, providerID uid.ID, returnTo string) *url.URL {
	e.t.Helper()
	toIDP, _ := b.get(e.url + "/auth/sso/" + itoa(providerID) + "/start?returnTo=" + returnTo)
	if !strings.Contains(toIDP.Host, "127.0.0.1") {
		return toIDP
	}
	callback, _ := b.get(toIDP.String())
	back, _ := b.get(callback.String())
	return back
}

// ssoCode is a happy sign-in's one-time code.
func (e *env) ssoCode(b *browser, providerID uid.ID) string {
	e.t.Helper()
	back := e.ssoFlow(b, providerID, "viewer")
	f := fragment(e.t, back)
	if back.Path != "/" || f.Get("sso") == "" {
		e.t.Fatalf("callback sent the browser to %s", back)
	}
	return f.Get("sso")
}

func (e *env) ssoError(b *browser, providerID uid.ID) string {
	e.t.Helper()
	back := e.ssoFlow(b, providerID, "viewer")
	return fragment(e.t, back).Get("sso_error")
}

// exchange spends code from b, with whatever cookie b holds.
func (e *env) exchange(b *browser, code string) (int, api.SignInResult) {
	e.t.Helper()
	var out api.SignInResult
	status, cookies := b.post(e.url+"/auth/sso/exchange", map[string]any{"code": code, "deviceName": "Kitchen browser", "platform": "web"}, &out)
	if len(cookies) != 1 || !strings.Contains(cookies[0], "Max-Age=0") {
		e.t.Fatalf("the exchange clears the cookie: %v", cookies)
	}
	return status, out
}

func TestSSOSignInEndToEnd(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	if !prov.ClientSecretSet || !prov.LinksByEmail || !prov.Enabled || prov.RequiredGroup != nil {
		t.Fatalf("%+v", prov)
	}

	var opts []api.SsoOption
	raw := e.expect(200, "GET", "/auth/sso-providers", "", nil, &opts)
	if len(opts) != 1 || opts[0].Id != prov.Id || opts[0].Name != "Keyhole" || strings.Contains(string(raw), "issuer") {
		t.Fatalf("%s", raw)
	}

	b := newBrowser(t)
	code := e.ssoCode(b, prov.Id)
	p.mu.Lock()
	redirect := p.redirects[0]
	p.mu.Unlock()
	if redirect != e.url+"/auth/sso/callback" {
		t.Fatalf("redirect URI %s", redirect)
	}
	if status, _ := e.exchange(newBrowser(t), code); status != 401 {
		t.Fatalf("a code exchanged from another browser: %d", status)
	}
	if status, _ := e.exchange(b, code); status != 401 {
		t.Fatalf("a code refused once is spent: %d", status)
	}
	code = e.ssoCode(b, prov.Id)
	status, signed := e.exchange(b, code)
	if status != 201 || signed.Token == "" || signed.Me.Account.Id != e.plain.account.ID || signed.Me.Device.Name != "Kitchen browser" {
		t.Fatalf("%d %+v", status, signed)
	}
	var me api.Me
	e.expect(200, "GET", "/me", signed.Token, nil, &me)
	if me.Account.Id != e.plain.account.ID || me.Device.Platform != api.Web {
		t.Fatalf("%+v", me)
	}
	if status, _ := e.exchange(b, code); status != 401 {
		t.Fatalf("a one-time code works once: %d", status)
	}

	// Each browser exchanges only the code its own sign-in left it.
	other := newBrowser(t)
	mine, theirs := e.ssoCode(b, prov.Id), e.ssoCode(other, prov.Id)
	if status, _ := e.exchange(other, mine); status != 401 {
		t.Fatalf("a code exchanged with another flow's cookie: %d", status)
	}
	if status, _ := e.exchange(other, theirs); status != 401 {
		t.Fatalf("the refused exchange cleared the cookie: %d", status)
	}

	back := e.ssoFlow(b, prov.Id, "admin")
	if back.Path != "/admin/" || fragment(t, back).Get("sso") == "" {
		t.Fatalf("admin sign-in returned to %s", back)
	}
	if status, _ := e.exchange(b, "not-a-code"); status != 401 {
		t.Fatal(status)
	}
}

func TestSSOLinksByEmailOnceThenBySubject(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	b := newBrowser(t)
	if _, s := e.exchange(b, e.ssoCode(b, prov.Id)); s.Me.Account.Id != e.plain.account.ID {
		t.Fatalf("%+v", s.Me.Account)
	}
	var ids api.AccountIdentityPage
	e.expect(200, "GET", "/accounts/"+itoa(e.plain.account.ID)+"/identities", e.admin.token, nil, &ids)
	if len(ids.Items) != 1 || ids.Items[0].Subject != "user-1" || ids.Items[0].ProviderId != prov.Id || *ids.Items[0].Email != "plain@example.test" {
		t.Fatalf("%+v", ids)
	}

	// The provider's email now names the admin; the subject still names plain.
	p.signInAs(map[string]any{"sub": "user-1", "email": "admin@example.test", "email_verified": true})
	if _, s := e.exchange(b, e.ssoCode(b, prov.Id)); s.Me.Account.Id != e.plain.account.ID {
		t.Fatalf("signed in as %+v", s.Me.Account)
	}

	// Another subject with plain's email cannot take the linked account.
	p.signInAs(map[string]any{"sub": "user-2", "email": "plain@example.test", "email_verified": true})
	if got := e.ssoError(b, prov.Id); got != "no_account" {
		t.Fatal(got)
	}

	// Unlinked, the account links again by email.
	e.expect(404, "DELETE", "/accounts/"+itoa(e.admin.account.ID)+"/identities/"+itoa(ids.Items[0].Id), e.admin.token, nil, nil)
	e.expect(204, "DELETE", "/accounts/"+itoa(e.plain.account.ID)+"/identities/"+itoa(ids.Items[0].Id), e.admin.token, nil, nil)
	if _, s := e.exchange(b, e.ssoCode(b, prov.Id)); s.Me.Account.Id != e.plain.account.ID {
		t.Fatalf("%+v", s.Me.Account)
	}
	e.expect(404, "GET", "/accounts/"+itoa(uid.N(999))+"/identities", e.admin.token, nil, nil)
}

// A user may set their email to anyone's on some providers, so only one the
// provider says it has verified links an account; a linked subject still
// signs in whatever its email.
func TestSSOLinksOnlyAVerifiedEmail(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	for _, v := range []any{nil, false, "false", "yes"} {
		claims := map[string]any{"sub": "user-2", "email": "admin@example.test", "email_verified": v}
		if v == nil {
			delete(claims, "email_verified")
		}
		p.signInAs(claims)
		if got := e.ssoError(newBrowser(t), prov.Id); got != "no_account" {
			t.Fatalf("unverified %#v: %s", v, got)
		}
	}
	var ids api.AccountIdentityPage
	if e.expect(200, "GET", "/accounts/"+itoa(e.admin.account.ID)+"/identities", e.admin.token, nil, &ids); len(ids.Items) != 0 {
		t.Fatalf("%+v", ids)
	}
	for _, c := range []struct {
		verified any
		sub      string
		as       user
	}{{true, "user-3", e.plain}, {"true", "user-4", e.admin}} {
		p.signInAs(map[string]any{"sub": c.sub, "email": c.as.account.Email, "email_verified": c.verified})
		b := newBrowser(t)
		if _, s := e.exchange(b, e.ssoCode(b, prov.Id)); s.Me.Account.Id != c.as.account.ID {
			t.Fatalf("verified %#v: %+v", c.verified, s.Me.Account)
		}
	}
	p.signInAs(map[string]any{"sub": "user-3", "email": "plain@example.test", "email_verified": false})
	b := newBrowser(t)
	if _, s := e.exchange(b, e.ssoCode(b, prov.Id)); s.Me.Account.Id != e.plain.account.ID {
		t.Fatalf("linked, then unverified: %+v", s.Me.Account)
	}
}

func TestSSONeverLinksWithoutLinksByEmail(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, map[string]any{"linksByEmail": false})
	if got := e.ssoError(newBrowser(t), prov.Id); got != "no_account" {
		t.Fatal(got)
	}
	p.signInAs(map[string]any{"sub": "user-9", "email": "nobody@example.test", "email_verified": true})
	prov = e.provider(p, map[string]any{"name": "Second"})
	if got := e.ssoError(newBrowser(t), prov.Id); got != "no_account" {
		t.Fatalf("an unknown email creates no account: %s", got)
	}
	var page api.AccountPage
	e.expect(200, "GET", "/accounts", e.admin.token, nil, &page)
	if len(page.Items) != 2 {
		t.Fatalf("%+v", page.Items)
	}
}

func TestSSORequiredGroup(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, map[string]any{"requiredGroup": "media"})
	b := newBrowser(t)
	if got := e.ssoError(b, prov.Id); got != "not_allowed" {
		t.Fatalf("no groups claim: %s", got)
	}
	p.signInAs(map[string]any{"sub": "user-1", "email": "plain@example.test", "email_verified": true, "groups": []string{"staff"}})
	if got := e.ssoError(b, prov.Id); got != "not_allowed" {
		t.Fatalf("another group: %s", got)
	}
	p.signInAs(map[string]any{"sub": "user-1", "email": "plain@example.test", "email_verified": true, "groups": []string{"staff", "media"}})
	e.ssoCode(b, prov.Id)
}

func TestSSOTokenVerification(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	b := newBrowser(t)
	for name, tweak := range map[string]func(map[string]any){
		"nonce":    func(c map[string]any) { c["nonce"] = "another" },
		"audience": func(c map[string]any) { c["aud"] = "someone-else" },
		"expiry":   func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"issuer":   func(c map[string]any) { c["iss"] = "https://elsewhere.example.com" },
	} {
		p.mu.Lock()
		p.tweak = tweak
		p.mu.Unlock()
		if got := e.ssoError(b, prov.Id); got != "provider_error" {
			t.Errorf("wrong %s: %s", name, got)
		}
	}
	p.mu.Lock()
	p.tweak, p.forge = nil, true
	p.mu.Unlock()
	if got := e.ssoError(b, prov.Id); got != "provider_error" {
		t.Errorf("wrong signature: %s", got)
	}
	p.mu.Lock()
	p.forge = false
	p.mu.Unlock()
	e.ssoCode(b, prov.Id)
}

func TestSSOStateIsSingleUseBoundAndShortLived(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)

	b := newBrowser(t)
	toIDP, cookies := b.get(e.url + "/auth/sso/" + itoa(prov.Id) + "/start")
	if len(cookies) != 1 || !strings.Contains(cookies[0], "HttpOnly") || !strings.Contains(cookies[0], "SameSite=Lax") || strings.Contains(cookies[0], "Secure") {
		t.Fatalf("%v", cookies)
	}
	callback, _ := b.get(toIDP.String())
	if back, _ := b.get(callback.String()); fragment(t, back).Get("sso") == "" {
		t.Fatalf("%s", back)
	}
	if back, _ := b.get(callback.String()); fragment(t, back).Get("sso_error") != "expired" {
		t.Fatalf("state replay: %s", back)
	}

	// Started in one browser, finished in another.
	toIDP, _ = b.get(e.url + "/auth/sso/" + itoa(prov.Id) + "/start?returnTo=admin")
	callback, _ = b.get(toIDP.String())
	other := newBrowser(t)
	if back, _ := other.get(callback.String()); back.Path != "/admin/" || fragment(t, back).Get("sso_error") != "expired" {
		t.Fatalf("callback without the cookie: %s", back)
	}
	if back, _ := b.get(callback.String()); fragment(t, back).Get("sso_error") != "expired" {
		t.Fatalf("a refused state is spent: %s", back)
	}

	toIDP, _ = b.get(e.url + "/auth/sso/" + itoa(prov.Id) + "/start?returnTo=admin")
	callback, _ = b.get(toIDP.String())
	e.svc.SSO.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if back, _ := b.get(callback.String()); back.Path != "/admin/" || fragment(t, back).Get("sso_error") != "expired" {
		t.Fatalf("expired state: %s", back)
	}

	// A code exchanged after its minute is refused.
	e.svc.SSO.Now = time.Now
	code := e.ssoCode(b, prov.Id)
	e.svc.SSO.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if status, _ := e.exchange(b, code); status != 401 {
		t.Fatal(status)
	}
}

// A full table of sign-ins in flight drops the one closest to expiring, so
// callers who never return cannot lock others out.
func TestSSOPendingSignInsAreBounded(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	e.svc.SSO.MaxPending = 2
	var browsers []*browser
	var callbacks []*url.URL
	for range 3 {
		b := newBrowser(t)
		toIDP, _ := b.get(e.url + "/auth/sso/" + itoa(prov.Id) + "/start")
		callback, _ := b.get(toIDP.String())
		browsers, callbacks = append(browsers, b), append(callbacks, callback)
	}
	if back, _ := browsers[2].get(callbacks[2].String()); fragment(t, back).Get("sso") == "" {
		t.Fatalf("the newest sign-in: %s", back)
	}
	if back, _ := browsers[1].get(callbacks[1].String()); fragment(t, back).Get("sso") == "" {
		t.Fatalf("the middle sign-in: %s", back)
	}
	if back, _ := browsers[0].get(callbacks[0].String()); fragment(t, back).Get("sso_error") != "expired" {
		t.Fatalf("the oldest sign-in: %s", back)
	}
}

func TestSSODisabledProvider(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, map[string]any{"enabled": false})
	var opts []api.SsoOption
	e.expect(200, "GET", "/auth/sso-providers", "", nil, &opts)
	if len(opts) != 0 {
		t.Fatalf("%+v", opts)
	}
	b := newBrowser(t)
	if to, _ := b.get(e.url + "/auth/sso/" + itoa(prov.Id) + "/start"); to.Path != "/" || fragment(t, to).Get("sso_error") != "provider_error" {
		t.Fatal(to)
	}
	if to, _ := b.get(e.url + "/auth/sso/" + itoa(uid.N(999)) + "/start?returnTo=admin"); to.Path != "/admin/" || fragment(t, to).Get("sso_error") != "provider_error" {
		t.Fatal(to)
	}

	// Disabled between start and callback.
	in := map[string]any{"name": "Keyhole", "issuer": p.srv.URL, "clientId": p.client, "linksByEmail": true}
	e.expect(200, "PUT", "/admin/sso-providers/"+itoa(prov.Id), e.admin.token, in, nil)
	toIDP, _ := b.get(e.url + "/auth/sso/" + itoa(prov.Id) + "/start")
	callback, _ := b.get(toIDP.String())
	in["enabled"] = false
	e.expect(200, "PUT", "/admin/sso-providers/"+itoa(prov.Id), e.admin.token, in, nil)
	if back, _ := b.get(callback.String()); fragment(t, back).Get("sso_error") != "provider_error" {
		t.Fatal(back)
	}
}

func TestSSOAccountThatMayNotSignIn(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	b := newBrowser(t)
	code := e.ssoCode(b, prov.Id)

	now := time.Now().UTC()
	e.plain.account.RevokedAt = &now
	must(t, e.svc.Accounts.Repo().Update(e.plain.account, nil))
	if status, _ := e.exchange(b, code); status != 401 {
		t.Fatalf("revoked after the callback: %d", status)
	}
	if got := e.ssoError(b, prov.Id); got != "not_allowed" {
		t.Fatalf("revoked, by subject: %s", got)
	}

	pending := &account.Account{Kind: account.Adult, Email: "pending@example.test", Name: "Pending"}
	must(t, e.svc.Accounts.Repo().Create(pending, nil))
	p.signInAs(map[string]any{"sub": "user-3", "email": "pending@example.test", "email_verified": true})
	if got := e.ssoError(b, prov.Id); got != "not_allowed" {
		t.Fatalf("pending, by email: %s", got)
	}
	var ids api.AccountIdentityPage
	e.expect(200, "GET", "/accounts/"+itoa(pending.ID)+"/identities", e.admin.token, nil, &ids)
	if len(ids.Items) != 0 {
		t.Fatalf("a pending adult is not linked: %+v", ids)
	}
}

func TestSSOFailuresDoNotCountTowardTheSignInLimit(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, map[string]any{"linksByEmail": false})
	b := newBrowser(t)
	for range 11 {
		e.ssoError(b, prov.Id)
	}
	e.expect(401, "POST", "/auth/sign-in", "", map[string]any{"email": "plain@example.test", "password": "nope", "deviceName": "Laptop", "platform": "web"}, nil)
}

func TestSSOProvidersCRUDKeepTheSecretWriteOnly(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, map[string]any{"requiredGroup": "media"})
	path := "/admin/sso-providers/" + itoa(prov.Id)
	for _, raw := range [][]byte{
		e.expect(200, "GET", path, e.admin.token, nil, nil),
		e.expect(200, "GET", "/admin/sso-providers", e.admin.token, nil, nil),
		e.expect(200, "GET", "/auth/sso-providers", "", nil, nil),
	} {
		if bytes.Contains(raw, []byte(p.secret)) {
			t.Fatalf("%s", raw)
		}
	}

	in := map[string]any{"name": "Front door", "issuer": p.srv.URL, "clientId": p.client, "linksByEmail": true}
	var got api.SsoProvider
	e.expect(200, "PUT", path, e.admin.token, in, &got)
	if got.Name != "Front door" || !got.ClientSecretSet || got.RequiredGroup != nil {
		t.Fatalf("absent keeps the secret: %+v", got)
	}
	e.ssoCode(newBrowser(t), prov.Id)

	p.secret = "rotated"
	in["clientSecret"] = "rotated"
	raw := e.expect(200, "PUT", path, e.admin.token, in, &got)
	if bytes.Contains(raw, []byte("rotated")) {
		t.Fatalf("%s", raw)
	}
	e.ssoCode(newBrowser(t), prov.Id)

	in["clientSecret"] = ""
	e.expect(200, "PUT", path, e.admin.token, in, &got)
	if got.ClientSecretSet {
		t.Fatalf("empty removes the secret: %+v", got)
	}
	p.secret = "" // a public client

	var page api.SsoProviderPage
	e.expect(200, "GET", "/admin/sso-providers", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != prov.Id {
		t.Fatalf("%+v", page)
	}
	e.ssoCode(newBrowser(t), prov.Id)
	e.expect(204, "DELETE", path, e.admin.token, nil, nil)
	e.expect(404, "GET", path, e.admin.token, nil, nil)
	e.expect(404, "PUT", path, e.admin.token, in, nil)
	e.expect(404, "DELETE", path, e.admin.token, nil, nil)
	var ids api.AccountIdentityPage
	e.expect(200, "GET", "/accounts/"+itoa(e.plain.account.ID)+"/identities", e.admin.token, nil, &ids)
	if len(ids.Items) != 0 {
		t.Fatalf("identities go with their provider: %+v", ids)
	}
	e.expect(403, "GET", "/admin/sso-providers", e.plain.token, nil, nil)
}

func TestSSOIssuerIsCheckedOnSave(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	for _, issuer := range []string{gone.URL, p.srv.URL + "/nothing-here"} {
		raw := e.expect(400, "POST", "/admin/sso-providers", e.admin.token, map[string]any{"name": "X", "issuer": issuer, "clientId": "c"}, nil)
		if !strings.Contains(string(raw), `"field":"issuer"`) {
			t.Fatalf("%s", raw)
		}
	}
	p.issuer = "https://elsewhere.example.com"
	raw := e.expect(400, "POST", "/admin/sso-providers", e.admin.token, map[string]any{"name": "X", "issuer": p.srv.URL, "clientId": "c"}, nil)
	if !strings.Contains(string(raw), `"field":"issuer"`) {
		t.Fatalf("%s", raw)
	}
	p.issuer = p.srv.URL
	prov := e.provider(p, nil)
	p.issuer = "https://elsewhere.example.com"
	e.expect(400, "PUT", "/admin/sso-providers/"+itoa(prov.Id), e.admin.token, map[string]any{"name": "X", "issuer": p.srv.URL, "clientId": "c"}, nil)
}

// The redirect URI is https only when the connection is TLS or a proxy on
// this host says the client used https.
func TestSSORedirectURIScheme(t *testing.T) {
	e := newEnv(t)
	p := newIDP(t)
	prov := e.provider(p, nil)
	start := httpapi.BasePath + "/auth/sso/" + itoa(prov.Id) + "/start"
	redirect := func(peer string) (string, string) {
		h, err := httpapi.NewHandler("1.2.3", e.svc)
		must(t, err)
		req := httptest.NewRequest("GET", "http://media.example.com"+start, nil)
		req.RemoteAddr = peer
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusFound {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		to, err := url.Parse(w.Header().Get("Location"))
		must(t, err)
		return to.Query().Get("redirect_uri"), w.Header().Get("Set-Cookie")
	}
	uri, cookie := redirect("127.0.0.1:51000")
	if uri != "https://media.example.com/api/v2/auth/sso/callback" || !strings.Contains(cookie, "Secure") {
		t.Fatalf("from a local proxy: %s %s", uri, cookie)
	}
	uri, cookie = redirect("192.0.2.10:51000")
	if uri != "http://media.example.com/api/v2/auth/sso/callback" || strings.Contains(cookie, "Secure") {
		t.Fatalf("from elsewhere: %s %s", uri, cookie)
	}
}

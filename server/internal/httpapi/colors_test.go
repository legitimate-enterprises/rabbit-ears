package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/gravatar"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
)

func color(a api.Account) string {
	if a.Color == nil {
		return ""
	}
	return *a.Color
}

// Every profile carries a palette color the server picks; an adult sets
// their own, and any adult a kid's of the household.
func TestProfileColorsAndPreferences(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	var me api.Me
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if color(me.Account) != account.Palette[0] || len(me.Profiles) != 2 || color(me.Profiles[1]) != account.Palette[1] || color(k) != account.Palette[1] {
		t.Fatalf("%+v", me)
	}
	if me.Account.HideKidsContent == nil || *me.Account.HideKidsContent || me.Profiles[1].HideKidsContent != nil {
		t.Fatalf("an adult says whether they hide kids' content; a kid says nothing: %+v", me)
	}
	var k2 api.Account
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"name": "Two", "birthday": "2018-05-01", "color": "#6B2E5A"}, &k2)
	if color(k2) != "#6B2E5A" {
		t.Fatalf("%+v", k2)
	}
	e.expect(200, "PUT", "/profiles/"+itoa(k2.Id), e.plain.token, map[string]any{"name": "Two", "birthday": "2018-05-01"}, &k2)
	if color(k2) != "#6B2E5A" {
		t.Fatalf("replacing without a color keeps it: %+v", k2)
	}
	e.expect(400, "POST", "/profiles", e.plain.token, map[string]any{"name": "Bad", "birthday": "2018-05-01", "color": "#000000"}, nil)

	var got api.Account
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id)+"/preferences", e.plain.token, map[string]any{"color": "#4A6B2E"}, &got)
	if color(got) != "#4A6B2E" || got.Id != k.Id {
		t.Fatalf("%+v", got)
	}
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/preferences", e.plain.token, map[string]any{"color": "#8A5A2B"}, &got)
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID), e.plain.token, nil, &got)
	if color(got) != "#8A5A2B" || got.HideKidsContent == nil || *got.HideKidsContent {
		t.Fatalf("a color alone leaves hideKidsContent: %+v", got)
	}
	e.expect(400, "PUT", "/profiles/"+itoa(k.Id)+"/preferences", e.plain.token, map[string]any{"color": "#8a5a2b"}, nil)
	e.expect(400, "PUT", "/profiles/"+itoa(k.Id)+"/preferences", e.plain.token, map[string]any{"color": "#123456"}, nil)
	e.expect(403, "PUT", "/profiles/"+itoa(k.Id)+"/preferences", e.plain.token, map[string]any{"hideKidsContent": true}, nil)

	// Another adult of the household is theirs to set; a kid sets nothing.
	neighbor := e.neighbor(e.plain)
	e.expect(403, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/preferences", neighbor, map[string]any{"color": "#2E5B8A"}, nil)
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id)+"/preferences", neighbor, map[string]any{"color": "#2E5B8A"}, nil)
	kidToken := e.kidDevice(e.plain, k.Id)
	e.expect(403, "PUT", "/profiles/"+itoa(k.Id)+"/preferences", kidToken, map[string]any{"color": "#3C4170"}, nil)
	e.expect(404, "PUT", "/profiles/"+itoa(e.admin.account.ID)+"/preferences", e.plain.token, map[string]any{"color": "#3C4170"}, nil)

	// A profile with no color stored shows the palette color its id picks.
	res, err := e.svc.Accounts.Repo().Get(k2.Id)
	must(t, err)
	res.Color = ""
	must(t, e.svc.Accounts.Repo().Update(res, nil))
	e.expect(200, "GET", "/profiles/"+itoa(k2.Id), e.plain.token, nil, &got)
	if color(got) != account.Palette[int(k2.Id[len(k2.Id)-1])%len(account.Palette)] {
		t.Fatalf("%+v", got)
	}
}

// With the gravatar setting on, an adult with a Gravatar has an avatarUrl
// the server answers from its own copy, also while the device is locked.
func TestGravatar(t *testing.T) {
	e := newEnv(t)
	sum := sha256.Sum256([]byte("plain@example.test"))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/avatar/"+hex.EncodeToString(sum[:]) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nplain"))
	}))
	t.Cleanup(srv.Close)
	c := gravatar.New(t.TempDir())
	c.Base, c.Client = srv.URL+"/avatar", srv.Client()
	e.svc.Avatars = c
	must(t, e.svc.Server.Save("Den"))
	k := e.kid(e.plain, "Kid")
	neighbor := e.neighbor(e.plain)

	var me api.Me
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if me.Account.AvatarUrl != nil || calls != 0 {
		t.Fatalf("off by default, and nothing is asked: %+v", me.Account)
	}
	avatar := "/profiles/" + itoa(e.plain.account.ID) + "/avatar"
	e.expect(404, "GET", avatar, e.plain.token, nil, nil)

	var set api.Settings
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "gravatar": true}, &set)
	if set.Gravatar == nil || !*set.Gravatar {
		t.Fatalf("%+v", set)
	}
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den"}, &set)
	if !*set.Gravatar {
		t.Fatal("absent keeps the setting")
	}
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if me.Account.AvatarUrl == nil || *me.Account.AvatarUrl != httpapi.BasePath+avatar {
		t.Fatalf("%+v", me.Account)
	}
	for _, p := range me.Profiles {
		if (p.AvatarUrl != nil) != (p.Id == e.plain.account.ID) {
			t.Fatalf("only the adult with a Gravatar has one: %+v", p)
		}
	}
	var page api.AccountPage
	e.expect(200, "GET", "/profiles", neighbor, nil, &page)
	if p := profileByID(page, e.plain.account.ID); p == nil || p.AvatarUrl == nil {
		t.Fatalf("%+v", page)
	}

	e.expect(204, "POST", "/me/lock", neighbor, nil, nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.url+avatar, http.NoBody)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+neighbor)
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || string(body) != "\x89PNG\r\n\x1a\nplain" || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("%d %q %v", res.StatusCode, body, res.Header)
	}
	e.expect(404, "GET", "/profiles/"+itoa(k.Id)+"/avatar", e.plain.token, nil, nil)
	e.expect(404, "GET", "/profiles/"+itoa(e.admin.account.ID)+"/avatar", e.plain.token, nil, nil)
	if calls != 2 {
		t.Fatalf("each address is asked once: %d", calls)
	}
	e.expect(404, "GET", avatar, e.admin.token, nil, nil)
	e.expect(201, "POST", "/adult-links", e.admin.token, map[string]any{"accountId": e.plain.account.ID, "otherAccountId": e.admin.account.ID}, nil)
	e.expect(200, "GET", avatar, e.admin.token, nil, nil)

	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "gravatar": false}, nil)
	e.expect(404, "GET", avatar, e.plain.token, nil, nil)
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if me.Account.AvatarUrl != nil {
		t.Fatalf("%+v", me.Account)
	}
}

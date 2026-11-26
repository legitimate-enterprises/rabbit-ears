package httpapi_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

func TestHealthIsPublic(t *testing.T) {
	e := newEnv(t)
	var h api.Health
	e.expect(200, "GET", "/health", "", nil, &h)
	if h.Status != "ok" || h.Version != "1.2.3" {
		t.Fatalf("%+v", h)
	}
	e.expect(405, "POST", "/health", "", nil, nil)
	e.expect(404, "GET", "/nope", "", nil, nil)
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// Every operation's security in the spec is what the server enforces: no
// token is 401 unless the operation is public, and a non-admin is 403 on
// every admin-scoped operation. A device locked at Who's watching is 403
// device_locked on every operation the spec does not mark x-while-locked.
func TestSpecSecurityIsEnforced(t *testing.T) {
	e := newEnv(t)
	doc, err := api.GetSpec()
	must(t, err)
	locked, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "Tablet", "web")
	must(t, err)
	e.expect(204, "POST", "/me/lock", locked, nil, nil)
	for path, item := range doc.Paths.Map() {
		concrete := pathParam.ReplaceAllStringFunc(path, func(p string) string {
			if p == "{code}" {
				return "AAAA-AAAA"
			}
			return "999"
		})
		for method, op := range item.Operations() {
			sec := doc.Security
			if op.Security != nil {
				sec = *op.Security
			}
			public := len(sec) == 0
			admin := !public && len(sec[0]["device"]) > 0
			status, raw := e.do(method, concrete, "", nil, nil)
			if public && status == http.StatusUnauthorized {
				t.Errorf("%s %s is public but answered 401", method, path)
			}
			if !public && status != http.StatusUnauthorized {
				t.Errorf("%s %s without a token = %d %s", method, path, status, raw)
			}
			if !public {
				if status, _ := e.do(method, concrete, "bogus", nil, nil); status != http.StatusUnauthorized {
					t.Errorf("%s %s with a bad token = %d", method, path, status)
				}
			}
			if admin {
				status, raw := e.do(method, concrete, e.plain.token, nil, nil)
				if status != http.StatusForbidden || errorCode(t, raw) != "forbidden" {
					t.Errorf("%s %s as non-admin = %d %s", method, path, status, raw)
				}
			}
			if !public && op.Extensions["x-while-locked"] != true {
				status, raw := e.do(method, concrete, locked, nil, nil)
				if status != http.StatusForbidden || errorCode(t, raw) != "device_locked" {
					t.Errorf("%s %s while locked = %d %s", method, path, status, raw)
				}
			}
		}
	}
}

func TestSignOutRevokesTheToken(t *testing.T) {
	e := newEnv(t)
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)
	e.expect(204, "POST", "/auth/sign-out", e.plain.token, nil, nil)
	e.expect(401, "GET", "/me", e.plain.token, nil, nil)
}

func TestMeShowsOnlyTheDeviceHousehold(t *testing.T) {
	e := newEnv(t)
	var me api.Me
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if me.Account.Id != e.plain.account.ID || me.Device.Id != e.plain.deviceID || me.Household == nil ||
		me.Household.Id != e.plain.household.ID || len(me.Profiles) != 1 || me.Profiles[0].Id != e.plain.account.ID {
		t.Fatalf("%+v", me)
	}
}

func TestValidationErrorsNameTheField(t *testing.T) {
	e := newEnv(t)
	raw := e.expect(400, "POST", "/libraries", e.admin.token, map[string]any{"name": "Films", "kind": "vhs"}, nil)
	if errorCode(t, raw) != "bad_request" || !strings.Contains(string(raw), "kind") {
		t.Fatalf("%s", raw)
	}
	raw = e.expect(400, "GET", "/libraries?limit=0", e.admin.token, nil, nil)
	if !strings.Contains(string(raw), "limit") {
		t.Fatalf("%s", raw)
	}
	e.expect(400, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/pin", e.plain.token, map[string]any{"pin": "12a4"}, nil)
	e.expect(400, "GET", "/libraries/abc", e.admin.token, nil, nil)
}

func TestMissingBodyFieldsAreNamed(t *testing.T) {
	e := newEnv(t)
	raw := e.expect(400, "POST", "/auth/sign-in", "", map[string]any{"email": "a@example.test"}, nil)
	for _, f := range []string{"password", "deviceName", "platform"} {
		if !strings.Contains(string(raw), `"field":"`+f+`"`) {
			t.Errorf("%s not named in %s", f, raw)
		}
	}
}

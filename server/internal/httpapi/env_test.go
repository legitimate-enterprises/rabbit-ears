package httpapi_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// env is a server over a migrated SQLite file with two households: an admin
// adult and a plain adult, each signed in on one device.
type env struct {
	t     *testing.T
	url   string
	svc   *service.Services
	db    *sql.DB
	admin user
	plain user
}

type user struct {
	token     string
	account   *account.Account
	household *household.Household
	deviceID  uid.ID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.OpenPreferLocal(t)
	svc := service.New(d)
	h, err := httpapi.NewHandler("1.2.3", svc)
	if err != nil {
		t.Fatal(err)
	}
	svc.Channels.IdentDir = t.TempDir()
	t.Cleanup(svc.Channels.Close)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e := &env{t: t, url: srv.URL + httpapi.BasePath, svc: svc, db: d}
	e.admin = e.adult("admin@example.test", true)
	e.plain = e.adult("plain@example.test", false)
	return e
}

func (e *env) adult(email string, admin bool) user {
	e.t.Helper()
	a := &account.Account{Kind: account.Adult, Email: email, Name: email, IsAdmin: admin, PasswordHash: "x"}
	must(e.t, e.svc.Accounts.Repo().Create(a, nil))
	h := &household.Household{Name: email, AdultID: a.ID}
	must(e.t, e.svc.Households.Repo().Create(h, nil))
	a.HouseholdID = &h.ID
	must(e.t, e.svc.Accounts.Repo().Update(a, nil))
	token, d, err := e.svc.Devices.Register(a.ID, h.ID, "Laptop", "web")
	must(e.t, err)
	return user{token, a, h, d.ID}
}

// onDisk makes an empty file at p inside a directory of the test's own and
// returns its path: playback starts only on a file that is there.
func (e *env) onDisk(p string) string {
	e.t.Helper()
	p = filepath.Join(e.t.TempDir(), p)
	must(e.t, os.MkdirAll(filepath.Dir(p), 0o750))
	must(e.t, os.WriteFile(p, nil, 0o600))
	return p
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// do sends body as JSON (nil for none) and decodes a 2xx reply into a
// non-nil out.
func (e *env) do(method, path, token string, body, out any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(e.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.url+path, rd)
	must(e.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 && res.StatusCode < 300 {
		// Decode into a zeroed value so fields left over from a reused
		// variable cannot pass for fields the server sent.
		v := reflect.ValueOf(out).Elem()
		v.Set(reflect.Zero(v.Type()))
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: %v in %s", method, path, err, raw)
		}
	}
	return res.StatusCode, raw
}

// expect fails unless the call answers want; non-2xx replies must carry the
// shared Error shape.
func (e *env) expect(want int, method, path, token string, body, out any) []byte {
	e.t.Helper()
	got, raw := e.do(method, path, token, body, out)
	if got != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, got, want, raw)
	}
	if got >= 400 {
		var er struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &er); err != nil || er.Code == "" || er.Message == "" {
			e.t.Fatalf("%s %s: error body not the shared shape: %s", method, path, raw)
		}
	}
	return raw
}

func errorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var er struct {
		Code   string `json:"code"`
		Fields []struct{ Field, Message string }
	}
	must(t, json.Unmarshal(raw, &er))
	return er.Code
}

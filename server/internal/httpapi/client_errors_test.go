package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func clientError(id uid.ID, msg string) map[string]any {
	return map[string]any{"id": id, "at": "2026-03-01T12:00:00Z", "message": msg, "context": "home"}
}

func clientErrors(entries ...map[string]any) map[string]any {
	es := make([]any, len(entries))
	for i, x := range entries {
		es[i] = x
	}
	return map[string]any{"appVersion": "1.4.7", "osVersion": "iOS 27.1", "entries": es}
}

func (e *env) clientLogs(query string) []api.LogEntry {
	e.t.Helper()
	var p api.LogEntryPage
	e.expect(200, "GET", "/logs?source=client"+query, e.admin.token, nil, &p)
	return p.Items
}

// Each error is stored once however often it is sent, as the caller's
// device and profile, and the admin's Logs list and download show it.
func TestClientErrorsStoreOnceAsTheCaller(t *testing.T) {
	e := newEnv(t)
	first, second := uid.New(), uid.New()
	detail := map[string]any{"id": second, "at": time.Now().Add(time.Hour).UTC(), "message": "Could not load details",
		"context": "details", "detail": "TypeError: x is undefined\n  at render (details.ts:12)"}
	body := clientErrors(clientError(first, "Home failed to load"), detail)
	before := time.Now()
	e.expect(204, "POST", "/client-errors", e.plain.token, body, nil)
	e.expect(204, "POST", "/client-errors", e.plain.token, body, nil)
	if n := e.count(`SELECT COUNT(*) FROM client_log_entry`); n != 2 {
		t.Fatalf("stored %d", n)
	}
	got := e.clientLogs("")
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	newest := got[0]
	if newest.Id != second || newest.Level != api.LogLevelError || newest.Source != "client" || newest.Message != "Could not load details" ||
		newest.At.Before(before) || newest.At.After(time.Now()) {
		t.Fatalf("a future at is now: %+v", newest)
	}
	c := newest.Client
	if c == nil || c.Platform != api.Web || c.AppVersion != "1.4.7" || c.OsVersion != "iOS 27.1" || c.Context != "details" ||
		deref(c.Detail) != "TypeError: x is undefined\n  at render (details.ts:12)" || *c.DeviceId != e.plain.deviceID ||
		deref(c.DeviceName) != "Laptop" || *c.ProfileId != e.plain.account.ID || deref(c.ProfileName) != e.plain.account.Name {
		t.Fatalf("origin: %+v", c)
	}
	if !got[1].At.Equal(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("at: %v", got[1].At)
	}
	if n := len(e.clientLogs("&platform=web")); n != 2 {
		t.Errorf("platform web: %d", n)
	}
	if n := len(e.clientLogs("&platform=ios")); n != 0 {
		t.Errorf("platform ios: %d", n)
	}
	var server api.LogEntryPage
	e.expect(200, "GET", "/logs?source=scan", e.admin.token, nil, &server)
	if len(server.Items) != 0 {
		t.Errorf("source scan: %+v", server.Items)
	}
	_, raw := e.do("GET", "/logs/download?source=client", e.admin.token, nil, nil)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "2026-03-01T12:00:00Z ERROR client Home failed to load | web 1.4.7 on iOS 27.1 | context: home | device: Laptop | profile: plain@example.test" ||
		!strings.HasSuffix(lines[1], " | context: details | device: Laptop | profile: plain@example.test | detail: TypeError: x is undefined   at render (details.ts:12)") {
		t.Fatalf("download: %q", raw)
	}
}

// Credentials the server recognizes are redacted and control characters
// escaped before anything is stored; a detail keeps its lines.
func TestClientErrorsScrubTheirText(t *testing.T) {
	e := newEnv(t)
	x := clientError(uid.New(), "GET /api/v2/streams/SECRET1/index.m3u8?token=SECRET2 failed\x1b[2J")
	x["context"] = "player\r\nlevel=ERROR"
	x["detail"] = "Authorization: Bearer SECRET3\nline two"
	e.expect(204, "POST", "/client-errors", e.plain.token, clientErrors(x), nil)
	got := e.clientLogs("")[0]
	all := got.Message + got.Client.Context + deref(got.Client.Detail)
	for _, s := range []string{"SECRET1", "SECRET2", "SECRET3", "\x1b", "\r"} {
		if strings.Contains(all, s) {
			t.Errorf("stored %q", s)
		}
	}
	if strings.Contains(got.Client.Context, "\n") || !strings.Contains(deref(got.Client.Detail), "\nline two") {
		t.Errorf("context %q detail %q", got.Client.Context, deref(got.Client.Detail))
	}
}

// Every bound the spec states is enforced at the boundary with the shared
// error shape, and nothing of a refused batch is stored.
func TestClientErrorsRefuseOversizeInput(t *testing.T) {
	e := newEnv(t)
	with := func(k string, v any) map[string]any {
		x := clientError(uid.New(), "m")
		x[k] = v
		return clientErrors(x)
	}
	many := make([]map[string]any, 26)
	for i := range many {
		many[i] = clientError(uid.New(), "m")
	}
	batch := func(k string, v any) map[string]any {
		b := clientErrors(clientError(uid.New(), "m"))
		b[k] = v
		return b
	}
	noContext := clientError(uid.New(), "m")
	delete(noContext, "context")
	for name, body := range map[string]any{
		"no entries":       map[string]any{"appVersion": "1", "osVersion": "x", "entries": []any{}},
		"26 entries":       clientErrors(many...),
		"message 1001":     with("message", strings.Repeat("é", 1001)),
		"empty message":    with("message", ""),
		"context 101":      with("context", strings.Repeat("c", 101)),
		"no context":       clientErrors(noContext),
		"detail 4001":      with("detail", strings.Repeat("d", 4001)),
		"id not a UUIDv7":  with("id", "00000000-0000-4000-8000-000000000001"),
		"at not a time":    with("at", "yesterday"),
		"appVersion 65":    batch("appVersion", strings.Repeat("1", 65)),
		"no appVersion":    map[string]any{"osVersion": "x", "entries": []any{clientError(uid.New(), "m")}},
		"osVersion 257":    batch("osVersion", strings.Repeat("o", 257)),
		"empty osVersion":  batch("osVersion", ""),
		"entries not list": batch("entries", "x"),
	} {
		if got, raw := e.do("POST", "/client-errors", e.plain.token, body, nil); got != 400 || !strings.Contains(string(raw), `"bad_request"`) {
			t.Errorf("%s: %d %s", name, got, raw)
		}
	}
	e.expect(413, "POST", "/client-errors", e.plain.token, with("detail", strings.Repeat("d", 1<<20)), nil)
	e.expect(401, "POST", "/client-errors", "", clientErrors(clientError(uid.New(), "m")), nil)
	if n := e.count(`SELECT COUNT(*) FROM log_entry WHERE source = 'client'`); n != 0 {
		t.Fatalf("stored %d", n)
	}
	most := clientError(uid.New(), strings.Repeat("é", 1000))
	most["context"], most["detail"] = strings.Repeat("c", 100), strings.Repeat("d", 4000)
	full := clientErrors(most)
	full["appVersion"], full["osVersion"] = strings.Repeat("1", 64), strings.Repeat("o", 256)
	e.expect(204, "POST", "/client-errors", e.plain.token, full, nil)
}

// A device sends at most 100 entries an hour; past that a batch answers 429
// and stores nothing, while other devices are unaffected.
func TestClientErrorsAreLimitedPerDevice(t *testing.T) {
	e := newEnv(t)
	full := func() map[string]any {
		es := make([]map[string]any, 25)
		for i := range es {
			es[i] = clientError(uid.New(), "m")
		}
		return clientErrors(es...)
	}
	for range 4 {
		e.expect(204, "POST", "/client-errors", e.plain.token, full(), nil)
	}
	one := clientErrors(clientError(uid.New(), "one too many"))
	raw, err := json.Marshal(one)
	must(t, err)
	status, header, _ := e.send("POST", "/client-errors", e.plain.token, "application/json", raw)
	if status != 429 || header.Get("Retry-After") == "" {
		t.Fatalf("%d %v", status, header)
	}
	if n := e.count(`SELECT COUNT(*) FROM log_entry WHERE message = 'one too many'`); n != 0 {
		t.Fatal("a refused batch was stored")
	}
	e.expect(204, "POST", "/client-errors", e.admin.token, full(), nil)
	e.svc.ClientErrors.Now = func() time.Time { return time.Now().Add(time.Hour) }
	e.expect(204, "POST", "/client-errors", e.plain.token, one, nil)
}

// Queued offline, a batch replays through the sync actions path as the
// acting profile, even on a locked device; sent again under a new action,
// its entries are not stored twice, and past the limit it is rejected.
func TestClientErrorsReplay(t *testing.T) {
	e := newEnv(t)
	kid := uid.New()
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"id": kid, "name": "Kid", "birthday": "2018-05-01",
		"movieCeiling": "PG", "tvCeiling": "TV-Y7"}, nil)
	e.expect(204, "POST", "/me/lock", e.plain.token, nil, nil)
	id := uid.New()
	body := clientErrors(clientError(id, "Offline play failed"))
	e.accepted(e.plain, action("reportClientErrors", "POST", "/client-errors", kid, body))
	e.accepted(e.plain, action("reportClientErrors", "POST", "/client-errors", kid, body))
	got := e.clientLogs("")
	if len(got) != 1 || got[0].Id != id || *got[0].Client.ProfileId != kid || deref(got[0].Client.ProfileName) != "Kid" ||
		*got[0].Client.DeviceId != e.plain.deviceID {
		t.Fatalf("%+v", got)
	}
	e.expect(204, "POST", "/client-errors", e.plain.token, clientErrors(clientError(uid.New(), "Locked at Who's watching")), nil)
	bad := clientErrors(clientError(uid.New(), ""))
	rejected(t, e.replay(e.plain, action("reportClientErrors", "POST", "/client-errors", kid, bad))[0], "bad_request")
	es := make([]map[string]any, 25)
	for i := range es {
		es[i] = clientError(uid.New(), "m")
	}
	// Three counted so far, each sent counting: 78 after these, 103 after one more.
	for range 3 {
		e.accepted(e.plain, action("reportClientErrors", "POST", "/client-errors", kid, clientErrors(es...)))
	}
	rejected(t, e.replay(e.plain, action("reportClientErrors", "POST", "/client-errors", kid, clientErrors(es...)))[0], "too_many_requests")
}

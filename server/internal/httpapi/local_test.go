package httpapi_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
)

// visitor is where the public proxy says a request came from.
const visitor = "203.0.113.5"

// through sends a call as the server's reverse proxy forwards it.
func (e *env) through(forwardedFor, method, path, token string, body, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(e.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.url+path, rd)
	must(e.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", forwardedFor)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	defer func() { _ = res.Body.Close() }()
	if out != nil && res.StatusCode < 300 {
		must(e.t, json.NewDecoder(res.Body).Decode(out))
	}
	return res.StatusCode
}

func proof(t *testing.T, key, side, nonce string) string {
	t.Helper()
	k, err := base64.StdEncoding.DecodeString(key)
	must(t, err)
	m := hmac.New(sha256.New, k)
	m.Write([]byte(side + ":" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// A device that reaches the server through its public address plays as
// away from home until it confirms a local check from the server's own
// network; the API calls stay on the public address throughout.
func TestConfirmedLocalCheckPlaysAsAtHome(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	snap := awayWeb(true, 0)
	snap["network"].(map[string]any)["discoveredLocally"] = true
	start := func() api.PlaybackStream {
		t.Helper()
		var st api.PlaybackStream
		body := map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": snap}
		if got := e.through(visitor, "POST", "/playback/sessions", e.plain.token, body, &st); got != 201 {
			t.Fatalf("start = %d", got)
		}
		e.expect(204, "DELETE", "/playback/sessions/"+itoa(st.SessionId), e.plain.token, nil, nil)
		return st
	}
	if st := start(); st.Away == nil || !*st.Away {
		t.Fatalf("through the public address with nothing confirmed: %+v", st)
	}

	var c api.LocalCheck
	if got := e.through(visitor, "POST", "/local-checks", e.plain.token, nil, &c); got != 201 {
		t.Fatalf("open = %d", got)
	}
	port := strings.TrimSuffix(e.url, httpapi.BasePath)
	port = port[strings.LastIndex(port, ":"):]
	for _, a := range c.Addresses {
		if u, err := url.Parse(a); err != nil || u.Scheme != "http" || !strings.HasSuffix(u.Host, port) || u.Path != "" {
			t.Fatalf("address %q is not the server's own port over http", a)
		}
	}
	const nonce = "0123456789abcdef"
	confirm := "/local-checks/" + c.Id + "/confirmation"
	mine := map[string]any{"nonce": nonce, "proof": proof(t, c.Key, "client", nonce)}
	if got := e.through(visitor, "POST", confirm, "", mine, nil); got != 403 {
		t.Fatalf("a confirmation through the public address = %d", got)
	}
	e.expect(403, "POST", confirm, "", map[string]any{"nonce": nonce, "proof": proof(t, c.Key, "server", nonce)}, nil)
	e.expect(400, "POST", confirm, "", map[string]any{"nonce": "short", "proof": proof(t, c.Key, "client", "short")}, nil)
	if st := start(); st.Away == nil || !*st.Away {
		t.Fatalf("refused confirmations change nothing: %+v", st)
	}

	var done api.LocalCheckConfirmation
	e.expect(200, "POST", confirm, "", mine, &done)
	if done.Proof != proof(t, c.Key, "server", nonce) {
		t.Fatalf("the server's proof: %q", done.Proof)
	}
	e.expect(404, "POST", confirm, "", mine, nil)
	if st := start(); st.Away != nil || st.Entry.Verdict != api.DirectPlay {
		t.Fatalf("after a local confirmation the device is at home: %+v", st)
	}

	snap["network"].(map[string]any)["discoveredLocally"] = false
	if st := start(); st.Away == nil || !*st.Away {
		t.Fatalf("a device that says it is away is away: %+v", st)
	}
	e.expect(401, "POST", "/local-checks", "", nil, nil)
}

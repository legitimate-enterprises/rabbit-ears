package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// A body past the cap is refused with the shared error shape before it is
// read whole, on unauthenticated operations too.
func TestOversizedBodyIsRefused(t *testing.T) {
	e := newEnv(t)
	name := bytes.Repeat([]byte("a"), 2<<20)
	body, err := json.Marshal(map[string]any{"deviceName": string(name), "platform": "roku"})
	must(t, err)
	req, err := http.NewRequest("POST", e.url+"/device-links", bytes.NewReader(body))
	must(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusRequestEntityTooLarge || errorCode(t, raw) != "payload_too_large" {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	e.newLink()
}

// A member sent as null is absent, as generated clients that write every
// optional property send them; a required one stays required.
func TestNullMembersAreAbsent(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	path := "/profiles/" + itoa(e.plain.account.ID) + "/watch-state/" + itoa(m)
	e.expect(200, "PUT", path, e.plain.token, map[string]any{"watched": true, "positionMs": nil, "editionId": nil, "at": nil}, nil)
	raw := e.expect(400, "PUT", path, e.plain.token, map[string]any{"watched": nil}, nil)
	if !bytes.Contains(raw, []byte(`"watched"`)) {
		t.Fatalf("%s", raw)
	}
}

// Repeated failed sign-ins from one address answer 429 with Retry-After in
// the shared error shape.
func TestSignInFailuresAreLimited(t *testing.T) {
	e := newEnv(t)
	for range 10 {
		e.expect(401, "POST", "/auth/sign-in", "", device("email", "admin@example.test", "password", "wrongwrong"), nil)
	}
	e.expect(429, "POST", "/auth/sign-in", "", device("email", "nobody@example.test", "password", "wrongwrong"), nil)
	body, err := json.Marshal(device("email", "admin@example.test", "password", "wrongwrong"))
	must(t, err)
	res, err := http.Post(e.url+"/auth/sign-in", "application/json", bytes.NewReader(body))
	must(t, err)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
}

// Behind a reverse proxy on the server's own host, failures count against
// the address the proxy forwarded for, so one visitor cannot pause sign-in
// for the rest; what a visitor wrote into the header themselves is ignored.
func TestSignInFailuresBehindAProxyCountPerVisitor(t *testing.T) {
	e := newEnv(t)
	signIn := func(forwardedFor, email string) int {
		body, err := json.Marshal(device("email", email, "password", "wrongwrong"))
		must(t, err)
		req, err := http.NewRequest("POST", e.url+"/auth/sign-in", bytes.NewReader(body))
		must(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", forwardedFor)
		res, err := http.DefaultClient.Do(req)
		must(t, err)
		_ = res.Body.Close()
		return res.StatusCode
	}
	for i := range 10 {
		if got := signIn("203.0.113.5", fmt.Sprintf("guess%d@example.test", i)); got != 401 {
			t.Fatalf("failure from the first visitor = %d", got)
		}
	}
	for forwardedFor, want := range map[string]int{
		"203.0.113.5":              429,
		"203.0.113.9":              401,
		"203.0.113.9, 203.0.113.5": 429,
		"203.0.113.5, 203.0.113.9": 401,
	} {
		if got := signIn(forwardedFor, "admin@example.test"); got != want {
			t.Errorf("X-Forwarded-For %q = %d, want %d", forwardedFor, got, want)
		}
	}
}

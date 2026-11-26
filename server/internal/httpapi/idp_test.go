package httpapi_test

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// idp is an OpenID provider for tests: discovery, a JWKS, an authorize
// endpoint that signs in whoever user names at once, and a token endpoint
// that issues RS256 ID tokens only for the right client secret, PKCE
// verifier and redirect URI.
type idp struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	client string
	secret string
	// issuer is what discovery and the ID token name; srv.URL unless a test
	// makes the provider lie about itself.
	issuer string

	mu sync.Mutex
	// user holds the claims, sub among them, the next sign-in carries.
	user map[string]any
	// tweak edits the next ID token's claims before signing.
	tweak func(map[string]any)
	// forge signs the next ID tokens with a key the JWKS does not hold.
	forge  bool
	grants map[string]grant
	// redirects records every redirect_uri the authorize endpoint saw.
	redirects []string
}

type grant struct {
	challenge, redirectURI, nonce string
	claims                        map[string]any
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	p := &idp{t: t, key: key, client: "rabbit-ears", secret: "s3cret-value", grants: map[string]grant{},
		user: map[string]any{"sub": "user-1", "email": "plain@example.test", "email_verified": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": p.issuer, "authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"}, "response_types_supported": []string{"code"}})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	p.srv = httptest.NewServer(mux)
	p.issuer = p.srv.URL
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) signInAs(claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.user = claims
}

func (p *idp) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.client || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("scope") != "openid email profile" {
		http.Error(w, "bad authorize request "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	code := b64([]byte(time.Now().String() + q.Get("state")))
	claims := map[string]any{}
	for k, v := range p.user {
		claims[k] = v
	}
	p.grants[code] = grant{q.Get("code_challenge"), q.Get("redirect_uri"), q.Get("nonce"), claims}
	p.redirects = append(p.redirects, q.Get("redirect_uri"))
	p.mu.Unlock()
	to, _ := url.Parse(q.Get("redirect_uri"))
	to.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	http.Redirect(w, r, to.String(), http.StatusFound) //nolint:gosec // a test provider returns wherever it is asked
}

func (p *idp) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.client || secret != p.secret {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	p.mu.Lock()
	g, found := p.grants[r.PostForm.Get("code")]
	delete(p.grants, r.PostForm.Get("code"))
	tweak, key := p.tweak, p.key
	if p.forge {
		var err error
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		must(p.t, err)
	}
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || r.PostForm.Get("grant_type") != "authorization_code" || b64(sum[:]) != g.challenge || r.PostForm.Get("redirect_uri") != g.redirectURI {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": p.issuer, "aud": p.client, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": g.nonce}
	for k, v := range g.claims {
		claims[k] = v
	}
	if tweak != nil {
		tweak(claims)
	}
	writeJSON(w, map[string]any{"access_token": "at-" + b64([]byte(g.nonce)), "token_type": "Bearer", "expires_in": 300,
		"id_token": p.sign(key, claims)})
}

// sign makes a compact RS256 JWT.
func (p *idp) sign(key *rsa.PrivateKey, claims map[string]any) string {
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	in := b64(head) + "." + b64(body)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	must(p.t, err)
	return in + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// browser follows no redirects on its own and keeps cookies, as the
// callback's cookie check needs.
type browser struct {
	t *testing.T
	c *http.Client
}

func newBrowser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// get answers a 302's Location and the response's Set-Cookie headers.
func (b *browser) get(rawURL string, header ...string) (*url.URL, []string) {
	b.t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	must(b.t, err)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := b.c.Do(req)
	must(b.t, err)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		b.t.Fatalf("GET %s = %d", rawURL, res.StatusCode)
	}
	loc, err := res.Location()
	if err != nil {
		loc, err = url.Parse(res.Header.Get("Location"))
	}
	must(b.t, err)
	return loc, res.Header.Values("Set-Cookie")
}

// post sends body as JSON and decodes a 2xx reply into out; it answers the
// status and the response's Set-Cookie headers.
func (b *browser) post(rawURL string, body, out any) (int, []string) {
	b.t.Helper()
	raw, err := json.Marshal(body)
	must(b.t, err)
	res, err := b.c.Post(rawURL, "application/json", bytes.NewReader(raw))
	must(b.t, err)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 300 {
		must(b.t, json.NewDecoder(res.Body).Decode(out))
	}
	return res.StatusCode, res.Header.Values("Set-Cookie")
}

// fragment parses a sign-in page URL's fragment, sso=… or sso_error=….
func fragment(t *testing.T, u *url.URL) url.Values {
	t.Helper()
	v, err := url.ParseQuery(u.Fragment)
	must(t, err)
	return v
}

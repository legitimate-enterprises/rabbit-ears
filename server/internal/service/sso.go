package service

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/sso"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Why a single sign-on failed, as the callback tells the sign-in page.
const (
	SSOExpired       = "expired"
	SSOProviderError = "provider_error"
	SSONotAllowed    = "not_allowed"
	SSONoAccount     = "no_account"
)

// SSOError is a single sign-on that failed for Reason, one of the SSO
// reasons above.
type SSOError struct {
	Reason string
	Err    error
}

func (e *SSOError) Error() string { return "single sign-on " + e.Reason + ": " + e.Err.Error() }
func (e *SSOError) Unwrap() error { return e.Err }

// ErrIssuer is an issuer whose discovery document does not answer or
// names another issuer.
var ErrIssuer = errors.New("no OpenID provider answers as this issuer")

// A started sign-in must come back within SSOStateLife, and its one-time
// code be exchanged within SSOCodeLife.
const (
	SSOStateLife = 10 * time.Minute
	SSOCodeLife  = time.Minute
	// ssoTimeout bounds the calls to a provider in one request.
	ssoTimeout = 15 * time.Second
)

// SSOService runs the OpenID Connect authorization code flow with PKCE and
// ends it in a one-time code the sign-in page exchanges for a device token.
// Sign-ins in flight and codes live in memory: a restart drops them.
type SSOService struct {
	repo *sso.Repo
	d    *sql.DB
	// Now is the clock for expiry; tests replace it.
	Now func() time.Time
	// MaxPending bounds the sign-ins started and not yet back, so callers
	// who never return cannot grow memory without limit; past it the one
	// closest to expiring is dropped, never the newest refused.
	MaxPending int

	mu      sync.Mutex
	pending map[string]ssoPending // by state
	codes   map[string]ssoCode
}

type ssoPending struct {
	providerID                             uid.ID
	verifier, nonce, returnTo, redirectURI string
	expires                                time.Time
}

// ssoCode is a one-time code with the binding the browser that finished the
// flow must present: the flow's state, which only that browser's cookie holds.
type ssoCode struct {
	accountID uid.ID
	binding   string
	expires   time.Time
}

// NewSSOService returns an SSOService over d.
func NewSSOService(d *sql.DB) *SSOService {
	return &SSOService{repo: sso.New(d), d: d, Now: time.Now, MaxPending: 1000,
		pending: map[string]ssoPending{}, codes: map[string]ssoCode{}}
}

// Repo exposes the repository.
func (s *SSOService) Repo() *sso.Repo { return s.repo }

// CheckIssuer is ErrIssuer unless issuer's discovery document answers and
// names issuer itself. The detail goes to the log, not the caller.
func (s *SSOService) CheckIssuer(ctx context.Context, issuer string) error {
	ctx, cancel := context.WithTimeout(ctx, ssoTimeout)
	defer cancel()
	if _, err := oidc.NewProvider(ctx, issuer); err != nil {
		logstore.Log("auth").Warn("Single sign-on issuer refused", "issuer", issuer, "err", err)
		return ErrIssuer
	}
	return nil
}

func ssoFailed(provider, reason string, err error) *SSOError {
	logstore.Log("auth").Warn("Single sign-on failed", "provider", provider, "reason", reason, "err", err)
	return &SSOError{reason, err}
}

func oauthConfig(p *sso.Provider, op *oidc.Provider, redirectURI string) *oauth2.Config {
	return &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: op.Endpoint(), RedirectURL: redirectURI,
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
}

// sweep drops expired sign-ins and codes; s.mu is held.
func (s *SSOService) sweep(now time.Time) {
	for k, v := range s.pending {
		if !now.Before(v.expires) {
			delete(s.pending, k)
		}
	}
	for k, v := range s.codes {
		if !now.Before(v.expires) {
			delete(s.codes, k)
		}
	}
}

// dropSoonest drops the sign-in in flight closest to expiring; s.mu is held.
func (s *SSOService) dropSoonest() {
	var soonest string
	for k, v := range s.pending {
		if soonest == "" || v.expires.Before(s.pending[soonest].expires) {
			soonest = k
		}
	}
	delete(s.pending, soonest)
}

// Start begins a sign-in at an enabled provider and returns the provider
// URL to send the browser to, and the state the browser must bring back.
// Failures are *SSOError.
func (s *SSOService) Start(ctx context.Context, providerID uid.ID, returnTo, redirectURI string) (to, state string, err error) {
	p, err := s.repo.Get(providerID)
	if errors.Is(err, db.ErrNotFound) || err == nil && !p.Enabled {
		return "", "", ssoFailed("", SSOProviderError, errors.New("no such enabled provider"))
	}
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, ssoTimeout)
	defer cancel()
	op, err := oidc.NewProvider(ctx, p.Issuer)
	if err != nil {
		return "", "", ssoFailed(p.Name, SSOProviderError, err)
	}
	if state, err = randomToken(); err != nil {
		return "", "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	now := s.Now()
	s.mu.Lock()
	s.sweep(now)
	for len(s.pending) >= max(s.MaxPending, 1) {
		s.dropSoonest()
	}
	s.pending[state] = ssoPending{p.ID, verifier, nonce, returnTo, redirectURI, now.Add(SSOStateLife)}
	s.mu.Unlock()
	return oauthConfig(p, op, redirectURI).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, nil
}

// Finish completes the sign-in state names, once: binding is what the
// browser's cookie holds, code and providerErr what the provider returned.
// It answers the page the sign-in started from and a one-time code naming
// the account, bound to state: the browser must present state again to
// exchange it. Failures are *SSOError, with returnTo still set when known.
func (s *SSOService) Finish(ctx context.Context, state, binding, code, providerErr string) (returnTo, oneTime string, err error) {
	now := s.Now()
	s.mu.Lock()
	pd, ok := s.pending[state]
	delete(s.pending, state)
	s.sweep(now)
	s.mu.Unlock()
	if !ok || !now.Before(pd.expires) || subtle.ConstantTimeCompare([]byte(binding), []byte(state)) != 1 {
		return pd.returnTo, "", ssoFailed("", SSOExpired, errors.New("unknown or expired state, or another browser's"))
	}
	p, err := s.repo.Get(pd.providerID)
	if errors.Is(err, db.ErrNotFound) || err == nil && !p.Enabled {
		return pd.returnTo, "", ssoFailed("", SSOProviderError, errors.New("the provider is gone or disabled"))
	}
	if err != nil {
		return pd.returnTo, "", err
	}
	if providerErr != "" || code == "" {
		return pd.returnTo, "", ssoFailed(p.Name, SSOProviderError, errors.New("the provider answered "+providerErr))
	}
	a, err := s.verify(ctx, p, pd, code)
	if err != nil {
		return pd.returnTo, "", err
	}
	if oneTime, err = randomToken(); err != nil {
		return pd.returnTo, "", err
	}
	s.mu.Lock()
	s.codes[oneTime] = ssoCode{a.ID, state, s.Now().Add(SSOCodeLife)}
	s.mu.Unlock()
	return pd.returnTo, oneTime, nil
}

// verify redeems the provider's code, checks its ID token and applies the
// rules to find who signs in.
func (s *SSOService) verify(ctx context.Context, p *sso.Provider, pd ssoPending, code string) (*account.Account, error) {
	ctx, cancel := context.WithTimeout(ctx, ssoTimeout)
	defer cancel()
	op, err := oidc.NewProvider(ctx, p.Issuer)
	if err != nil {
		return nil, ssoFailed(p.Name, SSOProviderError, err)
	}
	tok, err := oauthConfig(p, op, pd.redirectURI).Exchange(ctx, code, oauth2.VerifierOption(pd.verifier))
	if err != nil {
		return nil, ssoFailed(p.Name, SSOProviderError, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, ssoFailed(p.Name, SSOProviderError, errors.New("no ID token"))
	}
	// Verify checks the issuer, the audience, the signature against the
	// provider's keys and expiry; the nonce is this flow's to check.
	idt, err := op.Verifier(&oidc.Config{ClientID: p.ClientID, Now: s.Now}).Verify(ctx, raw)
	if err != nil {
		return nil, ssoFailed(p.Name, SSOProviderError, err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pd.nonce)) != 1 {
		return nil, ssoFailed(p.Name, SSOProviderError, errors.New("the ID token's nonce is not this sign-in's"))
	}
	var claims struct {
		Email string `json:"email"`
		// Some providers send the boolean as a string.
		EmailVerified any      `json:"email_verified"`
		Groups        []string `json:"groups"`
	}
	if err := idt.Claims(&claims); err != nil {
		return nil, ssoFailed(p.Name, SSOProviderError, err)
	}
	if p.RequiredGroup != "" && !slices.Contains(claims.Groups, p.RequiredGroup) {
		return nil, ssoFailed(p.Name, SSONotAllowed, errors.New("not in the required group"))
	}
	email := claims.Email
	// An email the provider does not say it has verified may be anyone's: it
	// links no account.
	if v := claims.EmailVerified; v != true && v != "true" {
		email = ""
	}
	return s.account(p, idt.Subject, email)
}

// account is who a verified subject signs in as: its linked account, else,
// on a provider that links by email, the one account with that email,
// which is linked now. Single sign-on never creates an account.
func (s *SSOService) account(p *sso.Provider, subject, email string) (*account.Account, error) {
	accounts := account.New(s.d)
	id, err := s.repo.IdentityBySubject(p.ID, subject)
	if err == nil {
		a, err := accounts.Get(id.AccountID)
		if err != nil {
			return nil, err
		}
		if !maySignIn(a) {
			return nil, ssoFailed(p.Name, SSONotAllowed, errors.New("the linked account may not sign in"))
		}
		return a, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	if !p.LinksByEmail || email == "" {
		return nil, ssoFailed(p.Name, SSONoAccount, errors.New("no linked account"))
	}
	a, err := accounts.ByEmail(email)
	if errors.Is(err, db.ErrNotFound) {
		return nil, ssoFailed(p.Name, SSONoAccount, errors.New("no account has the email"))
	}
	if err != nil {
		return nil, err
	}
	if !maySignIn(a) {
		return nil, ssoFailed(p.Name, SSONotAllowed, errors.New("the account with the email may not sign in"))
	}
	err = s.repo.CreateIdentity(&sso.Identity{AccountID: a.ID, ProviderID: p.ID, Subject: subject, Email: email})
	if errors.Is(err, db.ErrConflict) {
		return nil, ssoFailed(p.Name, SSONoAccount, errors.New("the account with the email is linked to another identity"))
	}
	if err != nil {
		return nil, err
	}
	logstore.Log("auth").Info("Linked " + a.Name + " to " + p.Name + " by email")
	return a, nil
}

// Exchange spends a one-time code and signs the device in as its account,
// as password sign-in does. A spent, expired or unknown code, one whose
// binding is not the browser's, or an account that may no longer sign in,
// is ErrUnauthenticated; the code is spent either way.
func (s *SSOService) Exchange(code, binding, deviceName, platform string) (*Signed, error) {
	s.mu.Lock()
	c, ok := s.codes[code]
	delete(s.codes, code)
	s.mu.Unlock()
	if !ok || !s.Now().Before(c.expires) || subtle.ConstantTimeCompare([]byte(binding), []byte(c.binding)) != 1 {
		return nil, ErrUnauthenticated
	}
	a, err := account.New(s.d).Get(c.accountID)
	if errors.Is(err, db.ErrNotFound) || err == nil && !maySignIn(a) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	out := &Signed{}
	return out, db.InTx(s.d, func(d db.DBTX) error { return signIn(d, a, deviceName, platform, out) })
}

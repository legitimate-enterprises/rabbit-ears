package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/sso"
)

// ssoCookie holds the state of the sign-in this browser started, so a
// callback or a one-time code carrying another browser's is refused.
const ssoCookie = "rabbit_ears_sso"

// ssoCookieFor sets the cookie to state for life; an empty state clears it.
func ssoCookieFor(ctx context.Context, state string, life time.Duration) string {
	//nolint:gosec // Secure follows the scheme, so sign-in works on a plain-HTTP home network
	c := http.Cookie{Name: ssoCookie, Value: state, Path: BasePath + "/auth/sso", MaxAge: int(life.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: strings.HasPrefix(origin(ctx), "https:")}
	if state == "" {
		c.MaxAge = -1
	}
	return c.String()
}

// ssoPage is the sign-in page a sign-in started from, with the outcome in
// the fragment, which never reaches a server or a Referer.
func ssoPage(returnTo, key, value string) string {
	page := "/"
	if returnTo == string(api.Admin) {
		page = "/admin/"
	}
	return page + "#" + key + "=" + url.QueryEscape(value)
}

// ListSsoOptions lists the enabled providers for the sign-in buttons.
func (s *Server) ListSsoOptions(context.Context, api.ListSsoOptionsRequestObject) (api.ListSsoOptionsResponseObject, error) {
	ps, err := s.svc.SSO.Repo().Enabled()
	return api.ListSsoOptions200JSONResponse(mapAll(ps, func(p *sso.Provider) api.SsoOption { return api.SsoOption{Id: p.ID, Name: p.Name} })), err
}

// StartSso sends the browser to the provider. The redirect URI is this
// server as the request reached it; the provider's own list of allowed
// redirects is what guards against a forged Host.
func (s *Server) StartSso(ctx context.Context, r api.StartSsoRequestObject) (api.StartSsoResponseObject, error) {
	returnTo := string(or(r.Params.ReturnTo, api.Viewer))
	to, state, err := s.svc.SSO.Start(ctx, r.ProviderId, returnTo, origin(ctx)+BasePath+"/auth/sso/callback")
	var se *service.SSOError
	if errors.As(err, &se) {
		return api.StartSso302Response{Headers: api.StartSso302ResponseHeaders{Location: ssoPage(returnTo, "sso_error", se.Reason),
			SetCookie: ssoCookieFor(ctx, "", 0)}}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.StartSso302Response{Headers: api.StartSso302ResponseHeaders{Location: to, SetCookie: ssoCookieFor(ctx, state, service.SSOStateLife)}}, nil
}

// FinishSso is the callback every provider returns to.
func (s *Server) FinishSso(ctx context.Context, r api.FinishSsoRequestObject) (api.FinishSsoResponseObject, error) {
	q := r.Params
	returnTo, code, err := s.svc.SSO.Finish(ctx, or(q.State, ""), or(q.RabbitEarsSso, ""), or(q.Code, ""), or(q.Error, ""))
	to, cookie := ssoPage(returnTo, "sso", code), ssoCookieFor(ctx, or(q.State, ""), service.SSOCodeLife)
	var se *service.SSOError
	if errors.As(err, &se) {
		to, cookie = ssoPage(returnTo, "sso_error", se.Reason), ssoCookieFor(ctx, "", 0)
	} else if err != nil {
		return nil, err
	}
	return api.FinishSso302Response{Headers: api.FinishSso302ResponseHeaders{Location: to, SetCookie: cookie}}, nil
}

// ExchangeSsoCode turns a callback's one-time code into a device token for
// the browser the callback bound it to, clearing the binding either way.
func (s *Server) ExchangeSsoCode(ctx context.Context, r api.ExchangeSsoCodeRequestObject) (api.ExchangeSsoCodeResponseObject, error) {
	b, clear := r.Body, ssoCookieFor(ctx, "", 0)
	sg, err := s.svc.SSO.Exchange(b.Code, or(r.Params.RabbitEarsSso, ""), b.DeviceName, string(b.Platform))
	if errors.Is(err, service.ErrUnauthenticated) {
		return api.ExchangeSsoCode401JSONResponse{Body: api.Error{Code: api.ErrorCodeUnauthorized, Message: "sign in required"},
			Headers: api.ExchangeSsoCode401ResponseHeaders{SetCookie: clear}}, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := s.signedIn(sg)
	return api.ExchangeSsoCode201JSONResponse{Body: res, Headers: api.ExchangeSsoCode201ResponseHeaders{SetCookie: clear}}, err
}

func toSSOProvider(p *sso.Provider) api.SsoProvider {
	return api.SsoProvider{Id: p.ID, Name: p.Name, Issuer: p.Issuer, ClientId: p.ClientID, ClientSecretSet: p.ClientSecret != "",
		RequiredGroup: optStr(p.RequiredGroup), LinksByEmail: p.LinksByEmail, Enabled: p.Enabled, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// ssoProviderFrom applies an admin's form once the issuer answers; an
// absent secret keeps the saved one.
func (s *Server) ssoProviderFrom(ctx context.Context, in *api.SsoProviderInput, p *sso.Provider) error {
	issuer := strings.TrimSpace(in.Issuer)
	if err := s.svc.SSO.CheckIssuer(ctx, issuer); err != nil {
		return invalid("issuer", err.Error())
	}
	p.Name, p.Issuer, p.ClientID = strings.TrimSpace(in.Name), issuer, strings.TrimSpace(in.ClientId)
	if in.ClientSecret != nil {
		p.ClientSecret = *in.ClientSecret
	}
	p.RequiredGroup = strings.TrimSpace(or(in.RequiredGroup, ""))
	p.LinksByEmail, p.Enabled = or(in.LinksByEmail, false), or(in.Enabled, true)
	return nil
}

// ListSsoProviders pages every provider.
func (s *Server) ListSsoProviders(_ context.Context, r api.ListSsoProvidersRequestObject) (api.ListSsoProvidersResponseObject, error) {
	all, err := s.svc.SSO.Repo().List()
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListSsoProviders200JSONResponse{Items: mapAll(rows, toSSOProvider), NextOffset: next}, nil
}

// CreateSsoProvider adds a provider.
func (s *Server) CreateSsoProvider(ctx context.Context, r api.CreateSsoProviderRequestObject) (api.CreateSsoProviderResponseObject, error) {
	p := &sso.Provider{}
	if err := s.ssoProviderFrom(ctx, r.Body, p); err != nil {
		return nil, err
	}
	if err := s.svc.SSO.Repo().Create(p); err != nil {
		return nil, err
	}
	audit(ctx, "Added the single sign-on provider "+p.Name)
	return api.CreateSsoProvider201JSONResponse(toSSOProvider(p)), nil
}

// GetSsoProvider returns one provider.
func (s *Server) GetSsoProvider(_ context.Context, r api.GetSsoProviderRequestObject) (api.GetSsoProviderResponseObject, error) {
	p, err := s.svc.SSO.Repo().Get(r.ProviderId)
	if err != nil {
		return nil, err
	}
	return api.GetSsoProvider200JSONResponse(toSSOProvider(p)), nil
}

// ReplaceSsoProvider edits, enables or disables a provider.
func (s *Server) ReplaceSsoProvider(ctx context.Context, r api.ReplaceSsoProviderRequestObject) (api.ReplaceSsoProviderResponseObject, error) {
	p, err := s.svc.SSO.Repo().Get(r.ProviderId)
	if err != nil {
		return nil, err
	}
	if err := s.ssoProviderFrom(ctx, r.Body, p); err != nil {
		return nil, err
	}
	if err := s.svc.SSO.Repo().Update(p); err != nil {
		return nil, err
	}
	audit(ctx, "Changed the single sign-on provider "+p.Name)
	return api.ReplaceSsoProvider200JSONResponse(toSSOProvider(p)), nil
}

// DeleteSsoProvider removes a provider and unlinks its identities.
func (s *Server) DeleteSsoProvider(ctx context.Context, r api.DeleteSsoProviderRequestObject) (api.DeleteSsoProviderResponseObject, error) {
	p, err := s.svc.SSO.Repo().Get(r.ProviderId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.SSO.Repo().Delete(p.ID); err != nil {
		return nil, err
	}
	audit(ctx, "Removed the single sign-on provider "+p.Name)
	return api.DeleteSsoProvider204Response{}, nil
}

func toIdentity(i *sso.Identity) api.AccountIdentity {
	return api.AccountIdentity{Id: i.ID, AccountId: i.AccountID, ProviderId: i.ProviderID, Subject: i.Subject, Email: optStr(i.Email), CreatedAt: i.CreatedAt}
}

// ListAccountIdentities pages an account's identities.
func (s *Server) ListAccountIdentities(_ context.Context, r api.ListAccountIdentitiesRequestObject) (api.ListAccountIdentitiesResponseObject, error) {
	if _, err := s.svc.Accounts.Repo().Get(r.AccountId); err != nil {
		return nil, err
	}
	all, err := s.svc.SSO.Repo().Identities(r.AccountId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListAccountIdentities200JSONResponse{Items: mapAll(rows, toIdentity), NextOffset: next}, nil
}

// DeleteAccountIdentity unlinks an identity from its account.
func (s *Server) DeleteAccountIdentity(ctx context.Context, r api.DeleteAccountIdentityRequestObject) (api.DeleteAccountIdentityResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.SSO.Repo().DeleteIdentity(a.ID, r.IdentityId); err != nil {
		return nil, err
	}
	audit(ctx, "Unlinked a single sign-on identity from "+a.Name)
	return api.DeleteAccountIdentity204Response{}, nil
}

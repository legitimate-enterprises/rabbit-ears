package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// adminScope is the scope the spec lists on operations only admins may call.
const adminScope = "admin"

// whileLocked is the spec extension marking the operations a device locked at
// Who's watching may still call.
const whileLocked = "x-while-locked"

// replayable is the spec extension marking the operations replaySyncActions
// may run.
const replayable = "x-replayable"

// maxBody is the largest request body accepted, the spec's 1 MiB, and
// maxUpload that of an operation the spec marks upload.
const (
	maxBody   = 1 << 20
	maxUpload = 16 << 20
	upload    = "x-upload"
)

// guard runs before every generated handler: it finds the spec operation,
// enforces the security the spec declares for it, then validates the request
// against the spec. Handlers can therefore trust shape, ranges and enums.
type guard struct {
	svc    *service.Services
	doc    *openapi3.T
	router routers.Router
}

func newGuard(svc *service.Services) (*guard, error) {
	doc, err := api.GetSpec()
	if err != nil {
		return nil, err
	}
	r, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, err
	}
	return &guard{svc, doc, r}, nil
}

type principalKey struct{}

// principal is the authenticated device, the adult who signed in on it, and
// the household profile it acts as (that adult unless switched).
// Powers follow the active profile; admin needs the signed-in adult active
// on a device that did not sign in through a link. adminOp marks a request
// to an operation only admins may call, which therefore holds admin powers.
type principal struct {
	device  *device.Device
	account *account.Account
	profile *account.Account
	adminOp bool
}

// asKid reports a device acting as a kid.
func (p principal) asKid() bool { return p.profile.Kind == account.Kid }

// present reports an adult active on the device whose own household is
// another: there to watch, they manage nothing of it.
func (p principal) present() bool {
	return p.profile.Kind == account.Adult && (p.profile.HouseholdID == nil || *p.profile.HouseholdID != p.device.HouseholdID)
}

// manages reports an adult of the device household active, who may change
// the household and its kids.
func (p principal) manages() bool { return !p.asKid() && !p.present() }

// asSelf reports that the device acts as the adult who signed in on it, not
// as a profile it switched to.
func (p principal) asSelf() bool { return p.profile.ID == p.account.ID }

// asAdmin reports a device holding admin powers.
func (p principal) asAdmin() bool { return p.account.IsAdmin && p.asSelf() && !p.device.Linked }

type remoteKey struct{}

// fromProxy reports a loopback peer: a reverse proxy on this host, the only
// peer whose forwarding headers are believed.
func fromProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

// sourceAddr is the address a request came from. Behind a proxy the client
// is the nearest address it forwarded for; anything left of that the client
// could have written.
func sourceAddr(r *http.Request) string {
	if !fromProxy(r) {
		return r.RemoteAddr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			break
		}
		if !ip.IsLoopback() {
			return ip.String()
		}
	}
	return r.RemoteAddr
}

// clientIP is the request's source address, for the same-LAN rule.
func clientIP(ctx context.Context) net.IP {
	addr, _ := ctx.Value(remoteKey{}).(string)
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.ParseIP(host)
}

type originKey struct{}

// origin is the scheme and host the client reached the server at, for a
// full URL to hand to another device.
func origin(ctx context.Context) string {
	o, _ := ctx.Value(originKey{}).(string)
	return o
}

func caller(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

// actor is the acting profile a write is attributed to, nil outside an
// authenticated request.
func actor(ctx context.Context) *uid.ID {
	if p := caller(ctx).profile; p != nil {
		id := p.ID
		return &id
	}
	return nil
}

func (g *guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, err := g.router.FindRoute(r)
		if err != nil {
			e := errNotFound
			if errors.Is(err, routers.ErrMethodNotAllowed) {
				e = &apiError{http.StatusMethodNotAllowed, api.Error{Code: api.ErrorCodeBadRequest, Message: "method not allowed"}}
			}
			writeError(w, e)
			return
		}
		security := g.doc.Security
		if route.Operation.Security != nil {
			security = *route.Operation.Security
		}
		ctx := r.Context()
		// A replayed request keeps the source and origin of its batch's.
		replay, replayed := ctx.Value(replayKey{}).(principal)
		if !replayed {
			scheme := "http"
			if r.TLS != nil || fromProxy(r) && r.Header.Get("X-Forwarded-Proto") == "https" {
				scheme = "https"
			}
			ctx = context.WithValue(context.WithValue(ctx, remoteKey{}, sourceAddr(r)), originKey{}, scheme+"://"+r.Host)
		}
		if len(security) > 0 {
			p, err := replay, error(nil)
			if !replayed {
				p, err = g.authenticate(r, route.Operation.Extensions[whileLocked] == true)
			}
			if err == nil {
				p, err = permitted(p, security)
			}
			if err != nil {
				responseError(w, r, err)
				return
			}
			ctx = context.WithValue(ctx, principalKey{}, p)
		}
		r = r.WithContext(ctx)
		// Validation reads the whole body into memory, so it is capped first.
		limit, over := int64(maxBody), "request body over 1 MiB"
		if route.Operation.Extensions[upload] == true {
			limit, over = maxUpload, "request body over 16 MiB"
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		err = dropNulls(r)
		if err == nil {
			in := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route,
				Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, MultiError: true}}
			err = openapi3filter.ValidateRequest(ctx, in)
		}
		if err != nil {
			var big *http.MaxBytesError
			if errors.As(err, &big) {
				writeError(w, fail(http.StatusRequestEntityTooLarge, api.ErrorCodePayloadTooLarge, over))
				return
			}
			writeError(w, validationError(err))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// dropNulls rewrites a JSON body without its null object members, which
// the spec reads as absent: generated clients send every optional property,
// null when unset. A body that is not one JSON value is left for
// validation to refuse.
func dropNulls(r *http.Request) error {
	if r.Body == nil || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	body := raw
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) == nil && dec.Decode(&struct{}{}) == io.EOF {
		if b, err := json.Marshal(withoutNulls(v)); err == nil {
			body = b
		}
	}
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	return nil
}

func withoutNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if x == nil {
				delete(t, k)
			} else {
				t[k] = withoutNulls(x)
			}
		}
	case []any:
		for i, x := range t {
			t[i] = withoutNulls(x)
		}
	}
	return v
}

// replayable reports whether r calls the operation named id, in any case,
// and the spec marks it replayable.
func (g *guard) replayable(r *http.Request, id string) bool {
	route, _, err := g.router.FindRoute(r)
	return err == nil && strings.EqualFold(route.Operation.OperationID, id) && route.Operation.Extensions[replayable] == true
}

// authenticate resolves the device token to its principal. A locked device
// is refused unless the operation is open while locked.
func (g *guard) authenticate(r *http.Request, openWhileLocked bool) (principal, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return principal{}, service.ErrUnauthenticated
	}
	d, a, err := g.svc.Devices.Authenticate(token)
	if err != nil {
		return principal{}, err
	}
	p, err := acting(g.svc, d, a)
	if err != nil {
		return principal{}, err
	}
	if d.LockedAt != nil && !openWhileLocked {
		return principal{}, errLocked
	}
	return p, nil
}

// permitted admits p when any one requirement is met, as OpenAPI defines
// alternatives; the spec only ever lists one.
func permitted(p principal, security openapi3.SecurityRequirements) (principal, error) {
	for _, req := range security {
		if scopes, ok := req["device"]; ok && (!slices.Contains(scopes, adminScope) || p.asAdmin()) {
			p.adminOp = slices.Contains(scopes, adminScope)
			return p, nil
		}
	}
	return principal{}, errForbidden
}

// acting is the principal a device acts as: the adult who signed in on it,
// or the household profile it switched to.
func acting(svc *service.Services, d *device.Device, a *account.Account) (principal, error) {
	p := principal{device: d, account: a, profile: a}
	if d.ProfileID != nil && *d.ProfileID != a.ID {
		var err error
		if p.profile, err = svc.Accounts.Profile(d.HouseholdID, *d.ProfileID); err != nil {
			return principal{}, err
		}
		if p.profile.RevokedAt != nil {
			return principal{}, service.ErrUnauthenticated
		}
	}
	return p, nil
}

// validationError flattens kin-openapi's errors into per-field messages.
func validationError(err error) *apiError {
	var fields []api.FieldError
	var walk func(error)
	walk = func(err error) {
		var req *openapi3filter.RequestError
		var schema *openapi3.SchemaError
		// Only a top-level MultiError is split: a parameter's RequestError
		// wraps one too, and splitting that would lose the parameter name.
		multi, isMulti := err.(openapi3.MultiError) //nolint:errorlint // deliberately not unwrapping
		switch {
		case isMulti:
			for _, e := range multi {
				walk(e)
			}
		case errors.As(err, &req) && req.Parameter != nil:
			msg := req.Reason
			if errors.As(err, &schema) {
				msg = schema.Reason
			} else if msg == "" && req.Err != nil {
				msg = req.Err.Error()
			}
			fields = append(fields, api.FieldError{Field: req.Parameter.Name, Message: msg})
		case errors.As(err, &req) && isMultiError(req.Err):
			walk(req.Err)
		case errors.As(err, &schema):
			fields = append(fields, api.FieldError{Field: strings.Join(schema.JSONPointer(), "."), Message: schema.Reason})
		case errors.As(err, &req):
			fields = append(fields, api.FieldError{Field: "body", Message: req.Error()})
		default:
			fields = append(fields, api.FieldError{Field: "", Message: err.Error()})
		}
	}
	walk(err)
	return &apiError{http.StatusBadRequest, api.Error{Code: api.ErrorCodeBadRequest,
		Message: fmt.Sprintf("invalid request: %d problem(s)", len(fields)), Fields: &fields}}
}

func isMultiError(err error) bool {
	_, ok := err.(openapi3.MultiError) //nolint:errorlint // only a direct MultiError is split
	return ok
}

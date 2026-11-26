package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"golang.org/x/crypto/bcrypt"
)

// apiError is returned by handlers for any non-2xx answer; responseError
// renders it as the spec's Error schema.
type apiError struct {
	status int
	body   api.Error
}

func (e *apiError) Error() string { return e.body.Message }

func fail(status int, code api.ErrorCode, msg string) *apiError {
	return &apiError{status, api.Error{Code: code, Message: msg}}
}

var (
	errForbidden = fail(http.StatusForbidden, api.ErrorCodeForbidden, "not allowed")
	errNotFound  = fail(http.StatusNotFound, api.ErrorCodeNotFound, "not found")
	errLocked    = fail(http.StatusForbidden, api.ErrorCodeDeviceLocked, "locked at Who's watching; unlock a profile")
)

func invalid(field, msg string) error {
	fs := []api.FieldError{{Field: field, Message: msg}}
	return &apiError{http.StatusBadRequest, api.Error{Code: api.ErrorCodeBadRequest, Message: "invalid request", Fields: &fs}}
}

func writeError(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(e.body)
}

func badRequest(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, &apiError{http.StatusBadRequest, api.Error{Code: api.ErrorCodeBadRequest, Message: err.Error()}})
}

// responseError maps handler and repository errors onto the shared shape.
// Anything unrecognized is logged and hidden behind a 500.
func responseError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var limited *service.RateLimitedError
	switch {
	case errors.As(err, &ae):
		writeError(w, ae)
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(limited.RetryAfter.Seconds())))))
		writeError(w, fail(http.StatusTooManyRequests, api.ErrorCodeTooManyRequests, err.Error()))
	case errors.Is(err, db.ErrNotFound):
		writeError(w, errNotFound)
	case errors.Is(err, bcrypt.ErrPasswordTooLong):
		responseError(w, r, invalid("password", "at most 72 bytes"))
	case errors.Is(err, db.ErrConflict):
		writeError(w, &apiError{http.StatusConflict, api.Error{Code: api.ErrorCodeConflict, Message: "already exists"}})
	case errors.Is(err, db.ErrReference):
		writeError(w, &apiError{http.StatusBadRequest, api.Error{Code: api.ErrorCodeBadRequest, Message: "refers to something that does not exist"}})
	case errors.Is(err, service.ErrNothingPlayable), errors.Is(err, transcode.ErrBusy):
		writeError(w, fail(http.StatusConflict, api.ErrorCodeConflict, err.Error()))
	case errors.Is(err, service.ErrSetupDone), errors.Is(err, service.ErrNotJoinable), errors.Is(err, service.ErrNoPassword),
		errors.Is(err, service.ErrSameKind), errors.Is(err, service.ErrLastAdmin), errors.Is(err, service.ErrNotJoined),
		errors.Is(err, service.ErrLastHousehold), errors.Is(err, service.ErrOwnerStays), errors.Is(err, service.ErrNotOfferable):
		writeError(w, fail(http.StatusConflict, api.ErrorCodeConflict, err.Error()))
	case errors.Is(err, service.ErrNotLinked):
		writeError(w, fail(http.StatusForbidden, api.ErrorCodeForbidden, err.Error()))
	case errors.Is(err, service.ErrPINRequired):
		writeError(w, fail(http.StatusForbidden, api.ErrorCodePinRequired, err.Error()))
	case errors.Is(err, service.ErrPINLocked):
		writeError(w, fail(http.StatusForbidden, api.ErrorCodePinLocked, err.Error()))
	case errors.Is(err, service.ErrPINWrong), errors.Is(err, service.ErrPasswordWrong):
		writeError(w, fail(http.StatusForbidden, api.ErrorCodeForbidden, err.Error()))
	case errors.Is(err, service.ErrUnauthenticated):
		writeError(w, &apiError{http.StatusUnauthorized, api.Error{Code: api.ErrorCodeUnauthorized, Message: "sign in required"}})
	default:
		logstore.Log("http").Error("Request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, &apiError{http.StatusInternalServerError, api.Error{Code: api.ErrorCodeInternal, Message: "internal error"}})
	}
}

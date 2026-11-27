package httpapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
)

// Action replay: a client's queued offline writes, each run as the request
// it names.

// replayKey carries the principal a replayed request runs as. Only this
// package sets it, so no request from outside can claim one.
type replayKey struct{}

func (s *Server) ReplaySyncActions(ctx context.Context, r api.ReplaySyncActionsRequestObject) (api.ReplaySyncActionsResponseObject, error) {
	in := r.Body
	out := api.ReplaySyncActions200JSONResponse{Results: []api.SyncActionResult{}}
	if !s.replays(in.ApiVersion) {
		for _, a := range in.Actions {
			out.Results = append(out.Results, refused(a, "unsupported_version", "this server replays API versions "+s.syncRanges().ApiMin+" to "+s.syncRanges().ApiMax))
		}
		return out, nil
	}
	for i, a := range in.Actions {
		if i > 0 && a.Seq <= in.Actions[i-1].Seq {
			return nil, invalid("actions."+strconv.Itoa(i)+".seq", "not after the action before")
		}
		for j, t := range a.Touched {
			ok, err := s.svc.Sync.Touchable(ctx, replica.V1, t.Table, t.Key)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, invalid("actions."+strconv.Itoa(i)+".touched."+strconv.Itoa(j), "not a row of sync schema 1")
			}
		}
	}
	p := caller(ctx)
	mu, _ := s.batches.LoadOrStore(p.device.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	for _, a := range in.Actions {
		res, ok, err := s.answer(ctx, p, a)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// replays reports whether v is an API version from syncAPIMin to the
// server's own.
func (s *Server) replays(v string) bool {
	parse := func(v string) []int {
		parts := strings.Split(v, ".")
		out := make([]int, len(parts))
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || len(parts) != 3 {
				return nil
			}
			out[i] = n
		}
		return out
	}
	r := s.syncRanges()
	got, low, high := parse(v), parse(r.ApiMin), parse(r.ApiMax)
	return got != nil && high != nil && slices.Compare(low, got) <= 0 && slices.Compare(got, high) <= 0
}

func refused(a api.SyncAction, code, msg string) api.SyncActionResult {
	return api.SyncActionResult{Id: a.Id, Result: api.Rejected, Code: &code, Message: &msg}
}

// answer is a's result: the one remembered, or that of running it, then
// remembered with its touched rows marked. ok is false when the server
// failed, and nothing is remembered. A crash between the operation's own
// writes and the remembering runs it again when the client resends it.
func (s *Server) answer(ctx context.Context, p principal, a api.SyncAction) (api.SyncActionResult, bool, error) {
	id := a.Id.String()
	result, code, ok, err := s.svc.Sync.Answered(ctx, p.device.ID, id)
	if err != nil || ok {
		return api.SyncActionResult{Id: a.Id, Result: api.SyncActionResultResult(result), Code: code}, ok, err
	}
	res, ok, err := s.run(ctx, p, a)
	if !ok || err != nil {
		return res, false, err
	}
	touched := make([]replica.Row, len(a.Touched))
	for i, t := range a.Touched {
		touched[i] = replica.Row{Table: t.Table, Key: t.Key}
	}
	err = s.svc.Sync.Answer(p.device.HouseholdID, p.device.ID, id, string(res.Result), res.Code, touched)
	return res, err == nil, err
}

// run sends a through the chain every request takes, as its acting
// profile with that profile's permissions now. A device locked at Who's
// watching runs it too: offline, the client could not know of the lock.
func (s *Server) run(ctx context.Context, p principal, a api.SyncAction) (api.SyncActionResult, bool, error) {
	acting, err := s.svc.Accounts.Profile(p.device.HouseholdID, a.ActingProfileId)
	if errors.Is(err, db.ErrNotFound) || err == nil && acting.RevokedAt != nil {
		return refused(a, string(api.ErrorCodeNotFound), "actingProfileId is not a profile of this household"), true, nil
	} else if err != nil {
		return api.SyncActionResult{}, false, err
	}
	var body io.Reader = http.NoBody
	if a.Body != nil {
		b, err := json.Marshal(a.Body)
		if err != nil {
			return api.SyncActionResult{}, false, err
		}
		body = bytes.NewReader(b)
	}
	as := principal{device: p.device, account: p.account, profile: acting}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, replayKey{}, as), string(a.Method), origin(ctx)+BasePath+a.Path, body)
	if err != nil || !s.guard.replayable(req, a.OperationId) {
		return refused(a, "not_replayable", string(a.Method)+" "+a.Path+" is not a replayable call of "+a.OperationId), true, nil
	}
	if a.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.replay.ServeHTTP(rec, req)
	switch {
	case rec.Code >= 500:
		return api.SyncActionResult{}, false, nil
	case rec.Code < 300:
		return api.SyncActionResult{Id: a.Id, Result: api.Accepted}, true, nil
	}
	var e api.Error
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return refused(a, cmp.Or(string(e.Code), string(api.ErrorCodeBadRequest)), e.Message), true, nil
}

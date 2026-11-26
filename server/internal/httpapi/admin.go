package httpapi

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
)

// audit logs an admin action with who took it.
func audit(ctx context.Context, msg string) {
	logstore.Log("auth").Info(msg + " by " + caller(ctx).account.Name)
}

// accountFrom applies an admin edit. Kind decides which fields may be set;
// password, PIN and household are never written here. A birthday or filter
// mode left out is kept, and a kid's ceilings are defaulted as the profile
// route does.
func accountFrom(in *api.AccountInput, a *account.Account) error {
	if a.ID != 0 && string(in.Kind) != a.Kind {
		return invalid("kind", "kind is fixed at creation")
	}
	adult := in.Kind == api.Adult
	if adult && (in.Email == nil || *in.Email == "") {
		return invalid("email", "adults need an email")
	}
	if !adult && (in.Email != nil || or(in.IsAdmin, false)) {
		return invalid("email", "kids have no email and are never admins")
	}
	if adult && (in.MovieCeiling != nil || in.TvCeiling != nil) {
		return invalid("movieCeiling", "only kids have rating ceilings")
	}
	a.Kind, a.Name, a.IsAdmin, a.Birthday = string(in.Kind), in.Name, or(in.IsAdmin, false), cmp.Or(fromDate(in.Birthday), a.Birthday)
	a.Email = string(or(in.Email, ""))
	a.FilterMode = filterModeFrom(in.FilterMode, a)
	a.MovieCeiling, a.TVCeiling = enumStr(in.MovieCeiling), enumStr(in.TvCeiling)
	if !adult {
		suggestCeilings(a)
	}
	return nil
}

// withHouseholds is toAccount with every household a is in, for an admin.
func (s *Server) withHouseholds(a *account.Account) (api.Account, error) {
	out := toAccount(a)
	hs, err := s.svc.Accounts.Repo().Households(a)
	if hs == nil {
		hs = []int64{}
	}
	out.HouseholdIds = &hs
	return out, err
}

// ListAccounts pages accounts by name.
func (s *Server) ListAccounts(_ context.Context, r api.ListAccountsRequestObject) (api.ListAccountsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Accounts.Repo().List(enumStr(r.Params.Kind), limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	out := api.ListAccounts200JSONResponse{Items: []api.Account{}, NextOffset: next}
	for _, a := range rows {
		v, err := s.withHouseholds(a)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreateAccount adds a pending adult.
func (s *Server) CreateAccount(ctx context.Context, r api.CreateAccountRequestObject) (api.CreateAccountResponseObject, error) {
	if r.Body.Kind != api.Adult {
		return nil, invalid("kind", "kids are added by their adult through profiles")
	}
	var a account.Account
	if err := accountFrom(r.Body, &a); err != nil {
		return nil, err
	}
	if err := s.svc.Accounts.Repo().Create(&a); err != nil {
		return nil, err
	}
	audit(ctx, "Added adult "+a.Name)
	out, err := s.withHouseholds(&a)
	return api.CreateAccount201JSONResponse(out), err
}

// GetAccount returns any account.
func (s *Server) GetAccount(_ context.Context, r api.GetAccountRequestObject) (api.GetAccountResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	out, err := s.withHouseholds(a)
	return api.GetAccount200JSONResponse(out), err
}

// ReplaceAccount edits, promotes, revokes or restores an account; revoking
// also signs out every device of the account or acting as it.
func (s *Server) ReplaceAccount(ctx context.Context, r api.ReplaceAccountRequestObject) (api.ReplaceAccountResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	was := *a
	if err := accountFrom(r.Body, a); err != nil {
		return nil, err
	}
	revoke := or(r.Body.Revoked, a.RevokedAt != nil)
	if !a.IsAdmin || revoke {
		if err := s.notLastAdmin(&was); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	revoking := revoke && a.RevokedAt == nil
	switch {
	case revoking:
		a.RevokedAt = &now
	case !revoke:
		a.RevokedAt = nil
	}
	if revoking {
		if err := s.signedOut(s.svc.Accounts.Revoke(a)); err != nil {
			return nil, err
		}
		if err := s.svc.Together.LeaveAccount(a.ID); err != nil {
			return nil, err
		}
	} else if err := s.svc.Accounts.Update(a); err != nil {
		return nil, err
	}
	if err := s.profileChanged(a); err != nil {
		return nil, err
	}
	what := "Updated account "
	if revoke {
		what = "Revoked account "
	}
	audit(ctx, what+a.Name)
	out, err := s.withHouseholds(a)
	return api.ReplaceAccount200JSONResponse(out), err
}

// DeleteAccount removes an account and everything it owns.
func (s *Server) DeleteAccount(ctx context.Context, r api.DeleteAccountRequestObject) (api.DeleteAccountResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	if err := s.deleteAccount(a); err != nil {
		return nil, err
	}
	audit(ctx, "Deleted account "+a.Name)
	return api.DeleteAccount204Response{}, nil
}

// deleteAccount deletes a and every account that cascades from it, unless
// the last active admin is among them.
func (s *Server) deleteAccount(a *account.Account) error {
	gone, err := s.svc.Accounts.Repo().DeletedWith(a.ID)
	if err != nil {
		return err
	}
	if err := s.notLastAdmin(gone...); err != nil {
		return err
	}
	return s.removed(gone, func() (*service.SignedOut, error) { return s.svc.Accounts.Delete(a.ID) })
}

// removed runs del, which deletes the accounts in gone, and finishes what
// the database cannot: they leave Watch together, their devices are signed
// out as sign-out does, the conversions only their downloads claimed stop
// and go, their Gravatar copies go, and the stations they took are
// announced.
func (s *Server) removed(gone []*account.Account, del func() (*service.SignedOut, error)) error {
	ids := make([]int64, len(gone))
	for i, a := range gone {
		ids[i] = a.ID
	}
	claimed, err := s.svc.Downloads.Repo().ClaimedBy(ids)
	if err != nil {
		return err
	}
	for _, a := range gone {
		if err := s.svc.Together.LeaveAccount(a.ID); err != nil {
			return err
		}
	}
	if err := s.signedOut(del()); err != nil {
		return err
	}
	if err := s.svc.Downloads.ReleaseConversions(claimed); err != nil {
		logstore.Log("transcode").Warn("Conversions of a deleted account not released", "err", err)
	}
	for _, a := range gone {
		if a.Email == "" || s.svc.Avatars == nil {
			continue
		}
		if err := s.svc.Avatars.Forget(a.Email); err != nil {
			logstore.Log("gravatar").Warn("Gravatar copy not removed", "err", err)
		}
	}
	// It also wakes every held events poll, so those of the devices signed
	// out answer 401 at once.
	s.svc.Events.Publish(live.Event{Kind: live.Station})
	return nil
}

// notLastAdmin refuses an action that ends the accounts in gone as active
// admins when no other is left, which would reopen first run to anyone who
// can reach the server.
func (s *Server) notLastAdmin(gone ...*account.Account) error {
	last, err := s.lastAdmin(gone)
	if err == nil && last {
		return service.ErrLastAdmin
	}
	return err
}

// lastAdmin reports that the accounts in gone hold every active admin.
func (s *Server) lastAdmin(gone []*account.Account) (bool, error) {
	lost := 0
	for _, a := range gone {
		if a.IsAdmin && a.RevokedAt == nil && !a.Pending() {
			lost++
		}
	}
	if lost == 0 {
		return false, nil
	}
	n, err := s.svc.Accounts.Repo().AdminCount()
	return n <= lost, err
}

// ListHouseholds pages households by name.
func (s *Server) ListHouseholds(_ context.Context, r api.ListHouseholdsRequestObject) (api.ListHouseholdsResponseObject, error) {
	all, err := s.svc.Households.Repo().List()
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListHouseholds200JSONResponse{Items: mapAll(rows, toHousehold), NextOffset: next}, nil
}

// CreateHousehold gives an adult without one a household.
func (s *Server) CreateHousehold(_ context.Context, r api.CreateHouseholdRequestObject) (api.CreateHouseholdResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.Body.AdultId)
	if errors.Is(err, db.ErrNotFound) || (err == nil && a.Kind != account.Adult) {
		return nil, invalid("adultId", "no such adult")
	}
	if err != nil {
		return nil, err
	}
	if a.HouseholdID != nil {
		return nil, fail(http.StatusConflict, api.ErrorCodeConflict, "the adult already belongs to a household")
	}
	h, err := s.svc.Households.CreateFor(r.Body.Name, a)
	if err != nil {
		return nil, err
	}
	return api.CreateHousehold201JSONResponse(toHousehold(h)), nil
}

// GetHousehold returns one household.
func (s *Server) GetHousehold(_ context.Context, r api.GetHouseholdRequestObject) (api.GetHouseholdResponseObject, error) {
	h, err := s.svc.Households.Repo().Get(r.HouseholdId)
	if err != nil {
		return nil, err
	}
	return api.GetHousehold200JSONResponse(toHousehold(h)), nil
}

// ReplaceHousehold renames a household.
func (s *Server) ReplaceHousehold(_ context.Context, r api.ReplaceHouseholdRequestObject) (api.ReplaceHouseholdResponseObject, error) {
	h, err := s.svc.Households.Repo().Get(r.HouseholdId)
	if err != nil {
		return nil, err
	}
	if r.Body.AdultId != h.AdultID {
		return nil, invalid("adultId", "a household's adult is fixed")
	}
	h.Name = r.Body.Name
	if err := s.svc.Households.Repo().Update(h); err != nil {
		return nil, err
	}
	return api.ReplaceHousehold200JSONResponse(toHousehold(h)), nil
}

// DeleteHousehold removes a household with its other members and devices;
// the owner stays.
func (s *Server) DeleteHousehold(_ context.Context, r api.DeleteHouseholdRequestObject) (api.DeleteHouseholdResponseObject, error) {
	h, err := s.svc.Households.Repo().Get(r.HouseholdId)
	if err != nil {
		return nil, err
	}
	gone, err := s.svc.Accounts.Repo().DeletedWithHousehold(h.ID)
	if err != nil {
		return nil, err
	}
	if err := s.notLastAdmin(gone...); err != nil {
		return nil, err
	}
	return api.DeleteHousehold204Response{}, s.signedOut(s.svc.Households.Delete(r.HouseholdId))
}

// PutHouseholdMember puts a profile into a household besides its others.
func (s *Server) PutHouseholdMember(ctx context.Context, r api.PutHouseholdMemberRequestObject) (api.PutHouseholdMemberResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Households.Join(a, r.HouseholdId); err != nil {
		return nil, err
	}
	s.householdChanged(r.HouseholdId)
	audit(ctx, "Put "+a.Name+" into household "+strconv.FormatInt(r.HouseholdId, 10))
	return api.PutHouseholdMember204Response{}, nil
}

// DeleteHouseholdMember takes a profile out of one of its households.
func (s *Server) DeleteHouseholdMember(ctx context.Context, r api.DeleteHouseholdMemberRequestObject) (api.DeleteHouseholdMemberResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	if err := s.signedOut(s.svc.Households.Leave(a, r.HouseholdId)); err != nil {
		return nil, err
	}
	s.householdChanged(r.HouseholdId)
	audit(ctx, "Took "+a.Name+" out of household "+strconv.FormatInt(r.HouseholdId, 10))
	return api.DeleteHouseholdMember204Response{}, nil
}

// ListDevices pages devices, optionally of one adult.
func (s *Server) ListDevices(_ context.Context, r api.ListDevicesRequestObject) (api.ListDevicesResponseObject, error) {
	repo := s.svc.Devices.Repo()
	all, err := repo.List()
	if r.Params.AccountId != nil {
		all, err = repo.ByAccount(*r.Params.AccountId)
	}
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListDevices200JSONResponse{Items: mapAll(rows, toDevice), NextOffset: next}, nil
}

// GetDevice returns one device.
func (s *Server) GetDevice(_ context.Context, r api.GetDeviceRequestObject) (api.GetDeviceResponseObject, error) {
	d, err := s.svc.Devices.Repo().Get(r.DeviceId)
	if err != nil {
		return nil, err
	}
	return api.GetDevice200JSONResponse(toDevice(d)), nil
}

// ReplaceDevice renames, revokes or restores a device.
func (s *Server) ReplaceDevice(ctx context.Context, r api.ReplaceDeviceRequestObject) (api.ReplaceDeviceResponseObject, error) {
	d, err := s.svc.Devices.Repo().Get(r.DeviceId)
	if err != nil {
		return nil, err
	}
	d.Name = r.Body.Name
	switch revoke := or(r.Body.Revoked, d.RevokedAt != nil); {
	case revoke && d.RevokedAt == nil:
		now := time.Now().UTC()
		d.RevokedAt = &now
	case !revoke:
		d.RevokedAt = nil
	}
	if err := s.svc.Devices.Repo().Update(d); err != nil {
		return nil, err
	}
	what := "Updated device "
	if d.RevokedAt != nil {
		what = "Revoked device "
		if err := s.svc.Playback.EndDevice(d.ID); err != nil {
			return nil, err
		}
		if err := s.svc.Together.LeaveDevice(d.ID, 0); err != nil {
			return nil, err
		}
	}
	audit(ctx, what+d.Name)
	return api.ReplaceDevice200JSONResponse(toDevice(d)), nil
}

// DeleteDevice signs a device out, as sign-out does, and removes it; its
// token stops working. The sign-out comes first so the device leaves Watch
// together before the delete cascades its membership away.
func (s *Server) DeleteDevice(ctx context.Context, r api.DeleteDeviceRequestObject) (api.DeleteDeviceResponseObject, error) {
	if err := s.signedOut(s.svc.Accounts.SignOutDevice(r.DeviceId)); err != nil {
		return nil, err
	}
	if err := s.svc.Devices.Repo().Delete(r.DeviceId); err != nil {
		return nil, err
	}
	audit(ctx, "Deleted device "+strconv.FormatInt(r.DeviceId, 10))
	return api.DeleteDevice204Response{}, nil
}

// ListJoinCodes pages an account's codes, newest first.
func (s *Server) ListJoinCodes(_ context.Context, r api.ListJoinCodesRequestObject) (api.ListJoinCodesResponseObject, error) {
	all, err := s.svc.JoinCodes.Repo().ByAccount(r.Params.AccountId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListJoinCodes200JSONResponse{Items: mapAll(rows, toJoinCode), NextOffset: next}, nil
}

// CreateJoinCode issues a code for an adult who has not signed in yet.
func (s *Server) CreateJoinCode(_ context.Context, r api.CreateJoinCodeRequestObject) (api.CreateJoinCodeResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.Body.AccountId)
	if err != nil {
		return nil, err
	}
	j, err := s.svc.Auth.IssueJoinCode(a, int(or(r.Body.ExpiresInDays, api.JoinCodeInputExpiresInDaysN7)))
	if err != nil {
		return nil, err
	}
	return api.CreateJoinCode201JSONResponse(toJoinCode(j)), nil
}

// GetJoinCode returns one code.
func (s *Server) GetJoinCode(_ context.Context, r api.GetJoinCodeRequestObject) (api.GetJoinCodeResponseObject, error) {
	j, err := s.svc.JoinCodes.Repo().Get(r.JoinCodeId)
	if err != nil {
		return nil, err
	}
	return api.GetJoinCode200JSONResponse(toJoinCode(j)), nil
}

// RevokeJoinCode stamps a code revoked; revoking twice is harmless.
func (s *Server) RevokeJoinCode(_ context.Context, r api.RevokeJoinCodeRequestObject) (api.RevokeJoinCodeResponseObject, error) {
	j, err := s.svc.JoinCodes.Repo().Get(r.JoinCodeId)
	if err != nil {
		return nil, err
	}
	if j.RevokedAt == nil {
		now := time.Now().UTC()
		j.RevokedAt = &now
		if err := s.svc.JoinCodes.Repo().Update(j); err != nil {
			return nil, err
		}
	}
	return api.RevokeJoinCode204Response{}, nil
}

func (s *Server) task(t *task.Task) (api.Task, error) {
	last, err := s.svc.Tasks.Repo().LastRun(t.ID)
	if errors.Is(err, db.ErrNotFound) {
		return toTask(t, nil), nil
	}
	return toTask(t, last), err
}

func taskFrom(in *api.TaskInput, t *task.Task) {
	t.Name, t.Kind, t.IntervalSeconds, t.Enabled, t.NextRunAt = in.Name, in.Kind, in.IntervalSeconds, or(in.Enabled, true), in.NextRunAt
}

// ListTasks pages tasks by name with their last run.
func (s *Server) ListTasks(_ context.Context, r api.ListTasksRequestObject) (api.ListTasksResponseObject, error) {
	all, err := s.svc.Tasks.Repo().List()
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	out := api.ListTasks200JSONResponse{Items: []api.Task{}, NextOffset: next}
	for _, t := range rows {
		v, err := s.task(t)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreateTask adds a task.
func (s *Server) CreateTask(_ context.Context, r api.CreateTaskRequestObject) (api.CreateTaskResponseObject, error) {
	var t task.Task
	taskFrom(r.Body, &t)
	if err := s.svc.Tasks.Repo().Create(&t); err != nil {
		return nil, err
	}
	return api.CreateTask201JSONResponse(toTask(&t, nil)), nil
}

// GetTask returns a task with its last run.
func (s *Server) GetTask(_ context.Context, r api.GetTaskRequestObject) (api.GetTaskResponseObject, error) {
	t, err := s.svc.Tasks.Repo().Get(r.TaskId)
	if err != nil {
		return nil, err
	}
	v, err := s.task(t)
	return api.GetTask200JSONResponse(v), err
}

// ReplaceTask rewrites a task's schedule.
func (s *Server) ReplaceTask(_ context.Context, r api.ReplaceTaskRequestObject) (api.ReplaceTaskResponseObject, error) {
	t, err := s.svc.Tasks.Repo().Get(r.TaskId)
	if err != nil {
		return nil, err
	}
	taskFrom(r.Body, t)
	if err := s.svc.Tasks.Repo().Update(t); err != nil {
		return nil, err
	}
	v, err := s.task(t)
	return api.ReplaceTask200JSONResponse(v), err
}

// DeleteTask removes a task and its history.
func (s *Server) DeleteTask(_ context.Context, r api.DeleteTaskRequestObject) (api.DeleteTaskResponseObject, error) {
	return api.DeleteTask204Response{}, s.svc.Tasks.Repo().Delete(r.TaskId)
}

// ListTaskRuns pages a task's runs, newest first.
func (s *Server) ListTaskRuns(_ context.Context, r api.ListTaskRunsRequestObject) (api.ListTaskRunsResponseObject, error) {
	if _, err := s.svc.Tasks.Repo().Get(r.TaskId); err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	all, err := s.svc.Tasks.Repo().History(r.TaskId, offset+limit+1)
	if err != nil {
		return nil, err
	}
	rows, next := slicePage(all, limit, offset)
	return api.ListTaskRuns200JSONResponse{Items: mapAll(rows, toRun), NextOffset: next}, nil
}

func (s *Server) session(v *session.Session) (api.Session, error) {
	plan, err := s.svc.Sessions.Repo().Plan(v.ID)
	if err != nil {
		return api.Session{}, err
	}
	out := toSession(v, plan)
	if snap, sel, ok := s.svc.Playback.Live(v.ID); ok && v.CurrentEntry < len(plan) {
		if _, m, err := s.svc.Playback.Media(v.MediaFileID); err == nil {
			out.TranscodeReason = optStr(playback.Reason(m, snap, sel, plan[v.CurrentEntry]))
		}
	}
	return out, nil
}

// ListSessions pages sessions newest first, or only the active ones.
func (s *Server) ListSessions(_ context.Context, r api.ListSessionsRequestObject) (api.ListSessionsResponseObject, error) {
	repo := s.svc.Sessions.Repo()
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	var rows []*session.Session
	var next *int
	if or(r.Params.Active, false) {
		all, err := repo.Active()
		if err != nil {
			return nil, err
		}
		rows, next = slicePage(all, limit, offset)
	} else {
		all, err := repo.List(limit+1, offset)
		if err != nil {
			return nil, err
		}
		rows, next = window(all, limit, offset)
	}
	out := api.ListSessions200JSONResponse{Items: []api.Session{}, NextOffset: next}
	for _, v := range rows {
		a, err := s.session(v)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, a)
	}
	return out, nil
}

// GetSession returns a session with its plan.
func (s *Server) GetSession(_ context.Context, r api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	v, err := s.svc.Sessions.Repo().Get(r.SessionId)
	if err != nil {
		return nil, err
	}
	a, err := s.session(v)
	return api.GetSession200JSONResponse(a), err
}

// DeleteSession removes a session record and stops its encoder.
func (s *Server) DeleteSession(_ context.Context, r api.DeleteSessionRequestObject) (api.DeleteSessionResponseObject, error) {
	s.svc.Playback.Forget(r.SessionId)
	return api.DeleteSession204Response{}, s.svc.Sessions.Repo().Delete(r.SessionId)
}

// SetAccountPassword replaces an adult's password and signs out their
// devices but the calling one.
func (s *Server) SetAccountPassword(ctx context.Context, r api.SetAccountPasswordRequestObject) (api.SetAccountPasswordResponseObject, error) {
	a, err := s.svc.Accounts.Repo().Get(r.AccountId)
	if err != nil {
		return nil, err
	}
	if err := s.signedOut(s.svc.Auth.SetPassword(a, r.Body.Password, caller(ctx).device.ID)); err != nil {
		return nil, err
	}
	audit(ctx, "Set a new password for "+a.Name)
	return api.SetAccountPassword204Response{}, nil
}

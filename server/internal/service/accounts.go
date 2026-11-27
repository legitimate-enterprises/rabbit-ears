package service

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/joincode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/offer"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Account flow errors.
var (
	ErrSetupDone     = errors.New("first run is already done")
	ErrPINRequired   = errors.New("this profile needs its PIN")
	ErrPINWrong      = errors.New("wrong PIN")
	ErrPINLocked     = errors.New("too many wrong PINs; try again later")
	ErrPasswordWrong = errors.New("the current password is wrong")
	ErrNotJoinable   = errors.New("only a pending adult can get a join link")
	ErrNoPassword    = errors.New("only an adult who is not revoked has a password")
	ErrSameKind      = errors.New("the profile is already that kind")
	ErrLastAdmin     = errors.New("the last admin cannot be removed")
)

// PIN lockout: this many consecutive failures lock a profile for PINLockout.
// The counter only resets on a right PIN or an adult's reset, so after a
// lockout every further failure locks again.
const (
	PINMaxFailures = 5
	PINLockout     = 15 * time.Minute
)

// Sign-in limit: this many failed sign-ins from one source (see SourceKey),
// or for one email, within SignInWindow refuse that source or email until
// the oldest leaves the window.
const (
	SignInMaxFailures = 10
	SignInWindow      = 10 * time.Minute
)

// AuthService runs first run, sign in, join, PINs and profile switching.
type AuthService struct {
	d *sql.DB
	// pinMu serializes PIN checks so parallel guesses cannot all be judged
	// before the first failure is counted.
	pinMu sync.Mutex
	// Now is the clock for the sign-in limit; tests replace it.
	Now func() time.Time
	// failures holds, per source or email, the sign-in attempts within
	// SignInWindow that failed or are still being checked. In memory: a
	// restart clears it.
	failMu   sync.Mutex
	failures map[string][]time.Time
}

// NewAuthService returns an AuthService over d.
func NewAuthService(d *sql.DB) *AuthService {
	return &AuthService{d: d, Now: time.Now, failures: map[string][]time.Time{}}
}

// attempt counts a sign-in from source as failed until settle says
// otherwise, so parallel guesses are counted before bcrypt judges any.
// It refuses the source once SignInMaxFailures sit within SignInWindow.
func (s *AuthService) attempt(source string) (settle func(failed bool), err error) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	now := s.Now()
	for k, ts := range s.failures {
		for len(ts) > 0 && now.Sub(ts[0]) >= SignInWindow {
			ts = ts[1:]
		}
		if len(ts) == 0 {
			delete(s.failures, k)
		} else {
			s.failures[k] = ts
		}
	}
	ts := s.failures[source]
	if len(ts) >= SignInMaxFailures {
		return nil, &RateLimitedError{RetryAfter: ts[0].Add(SignInWindow).Sub(now)}
	}
	s.failures[source] = append(ts, now)
	return func(failed bool) {
		if failed {
			return
		}
		s.failMu.Lock()
		defer s.failMu.Unlock()
		ts := s.failures[source]
		if i := slices.Index(ts, now); i >= 0 {
			s.failures[source] = slices.Delete(ts, i, i+1)
		}
	}, nil
}

func hashSecret(s string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	return string(b), err
}

func checkSecret(hash, s string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(s)) == nil
}

// dummyHash is compared against when no account matches, so an unknown
// email costs as much time as a wrong password.
var dummyHash = sync.OnceValue(func() string { h, _ := hashSecret("unused"); return h })

// Signed is a device just signed in, with its once-only token.
type Signed struct {
	Token  string
	Device *device.Device
	Adult  *account.Account
}

// Setup is first run: it creates the only admin with their household, names
// the server and signs the device in. It fails with ErrSetupDone once any
// active admin exists.
func (s *AuthService) Setup(serverName, name, email, password, deviceName, platform string) (*Signed, error) {
	if n, err := account.New(s.d).AdminCount(); err != nil {
		return nil, err
	} else if n > 0 {
		return nil, ErrSetupDone
	}
	hash, err := hashSecret(password)
	if err != nil {
		return nil, err
	}
	out := &Signed{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		accounts := account.New(d)
		if n, err := accounts.AdminCount(); err != nil {
			return err
		} else if n > 0 {
			return ErrSetupDone
		}
		a := &account.Account{Kind: account.Adult, Email: email, Name: name, IsAdmin: true, PasswordHash: hash}
		if err := accounts.Create(a, nil); err != nil {
			return err
		}
		if err := serverinfo.New(d).Save(serverName); err != nil {
			return err
		}
		return signIn(d, a, deviceName, platform, out)
	})
}

// signIn gives an adult a household if they lack one, then a device. The
// household and the account's move into it are the adult's own writes.
func signIn(d db.DBTX, a *account.Account, deviceName, platform string, out *Signed) error {
	if a.HouseholdID == nil {
		if _, err := newHousehold(d, a.Name, a, nil, &a.ID); err != nil {
			return err
		}
	}
	token, dev, err := register(device.New(d), &device.Device{Name: deviceName, Platform: platform, AccountID: a.ID, HouseholdID: *a.HouseholdID})
	out.Token, out.Device, out.Adult = token, dev, a
	return err
}

// SignIn checks an adult's email and password and signs the device in.
// Unknown, pending, revoked and wrong all answer ErrUnauthenticated and
// count against source and against the email, known or not; a limited
// source or email gets *RateLimitedError.
func (s *AuthService) SignIn(source, email, password, deviceName, platform string) (*Signed, error) {
	settle, err := s.attempt(source)
	if err != nil {
		return nil, err
	}
	// The prefix keeps an email's count apart from any source's.
	settleEmail, err := s.attempt("email " + strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		settle(false)
		return nil, err
	}
	sg, err := s.signIn(email, password, deviceName, platform)
	failed := errors.Is(err, ErrUnauthenticated)
	settle(failed)
	settleEmail(failed)
	return sg, err
}

func (s *AuthService) signIn(email, password, deviceName, platform string) (*Signed, error) {
	a, err := account.New(s.d).ByEmail(email)
	if errors.Is(err, db.ErrNotFound) {
		checkSecret(dummyHash(), password)
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if !maySignIn(a) || !checkSecret(a.PasswordHash, password) {
		return nil, ErrUnauthenticated
	}
	out := &Signed{}
	return out, db.InTx(s.d, func(d db.DBTX) error { return signIn(d, a, deviceName, platform, out) })
}

// maySignIn reports an account that may sign in a device, by password or
// single sign-on: an adult who has joined and is not revoked.
func maySignIn(a *account.Account) bool {
	return a.Kind == account.Adult && !a.Pending() && a.RevokedAt == nil
}

// joinable returns the pending adult behind a usable code, or db.ErrNotFound.
func joinable(d db.DBTX, j *joincode.JoinCode, now time.Time) (*account.Account, error) {
	if !j.Usable(now) {
		return nil, db.ErrNotFound
	}
	a, err := account.New(d).Get(j.AccountID)
	if err == nil && (!a.Pending() || a.RevokedAt != nil) {
		return nil, db.ErrNotFound
	}
	return a, err
}

// joinAlphabet drops 0/O and 1/I so a code survives being read aloud.
const joinAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

func newJoinCode() string { return randomGroups(2) }

// TemporaryPassword is a random password to read out to an adult.
func TemporaryPassword() string { return randomGroups(3) }

// randomGroups is n dash-separated groups of four joinAlphabet characters.
func randomGroups(n int) string {
	b := make([]byte, 4*n)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 5*n)
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			out = append(out, '-')
		}
		out = append(out, joinAlphabet[int(c)%len(joinAlphabet)])
	}
	return string(out)
}

// IssueJoinCode gives a pending adult an invitation valid for days, under
// id when given.
func (s *AuthService) IssueJoinCode(a *account.Account, days int, id, by *uid.ID) (*joincode.JoinCode, error) {
	if !a.Pending() || a.RevokedAt != nil {
		return nil, ErrNotJoinable
	}
	return newJoinCodeFor(s.d, a.ID, days, id, by)
}

// newJoinCodeFor creates a code under id, a new one when nil.
func newJoinCodeFor(d db.DBTX, accountID uid.ID, days int, id, by *uid.ID) (*joincode.JoinCode, error) {
	j := &joincode.JoinCode{Code: newJoinCode(), AccountID: accountID, ExpiresAt: time.Now().UTC().AddDate(0, 0, days)}
	if id != nil {
		j.ID = *id
	}
	return j, joincode.New(d).Create(j, by)
}

// ResendJoinCode revokes a pending adult's unused codes and issues a new one,
// under id when given.
func (s *AuthService) ResendJoinCode(a *account.Account, days int, id, by *uid.ID) (*joincode.JoinCode, error) {
	if !a.Pending() || a.RevokedAt != nil {
		return nil, ErrNotJoinable
	}
	var j *joincode.JoinCode
	err := db.InTx(s.d, func(d db.DBTX) (err error) {
		if err = joincode.New(d).RevokeUnused(a.ID, time.Now().UTC(), by); err != nil {
			return err
		}
		j, err = newJoinCodeFor(d, a.ID, days, id, by)
		return err
	})
	return j, err
}

// MakeAdult turns a kid into a pending adult of householdID, one of theirs,
// with a code valid for days; they leave every other household, where they
// would only be present. Kid rules and the PIN an adult set for them go, and
// any device acting as them is signed out, since powers follow the active
// profile. The code takes joinCodeID when given.
func (s *AuthService) MakeAdult(a *account.Account, householdID uid.ID, email string, days int, joinCodeID, by *uid.ID) (*joincode.JoinCode, *SignedOut, error) {
	if a.Kind != account.Kid {
		return nil, nil, ErrSameKind
	}
	a.Kind, a.Email, a.MovieCeiling, a.TVCeiling = account.Adult, email, "", ""
	a.PINHash, a.PINFailures, a.PINLockedUntil = "", 0, nil
	a.HouseholdID = &householdID
	var j *joincode.JoinCode
	out := &SignedOut{}
	err := db.InTx(s.d, func(d db.DBTX) (err error) {
		if err = signOut(d, []*account.Account{a}, out); err != nil {
			return err
		}
		if err = account.New(d).RemoveMemberships(a.ID); err != nil {
			return err
		}
		if err = offer.New(d).DeleteOf(a.ID, nil); err != nil {
			return err
		}
		if err = account.New(d).Update(a, by); err != nil {
			return err
		}
		j, err = newJoinCodeFor(d, a.ID, days, joinCodeID, by)
		return err
	})
	return j, out, err
}

// SignedOut is what a change did to the devices it signed out: the devices it
// revoked, those it sent back to Who's watching, and the sessions it ended on
// either. The caller frees those sessions' encoders and takes the devices out
// of Watch together.
type SignedOut struct {
	Devices  []uid.ID
	Reset    []uid.ID
	Sessions []uid.ID
}

// signOut revokes every device of a profile in gone or acting as it and
// ends its open sessions, recording both in out. It must run in the
// transaction that deletes the profiles: the delete clears
// device.profile_id, and a device with none acts as its adult, with no PIN
// asked.
func signOut(d db.DBTX, gone []*account.Account, out *SignedOut) error {
	for _, a := range gone {
		acting, err := device.New(d).Reaching(a.ID)
		if err != nil {
			return err
		}
		if err := signOutDevices(d, acting, out); err != nil {
			return err
		}
	}
	return nil
}

// signOutDevices revokes devs and ends their open sessions, recording both
// in out.
func signOutDevices(d db.DBTX, devs []*device.Device, out *SignedOut) error {
	devices, now := device.New(d), time.Now().UTC()
	for _, v := range devs {
		if err := devices.Revoke(v.ID, now); err != nil {
			return err
		}
		out.Devices = append(out.Devices, v.ID)
		if err := endSessions(d, v.ID, now, out); err != nil {
			return err
		}
	}
	return nil
}

// endSessions ends a device's open sessions, recording them in out.
func endSessions(d db.DBTX, deviceID uid.ID, now time.Time, out *SignedOut) error {
	sessions := session.New(d)
	open, err := sessions.ActiveByDevice(deviceID)
	if err != nil {
		return err
	}
	for _, o := range open {
		if err := sessions.End(o.ID, now); err != nil {
			return err
		}
		out.Sessions = append(out.Sessions, o.ID)
	}
	return nil
}

// Update saves a; demoting or revoking the last active admin is
// ErrLastAdmin.
func (s *AccountService) Update(a *account.Account, by *uid.ID) error {
	return db.InTx(s.d, func(d db.DBTX) error {
		return keepingAnAdmin(d, func() error { return account.New(d).Update(a, by) })
	})
}

// Revoke saves a, which the caller has revoked, and signs out every device
// of it or acting as it; revoking the last active admin is ErrLastAdmin.
func (s *AccountService) Revoke(a *account.Account, by *uid.ID) (*SignedOut, error) {
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		if err := keepingAnAdmin(d, func() error { return account.New(d).Update(a, by) }); err != nil {
			return err
		}
		return signOut(d, []*account.Account{a}, out)
	})
}

// SignOutDevice revokes one device and ends its open sessions.
func (s *AccountService) SignOutDevice(id uid.ID) (*SignedOut, error) {
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		v, err := device.New(d).Get(id)
		if err != nil {
			return err
		}
		return signOutDevices(d, []*device.Device{v}, out)
	})
}

// Delete removes an account and every account that cascades from it, signing
// out the devices of any of them; by is who moves out those who stay.
func (s *AccountService) Delete(id uid.ID, by *uid.ID) (*SignedOut, error) {
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error { return deleteAccount(d, id, out, by) })
}

func deleteAccount(d db.DBTX, id uid.ID, out *SignedOut, by *uid.ID) error {
	accounts := account.New(d)
	gone, err := accounts.DeletedWith(id)
	if err != nil {
		return err
	}
	if err := removing(d, gone, out); err != nil {
		return err
	}
	// Deleting a household's owner deletes the household.
	switch h, err := household.New(d).OwnedBy(id); {
	case err == nil:
		if err := vacate(d, h.ID, out, by); err != nil {
			return err
		}
	case !errors.Is(err, db.ErrNotFound):
		return err
	}
	return keepingAnAdmin(d, func() error {
		if err := accounts.Delete(id); err != nil {
			return err
		}
		return station.New(d).DeleteUnseen(nil, nil)
	})
}

// keepingAnAdmin runs write within d's transaction and fails it with
// ErrLastAdmin when it took away the last active admin, which would reopen
// first run to anyone who can reach the server.
func keepingAnAdmin(d db.DBTX, write func() error) error {
	accounts := account.New(d)
	before, err := accounts.AdminCount()
	if err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	after, err := accounts.AdminCount()
	if err == nil && before > 0 && after == 0 {
		return ErrLastAdmin
	}
	return err
}

// removing readies the accounts in gone for deleting in the same
// transaction: it signs out their devices and deletes the stations that
// play their playlists, which no foreign key cascades to.
func removing(d db.DBTX, gone []*account.Account, out *SignedOut) error {
	if err := signOut(d, gone, out); err != nil {
		return err
	}
	stations := station.New(d)
	for _, a := range gone {
		if err := stations.DeletePlayingOwnedBy(a.ID); err != nil {
			return err
		}
	}
	return nil
}

// InviteAdult adds a pending adult to a household with a code valid for days,
// the adult under id and the code under joinCodeID when given.
func (s *AuthService) InviteAdult(householdID uid.ID, name, email string, days int, id, joinCodeID, by *uid.ID) (*account.Account, *joincode.JoinCode, error) {
	a := &account.Account{Kind: account.Adult, Name: name, Email: email, HouseholdID: &householdID, FilterMode: account.FilterRatings}
	if id != nil {
		a.ID = *id
	}
	var j *joincode.JoinCode
	return a, j, db.InTx(s.d, func(d db.DBTX) error {
		if err := account.New(d).Create(a, by); err != nil {
			return err
		}
		var err error
		j, err = newJoinCodeFor(d, a.ID, days, joinCodeID, by)
		return err
	})
}

// PreviewJoin returns the invitee, the code and the inviter's name
// ("" when none is left) for a usable code, else db.ErrNotFound, which
// counts against source as a failed sign-in does.
func (s *AuthService) PreviewJoin(source, code string) (*account.Account, *joincode.JoinCode, string, error) {
	settle, err := s.attempt(source)
	if err != nil {
		return nil, nil, "", err
	}
	a, j, by, err := s.previewJoin(code)
	settle(errors.Is(err, db.ErrNotFound))
	return a, j, by, err
}

func (s *AuthService) previewJoin(code string) (*account.Account, *joincode.JoinCode, string, error) {
	j, err := joincode.New(s.d).ByCode(code)
	if err != nil {
		return nil, nil, "", err
	}
	a, err := joinable(s.d, j, time.Now().UTC())
	if err != nil {
		return nil, nil, "", err
	}
	// An adult added to a household is invited by its owner, else by an admin.
	by := ""
	if a.HouseholdID != nil {
		if h, err := household.New(s.d).Get(*a.HouseholdID); err == nil {
			if o, err := account.New(s.d).Get(h.AdultID); err == nil {
				by = o.Name
			}
		}
	} else if admin, err := account.New(s.d).FirstAdmin(); err == nil {
		by = admin.Name
	}
	return a, j, by, nil
}

// AcceptJoin consumes a code: it sets the adult's name and password, gives
// them a household, revokes their other codes and signs the device in. A
// code that is not usable answers db.ErrNotFound and counts against source
// as a failed sign-in does.
func (s *AuthService) AcceptJoin(source, code, name, password, deviceName, platform string) (*Signed, error) {
	settle, err := s.attempt(source)
	if err != nil {
		return nil, err
	}
	sg, err := s.acceptJoin(code, name, password, deviceName, platform)
	settle(errors.Is(err, db.ErrNotFound))
	return sg, err
}

func (s *AuthService) acceptJoin(code, name, password, deviceName, platform string) (*Signed, error) {
	now := time.Now().UTC()
	j, err := joincode.New(s.d).ByCode(code)
	if err != nil {
		return nil, err
	}
	if _, err := joinable(s.d, j, now); err != nil {
		return nil, err
	}
	hash, err := hashSecret(password)
	if err != nil {
		return nil, err
	}
	out := &Signed{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		codes := joincode.New(d)
		j, err := codes.ByCode(code)
		if err != nil {
			return err
		}
		a, err := joinable(d, j, now)
		if err != nil {
			return err
		}
		// The joining adult makes these writes.
		if _, err := codes.Redeem(code, now, &a.ID); err != nil {
			return err
		}
		if err := codes.RevokeUnused(a.ID, now, &a.ID); err != nil {
			return err
		}
		a.Name, a.PasswordHash = name, hash
		if err := account.New(d).Update(a, &a.ID); err != nil {
			return err
		}
		return signIn(d, a, deviceName, platform, out)
	})
}

// ChangePassword replaces an adult's password once current matches, as
// Confirm checks it, and signs out their devices but deviceID, the one it
// is changed on.
func (s *AuthService) ChangePassword(source string, deviceID uid.ID, a *account.Account, current, next string, by *uid.ID) (*SignedOut, error) {
	if err := s.Confirm(source, a, current); err != nil {
		return nil, err
	}
	return s.replacePassword(a, next, &deviceID, by)
}

// SetPassword is an admin replacing an adult's password, such as with a
// temporary one after they forgot theirs, and signing out their devices but
// keep, the admin's own when the adult is them; a nil keep signs out all.
func (s *AuthService) SetPassword(a *account.Account, password string, keep, by *uid.ID) (*SignedOut, error) {
	if a.Kind != account.Adult || a.Pending() || a.RevokedAt != nil {
		return nil, ErrNoPassword
	}
	return s.replacePassword(a, password, keep, by)
}

// replacePassword stores password as a's and signs out a's devices but
// keep in one transaction, so no device signed in before the change
// outlives it.
func (s *AuthService) replacePassword(a *account.Account, password string, keep, by *uid.ID) (*SignedOut, error) {
	hash, err := hashSecret(password)
	if err != nil {
		return nil, err
	}
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		a.PasswordHash = hash
		if err := account.New(d).Update(a, by); err != nil {
			return err
		}
		devs, err := device.New(d).ByAccount(a.ID)
		if err != nil {
			return err
		}
		return signOutDevices(d, slices.DeleteFunc(devs, func(v *device.Device) bool {
			return keep != nil && v.ID == *keep || v.RevokedAt != nil
		}), out)
	})
}

// SetPIN sets or resets a profile's PIN, clearing failures and any lock.
func (s *AuthService) SetPIN(a *account.Account, pin string, by *uid.ID) error {
	hash := ""
	if pin != "" {
		var err error
		if hash, err = hashSecret(pin); err != nil {
			return err
		}
	}
	a.PINHash, a.PINFailures, a.PINLockedUntil = hash, 0, nil
	return account.New(s.d).Update(a, by)
}

// Switch makes the device act as profile to, checking its PIN if it has one.
func (s *AuthService) Switch(dev *device.Device, to *account.Account, pin *string) error {
	if to.Pending() || to.RevokedAt != nil {
		return db.ErrNotFound
	}
	if to.PINHash != "" {
		if err := s.checkPIN(to.ID, pin); err != nil {
			return err
		}
	}
	var pid *uid.ID
	if to.ID != dev.AccountID {
		pid = ref(to.ID)
	}
	dev.ProfileID, dev.LockedAt = pid, nil
	return device.New(s.d).SetProfile(dev.ID, pid)
}

func (s *AuthService) checkPIN(id uid.ID, pin *string) error {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	accounts := account.New(s.d)
	a, err := accounts.Get(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	switch {
	case a.PINLockedUntil != nil && now.Before(*a.PINLockedUntil):
		return ErrPINLocked
	case pin == nil:
		return ErrPINRequired
	case checkSecret(a.PINHash, *pin):
		return accounts.ClearPINFailures(id)
	}
	n, err := accounts.RecordPINFailure(id, PINMaxFailures, now.Add(PINLockout))
	if err != nil {
		return err
	}
	log := logstore.Log("auth")
	if n >= PINMaxFailures {
		log.Warn(fmt.Sprintf("Failed PIN attempt for %s: locked for %s after %d wrong PINs", a.Name, PINLockout, n))
		return ErrPINLocked
	}
	log.Warn(fmt.Sprintf("Failed PIN attempt for %s: %d of %d before lockout", a.Name, n, PINMaxFailures))
	return ErrPINWrong
}

// AudienceFor is what a profile may see: everything for an adult, the
// ceilings or allow list for a kid.
func AudienceFor(a *account.Account) item.Audience {
	if a.Kind != account.Kid {
		return item.Audience{}
	}
	return item.Audience{Kid: true, AccountID: ref(a.ID), AllowList: a.FilterMode == account.FilterAllowList,
		Movie: account.AtOrBelow(account.MovieRatings, a.MovieCeiling), TV: account.AtOrBelow(account.TVRatings, a.TVCeiling)}
}

// BrowseAudienceFor is AudienceFor for browsing, which an adult hiding
// kids' content browses without it.
func BrowseAudienceFor(a *account.Account) item.Audience {
	if a.Kind != account.Kid && a.HideKidsContent {
		return item.Audience{HideKids: true}
	}
	return AudienceFor(a)
}

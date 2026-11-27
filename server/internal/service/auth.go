package service

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// ErrUnauthenticated means a token is unknown, revoked, or belongs to a
// revoked account.
var ErrUnauthenticated = errors.New("unauthenticated")

// HashToken is the form a bearer token is stored and looked up in.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Register issues a new device for an adult in a household and returns its
// bearer token, which is never stored and cannot be shown again.
func (s *DeviceService) Register(accountID, householdID uid.ID, name, platform string) (string, *device.Device, error) {
	return register(s.repo, &device.Device{Name: name, Platform: platform, AccountID: accountID, HouseholdID: householdID})
}

// register stores d with a new token and returns the token.
func register(repo *device.Repo, d *device.Device) (string, *device.Device, error) {
	token, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	d.TokenHash = HashToken(token)
	if err := repo.Create(d); err != nil {
		return "", nil, err
	}
	return token, d, nil
}

// randomToken is 32 random bytes, URL-safe: a bearer or poll token.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// touchEvery bounds last-seen writes: every request authenticates, and the
// database has one connection.
const touchEvery = time.Minute

// Authenticate resolves a bearer token to its device and adult, recording
// the device as seen at most once per touchEvery.
func (s *DeviceService) Authenticate(token string) (*device.Device, *account.Account, error) {
	d, err := s.repo.ByTokenHash(HashToken(token))
	if errors.Is(err, db.ErrNotFound) || (err == nil && d.RevokedAt != nil) {
		return nil, nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, nil, err
	}
	a, err := s.accounts.Get(d.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if a.RevokedAt != nil {
		return nil, nil, ErrUnauthenticated
	}
	now := time.Now().UTC()
	if d.LastSeenAt != nil && now.Sub(*d.LastSeenAt) < touchEvery {
		return d, a, nil
	}
	if err := s.repo.Touch(d.ID, now); err != nil {
		return nil, nil, err
	}
	d.LastSeenAt = &now
	return d, a, nil
}

// Profile returns an account only if it is in the household, so a device
// can never reach a profile of a household it is not in.
func (s *AccountService) Profile(householdID, accountID uid.ID) (*account.Account, error) {
	a, err := s.repo.Get(accountID)
	if err != nil {
		return nil, err
	}
	if in, err := s.repo.In(a.ID, householdID); err != nil || !in {
		return nil, cmp.Or(err, db.ErrNotFound)
	}
	return a, nil
}

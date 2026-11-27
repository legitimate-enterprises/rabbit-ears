package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

const (
	// LocalCheckTTL is how long an opened check waits for its confirmation.
	LocalCheckTTL = time.Minute
	// LocalHomeTTL is how long a confirmation counts; a device at home
	// confirms again before then.
	LocalHomeTTL = 15 * time.Minute
)

// ErrNotLocal refuses a confirmation with a wrong proof or one that did not
// come from the server's own network.
var ErrNotLocal = errors.New("not a local confirmation")

// LocalCheck is one opened check: its key reaches the device over the
// address it signed in at and never travels again.
type LocalCheck struct {
	ID        string
	Key       string // 32 random bytes, base64
	ExpiresAt time.Time
}

// LocalCheckService learns that a device reaching the server at a public
// address is on the server's own network. The device opens a check there,
// then confirms it at a local address with an HMAC under the check's key;
// the server answers with its own, so each end knows the other. Checks and
// standings live in memory: after a restart devices confirm again.
type LocalCheckService struct {
	// Now is the clock for expiry; tests replace it.
	Now  func() time.Time
	mu   sync.Mutex
	open map[string]openCheck
	home map[uid.ID]time.Time // by device, when it last confirmed
}

type openCheck struct {
	key     []byte
	device  uid.ID
	expires time.Time
}

// NewLocalCheckService returns a LocalCheckService.
func NewLocalCheckService() *LocalCheckService {
	return &LocalCheckService{Now: time.Now, open: map[string]openCheck{}, home: map[uid.ID]time.Time{}}
}

// Start opens a check for the device.
func (s *LocalCheckService) Start(device uid.ID) (LocalCheck, error) {
	id, err := newKey()
	if err != nil {
		return LocalCheck{}, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return LocalCheck{}, err
	}
	now := s.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.open {
		if !now.Before(c.expires) {
			delete(s.open, k)
		}
	}
	s.open[id] = openCheck{key, device, now.Add(LocalCheckTTL)}
	return LocalCheck{ID: id, Key: base64.StdEncoding.EncodeToString(key), ExpiresAt: now.Add(LocalCheckTTL)}, nil
}

func localProof(key []byte, side, nonce string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(side + ":" + nonce))
	return m.Sum(nil)
}

// Confirm takes the device's proof, hex of HMAC-SHA256 over "client:" and
// its nonce, and answers the server's, over "server:" and the same nonce.
// local says the request came straight from the server's own network. A
// refused confirmation leaves the check open; an accepted one closes it.
func (s *LocalCheckService) Confirm(id, nonce, proof string, local bool) (string, error) {
	now := s.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.open[id]
	if !ok || !now.Before(c.expires) {
		return "", db.ErrNotFound
	}
	got, err := hex.DecodeString(proof)
	if err != nil || !local || !hmac.Equal(got, localProof(c.key, "client", nonce)) {
		return "", ErrNotLocal
	}
	delete(s.open, id)
	s.home[c.device] = now
	return hex.EncodeToString(localProof(c.key, "server", nonce)), nil
}

// Forget drops a device's standing and its open checks, as when it is
// signed out.
func (s *LocalCheckService) Forget(device uid.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.home, device)
	for k, c := range s.open {
		if c.device == device {
			delete(s.open, k)
		}
	}
}

// Home reports a device that confirmed a check within LocalHomeTTL.
func (s *LocalCheckService) Home(device uid.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.home[device]
	return ok && s.Now().Sub(at) < LocalHomeTTL
}

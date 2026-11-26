package service_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

func localProof(t *testing.T, key, side, nonce string) string {
	t.Helper()
	k, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(k) != 32 {
		t.Fatalf("key %q: %v", key, err)
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(side + ":" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// A device is at home once it proves, from the server's own network, that
// it holds the key of a check the server opened for it; the reply proves
// the server holds it too. A check confirms once and the standing lapses.
func TestLocalCheck(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s := service.NewLocalCheckService()
	s.Now = func() time.Time { return now }
	const nonce = "0123456789abcdef"

	c, err := s.Start(7)
	if err != nil || c.ID == "" || !c.ExpiresAt.Equal(now.Add(service.LocalCheckTTL)) {
		t.Fatalf("%+v %v", c, err)
	}
	if s.Home(7) {
		t.Fatal("an open check is no proof")
	}
	if _, err := s.Confirm(c.ID, nonce, localProof(t, c.Key, "server", nonce), true); !errors.Is(err, service.ErrNotLocal) {
		t.Fatalf("a wrong proof: %v", err)
	}
	if _, err := s.Confirm(c.ID, nonce, localProof(t, c.Key, "client", nonce), false); !errors.Is(err, service.ErrNotLocal) {
		t.Fatalf("a proof from outside the server's network: %v", err)
	}
	if s.Home(7) {
		t.Fatal("a refused confirmation is no proof")
	}
	got, err := s.Confirm(c.ID, nonce, localProof(t, c.Key, "client", nonce), true)
	if err != nil || got != localProof(t, c.Key, "server", nonce) {
		t.Fatalf("%q %v", got, err)
	}
	if !s.Home(7) || s.Home(8) {
		t.Fatal("the device that confirmed is at home, and only that one")
	}
	if _, err := s.Confirm(c.ID, nonce, localProof(t, c.Key, "client", nonce), true); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a check confirms once: %v", err)
	}

	now = now.Add(service.LocalHomeTTL)
	if s.Home(7) {
		t.Fatal("the standing lapses")
	}
	c, err = s.Start(7)
	must(t, err)
	now = now.Add(service.LocalCheckTTL)
	if _, err := s.Confirm(c.ID, nonce, localProof(t, c.Key, "client", nonce), true); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("an expired check: %v", err)
	}
}

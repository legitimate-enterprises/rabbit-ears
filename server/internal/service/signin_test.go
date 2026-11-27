package service_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/joincode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// Wrong join codes count against the source as failed sign-ins do; another
// source still previews and accepts a right code.
func TestJoinCodeMissesLimitTheSource(t *testing.T) {
	s := service.New(dbtest.Open(t))
	sg, err := s.Auth.Setup("Den", "Owner", "o@example.test", "right-password", "Laptop", "web")
	if err != nil {
		t.Fatal(err)
	}
	_, j, err := s.Auth.InviteAdult(*sg.Adult.HouseholdID, "Bea", "bea@example.test", 7, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range service.SignInMaxFailures {
		if i%2 == 0 {
			_, _, _, err = s.Auth.PreviewJoin("10.0.0.9", "ZZZZ-ZZZZ")
		} else {
			_, err = s.Auth.AcceptJoin("10.0.0.9", "ZZZZ-ZZZZ", "Bea", "right-password", "Phone", "ios")
		}
		if !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("miss %d: %v", i, err)
		}
	}
	var limited *service.RateLimitedError
	if _, _, _, err := s.Auth.PreviewJoin("10.0.0.9", j.Code); !errors.As(err, &limited) {
		t.Fatalf("preview from the guessing source: %v", err)
	}
	if _, err := s.Auth.AcceptJoin("10.0.0.9", j.Code, "Bea", "right-password", "Phone", "ios"); !errors.As(err, &limited) {
		t.Fatalf("accept from the guessing source: %v", err)
	}
	if _, _, _, err := s.Auth.PreviewJoin("10.0.0.10", j.Code); err != nil {
		t.Fatalf("preview from another source: %v", err)
	}
	if _, err := s.Auth.AcceptJoin("10.0.0.10", j.Code, "Bea", "right-password", "Phone", "ios"); err != nil {
		t.Fatalf("accept from another source: %v", err)
	}
}

// Ten failed sign-ins from one source in ten minutes refuse that source,
// even with the right password, until the oldest failure leaves the window.
// Other sources are unaffected.
func TestSignInFailuresLimitTheSource(t *testing.T) {
	s := service.New(dbtest.Open(t))
	now := time.Now().UTC()
	s.Auth.Now = func() time.Time { return now }
	if _, err := s.Auth.Setup("Den", "Owner", "o@example.test", "right-password", "Laptop", "web"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range service.SignInMaxFailures + 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Auth.SignIn("10.0.0.9", fmt.Sprintf("guess%d@example.test", i), "wrong", "Laptop", "web")
		}()
	}
	wg.Wait()
	_, err := s.Auth.SignIn("10.0.0.9", "o@example.test", "right-password", "Laptop", "web")
	var limited *service.RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 || limited.RetryAfter > service.SignInWindow {
		t.Fatalf("parallel guesses count before bcrypt finishes: %v", err)
	}
	if _, err := s.Auth.SignIn("10.0.0.10", "o@example.test", "right-password", "Laptop", "web"); err != nil {
		t.Fatalf("another source: %v", err)
	}
	now = now.Add(service.SignInWindow)
	if _, err := s.Auth.SignIn("10.0.0.9", "o@example.test", "right-password", "Laptop", "web"); err != nil {
		t.Fatalf("the window passes: %v", err)
	}
}

// Ten failed sign-ins for one email in ten minutes, from any sources,
// refuse that email, even with the right password, until the oldest failure
// leaves the window. An email no account has counts the same, so the limit
// tells no one which emails exist. Devices signed in already keep working.
func TestSignInFailuresLimitTheEmail(t *testing.T) {
	s := service.New(dbtest.Open(t))
	now := time.Now().UTC()
	s.Auth.Now = func() time.Time { return now }
	sg, err := s.Auth.Setup("Den", "Owner", "o@example.test", "right-password", "Laptop", "web")
	if err != nil {
		t.Fatal(err)
	}
	var limited *service.RateLimitedError
	for _, email := range []string{"o@example.test", "nobody@example.test"} {
		for i := range service.SignInMaxFailures {
			source := fmt.Sprintf("10.0.%d.%d", len(email), i)
			if _, err := s.Auth.SignIn(source, email, "wrong", "Laptop", "web"); !errors.Is(err, service.ErrUnauthenticated) {
				t.Fatalf("%s guess %d: %v", email, i, err)
			}
		}
		if _, err := s.Auth.SignIn("10.0.9.9", " "+strings.ToUpper(email), "right-password", "Laptop", "web"); !errors.As(err, &limited) ||
			limited.RetryAfter <= 0 || limited.RetryAfter > service.SignInWindow {
			t.Fatalf("%s from a fresh source: %v", email, err)
		}
	}
	if _, _, err := s.Devices.Authenticate(sg.Token); err != nil {
		t.Fatalf("a device signed in already: %v", err)
	}
	if _, err := s.Auth.SignIn("10.0.9.9", "else@example.test", "wrong", "Laptop", "web"); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("another email from that source: %v", err)
	}
	now = now.Add(service.SignInWindow)
	if _, err := s.Auth.SignIn("10.0.9.9", "o@example.test", "right-password", "Laptop", "web"); err != nil {
		t.Fatalf("the window passes: %v", err)
	}
}

// A successful sign-in neither counts nor clears earlier failures.
func TestSignInSuccessDoesNotCount(t *testing.T) {
	s := service.New(dbtest.Open(t))
	if _, err := s.Auth.Setup("Den", "Owner", "o@example.test", "right-password", "Laptop", "web"); err != nil {
		t.Fatal(err)
	}
	for range service.SignInMaxFailures + 1 {
		if _, err := s.Auth.SignIn("10.0.0.9", "o@example.test", "right-password", "Laptop", "web"); err != nil {
			t.Fatal(err)
		}
	}
}

// Joining and signing in are the adult's own writes: they never leave the
// rows they change looking written by no one.
func TestJoiningAndSigningInAreTheAdultsWrites(t *testing.T) {
	d := dbtest.Open(t)
	s := service.New(d)
	sg, err := s.Auth.Setup("Den", "Owner", "o@example.test", "right-password", "Laptop", "web")
	if err != nil {
		t.Fatal(err)
	}
	owner := sg.Adult
	bea, j, err := s.Auth.InviteAdult(*owner.HouseholdID, "Bea", "bea@example.test", 7, nil, nil, &owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Auth.AcceptJoin("10.0.0.10", j.Code, "Bea", "right-password", "Phone", "ios"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Accounts.Repo().Get(bea.ID)
	if err != nil || got.ModifiedBy == nil || *got.ModifiedBy != bea.ID {
		t.Fatalf("the joined account was last written by %v: %v", got.ModifiedBy, err)
	}
	if code, err := joincode.New(d).Get(j.ID); err != nil || code.ModifiedBy == nil || *code.ModifiedBy != bea.ID {
		t.Fatalf("the redeemed code was last written by %+v: %v", code, err)
	}

	if _, err := s.Households.Delete(*owner.HouseholdID, &owner.ID); err != nil {
		t.Fatal(err)
	}
	signed, err := s.Auth.SignIn("10.0.0.11", "o@example.test", "right-password", "Tablet", "ios")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = s.Accounts.Repo().Get(owner.ID); err != nil || got.ModifiedBy == nil || *got.ModifiedBy != owner.ID {
		t.Fatalf("the adult given a household was last written by %v: %v", got.ModifiedBy, err)
	}
	if h, err := s.Households.Repo().Get(*signed.Adult.HouseholdID); err != nil || h.CreatedBy == nil || *h.CreatedBy != owner.ID {
		t.Fatalf("the household made at sign-in %+v: %v", h, err)
	}
}

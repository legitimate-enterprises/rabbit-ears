package service_test

import (
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// An IPv6 sender owns a /64, so its whole /64 is one source; IPv4 senders
// are one address each.
func TestSourceKey(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":           "192.0.2.7",
		"::ffff:192.0.2.7":    "192.0.2.7",
		"2001:db8:1:2:3::9":   "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::": "2001:db8:1:2::/64",
	} {
		if got := service.SourceKey(net.ParseIP(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	if service.SourceKey(nil) != "" {
		t.Error("no address")
	}
}

// A TV joins the household of the device its link was approved on, while
// the approving adult is still in it.
func TestALinkedTVJoinsTheApprovingDevicesHousehold(t *testing.T) {
	s := service.New(dbtest.Open(t))
	adult := func(email string) (*account.Account, int64) {
		a := &account.Account{Kind: account.Adult, Email: email, Name: email, PasswordHash: "x"}
		if err := s.Accounts.Repo().Create(a); err != nil {
			t.Fatal(err)
		}
		h, err := s.Households.CreateFor(email, a)
		if err != nil {
			t.Fatal(err)
		}
		return a, h.ID
	}
	a, _ := adult("a@example.test")
	_, theirs := adult("b@example.test")
	if err := s.Households.Join(a, theirs); err != nil {
		t.Fatal(err)
	}
	link := func() (int64, string) {
		l, token, err := s.DeviceLinks.Create("TV", "roku", "10.0.0.2", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeviceLinks.Decide(a, theirs, l.Code, true); err != nil {
			t.Fatal(err)
		}
		return l.ID, token
	}
	id, token := link()
	if _, sg, err := s.DeviceLinks.Poll(id, token); err != nil || sg == nil || sg.Device.HouseholdID != theirs {
		t.Fatalf("the TV is not in the approving device's household: %+v %v", sg, err)
	}
	id, token = link()
	if _, err := s.Households.Leave(a, theirs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeviceLinks.Poll(id, token); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a link outlived its adult leaving the household: %v", err)
	}
}

// However many remote sources ask, only so many links are pending at once,
// and they never lock out a TV at home.
func TestPendingLinksAreCappedOverall(t *testing.T) {
	s := service.NewDeviceLinkService(dbtest.Open(t))
	for i := range service.LinkPendingTotal {
		if _, _, err := s.Create("TV", "roku", "192.0.2."+strconv.Itoa(i), false); err != nil {
			t.Fatal(i, err)
		}
	}
	var limited *service.RateLimitedError
	if _, _, err := s.Create("TV", "roku", "198.51.100.1", false); !errors.As(err, &limited) || limited.RetryAfter <= 0 {
		t.Fatalf("%v", err)
	}
	for i := range service.LinkPendingPerSource {
		if _, _, err := s.Create("TV", "roku", "10.0.0.2", true); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, _, err := s.Create("TV", "roku", "10.0.0.2", true); !errors.As(err, &limited) {
		t.Fatalf("one address at home is still capped: %v", err)
	}
}

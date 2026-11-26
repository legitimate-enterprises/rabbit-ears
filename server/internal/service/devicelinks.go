package service

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/devicelink"
)

// Device link limits. A code lives LinkLife and works once. One address may
// hold LinkPendingPerSource pending links. An adult who misses LinkMisses
// codes within LinkMissWindow is refused every code until the window
// passes: with about 2^40 codes, a code cannot be guessed in its life.
// At most LinkPendingTotal links are pending at once from all sources, a cap
// a local source passes, and a link nobody collected is deleted LinkKeep
// after it expired.
const (
	LinkLife             = 10 * time.Minute
	LinkPendingPerSource = 5
	LinkPendingTotal     = 50
	LinkMisses           = 10
	LinkMissWindow       = 10 * time.Minute
	LinkKeep             = 24 * time.Hour
)

// SourceKey is the requester an address limit counts: the address for IPv4,
// its /64 for IPv6, which one subscriber holds whole; "" for none.
func SourceKey(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}

// RateLimitedError refuses a request until RetryAfter has passed.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "too many requests; try again later" }

// DeviceLinkService runs Link a TV: a device asks for a code, an adult
// approves or denies it elsewhere, and the device collects its token by
// polling.
type DeviceLinkService struct {
	d    *sql.DB
	repo *devicelink.Repo
	// Now is the clock for expiry and the miss window; tests replace it.
	Now func() time.Time
	// mu guards misses and serializes the per-address check with its insert.
	mu     sync.Mutex
	misses map[int64][]time.Time // by adult, oldest first, within the window
}

// NewDeviceLinkService returns a DeviceLinkService over d.
func NewDeviceLinkService(d *sql.DB) *DeviceLinkService {
	return &DeviceLinkService{d: d, repo: devicelink.New(d), Now: time.Now, misses: map[int64][]time.Time{}}
}

// Repo exposes the repository.
func (s *DeviceLinkService) Repo() *devicelink.Repo { return s.repo }

// now is at the millisecond the database keeps.
func (s *DeviceLinkService) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// Status is a link's status as of now.
func (s *DeviceLinkService) Status(l *devicelink.Link) string { return l.StatusAt(s.now()) }

// Create opens a pending link for a device at source, local when it is on
// the server's own network, and returns it with its poll token, which is
// never stored and cannot be shown again.
func (s *DeviceLinkService) Create(name, platform, source string, local bool) (*devicelink.Link, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if err := s.repo.PruneUncollected(now.Add(-LinkKeep)); err != nil {
		return nil, "", err
	}
	n, first, err := s.repo.PendingFrom(source, now)
	if err != nil {
		return nil, "", err
	}
	if n >= LinkPendingPerSource {
		return nil, "", &RateLimitedError{first.Sub(now)}
	}
	if !local {
		if n, first, err = s.repo.PendingFrom("", now); err != nil {
			return nil, "", err
		}
		if n >= LinkPendingTotal {
			return nil, "", &RateLimitedError{first.Sub(now)}
		}
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	l := &devicelink.Link{PollTokenHash: HashToken(token), DeviceName: name, Platform: platform, Source: source, ExpiresAt: now.Add(LinkLife)}
	// Codes of old links stay taken, so a new code may collide once in a while.
	for range 3 {
		l.Code = newJoinCode()
		if err = s.repo.Create(l); !errors.Is(err, db.ErrConflict) {
			break
		}
	}
	return l, token, err
}

// ByToken returns the link behind id when token is its poll token and it has
// not yet handed its device over, else db.ErrNotFound.
func (s *DeviceLinkService) ByToken(id int64, token string) (*devicelink.Link, error) {
	l, err := s.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(l.PollTokenHash), []byte(HashToken(token))) != 1 || l.CollectedAt != nil {
		return nil, db.ErrNotFound
	}
	return l, nil
}

// Poll answers the device. On the first poll of an approved link it signs
// the device in as the approving adult, in the household it was approved
// in while they are still in it, and returns the once-only token; every
// later poll is db.ErrNotFound.
func (s *DeviceLinkService) Poll(id int64, token string) (*devicelink.Link, *Signed, error) {
	l, err := s.ByToken(id, token)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if l.StatusAt(now) != devicelink.Approved {
		return l, nil, nil
	}
	out := &Signed{}
	err = db.InTx(s.d, func(d db.DBTX) error {
		accounts := account.New(d)
		a, err := accounts.Get(*l.AccountID)
		if err != nil {
			return err
		}
		in, err := accounts.In(a.ID, *l.HouseholdID)
		if err != nil {
			return err
		}
		if a.Kind != account.Adult || a.RevokedAt != nil || !in {
			return db.ErrNotFound
		}
		out.Token, out.Device, err = register(device.New(d), &device.Device{Name: l.DeviceName, Platform: l.Platform, AccountID: a.ID,
			HouseholdID: *l.HouseholdID, Linked: true})
		if err != nil {
			return err
		}
		out.Adult = a
		return devicelink.New(d).Collect(l.ID, out.Device.ID, now)
	})
	if err != nil {
		return nil, nil, err
	}
	l.DeviceID, l.CollectedAt = &out.Device.ID, &now
	return l, out, nil
}

// Preview returns the pending link behind a code for an adult to recognize.
func (s *DeviceLinkService) Preview(adult *account.Account, code string) (*devicelink.Link, error) {
	return s.guarded(adult.ID, func(now time.Time) (*devicelink.Link, error) { return s.repo.Pending(code, now) })
}

// Decide approves or denies a pending code as an adult on a device of a
// household, into that household.
func (s *DeviceLinkService) Decide(adult *account.Account, householdID int64, code string, approve bool) (*devicelink.Link, error) {
	status := devicelink.Denied
	if approve {
		status = devicelink.Approved
	}
	return s.guarded(adult.ID, func(now time.Time) (*devicelink.Link, error) {
		return s.repo.Decide(code, status, adult.ID, householdID, now)
	})
}

// guarded refuses an adult over the miss limit, else runs find and counts
// db.ErrNotFound as a miss.
func (s *DeviceLinkService) guarded(adultID int64, find func(time.Time) (*devicelink.Link, error)) (*devicelink.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := s.misses[adultID]
	for len(recent) > 0 && !recent[0].After(now.Add(-LinkMissWindow)) {
		recent = recent[1:]
	}
	if len(recent) >= LinkMisses {
		s.misses[adultID] = recent
		return nil, &RateLimitedError{recent[0].Add(LinkMissWindow).Sub(now)}
	}
	l, err := find(now)
	if errors.Is(err, db.ErrNotFound) {
		recent = append(recent, now)
	}
	if len(recent) == 0 {
		delete(s.misses, adultID)
	} else {
		s.misses[adultID] = recent
	}
	return l, err
}

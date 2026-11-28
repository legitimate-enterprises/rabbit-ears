package service

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/friend"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Friend request limits: a request lives FriendRequestLife unless
// accepted or withdrawn, and an adult sends at most FriendRequestsPerDay
// within FriendRequestWindow, each send counting, a repeat too.
const (
	FriendRequestLife    = 30 * 24 * time.Hour
	FriendRequestsPerDay = 20
	FriendRequestWindow  = 24 * time.Hour
)

// ErrNotBefriendable refuses accepting a request whose sender can no longer
// be a friend, as one revoked.
var ErrNotBefriendable = errors.New("the sender can no longer be a friend")

// FriendService runs friend requests and friendships. A request is to an
// email and its sending reads nothing of who holds it, so that its sender
// learns of them only by their accepting.
type FriendService struct {
	d    *sql.DB
	repo *friend.Repo
	// Now is the clock for expiry and the sending limit; tests replace it.
	Now func() time.Time
	// mu guards sent, by adult the sends within FriendRequestWindow, oldest
	// first. In memory: a restart clears it.
	mu   sync.Mutex
	sent map[uid.ID][]time.Time
}

// NewFriendService returns a FriendService over d.
func NewFriendService(d *sql.DB) *FriendService {
	return &FriendService{d: d, repo: friend.New(d), Now: time.Now, sent: map[uid.ID][]time.Time{}}
}

// Repo exposes the repository.
func (s *FriendService) Repo() *friend.Repo { return s.repo }

func (s *FriendService) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// limit counts a send by an adult, or refuses it with *RateLimitedError.
func (s *FriendService) limit(id uid.ID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := slices.DeleteFunc(s.sent[id], func(t time.Time) bool { return now.Sub(t) >= FriendRequestWindow })
	if len(ts) >= FriendRequestsPerDay {
		s.sent[id] = ts
		return &RateLimitedError{ts[0].Add(FriendRequestWindow).Sub(now)}
	}
	s.sent[id] = append(ts, now)
	return nil
}

// Send asks whoever holds email to be friends with from, under id when
// given, and returns the request as from may read it.
func (s *FriendService) Send(from *account.Account, email string, id *uid.ID) (*friend.Request, error) {
	now := s.now()
	if err := s.limit(from.ID, now); err != nil {
		return nil, err
	}
	q := &friend.Request{FromID: from.ID, Email: strings.TrimSpace(email), ExpiresAt: now.Add(FriendRequestLife)}
	if id != nil {
		q.ID = *id
	}
	return s.repo.Send(q, now)
}

// Sent lists the requests a has sent that have not expired.
func (s *FriendService) Sent(a *account.Account) ([]*friend.Request, error) {
	return s.repo.Sent(a.ID, s.now())
}

// Received lists the requests to a that have not expired or been declined.
func (s *FriendService) Received(a *account.Account) ([]*friend.Request, error) {
	if a.Kind != account.Adult || a.Email == "" {
		return nil, nil
	}
	return s.repo.Received(a.ID, a.Email, s.now())
}

// received finds one of the requests a lists as received, else
// db.ErrNotFound.
func received(repo *friend.Repo, a *account.Account, id uid.ID, now time.Time) (*friend.Request, error) {
	if a.Kind != account.Adult || a.Email == "" {
		return nil, db.ErrNotFound
	}
	rs, err := repo.Received(a.ID, a.Email, now)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(rs, func(r *friend.Request) bool { return r.ID == id }); i >= 0 {
		return rs[i], nil
	}
	return nil, db.ErrNotFound
}

// Accept makes to friends with the sender of a request it received, and
// deletes the requests between the two.
func (s *FriendService) Accept(to *account.Account, id uid.ID) error {
	now := s.now()
	return db.InTx(s.d, func(d db.DBTX) error {
		repo := friend.New(d)
		r, err := received(repo, to, id, now)
		if err != nil {
			return err
		}
		from, err := account.New(d).Get(r.FromID)
		if err != nil {
			return err
		}
		if from.Pending() || from.RevokedAt != nil {
			return ErrNotBefriendable
		}
		if err := repo.Befriend(to.ID, from.ID, now); err != nil {
			return err
		}
		return repo.DeleteBetween(to.ID, to.Email, from.ID, from.Email)
	})
}

// Delete withdraws a request by its sender, or declines one by an adult
// who received it.
func (s *FriendService) Delete(by *account.Account, id uid.ID) error {
	now := s.now()
	return db.InTx(s.d, func(d db.DBTX) error {
		repo := friend.New(d)
		if r, err := repo.Get(id); err == nil && r.FromID == by.ID {
			return repo.Delete(id)
		} else if err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
		if _, err := received(repo, by, id, now); err != nil {
			return err
		}
		return repo.Decline(by.ID, id, now)
	})
}

// Expire deletes the requests that expired.
func (s *FriendService) Expire() (int64, error) { return s.repo.Expire(s.now()) }

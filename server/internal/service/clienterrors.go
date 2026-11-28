package service

import (
	"slices"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// A device reports at most ClientErrorsPerHour entries within
// ClientErrorWindow, each sent counting, a repeat too.
const (
	ClientErrorsPerHour = 100
	ClientErrorWindow   = time.Hour
)

// ClientError is one error a client app reported, as it sent it.
type ClientError struct {
	ID                       uid.ID
	At                       time.Time
	Message, Context, Detail string
}

// ClientErrorService stores the errors client apps report into the server's
// log, beside its own entries.
type ClientErrorService struct {
	repo *logstore.Repo
	// Now is the clock for the reporting limit and for an at ahead of it;
	// tests replace it.
	Now func() time.Time
	// mu guards sent, by device the entries within ClientErrorWindow,
	// oldest first. In memory: a restart clears it.
	mu   sync.Mutex
	sent map[uid.ID][]time.Time
}

// NewClientErrorService returns a ClientErrorService over repo.
func NewClientErrorService(repo *logstore.Repo) *ClientErrorService {
	return &ClientErrorService{repo: repo, Now: time.Now, sent: map[uid.ID][]time.Time{}}
}

// limit counts n entries from a device, or refuses them all with
// *RateLimitedError.
func (s *ClientErrorService) limit(id uid.ID, n int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := slices.DeleteFunc(s.sent[id], func(t time.Time) bool { return now.Sub(t) >= ClientErrorWindow })
	if over := len(ts) + n - ClientErrorsPerHour; over > 0 {
		s.sent[id] = ts
		// A batch holds fewer entries than the limit, so the over oldest
		// counted are there, and n fit once they have left the window.
		return &RateLimitedError{ts[over-1].Add(ClientErrorWindow).Sub(now)}
	}
	for range n {
		ts = append(ts, now)
	}
	s.sent[id] = ts
	return nil
}

// Report stores es as reported from d acting as profile, by an app of
// version app on os; an entry whose id is stored already is skipped.
func (s *ClientErrorService) Report(d *device.Device, profile uid.ID, app, os string, es []ClientError) error {
	now := s.Now().UTC().Truncate(time.Millisecond)
	if err := s.limit(d.ID, len(es), now); err != nil {
		return err
	}
	for _, x := range es {
		at := x.At.UTC()
		if at.After(now) {
			at = now
		}
		err := s.repo.InsertClient(&logstore.Entry{ID: x.ID, At: at, Level: "error", Source: logstore.ClientSource,
			Message: logstore.Untrusted(x.Message, false), Client: &logstore.Client{Platform: d.Platform,
				AppVersion: logstore.Untrusted(app, false), OSVersion: logstore.Untrusted(os, false),
				Context: logstore.Untrusted(x.Context, false), Detail: logstore.Untrusted(x.Detail, true), DeviceID: &d.ID, ProfileID: &profile}})
		if err != nil {
			return err
		}
	}
	return nil
}

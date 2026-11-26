package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/together"
)

const (
	// Present is how recently a member must have polled to count as there;
	// it outlasts a held poll.
	Present = 30 * time.Second
	// QuietEnd ends a Watch together none of whose members polled for it.
	QuietEnd = time.Minute
	// Drift is how far apart two positions may be and still be in sync.
	Drift = 500 * time.Millisecond
	// Lead is the most time between an action and the instant it takes
	// effect, and what a member that reports nothing needs to hear of it
	// first.
	Lead = time.Second
	// MinLead is the least, however quick the members say their links are.
	MinLead = 300 * time.Millisecond
	// LinkMargin is added to a member's round trip for the jitter of the
	// next one.
	LinkMargin = 200 * time.Millisecond
	// Hold is the longest a poll waits for a change.
	Hold = 25 * time.Second
)

var (
	// ErrTogetherConflict wraps every refusal answered as a conflict: joining
	// one that is full, ended, past its window or one's own, and changing the
	// play state of one that is not joined or has ended.
	ErrTogetherConflict = errors.New("watch together")
	// ErrNotMember refuses a household profile changing one they are not in.
	ErrNotMember = errors.New("not in this watch together")
)

func conflict(why string) error { return fmt.Errorf("%w: %s", ErrTogetherConflict, why) }

// WatchTogetherService runs Watch together: opening, joining and the shared
// play state two profiles follow. Every change happens under one lock, so two
// members acting at once are applied one after the other. Every member may
// see the title: to an audience that may not, a Watch together is not found.
// Beyond its members, it is found by the profiles of its household and by
// those who see its host, as account.Repo.Sees has it.
type WatchTogetherService struct {
	repo     *together.Repo
	items    *item.Repo
	accounts *account.Repo
	mu       sync.Mutex
	// changed holds, per Watch together, a channel closed on its next change.
	changed map[int64]chan struct{}
	// needs holds, per Watch together and device, what the device's latest
	// poll said it needs to hear of an action and act on it; a device with
	// none is taken to need Lead.
	needs map[int64]map[int64]time.Duration
	// Now is the clock for the window, presence and the shared position;
	// tests replace it.
	Now func() time.Time
	// Hold is how long Wait waits; tests shorten it.
	Hold time.Duration
	// Events hears of each one opened, joined and ended; play, pause and
	// seek are the held poll's to tell.
	Events *live.Hub
}

func (s *WatchTogetherService) announce(v Shared, change string, sessionID int64) {
	s.Events.Publish(live.Event{Kind: live.WatchTogether, Change: change, HouseholdID: v.W.HouseholdID, OpenerID: v.host(), ItemID: v.W.ItemID,
		WatchTogetherID: v.W.ID, SessionID: sessionID})
}

// NewWatchTogetherService returns a WatchTogetherService over repo, whose
// titles are in items and profiles in accounts.
func NewWatchTogetherService(repo *together.Repo, items *item.Repo, accounts *account.Repo) *WatchTogetherService {
	return &WatchTogetherService{repo: repo, items: items, accounts: accounts, changed: map[int64]chan struct{}{},
		needs: map[int64]map[int64]time.Duration{}, Now: time.Now, Hold: Hold}
}

// Where is the device a profile acts on: its household, and whether it is
// a TV, on which only links set for TVs hold.
type Where struct {
	HouseholdID int64
	TV          bool
}

// finds reports whether a profile on a device at where finds v.
func (s *WatchTogetherService) finds(where Where, accountID int64, v Shared) (bool, error) {
	switch {
	case v.W.HouseholdID == where.HouseholdID, v.Member(accountID) != nil:
		return true, nil
	case v.host() == 0:
		return false, nil
	}
	return s.accounts.Sees(&account.Account{ID: accountID}, v.host(), where.TV)
}

// save writes w and wakes everyone waiting on it. Callers hold mu.
func (s *WatchTogetherService) save(w *together.Together) error {
	if ch, ok := s.changed[w.ID]; ok {
		close(ch)
		delete(s.changed, w.ID)
	}
	return s.repo.Update(w)
}

// Report records what the device's poll or action says it needs, as the
// round trip it measured and the least its player takes to start on time;
// both nil, or no round trip, is nothing, which needs Lead. Called with
// every poll and action, so a client that stops reporting stops being
// believed. Values are clamped to Lead, the most that is ever waited. Only
// a member device of one still open is believed, so a stranger's call
// stores nothing.
func (s *WatchTogetherService) Report(id, deviceID int64, rttMs, floorMs *int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.repo.Get(id)
	if err != nil || w.EndedAt != nil {
		return
	}
	if ms, err := s.repo.Members(id); err != nil || !slices.ContainsFunc(ms, func(m *together.Member) bool { return m.DeviceID == deviceID }) {
		return
	}
	need := Lead
	if rttMs != nil {
		need = min(Lead, max(time.Duration(max(0, *rttMs))*time.Millisecond+LinkMargin, MinLead))
		if floorMs != nil {
			need = min(Lead, max(need, time.Duration(max(0, *floorMs))*time.Millisecond))
		}
	}
	if s.needs[id] == nil {
		s.needs[id] = map[int64]time.Duration{}
	}
	s.needs[id][deviceID] = need
}

// lead is how far ahead an action takes effect: what the slowest present
// member needs. Callers hold mu.
func (s *WatchTogetherService) lead(v Shared) time.Duration {
	lead := MinLead
	for _, m := range v.Members {
		if !v.Present(m) {
			continue
		}
		need, ok := s.needs[v.W.ID][m.DeviceID]
		if !ok {
			need = Lead
		}
		lead = max(lead, need)
	}
	return lead
}

// next is closed on w's next change. Callers hold mu.
func (s *WatchTogetherService) next(id int64) <-chan struct{} {
	ch, ok := s.changed[id]
	if !ok {
		ch = make(chan struct{})
		s.changed[id] = ch
	}
	return ch
}

// Shared is a Watch together as of At, with its members, host first.
type Shared struct {
	W       *together.Together
	Members []*together.Member
	At      time.Time
}

// Status is waiting, joined or ended.
func (v Shared) Status() string {
	switch {
	case v.W.EndedAt != nil:
		return "ended"
	case v.Guest() != nil:
		return "joined"
	}
	return "waiting"
}

// host is the account that opened it, 0 if unknown.
func (v Shared) host() int64 {
	if len(v.Members) == 0 {
		return 0
	}
	return v.Members[0].AccountID
}

// Guest is the profile that joined, if one has.
func (v Shared) Guest() *together.Member {
	return v.member(func(m *together.Member) bool { return m.Role == together.Guest })
}

// Member is the given profile's membership, if any.
func (v Shared) Member(accountID int64) *together.Member {
	return v.member(func(m *together.Member) bool { return m.AccountID == accountID })
}

func (v Shared) member(match func(*together.Member) bool) *together.Member {
	for _, m := range v.Members {
		if match(m) {
			return m
		}
	}
	return nil
}

// PositionMs is the shared position at At, or where it stood when it ended.
func (v Shared) PositionMs() int64 {
	if v.W.EndedAt != nil && v.W.EndedAt.Before(v.At) {
		return v.W.SharedAt(*v.W.EndedAt)
	}
	return v.W.SharedAt(v.At)
}

// JoinLeft is what is left of the window to join while waiting.
func (v Shared) JoinLeft() time.Duration {
	if v.Status() != "waiting" || !v.At.Before(v.W.JoinBy) {
		return 0
	}
	return v.W.JoinBy.Sub(v.At)
}

// Present reports a member polled within Present of At.
func (v Shared) Present(m *together.Member) bool { return v.At.Sub(m.LastSeenAt) < Present }

func (s *WatchTogetherService) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// load reads one Watch together the profile finds on a device at where and
// ends it if its window passed unjoined or its members went quiet; one it
// does not find is not found, nor is one on a title the audience may not
// see, which ends if that audience is a member of it.
func (s *WatchTogetherService) load(where Where, accountID, id int64, a item.Audience, now time.Time) (Shared, error) {
	w, err := s.repo.Get(id)
	if err != nil {
		return Shared{}, err
	}
	ms, err := s.repo.Members(w.ID)
	if err != nil {
		return Shared{}, err
	}
	if ok, err := s.finds(where, accountID, Shared{w, ms, now}); err != nil || !ok {
		return Shared{}, cmp.Or(err, db.ErrNotFound)
	}
	v, err := s.settle(w, now)
	if err != nil {
		return v, err
	}
	if ok, err := s.items.Visible(w.ItemID, a); err != nil || ok {
		return v, err
	}
	if v.Member(a.AccountID) != nil && w.EndedAt == nil {
		if err := s.end(v, now); err != nil {
			return Shared{}, err
		}
	}
	return Shared{}, db.ErrNotFound
}

func (s *WatchTogetherService) settle(w *together.Together, now time.Time) (Shared, error) {
	ms, err := s.repo.Members(w.ID)
	v := Shared{w, ms, now}
	if err != nil || w.EndedAt != nil {
		return v, err
	}
	var end time.Time
	if v.Guest() == nil && !now.Before(w.JoinBy) {
		end = w.JoinBy
	}
	last := w.OpenedAt
	for _, m := range ms {
		if m.LastSeenAt.After(last) {
			last = m.LastSeenAt
		}
	}
	if quiet := last.Add(QuietEnd); !now.Before(quiet) && (end.IsZero() || quiet.Before(end)) {
		end = quiet
	}
	if end.IsZero() {
		return v, nil
	}
	return v, s.end(v, end)
}

// end stops v at at; its play state stays as it was, so the member left
// plays on alone.
func (s *WatchTogetherService) end(v Shared, at time.Time) error {
	w := v.W
	w.EndedAt = &at
	w.Version++
	delete(s.needs, w.ID)
	if err := s.save(w); err != nil {
		return err
	}
	s.announce(v, live.Ended, 0)
	return nil
}

// leaveAll ends every open Watch together in list but keep.
func (s *WatchTogetherService) leaveAll(list []*together.Together, err error, keep func(*together.Together) bool, now time.Time) error {
	if err != nil {
		return err
	}
	for _, w := range list {
		if keep(w) {
			continue
		}
		if v, err := s.settle(w, now); err != nil {
			return err
		} else if v.W.EndedAt == nil {
			if err := s.end(v, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Open starts a Watch together on an item with host as its host, paused at
// positionMs, ending any other the host is in. fromSessionID, when not 0,
// is the playback session whose device is asked to join it at once.
func (s *WatchTogetherService) Open(householdID, itemID int64, host together.Member, positionMs, fromSessionID int64) (Shared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	list, err := s.repo.ForAccount(host.AccountID)
	if err := s.leaveAll(list, err, func(*together.Together) bool { return false }, now); err != nil {
		return Shared{}, err
	}
	w := &together.Together{HouseholdID: householdID, ItemID: itemID, PositionMs: positionMs, PositionAt: now, OpenedAt: now,
		JoinBy: now.Add(together.JoinWindow)}
	host.Role, host.JoinedAt, host.LastSeenAt = together.Host, now, now
	if err := s.repo.Create(w, &host); err != nil {
		return Shared{}, err
	}
	v := Shared{w, []*together.Member{&host}, now}
	s.announce(v, live.Opened, fromSessionID)
	return v, nil
}

// List is the open Watch togethers the profile finds on a device at where,
// on an item (0 for any), that are waiting or that the profile is in,
// newest first, on the titles the audience may see.
func (s *WatchTogetherService) List(where Where, accountID, itemID int64, a item.Audience) ([]Shared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	list, err := s.repo.Open(0, itemID)
	if err != nil {
		return nil, err
	}
	var out []Shared
	for _, w := range list {
		ms, err := s.repo.Members(w.ID)
		if err != nil {
			return nil, err
		}
		if ok, err := s.finds(where, accountID, Shared{w, ms, now}); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		v, err := s.settle(w, now)
		if err != nil {
			return nil, err
		}
		if v.Status() != "waiting" && (v.Status() != "joined" || v.Member(accountID) == nil) {
			continue
		}
		if ok, err := s.items.Visible(w.ItemID, a); err != nil {
			return nil, err
		} else if ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// Poll reads one Watch together, marking the profile present if a member.
func (s *WatchTogetherService) Poll(where Where, accountID, id int64, a item.Audience) (Shared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.poll(where, accountID, id, a)
}

func (s *WatchTogetherService) poll(where Where, accountID, id int64, a item.Audience) (Shared, error) {
	now := s.now()
	v, err := s.load(where, accountID, id, a, now)
	if m := v.Member(accountID); err == nil && m != nil && v.W.EndedAt == nil {
		m.LastSeenAt = now
		err = s.repo.Touch(id, accountID, now)
	}
	return v, err
}

// Wait is Poll held until the version passes after, it ends, or Hold runs
// out, whichever is first; the answer is as of when it returns.
func (s *WatchTogetherService) Wait(ctx context.Context, where Where, accountID, id int64, after int, a item.Audience) (Shared, error) {
	timeout := time.NewTimer(s.Hold)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		v, err := s.poll(where, accountID, id, a)
		if err != nil || v.W.Version > after || v.W.EndedAt != nil {
			s.mu.Unlock()
			return v, err
		}
		next := s.next(id)
		s.mu.Unlock()
		select {
		case <-next:
		case <-timeout.C:
			return s.Poll(where, accountID, id, a)
		case <-ctx.Done():
			return v, ctx.Err()
		}
	}
}

// Join makes guest the second member, ending any other the guest is in.
func (s *WatchTogetherService) Join(where Where, id int64, guest together.Member, a item.Audience) (Shared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	v, err := s.load(where, guest.AccountID, id, a, now)
	switch {
	case err != nil:
		return v, err
	// The host's other devices may join: one profile on two is both members.
	// shortcut: presence and who paused stay per profile, so such a pair shares them; key them by device if that shows.
	case slices.ContainsFunc(v.Members, func(m *together.Member) bool { return m.DeviceID == guest.DeviceID }):
		return v, conflict("the host cannot join their own")
	case v.Status() == "ended":
		return v, conflict("it has ended")
	case v.Status() == "joined":
		return v, conflict("someone has already joined")
	}
	list, err := s.repo.ForAccount(guest.AccountID)
	if err := s.leaveAll(list, err, func(w *together.Together) bool { return w.ID == id }, now); err != nil {
		return v, err
	}
	guest.WatchTogetherID, guest.Role, guest.JoinedAt, guest.LastSeenAt = id, together.Guest, now, now
	if err := s.repo.AddMember(&guest); err != nil {
		if errors.Is(err, db.ErrConflict) {
			return v, conflict("someone has already joined")
		}
		return v, err
	}
	v.Members = append(v.Members, &guest)
	v.W.Version++
	if err := s.save(v.W); err != nil {
		return v, err
	}
	s.announce(v, live.Joined, 0)
	return v, nil
}

// SetState applies a member's play, pause or seek to both at one instant, a
// lead away. A seek while playing lands where the seeker will be by then; a
// play or pause holds the sender's position. A change that matches the
// shared state within Drift, as when both press pause, is the same state
// and changes nothing, so the first pause keeps who paused.
func (s *WatchTogetherService) SetState(where Where, accountID, id int64, playing bool, positionMs int64, a item.Audience) (Shared, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	v, err := s.load(where, accountID, id, a, now)
	switch {
	case err != nil:
		return v, err
	case v.Member(accountID) == nil:
		return v, ErrNotMember
	case v.Status() == "ended":
		return v, conflict("it has ended")
	case v.Status() == "waiting":
		return v, conflict("no one has joined yet")
	}
	v.Member(accountID).LastSeenAt = now
	if err := s.repo.Touch(id, accountID, now); err != nil {
		return v, err
	}
	w := v.W
	if off := w.SharedAt(now) - positionMs; playing == w.Playing && max(off, -off) <= Drift.Milliseconds() {
		return v, nil
	}
	switch {
	case playing:
		w.PausedBy = nil
	case w.Playing:
		w.PausedBy = &accountID
	}
	at := now.Add(s.lead(v))
	if w.Playing && playing {
		positionMs += at.Sub(now).Milliseconds()
	}
	w.Playing, w.PositionMs, w.PositionAt = playing, positionMs, at
	w.Version++
	return v, s.save(w)
}

// Leave ends a Watch together for both; leaving one that has ended is done.
func (s *WatchTogetherService) Leave(where Where, accountID, id int64, a item.Audience) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	v, err := s.load(where, accountID, id, a, now)
	switch {
	case err != nil:
		return err
	case v.Member(accountID) == nil:
		return ErrNotMember
	case v.W.EndedAt != nil:
		return nil
	}
	return s.end(v, now)
}

// LeaveDevice ends the open Watch togethers with a member on the device,
// except those on keepItemID (0 keeps none): a device that plays another
// title, or signs out, has left.
func (s *WatchTogetherService) LeaveDevice(deviceID, keepItemID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.repo.ForDevice(deviceID)
	return s.leaveAll(list, err, func(w *together.Together) bool { return w.ItemID == keepItemID }, s.now())
}

// LeaveAccount ends the open Watch togethers a profile is in: a revoked or
// deleted account has left on every device.
func (s *WatchTogetherService) LeaveAccount(accountID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.repo.ForAccount(accountID)
	return s.leaveAll(list, err, func(*together.Together) bool { return false }, s.now())
}

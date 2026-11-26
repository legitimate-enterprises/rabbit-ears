package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
)

// What a station is doing now.
const (
	OnAir           = "on_air"
	BetweenPrograms = "between_programs"
	OffSchedule     = "off_schedule"
)

// ErrProgramHidden means the program on now fails the profile's kid filter.
var ErrProgramHidden = errors.New("the program on now is not for this profile")

// OffAirError means nothing is on to tune in to. Resumes is when the next
// program starts, nil when none is scheduled.
type OffAirError struct{ Resumes *time.Time }

func (e *OffAirError) Error() string { return "nothing is on this station now" }

// upNextLength is how many programs an Airing lists after the one on.
const upNextLength = 10

// Airing is what a station is doing now: the program on and how far into
// it, else whether it is between programs or off schedule, and what
// follows in the order it plays, Next being the first of UpNext.
type Airing struct {
	Status     string
	Now, Next  *station.Program
	UpNext     []*station.Program
	PositionMs int64
}

// StationService schedules stations and answers what is on. A schedule is
// rows only: nothing is probed, remuxed or transcoded for it.
type StationService struct {
	repo      *station.Repo
	playlists *playlist.Repo
	items     *item.Repo
	// Now is the clock; its location is the one hours, half hours, days and
	// time ranges are read in, the server's local time.
	Now func() time.Time
	// Shuffle orders a randomized loop; nil shuffles at random.
	Shuffle func([]station.Key)
	// mu is held across every schedule, and the Unstart before one, so no
	// two runs extend a station from the same last program, and across an
	// Update, so no run writes back a loop the Update cleared.
	mu sync.Mutex
	// Events hears of every change to a station and to what it has on.
	Events *live.Hub
}

// announce tells those st is shared with that it, its schedule or what it
// has on changed.
func (s *StationService) announce(st *station.Station) {
	e := live.Event{Kind: live.Station, StationID: st.ID}
	switch st.Visibility {
	case station.Private:
		e.AccountIDs = []int64{st.AccountID}
	case station.Household:
		e.HouseholdID = st.HouseholdID
	}
	s.Events.Publish(e)
}

// Announce tells those who see st that it changed.
func (s *StationService) Announce(st *station.Station) { s.announce(st) }

// Aired announces each station on which a program started or ended after
// from and by to.
func (s *StationService) Aired(from, to time.Time) error {
	ids, err := s.repo.Changing(from, to)
	if err != nil {
		return err
	}
	for _, id := range ids {
		st, err := s.repo.Get(id)
		if errors.Is(err, db.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		s.announce(st)
	}
	return nil
}

// Delete removes st with its schedule and the playlist it owns.
func (s *StationService) Delete(st *station.Station) error {
	if err := s.repo.Delete(st.ID); err != nil {
		return err
	}
	s.announce(st)
	return nil
}

// SetVisibility shares st as visibility says, and tells those who saw it
// before as well as those who see it now.
func (s *StationService) SetVisibility(st *station.Station, visibility string) error {
	if err := s.repo.SetVisibility(st.ID, visibility); err != nil {
		return err
	}
	s.announce(st)
	st.Visibility = visibility
	s.announce(st)
	return nil
}

// NewStationService returns a StationService over its repositories.
func NewStationService(repo *station.Repo, playlists *playlist.Repo, items *item.Repo) *StationService {
	return &StationService{repo: repo, playlists: playlists, items: items, Now: time.Now}
}

// Repo exposes the repository.
func (s *StationService) Repo() *station.Repo { return s.repo }

// Create makes st and schedules it at once. A station without a playlist
// gets one of its own, of st.Kind, owned by its owner; one without a
// timing takes its kind's.
func (s *StationService) Create(st *station.Station) error {
	if st.Timing == "" {
		kind := st.Kind
		if st.PlaylistID != 0 {
			p, err := s.playlists.Get(st.PlaylistID)
			if err != nil {
				return err
			}
			kind = p.Kind
		}
		st.Timing = station.DefaultTiming(kind)
	}
	if st.PlaylistID == 0 {
		p := &playlist.Playlist{AccountID: st.AccountID, Name: st.Name, Kind: st.Kind, StationOwned: true}
		if err := s.playlists.Create(p); err != nil {
			return err
		}
		st.PlaylistID = p.ID
		if err := s.repo.Create(st); err != nil {
			return errors.Join(err, s.playlists.Delete(p.ID))
		}
	} else if err := s.repo.Create(st); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.schedule(st); err != nil {
		return err
	}
	s.announce(st)
	return nil
}

// Update writes st. What is scheduled stays; a new playlist or order starts
// a new loop where the schedule next extends.
func (s *StationService) Update(st *station.Station) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.repo.Get(st.ID)
	if err != nil {
		return err
	}
	if err := s.repo.Update(st); err != nil {
		return err
	}
	if old.PlaylistID != st.PlaylistID || old.Random != st.Random {
		if err := s.repo.ClearLoop(st.ID); err != nil {
			return err
		}
	}
	if old.PlaylistID != st.PlaylistID {
		if err := s.dropOwned(old.PlaylistID); err != nil {
			return err
		}
	}
	got, err := s.repo.Get(st.ID)
	if err != nil {
		return err
	}
	*st = *got
	// Those it was shared with before hear of it too, to drop it.
	if old.Visibility != st.Visibility {
		s.announce(old)
	}
	s.announce(st)
	return nil
}

// dropOwned deletes a playlist a station owned once no station plays it.
func (s *StationService) dropOwned(playlistID int64) error {
	p, err := s.playlists.Get(playlistID)
	if err != nil || !p.StationOwned {
		return err
	}
	return s.playlists.Delete(p.ID)
}

// Seen returns one station the viewer may see, else db.ErrNotFound: shared
// with them and, for a kid, of a play order the kid may see all of; for an
// adult hiding kids' content, of one with something besides kids' content.
func (s *StationService) Seen(id int64, v station.Viewer) (*station.Station, error) {
	st, err := s.repo.Seen(id, v)
	if err != nil {
		return nil, err
	}
	ok, err := s.allows(st, v.Audience)
	if err == nil && !ok {
		err = db.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

// List pages by name the stations the viewer may see, of one kind or any
// (""), as Seen decides, and counts them all.
func (s *StationService) List(v station.Viewer, kind string, limit, offset int) ([]*station.Station, int, error) {
	if !v.Audience.Kid && !v.Audience.HideKids {
		out, err := s.repo.List(v, kind, limit, offset)
		if err != nil {
			return nil, 0, err
		}
		total, err := s.repo.Count(v, kind)
		return out, total, err
	}
	out, err := s.kidList(v, kind)
	if err != nil {
		return nil, 0, err
	}
	total := len(out)
	out = out[min(offset, len(out)):]
	return out[:min(limit, len(out))], total, nil
}

// kidList is every station of the kind a kid viewer may see, which List
// pages and counts.
func (s *StationService) kidList(v station.Viewer, kind string) ([]*station.Station, error) {
	all, err := s.repo.List(v, kind, -1, 0)
	if err != nil {
		return nil, err
	}
	var out []*station.Station
	for _, st := range all {
		ok, err := s.allows(st, v.Audience)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *StationService) allows(st *station.Station, a item.Audience) (bool, error) {
	if !a.Kid && !a.HideKids {
		return true, nil
	}
	return s.playlists.Allows(st.PlaylistID, a)
}

// Reschedule lets what is on play to its end and rebuilds the schedule
// from there, with the rating.
func (s *StationService) Reschedule(st *station.Station) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repo.Unstart(st.ID, s.Now()); err != nil {
		return err
	}
	if _, err := s.schedule(st); err != nil {
		return err
	}
	s.announce(st)
	return nil
}

// ScheduleAll is the hourly task: it extends every station to a day ahead
// and recalculates its rating, then drops programs that ended before
// yesterday. Pruning last keeps each station's loop cursor through a long gap.
func (s *StationService) ScheduleAll(ctx context.Context) (string, error) {
	all, err := s.repo.All(-1, 0)
	if err != nil {
		return "", err
	}
	var errs []error
	for _, st := range all {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		s.mu.Lock()
		// Read again under the lock, so an Update since All is scheduled.
		cur, err := s.repo.Get(st.ID)
		if err == nil {
			var changed bool
			if changed, err = s.schedule(cur); changed {
				s.announce(cur)
			}
		}
		s.mu.Unlock()
		if errors.Is(err, db.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("station %d: %w", st.ID, err))
		}
	}
	now := s.Now()
	y, m, d := now.Date()
	if err := s.repo.Prune(time.Date(y, m, d-1, 0, 0, 0, 0, now.Location())); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d stations scheduled", len(all)-len(errs)), errors.Join(errs...)
}

// schedule extends st's programs from its last, or from now when that has
// ended, to a day after now or to its next window when that is later, and
// writes its rating. A run with nothing to add writes nothing, and
// reports no change.
func (s *StationService) schedule(st *station.Station) (bool, error) {
	now := s.Now()
	ratings, err := s.playlists.Ratings(st.PlaylistID)
	if err != nil {
		return false, err
	}
	order, err := s.playlists.PlayOrder(st.PlaylistID, item.Audience{}, -1, 0)
	if err != nil {
		return false, err
	}
	rows := make([]station.Row, 0, len(order))
	for _, r := range order {
		v := station.Row{Key: station.Key{EntryID: r.EntryID, ItemID: r.ItemID}, EditionID: r.EditionID}
		if r.RuntimeMs != nil && r.EditionID != 0 {
			v.RuntimeMs = *r.RuntimeMs
		}
		rows = append(rows, v)
	}
	cur, end, err := s.repo.Last(st.ID)
	if err != nil {
		return false, err
	}
	loop, err := s.repo.Loop(st.ID)
	if err != nil {
		return false, err
	}
	from := now
	if end.After(now) {
		from = end.In(now.Location())
	}
	until := now.Add(24 * time.Hour)
	plan := station.Plan{Timing: st.Timing, Schedule: st.Schedule, Random: st.Random, Shuffle: s.Shuffle}
	programs := plan.Fill(rows, loop, cur, from, until)
	// A station off at the end of the day ahead is scheduled to its next
	// window, which a weekly schedule opens within the week, so it can say
	// when it is back. Once it is, from is past until and nothing is added.
	off := from.Before(until) && !st.Schedule.Open(until)
	for days := 0; off && len(programs) == 0 && days < 7; days++ {
		until = until.AddDate(0, 0, 1)
		programs = plan.Fill(rows, loop, cur, from, until)
	}
	if len(programs) > 0 {
		if err := s.repo.Write(st.ID, programs, loop); err != nil {
			return false, err
		}
	}
	rating := station.Rating(st.Kind, ratings)
	if rating == st.Rating {
		return len(programs) > 0, nil
	}
	st.Rating = rating
	return true, s.repo.SetRating(st.ID, rating)
}

func found(p *station.Program, err error) (*station.Program, error) {
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return p, err
}

// OnAir is what st is doing now as the audience sees it: a program it may
// not see is left out, as if the station were between programs then.
func (s *StationService) OnAir(st *station.Station, a item.Audience) (Airing, error) {
	now := s.Now()
	var out Airing
	var err error
	if out.Now, err = found(s.repo.On(st.ID, now, a)); err != nil {
		return out, err
	}
	if out.UpNext, err = s.repo.Upcoming(st.ID, now, a, upNextLength); err != nil {
		return out, err
	}
	if len(out.UpNext) > 0 {
		out.Next = out.UpNext[0]
	}
	switch {
	case out.Now != nil:
		out.Status, out.PositionMs = OnAir, now.Sub(out.Now.Start).Milliseconds()
	case st.Schedule.Open(now):
		out.Status = BetweenPrograms
	default:
		out.Status = OffSchedule
	}
	return out, nil
}

// Tune is the program on st now and the live position in it, for a viewer
// of the audience: an OffAirError when nothing is on, ErrProgramHidden when
// the program fails the audience's kid filter.
func (s *StationService) Tune(st *station.Station, a item.Audience) (*station.Program, int64, error) {
	now := s.Now()
	p, err := found(s.repo.On(st.ID, now, item.Audience{}))
	if err != nil {
		return nil, 0, err
	}
	if p == nil {
		next, err := s.repo.Upcoming(st.ID, now, a, 1)
		if err != nil {
			return nil, 0, err
		}
		off := &OffAirError{}
		if len(next) > 0 {
			off.Resumes = &next[0].Start
		}
		return nil, 0, off
	}
	ok, err := s.items.Visible(p.ItemID, a)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, ErrProgramHidden
	}
	return p, now.Sub(p.Start).Milliseconds(), nil
}

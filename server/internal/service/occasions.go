package service

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
)

// OccasionService manages occasions and finds those in season.
type OccasionService struct {
	repo  *occasion.Repo
	items *item.Repo
	// Pick returns an index in [0, n) choosing an occasion's headline.
	Pick func(n int) int
}

// NewOccasionService returns an OccasionService over its repositories.
func NewOccasionService(repo *occasion.Repo, items *item.Repo) *OccasionService {
	return &OccasionService{repo, items, rand.IntN}
}

// Repo exposes the repository.
func (s *OccasionService) Repo() *occasion.Repo { return s.repo }

// ActiveOccasion is an occasion in season, the window it is in, and one of
// its phrases, empty when it has none.
type ActiveOccasion struct {
	*occasion.Occasion
	occasion.Activation
	Headline string
}

// Active lists the occasions in season on the calendar day d, given at
// midnight UTC, that have a member the audience sees, nearest their
// anchor first, then by name. A nonzero itemID keeps those with that item
// among the members the audience sees.
func (s *OccasionService) Active(d time.Time, a item.Audience, itemID int64) ([]ActiveOccasion, error) {
	all, err := s.repo.List(-1, 0)
	if err != nil {
		return nil, err
	}
	out := []ActiveOccasion{}
	for _, o := range all {
		w, ok := o.On(d)
		if !ok {
			continue
		}
		seen, err := s.items.List(item.Filter{OccasionID: o.ID, ID: itemID, Audience: a, Catalog: true, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(seen) == 0 {
			continue
		}
		v := ActiveOccasion{o, w, ""}
		if len(o.Phrases) > 0 {
			v.Headline = o.Phrases[s.Pick(len(o.Phrases))]
		}
		out = append(out, v)
	}
	// List orders by name, which a stable sort keeps among equal distances.
	slices.SortStableFunc(out, func(x, y ActiveOccasion) int { return cmp.Compare(x.Distance, y.Distance) })
	return out, nil
}

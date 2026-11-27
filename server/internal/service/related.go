package service

import (
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// RelatedService answers what relates to a title: a movie's collections,
// and the titles before and after it.
type RelatedService struct {
	items       *item.Repo
	collections *collection.Repo
}

// NewRelatedService returns a RelatedService over its repositories.
func NewRelatedService(items *item.Repo, collections *collection.Repo) *RelatedService {
	return &RelatedService{items, collections}
}

// relatedLimit caps each RelatedCollection's movies.
const relatedLimit = 10

// Related is what relates to one title for one reader, never the title
// itself.
type Related struct {
	// Collections are a movie's collections the reader sees, its TMDB
	// collection first, then custom ones by name, each with, as
	// collection.Repo.Around picks them, its relatedLimit other movies
	// nearest the movie; one with none is left out.
	Collections []RelatedCollection
	// Previous and Next are a movie's neighbors in its TMDB collection or
	// an episode's in its show's play order; nil for none.
	Previous, Next *item.Item
}

// RelatedCollection is one of Related.Collections.
type RelatedCollection struct {
	*collection.View
	Movies []*item.Item
}

// Related answers for household and audience what relates to it; a kind
// other than movie or episode has nothing.
func (s *RelatedService) Related(householdID uid.ID, it *item.Item, a item.Audience) (*Related, error) {
	out := &Related{}
	var prev, next *uid.ID
	var err error
	switch it.Kind {
	case item.Movie:
		if prev, next, err = s.collections.MovieNeighbors(it.ID, a); err == nil {
			err = s.movies(out, householdID, it.ID, a)
		}
	case item.Episode:
		prev, next, err = s.items.EpisodeNeighbors(it.ID, a)
	}
	if err == nil {
		out.Previous, err = s.get(prev)
	}
	if err == nil {
		out.Next, err = s.get(next)
	}
	return out, err
}

// movies fills out's Collections for movie id.
func (s *RelatedService) movies(out *Related, householdID, id uid.ID, a item.Audience) error {
	vs, err := s.collections.ForMovie(householdID, id, a)
	if err != nil {
		return err
	}
	for _, tmdb := range []bool{true, false} {
		for _, v := range vs {
			if (v.Source == collection.TMDB) != tmdb {
				continue
			}
			ids, err := s.collections.Around(v.ID, id, a, relatedLimit)
			if err != nil {
				return err
			}
			c := RelatedCollection{View: v}
			for _, m := range ids {
				it, err := s.items.Get(m)
				if err != nil {
					return err
				}
				c.Movies = append(c.Movies, it)
			}
			if len(c.Movies) > 0 {
				out.Collections = append(out.Collections, c)
			}
		}
	}
	return nil
}

// get is item id, nil for none.
func (s *RelatedService) get(id *uid.ID) (*item.Item, error) {
	if id == nil {
		return nil, nil
	}
	return s.items.Get(*id)
}

package service

import (
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

var (
	// ErrCollectionReadOnly means a TMDB collection is changed only by scans.
	ErrCollectionReadOnly = errors.New("a TMDB collection cannot be changed")
	// ErrNotAMovie means a collection holds movies only.
	ErrNotAMovie = errors.New("a collection holds movies only")
	// ErrSmartCollection means a smart collection's rules choose its movies.
	ErrSmartCollection = errors.New("a smart collection's movies follow its rules")
)

// CollectionService manages TMDB and custom movie collections.
type CollectionService struct {
	repo  *collection.Repo
	items *ItemService
}

// NewCollectionService returns a CollectionService over repo, drawing
// artwork through items.
func NewCollectionService(repo *collection.Repo, items *ItemService) *CollectionService {
	return &CollectionService{repo, items}
}

// Repo exposes the repository.
func (s *CollectionService) Repo() *collection.Repo { return s.repo }

func writable(c *collection.Collection) error {
	if c.Source != collection.Custom {
		return ErrCollectionReadOnly
	}
	return nil
}

// movable is writable for a change to the collection's movies or order.
func movable(c *collection.Collection) error {
	if err := writable(c); err != nil || !c.Smart {
		return err
	}
	return ErrSmartCollection
}

// Rename sets a custom collection's name.
func (s *CollectionService) Rename(c *collection.Collection, name string, by *uid.ID) error {
	if err := writable(c); err != nil {
		return err
	}
	c.Name = name
	return s.repo.Update(c, by)
}

// Delete removes a custom collection.
func (s *CollectionService) Delete(c *collection.Collection) error {
	if err := writable(c); err != nil {
		return err
	}
	return s.repo.Delete(c.ID)
}

// AddMovie puts a movie in a custom collection.
func (s *CollectionService) AddMovie(c *collection.Collection, it *item.Item) error {
	if err := movable(c); err != nil {
		return err
	}
	if it.Kind != item.Movie {
		return ErrNotAMovie
	}
	return s.repo.AddMovie(c.ID, it.ID)
}

// RemoveMovie takes a movie out of a custom collection.
func (s *CollectionService) RemoveMovie(c *collection.Collection, movieID uid.ID) error {
	if err := movable(c); err != nil {
		return err
	}
	return s.repo.RemoveMovie(c.ID, movieID)
}

// MoveMovie moves a movie of a custom collection to position at, nil for
// last; the first move hand-orders the collection.
func (s *CollectionService) MoveMovie(c *collection.Collection, movieID uid.ID, at *int) error {
	if err := movable(c); err != nil {
		return err
	}
	return s.repo.MoveMovie(c.ID, movieID, at)
}

// ResetOrder returns a custom collection to release order.
func (s *CollectionService) ResetOrder(c *collection.Collection) error {
	if err := movable(c); err != nil {
		return err
	}
	return s.repo.ResetOrder(c.ID)
}

// Artwork returns the local file of a collection's poster or backdrop: its
// own TMDB image, else its first movie the audience may see. db.ErrNotFound
// when neither has one.
func (s *CollectionService) Artwork(ctx context.Context, c *collection.Collection, a item.Audience, backdrop bool) (string, error) {
	size, path := "w500", c.PosterPath
	if backdrop {
		size, path = "w1280", c.BackdropPath
	}
	if path != "" && s.items.Images != nil {
		return s.items.Images.CacheImage(ctx, s.items.ImageDir, size, path)
	}
	first, err := s.repo.Movies(c.ID, a, 1, 0)
	if err != nil {
		return "", err
	}
	if len(first) == 0 {
		return "", db.ErrNotFound
	}
	it, err := s.items.repo.Get(first[0])
	if err != nil {
		return "", err
	}
	return s.items.Artwork(ctx, it, a, backdrop)
}

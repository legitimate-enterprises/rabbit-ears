package httpapi

import (
	"context"
	"mime"
	"path/filepath"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
)

// ListGenres pages genres by name; a kid sees only genres of items they may see.
func (s *Server) ListGenres(ctx context.Context, r api.ListGenresRequestObject) (api.ListGenresResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Genres.Repo().List(genre.Filter{LibraryID: or(r.Params.LibraryId, 0), Audience: browse(ctx), Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListGenres200JSONResponse{Items: mapAll(rows, toGenre), NextOffset: next}, nil
}

// CreateGenre adds a genre by hand.
func (s *Server) CreateGenre(_ context.Context, r api.CreateGenreRequestObject) (api.CreateGenreResponseObject, error) {
	g := &genre.Genre{Name: r.Body.Name, TMDBID: r.Body.TmdbId}
	if err := s.svc.Genres.Repo().Create(g); err != nil {
		return nil, err
	}
	return api.CreateGenre201JSONResponse(toGenre(g)), nil
}

// GetGenre returns a genre the active profile may see.
func (s *Server) GetGenre(ctx context.Context, r api.GetGenreRequestObject) (api.GetGenreResponseObject, error) {
	if ok, err := s.svc.Genres.Repo().Seen(r.GenreId, browse(ctx)); err != nil || !ok {
		return nil, orNotFound(err)
	}
	g, err := s.svc.Genres.Repo().Get(r.GenreId)
	if err != nil {
		return nil, err
	}
	return api.GetGenre200JSONResponse(toGenre(g)), nil
}

// ReplaceGenre rewrites a genre.
func (s *Server) ReplaceGenre(_ context.Context, r api.ReplaceGenreRequestObject) (api.ReplaceGenreResponseObject, error) {
	g := &genre.Genre{ID: r.GenreId, Name: r.Body.Name, TMDBID: r.Body.TmdbId}
	if err := s.svc.Genres.Repo().Update(g); err != nil {
		return nil, err
	}
	return api.ReplaceGenre200JSONResponse(toGenre(g)), nil
}

// DeleteGenre removes a genre from the catalog and every item.
func (s *Server) DeleteGenre(_ context.Context, r api.DeleteGenreRequestObject) (api.DeleteGenreResponseObject, error) {
	return api.DeleteGenre204Response{}, s.svc.Genres.Repo().Delete(r.GenreId)
}

// ListPeople pages people by name; a kid sees only people credited on items they may see.
func (s *Server) ListPeople(ctx context.Context, r api.ListPeopleRequestObject) (api.ListPeopleResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.People.Repo().List(person.Filter{Q: or(r.Params.Q, ""), Audience: browse(ctx), Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListPeople200JSONResponse{Items: mapAll(rows, toPerson), NextOffset: next}, nil
}

func personFrom(in *api.PersonInput, id int64) *person.Person {
	return &person.Person{ID: id, Name: in.Name, TMDBID: in.TmdbId, ProfilePath: or(in.ProfilePath, "")}
}

// CreatePerson adds a person by hand.
func (s *Server) CreatePerson(_ context.Context, r api.CreatePersonRequestObject) (api.CreatePersonResponseObject, error) {
	p := personFrom(r.Body, 0)
	if err := s.svc.People.Repo().Create(p); err != nil {
		return nil, err
	}
	return api.CreatePerson201JSONResponse(toPerson(p)), nil
}

// seenPerson returns a person only if the active profile may see them.
func (s *Server) seenPerson(ctx context.Context, id int64) (*person.Person, error) {
	if ok, err := s.svc.People.Repo().Seen(id, browse(ctx)); err != nil || !ok {
		return nil, orNotFound(err)
	}
	return s.svc.People.Repo().Get(id)
}

// GetPerson returns a person the active profile may see.
func (s *Server) GetPerson(ctx context.Context, r api.GetPersonRequestObject) (api.GetPersonResponseObject, error) {
	p, err := s.seenPerson(ctx, r.PersonId)
	if err != nil {
		return nil, err
	}
	return api.GetPerson200JSONResponse(toPerson(p)), nil
}

// ReplacePerson rewrites a person.
func (s *Server) ReplacePerson(_ context.Context, r api.ReplacePersonRequestObject) (api.ReplacePersonResponseObject, error) {
	p := personFrom(r.Body, r.PersonId)
	if err := s.svc.People.Repo().Update(p); err != nil {
		return nil, err
	}
	return api.ReplacePerson200JSONResponse(toPerson(p)), nil
}

// DeletePerson removes a person and their credits.
func (s *Server) DeletePerson(_ context.Context, r api.DeletePersonRequestObject) (api.DeletePersonResponseObject, error) {
	return api.DeletePerson204Response{}, s.svc.People.Repo().Delete(r.PersonId)
}

// GetPersonArtwork serves the headshot of a person the active profile may see.
func (s *Server) GetPersonArtwork(ctx context.Context, r api.GetPersonArtworkRequestObject) (api.GetPersonArtworkResponseObject, error) {
	p, err := s.seenPerson(ctx, r.PersonId)
	if err != nil {
		return nil, err
	}
	path, err := s.svc.People.Artwork(ctx, p)
	if err != nil {
		return nil, err
	}
	f, size, tag, err := openArtwork(path, r.Params.IfNoneMatch)
	if err != nil || f == nil {
		return api.GetPersonArtwork304Response{Headers: api.GetPersonArtwork304ResponseHeaders{CacheControl: artworkCacheFor(r.Params.V), ETag: tag}}, err
	}
	return api.GetPersonArtwork200ImageResponse{Body: f, ContentType: mime.TypeByExtension(filepath.Ext(path)), ContentLength: size,
		Headers: api.GetPersonArtwork200ResponseHeaders{CacheControl: artworkCacheFor(r.Params.V), ETag: tag}}, nil
}

// GetItemCredits returns the cast and key crew of an item the active profile may see.
func (s *Server) GetItemCredits(ctx context.Context, r api.GetItemCreditsRequestObject) (api.GetItemCreditsResponseObject, error) {
	if _, err := s.seen(ctx, r.ItemId); err != nil {
		return nil, err
	}
	cs, err := s.svc.People.Repo().Credits(r.ItemId)
	if err != nil {
		return nil, err
	}
	return api.GetItemCredits200JSONResponse(toCredits(cs)), nil
}

// ReplaceItemCredits sets an item's credits by hand.
func (s *Server) ReplaceItemCredits(_ context.Context, r api.ReplaceItemCreditsRequestObject) (api.ReplaceItemCreditsResponseObject, error) {
	if _, err := s.svc.Items.Repo().Get(r.ItemId); err != nil {
		return nil, err
	}
	var cs []person.Credit
	for _, k := range []struct {
		kind string
		in   []api.CreditInput
	}{{person.Cast, r.Body.Cast}, {person.Crew, r.Body.Crew}} {
		for _, c := range k.in {
			cs = append(cs, person.Credit{PersonID: c.PersonId, Kind: k.kind, Character: or(c.Character, ""), Job: or(c.Job, ""), Department: or(c.Department, "")})
		}
	}
	if err := s.svc.People.Repo().SetCredits(r.ItemId, cs); err != nil {
		return nil, err
	}
	out, err := s.svc.People.Repo().Credits(r.ItemId)
	if err != nil {
		return nil, err
	}
	return api.ReplaceItemCredits200JSONResponse(toCredits(out)), nil
}

func orNotFound(err error) error {
	if err != nil {
		return err
	}
	return errNotFound
}

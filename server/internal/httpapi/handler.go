// Package httpapi serves the REST API under /api/v1. The routes, request
// types and validation rules come from server/api/openapi.yaml; this package
// implements the generated strict interface over the services.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
)

// BasePath is the versioned prefix every route is served under.
const BasePath = "/api/v1"

// Server implements every operation of the spec.
type Server struct {
	svc     *service.Services
	version string
	guard   *guard
	// replay is the guarded chain every API request runs, which replayed
	// actions run too; batches holds a *sync.Mutex per device, so one
	// device's batches replay one at a time.
	replay  http.Handler
	batches sync.Map
}

var _ api.StrictServerInterface = (*Server)(nil)

// NewHandler returns the API router. version is the server's SemVer,
// reported by the health endpoint so clients can check compatibility.
func NewHandler(version string, svc *service.Services) (http.Handler, error) {
	g, err := newGuard(svc)
	if err != nil {
		return nil, err
	}
	s := &Server{svc: svc, version: version, guard: g}
	strict := api.NewStrictHandlerWithOptions(s, []api.StrictMiddlewareFunc{s.announcePlaylists}, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: responseError,
	})
	inner := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: BasePath, ErrorHandlerFunc: badRequest})
	s.replay = g.wrap(inner)
	mux := http.NewServeMux()
	mux.Handle("/", s.announceArtwork(gzipJSON(s.replay)))
	(&streams{svc}).register(mux)
	(&downloadFiles{svc}).register(mux)
	return mux, nil
}

// GetHealth reports liveness, the server version, and whether first run is
// still to do.
func (s *Server) GetHealth(context.Context, api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	admins, err := s.svc.Accounts.Repo().AdminCount()
	if err != nil {
		return nil, err
	}
	out := api.GetHealth200JSONResponse{Status: api.Ok, Version: s.version, SetupRequired: admins == 0}
	if info, err := s.svc.Server.Get(); err == nil {
		out.ServerName = &info.Name
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	return out, nil
}

// GetAttribution serves the notice TMDB's terms require clients to show.
func (s *Server) GetAttribution(context.Context, api.GetAttributionRequestObject) (api.GetAttributionResponseObject, error) {
	return api.GetAttribution200JSONResponse{Provider: "TMDB", Text: tmdb.Attribution, Url: "https://www.themoviedb.org"}, nil
}

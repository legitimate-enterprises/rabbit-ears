package service

import (
	"cmp"
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
)

// ErrKeyRejected is a TMDB key TMDB itself refused.
var ErrKeyRejected = errors.New("TMDB rejected this key")

// TMDBService keeps the running TMDB client on the key saved in Settings,
// or on FallbackKey while none is saved.
type TMDBService struct {
	repo *serverinfo.Repo
	// Client is the one client scans and artwork share; nil means TMDB is
	// not configured, and Load and Save need it set.
	Client      *tmdb.Client
	FallbackKey string
}

// NewTMDBService returns a TMDBService over repo with no client.
func NewTMDBService(repo *serverinfo.Repo) *TMDBService { return &TMDBService{repo: repo} }

// Configured reports whether the client has a key.
func (s *TMDBService) Configured() bool { return s.Client != nil && s.Client.Enabled() }

// Load puts the saved key, else the fallback, on the client. Before first
// run nothing is saved.
func (s *TMDBService) Load() error {
	info, err := s.repo.Get()
	if errors.Is(err, db.ErrNotFound) {
		info, err = &serverinfo.Info{}, nil
	}
	if err != nil {
		return err
	}
	s.Client.SetKey(cmp.Or(info.TMDBKey, s.FallbackKey))
	return nil
}

// Save saves key and puts it on the client at once; empty removes the
// saved key, leaving the fallback. A key TMDB refuses is ErrKeyRejected
// and not saved; one TMDB cannot judge, because it is down or limiting, is
// saved unchecked so an outage never blocks Settings.
func (s *TMDBService) Save(ctx context.Context, key string) error {
	if key != "" {
		err := s.Client.CheckKey(ctx, key)
		if errors.Is(err, tmdb.ErrUnauthorized) {
			return ErrKeyRejected
		}
		if err != nil {
			logstore.Log("tmdb").Warn("Could not check the new TMDB key; saved it unchecked", "err", err)
		}
	}
	if err := s.repo.SetTMDBKey(key); err != nil {
		return err
	}
	s.Client.SetKey(cmp.Or(key, s.FallbackKey))
	return nil
}

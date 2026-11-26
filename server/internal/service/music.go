package service

import "github.com/legitimate-enterprises/rabbit-ears/server/internal/item"

// MusicService serves artists, albums and songs from the item tree.
type MusicService struct{ repo *item.Repo }

// NewMusicService returns a MusicService over repo.
func NewMusicService(repo *item.Repo) *MusicService { return &MusicService{repo} }

// Repo exposes the repository.
func (s *MusicService) Repo() *item.Repo { return s.repo }

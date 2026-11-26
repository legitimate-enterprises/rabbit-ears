package service

import "github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"

// YouTubeService serves the channels and videos the scan recorded.
type YouTubeService struct{ repo *youtube.Repo }

// NewYouTubeService returns a YouTubeService over repo.
func NewYouTubeService(repo *youtube.Repo) *YouTubeService { return &YouTubeService{repo: repo} }

// Repo exposes the repository.
func (s *YouTubeService) Repo() *youtube.Repo { return s.repo }

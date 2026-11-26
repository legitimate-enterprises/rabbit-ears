package httpapi

import (
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
)

// GetAttention counts what the lists behind Needs attention hold.
func (s *Server) GetAttention(context.Context, api.GetAttentionRequestObject) (api.GetAttentionResponseObject, error) {
	var out api.Attention
	var err error
	open := false
	if out.Identity, err = s.svc.Reviews.Repo().Count(review.Filter{Dismissed: &open}); err != nil {
		return nil, err
	}
	files := s.svc.MediaFiles.Repo()
	for _, c := range []struct {
		n       *int
		problem string
	}{{&out.Missing, mediafile.Missing}, {&out.Unstreamable, mediafile.NoDuration}, {&out.Unstreamable, mediafile.Unreadable}} {
		n, err := files.ProblemCount(c.problem)
		if err != nil {
			return nil, err
		}
		*c.n += n
	}
	tasks, err := s.svc.Tasks.Repo().List()
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		last, err := s.svc.Tasks.Repo().LastRun(t.ID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
		if last != nil && last.Status == task.Failed {
			out.Failed++
		}
	}
	convs, err := s.svc.Downloads.Repo().FailedConversions()
	out.Failed += len(convs)
	return api.GetAttention200JSONResponse(out), err
}

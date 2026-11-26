package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
)

func toSeekPreview(p *seekpreview.SeekPreview) api.SeekPreview {
	return api.SeekPreview{Id: p.ID, MediaFileId: p.MediaFileID, IntervalMs: p.IntervalMs, Count: p.Count, Width: p.Width, Height: p.Height, CreatedAt: p.CreatedAt}
}

// ListSeekPreviews lists preview sets newest first, optionally of one file.
func (s *Server) ListSeekPreviews(_ context.Context, r api.ListSeekPreviewsRequestObject) (api.ListSeekPreviewsResponseObject, error) {
	all, err := s.svc.Previews.Repo().List(or(r.Params.MediaFileId, 0))
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListSeekPreviews200JSONResponse{Items: mapAll(rows, toSeekPreview), NextOffset: next}, nil
}

// GetSeekPreview returns one preview set.
func (s *Server) GetSeekPreview(_ context.Context, r api.GetSeekPreviewRequestObject) (api.GetSeekPreviewResponseObject, error) {
	p, err := s.svc.Previews.Repo().Get(r.SeekPreviewId)
	if err != nil {
		return nil, err
	}
	return api.GetSeekPreview200JSONResponse(toSeekPreview(p)), nil
}

package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// ReportClientErrors stores a client app's errors as from the caller's
// device and acting profile.
func (s *Server) ReportClientErrors(ctx context.Context, r api.ReportClientErrorsRequestObject) (api.ReportClientErrorsResponseObject, error) {
	p, in := caller(ctx), r.Body
	es := make([]service.ClientError, len(in.Entries))
	for i, x := range in.Entries {
		es[i] = service.ClientError{ID: x.Id, At: x.At, Message: x.Message, Context: x.Context, Detail: or(x.Detail, "")}
	}
	if err := s.svc.ClientErrors.Report(p.device, p.profile.ID, in.AppVersion, in.OsVersion, es); err != nil {
		return nil, err
	}
	return api.ReportClientErrors204Response{}, nil
}

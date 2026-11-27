package httpapi

import (
	"bytes"
	"context"

	"rsc.io/qr"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/devicelink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// linkPath is the web viewer page where an adult enters or confirms a code.
func linkPath(code string) string { return "/link?code=" + code }

func (s *Server) deviceLink(l *devicelink.Link) api.DeviceLink {
	return api.DeviceLink{Id: l.ID, Code: l.Code, DeviceName: l.DeviceName, Platform: api.Platform(l.Platform),
		Status: api.DeviceLinkStatus(s.svc.DeviceLinks.Status(l)), AccountId: l.AccountID, HouseholdId: l.HouseholdID,
		DeviceId: l.DeviceID, DecidedAt: l.DecidedAt, ExpiresAt: l.ExpiresAt, CreatedAt: l.CreatedAt}
}

func (s *Server) deviceLinkPage(fetch func(limit, offset int) ([]*devicelink.Link, error), limit, offset *int) (api.DeviceLinkPage, error) {
	l, o := paging(limit, offset)
	rows, err := fetch(l+1, o)
	if err != nil {
		return api.DeviceLinkPage{}, err
	}
	rows, next := window(rows, l, o)
	return api.DeviceLinkPage{Items: mapAll(rows, s.deviceLink), NextOffset: next}, nil
}

// CreateDeviceLink opens a link for an unsigned device.
func (s *Server) CreateDeviceLink(ctx context.Context, r api.CreateDeviceLinkRequestObject) (api.CreateDeviceLinkResponseObject, error) {
	ip := clientIP(ctx)
	l, token, err := s.svc.DeviceLinks.Create(r.Body.DeviceName, string(r.Body.Platform), service.SourceKey(ip),
		playback.SameLAN(ip, playback.InterfaceSubnets(), true))
	if err != nil {
		return nil, err
	}
	return api.CreateDeviceLink201JSONResponse{Id: l.ID, Code: l.Code, Link: linkPath(l.Code), ExpiresAt: l.ExpiresAt, PollToken: token}, nil
}

// PollDeviceLink tells the device where its link stands, and hands it its
// token on the first poll after approval.
func (s *Server) PollDeviceLink(_ context.Context, r api.PollDeviceLinkRequestObject) (api.PollDeviceLinkResponseObject, error) {
	l, sg, err := s.svc.DeviceLinks.Poll(r.LinkId, r.Params.Token)
	if err != nil {
		return nil, err
	}
	out := api.PollDeviceLink200JSONResponse{Status: api.DeviceLinkStatus(s.svc.DeviceLinks.Status(l)), ExpiresAt: l.ExpiresAt}
	if sg != nil {
		res, err := s.signedIn(sg)
		if err != nil {
			return nil, err
		}
		out.SignIn = &res
	}
	return out, nil
}

// GetDeviceLinkQr draws the full URL of the link's web page for a phone.
func (s *Server) GetDeviceLinkQr(ctx context.Context, r api.GetDeviceLinkQrRequestObject) (api.GetDeviceLinkQrResponseObject, error) {
	l, err := s.svc.DeviceLinks.ByToken(r.LinkId, r.Params.Token)
	if err != nil {
		return nil, err
	}
	c, err := qr.Encode(origin(ctx)+linkPath(l.Code), qr.M)
	if err != nil {
		return nil, err
	}
	b := c.PNG()
	return api.GetDeviceLinkQr200ImagepngResponse{Body: bytes.NewReader(b), ContentLength: int64(len(b))}, nil
}

// GetDeviceLinkByCode shows an adult the device behind a code.
func (s *Server) GetDeviceLinkByCode(ctx context.Context, r api.GetDeviceLinkByCodeRequestObject) (api.GetDeviceLinkByCodeResponseObject, error) {
	if !caller(ctx).asSelf() || caller(ctx).present() {
		return nil, errForbidden
	}
	l, err := s.svc.DeviceLinks.Preview(caller(ctx).profile, r.Code)
	if err != nil {
		return nil, err
	}
	return api.GetDeviceLinkByCode200JSONResponse{DeviceName: l.DeviceName, Platform: api.Platform(l.Platform), CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt}, nil
}

// ApproveDeviceLink lets the device in as the adult who signed in on the
// calling device, into the device household. A switched profile may not: the new
// sign-in would carry that adult's rights without their password. Nor may an
// adult present in the device household, which they do not manage.
func (s *Server) ApproveDeviceLink(ctx context.Context, r api.ApproveDeviceLinkRequestObject) (api.ApproveDeviceLinkResponseObject, error) {
	if !caller(ctx).asSelf() || caller(ctx).present() {
		return nil, errForbidden
	}
	l, err := s.svc.DeviceLinks.Decide(caller(ctx).profile, caller(ctx).device.HouseholdID, r.Code, true)
	if err != nil {
		return nil, err
	}
	audit(ctx, "Approved device link "+l.Code+" for "+l.DeviceName)
	return api.ApproveDeviceLink200JSONResponse(s.deviceLink(l)), nil
}

// DenyDeviceLink refuses a code.
func (s *Server) DenyDeviceLink(ctx context.Context, r api.DenyDeviceLinkRequestObject) (api.DenyDeviceLinkResponseObject, error) {
	if !caller(ctx).asSelf() || caller(ctx).present() {
		return nil, errForbidden
	}
	if _, err := s.svc.DeviceLinks.Decide(caller(ctx).profile, caller(ctx).device.HouseholdID, r.Code, false); err != nil {
		return nil, err
	}
	return api.DenyDeviceLink204Response{}, nil
}

// ListHouseholdDeviceLinks lists the links adults of the device household
// decided.
func (s *Server) ListHouseholdDeviceLinks(ctx context.Context, r api.ListHouseholdDeviceLinksRequestObject) (api.ListHouseholdDeviceLinksResponseObject, error) {
	if !caller(ctx).manages() {
		return nil, errForbidden
	}
	hh := caller(ctx).device.HouseholdID
	page, err := s.deviceLinkPage(func(l, o int) ([]*devicelink.Link, error) { return s.svc.DeviceLinks.Repo().ByHousehold(hh, l, o) },
		r.Params.Limit, r.Params.Offset)
	return api.ListHouseholdDeviceLinks200JSONResponse(page), err
}

// DeleteHouseholdDeviceLink deletes one of the household's links.
func (s *Server) DeleteHouseholdDeviceLink(ctx context.Context, r api.DeleteHouseholdDeviceLinkRequestObject) (api.DeleteHouseholdDeviceLinkResponseObject, error) {
	if !caller(ctx).manages() {
		return nil, errForbidden
	}
	return api.DeleteHouseholdDeviceLink204Response{}, s.svc.DeviceLinks.Repo().DeleteInHousehold(r.LinkId, caller(ctx).device.HouseholdID)
}

// ListDeviceLinks lists every household's links.
func (s *Server) ListDeviceLinks(_ context.Context, r api.ListDeviceLinksRequestObject) (api.ListDeviceLinksResponseObject, error) {
	page, err := s.deviceLinkPage(s.svc.DeviceLinks.Repo().List, r.Params.Limit, r.Params.Offset)
	return api.ListDeviceLinks200JSONResponse(page), err
}

// DeleteDeviceLink deletes any link.
func (s *Server) DeleteDeviceLink(ctx context.Context, r api.DeleteDeviceLinkRequestObject) (api.DeleteDeviceLinkResponseObject, error) {
	if err := s.svc.DeviceLinks.Repo().Delete(r.LinkId); err != nil {
		return nil, err
	}
	audit(ctx, "Deleted device link "+r.LinkId.String())
	return api.DeleteDeviceLink204Response{}, nil
}

package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// localAddresses lists the server's base URLs on its own network, on the
// port the request arrived at.
func localAddresses(ctx context.Context) []string {
	out := []string{}
	addr, _ := ctx.Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return out
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return out
	}
	for _, ip := range playback.LocalAddresses(playback.InterfaceSubnets()) {
		out = append(out, "http://"+net.JoinHostPort(ip.String(), port))
	}
	return out
}

// StartLocalCheck opens a check for the calling device.
func (s *Server) StartLocalCheck(ctx context.Context, _ api.StartLocalCheckRequestObject) (api.StartLocalCheckResponseObject, error) {
	c, err := s.svc.Local.Start(caller(ctx).device.ID)
	if err != nil {
		return nil, err
	}
	return api.StartLocalCheck201JSONResponse{Id: c.ID, Key: c.Key, Addresses: localAddresses(ctx), ExpiresAt: c.ExpiresAt}, nil
}

// ConfirmLocalCheck takes the device's proof when the request came straight
// from the server's own network, and answers the server's.
func (s *Server) ConfirmLocalCheck(ctx context.Context, r api.ConfirmLocalCheckRequestObject) (api.ConfirmLocalCheckResponseObject, error) {
	local := playback.SameLAN(clientIP(ctx), playback.InterfaceSubnets(), true)
	proof, err := s.svc.Local.Confirm(r.CheckId, r.Body.Nonce, r.Body.Proof, local)
	if errors.Is(err, service.ErrNotLocal) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	return api.ConfirmLocalCheck200JSONResponse{Proof: proof}, nil
}

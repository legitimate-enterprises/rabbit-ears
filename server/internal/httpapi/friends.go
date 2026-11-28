package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/friend"
)

// Friends: two adults who offer each other as share and Watch together
// targets, without household membership. The active profile acts; a kid
// gets 403 from every operation.

// adultCaller is the active profile, refused when a kid.
func adultCaller(ctx context.Context) (principal, error) {
	p := caller(ctx)
	if p.asKid() {
		return p, errForbidden
	}
	return p, nil
}

func toOutgoing(r *friend.Request) api.OutgoingFriendRequest {
	return api.OutgoingFriendRequest{Id: r.ID, Email: r.Email, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
}

// ListFriends pages the active adult's friends by name.
func (s *Server) ListFriends(ctx context.Context, r api.ListFriendsRequestObject) (api.ListFriendsResponseObject, error) {
	p, err := adultCaller(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Accounts.Repo().Friends(p.profile, limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	items := mapAll(rows, toAccount)
	for i := range items {
		s.withAvatars(ctx, &items[i])
		items[i].Email, items[i].HouseholdId = nil, nil
	}
	return api.ListFriends200JSONResponse{Items: items, NextOffset: next}, nil
}

// DeleteFriend ends a friendship of the active adult.
func (s *Server) DeleteFriend(ctx context.Context, r api.DeleteFriendRequestObject) (api.DeleteFriendResponseObject, error) {
	p, err := adultCaller(ctx)
	if err != nil {
		return nil, err
	}
	return api.DeleteFriend204Response{}, s.svc.Friends.Repo().Unfriend(p.profile.ID, r.ProfileId)
}

// ListFriendRequests lists what the active adult received and sent.
func (s *Server) ListFriendRequests(ctx context.Context, _ api.ListFriendRequestsRequestObject) (api.ListFriendRequestsResponseObject, error) {
	p, err := adultCaller(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListFriendRequests200JSONResponse{Incoming: []api.IncomingFriendRequest{}, Outgoing: []api.OutgoingFriendRequest{}}
	sent, err := s.svc.Friends.Sent(p.profile)
	if err != nil {
		return nil, err
	}
	out.Outgoing = append(out.Outgoing, mapAll(sent, toOutgoing)...)
	got, err := s.svc.Friends.Received(p.profile)
	if err != nil {
		return nil, err
	}
	for _, r := range got {
		from, err := s.svc.Accounts.Repo().Get(r.FromID)
		if err != nil {
			return nil, err
		}
		out.Incoming = append(out.Incoming, api.IncomingFriendRequest{Id: r.ID, FromProfileId: from.ID, FromName: from.Name,
			FromColor: from.ShownColor(), CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt})
	}
	return out, nil
}

// SendFriendRequest asks whoever holds an email to be friends. Its answer
// is made from the sender's own request alone.
func (s *Server) SendFriendRequest(ctx context.Context, r api.SendFriendRequestRequestObject) (api.SendFriendRequestResponseObject, error) {
	p, err := adultCaller(ctx)
	if err != nil {
		return nil, err
	}
	q, err := s.svc.Friends.Send(p.profile, string(r.Body.Email), r.Body.Id)
	if err != nil {
		return nil, err
	}
	return api.SendFriendRequest202JSONResponse(toOutgoing(q)), nil
}

// DeleteFriendRequest withdraws or declines a request.
func (s *Server) DeleteFriendRequest(ctx context.Context, r api.DeleteFriendRequestRequestObject) (api.DeleteFriendRequestResponseObject, error) {
	p, err := adultCaller(ctx)
	if err != nil {
		return nil, err
	}
	return api.DeleteFriendRequest204Response{}, s.svc.Friends.Delete(p.profile, r.RequestId)
}

// AcceptFriendRequest makes the active adult friends with a request's
// sender.
func (s *Server) AcceptFriendRequest(ctx context.Context, r api.AcceptFriendRequestRequestObject) (api.AcceptFriendRequestResponseObject, error) {
	p, err := adultCaller(ctx)
	if err != nil {
		return nil, err
	}
	return api.AcceptFriendRequest204Response{}, s.svc.Friends.Accept(p.profile, r.RequestId)
}

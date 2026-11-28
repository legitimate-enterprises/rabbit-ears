package httpapi

import (
	"context"
	"net/http"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

var errNotYet = fail(http.StatusNotImplemented, api.ErrorCodeNotImplemented, "not implemented")

// GetPlaylistRules returns a smart playlist's rules.
func (s *Server) GetPlaylistRules(ctx context.Context, r api.GetPlaylistRulesRequestObject) (api.GetPlaylistRulesResponseObject, error) {
	return nil, errNotYet
}

// ReplacePlaylistRules makes a playlist smart or replaces its rules.
func (s *Server) ReplacePlaylistRules(ctx context.Context, r api.ReplacePlaylistRulesRequestObject) (api.ReplacePlaylistRulesResponseObject, error) {
	return nil, errNotYet
}

// GetCollectionRules returns a smart collection's rules.
func (s *Server) GetCollectionRules(ctx context.Context, r api.GetCollectionRulesRequestObject) (api.GetCollectionRulesResponseObject, error) {
	return nil, errNotYet
}

// ReplaceCollectionRules makes a custom collection smart or replaces its rules.
func (s *Server) ReplaceCollectionRules(ctx context.Context, r api.ReplaceCollectionRulesRequestObject) (api.ReplaceCollectionRulesResponseObject, error) {
	return nil, errNotYet
}

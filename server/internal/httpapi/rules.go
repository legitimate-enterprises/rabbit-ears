package httpapi

import (
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/smart"
)

func fromRules(b api.SmartRules) smart.Rules {
	r := smart.Rules{Match: string(b.Match), Groups: make([]smart.Group, len(b.Groups))}
	for i, g := range b.Groups {
		r.Groups[i].Match = string(g.Match)
		for _, c := range g.Conditions {
			r.Groups[i].Conditions = append(r.Groups[i].Conditions,
				smart.Condition{Field: string(c.Field), Operator: string(c.Operator), Text: c.Text, Number: c.Number})
		}
	}
	return r
}

func toRules(r *smart.Rules) api.SmartRules {
	out := api.SmartRules{Match: api.SmartMatch(r.Match), Groups: make([]api.SmartGroup, len(r.Groups))}
	for i, g := range r.Groups {
		out.Groups[i] = api.SmartGroup{Match: api.SmartMatch(g.Match), Conditions: make([]api.SmartCondition, len(g.Conditions))}
		for j, c := range g.Conditions {
			out.Groups[i].Conditions[j] = api.SmartCondition{Field: api.SmartField(c.Field), Operator: api.SmartOperator(c.Operator), Text: c.Text,
				Number: c.Number}
		}
	}
	return out
}

func rulesError(err error) error {
	var bad *smart.Invalid
	if errors.As(err, &bad) {
		return invalid(bad.Field, bad.Message)
	}
	return err
}

// GetPlaylistRules returns a smart playlist's rules to whoever may read it.
func (s *Server) GetPlaylistRules(ctx context.Context, r api.GetPlaylistRulesRequestObject) (api.GetPlaylistRulesResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Read)
	if err != nil {
		return nil, err
	}
	rules, err := s.svc.Smart.Rules(&p.ID, nil)
	if err != nil {
		return nil, err
	}
	return api.GetPlaylistRules200JSONResponse(toRules(rules)), nil
}

// ReplacePlaylistRules makes the owner's playlist smart or replaces its
// rules, and applies them.
func (s *Server) ReplacePlaylistRules(ctx context.Context, r api.ReplacePlaylistRulesRequestObject) (api.ReplacePlaylistRulesResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	rules := fromRules(*r.Body)
	if err := s.svc.Smart.PutPlaylist(p, &rules, actor(ctx)); err != nil {
		return nil, rulesError(err)
	}
	return api.ReplacePlaylistRules200JSONResponse(toRules(&rules)), nil
}

// GetCollectionRules returns a smart collection's rules to whoever sees it.
func (s *Server) GetCollectionRules(ctx context.Context, r api.GetCollectionRulesRequestObject) (api.GetCollectionRulesResponseObject, error) {
	v, err := s.seenCollection(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	rules, err := s.svc.Smart.Rules(nil, &v.ID)
	if err != nil {
		return nil, err
	}
	return api.GetCollectionRules200JSONResponse(toRules(rules)), nil
}

// ReplaceCollectionRules makes a custom collection smart or replaces its
// rules, and applies them.
func (s *Server) ReplaceCollectionRules(ctx context.Context, r api.ReplaceCollectionRulesRequestObject) (api.ReplaceCollectionRulesResponseObject, error) {
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	if v.Source != collection.Custom {
		return nil, collectionError(service.ErrCollectionReadOnly)
	}
	rules := fromRules(*r.Body)
	if err := s.svc.Smart.PutCollection(&v.Collection, &rules, actor(ctx)); err != nil {
		return nil, rulesError(err)
	}
	return api.ReplaceCollectionRules200JSONResponse(toRules(&rules)), nil
}

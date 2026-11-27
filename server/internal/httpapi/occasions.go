package httpapi

import (
	"context"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func toOccasion(o *occasion.Occasion) api.Occasion {
	v := api.Occasion{Id: o.ID, Name: o.Name, DaysBefore: o.DaysBefore, DaysAfter: o.DaysAfter, KeywordIds: append([]int64{}, o.Keywords...),
		Phrases: append([]string{}, o.Phrases...), Colors: colorsOf(o), CreatedAt: o.CreatedAt, ModifiedAt: o.ModifiedAt,
		CreatedBy: o.CreatedBy, ModifiedBy: o.ModifiedBy}
	if o.Holiday != "" {
		v.Holiday = &o.Holiday
	} else {
		v.Month, v.Day = &o.Month, &o.Day
	}
	return v
}

func colorsOf(o *occasion.Occasion) *[]string {
	if len(o.Colors) == 0 {
		return nil
	}
	return &o.Colors
}

// occasionFrom copies the input after checking what the schema cannot:
// one anchor, a holiday the server knows, a day the month has in a leap
// year, no phrase blank once trimmed, none or three colors. Phrases or
// colors absent are left as o has them.
func occasionFrom(in *api.OccasionInput, o *occasion.Occasion) error {
	if in.Phrases != nil {
		o.Phrases = []string{}
		seen := map[string]bool{}
		for _, p := range *in.Phrases {
			p = strings.TrimSpace(p)
			if p == "" {
				return invalid("phrases", "a phrase is blank")
			}
			if k := strings.ToLower(p); !seen[k] {
				seen[k] = true
				o.Phrases = append(o.Phrases, p)
			}
		}
	}
	if in.Colors != nil {
		if n := len(*in.Colors); n != 0 && n != 3 {
			return invalid("colors", "give three colors, or none")
		}
		o.Colors = *in.Colors
	}
	switch {
	case (in.Holiday != nil) == (in.Month != nil || in.Day != nil):
		return invalid("holiday", "give a holiday, or a month and day")
	case in.Holiday != nil:
		if _, ok := occasion.HolidayByKey(*in.Holiday); !ok {
			return invalid("holiday", "not a holiday of listHolidays")
		}
	case in.Month == nil || in.Day == nil:
		return invalid("day", "give a month and day together")
	case time.Date(2028, time.Month(*in.Month), *in.Day, 0, 0, 0, 0, time.UTC).Day() != *in.Day:
		return invalid("day", "not a day of that month")
	}
	o.Name, o.Holiday, o.Month, o.Day = in.Name, or(in.Holiday, ""), or(in.Month, 0), or(in.Day, 0)
	o.DaysBefore, o.DaysAfter, o.Keywords = in.DaysBefore, in.DaysAfter, in.KeywordIds
	return nil
}

// ListOccasions pages occasions by name.
func (s *Server) ListOccasions(_ context.Context, r api.ListOccasionsRequestObject) (api.ListOccasionsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Occasions.Repo().List(limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListOccasions200JSONResponse{Items: mapAll(rows, toOccasion), NextOffset: next}, nil
}

// CreateOccasion adds an occasion, one anchored to a holiday starting with
// the holiday's phrases and colors where the input gives none.
func (s *Server) CreateOccasion(ctx context.Context, r api.CreateOccasionRequestObject) (api.CreateOccasionResponseObject, error) {
	o := occasion.Occasion{ID: or(r.Body.Id, uid.ID{})}
	if h, ok := occasion.HolidayByKey(or(r.Body.Holiday, "")); ok {
		o.Phrases, o.Colors = occasion.BuiltInPhrases(h.Key), h.Colors
	}
	if err := occasionFrom(r.Body, &o); err != nil {
		return nil, err
	}
	if err := s.svc.Occasions.Repo().Create(&o, actor(ctx)); err != nil {
		return nil, err
	}
	return api.CreateOccasion201JSONResponse(toOccasion(&o)), nil
}

// GetOccasion returns an occasion.
func (s *Server) GetOccasion(_ context.Context, r api.GetOccasionRequestObject) (api.GetOccasionResponseObject, error) {
	o, err := s.svc.Occasions.Repo().Get(r.OccasionId)
	if err != nil {
		return nil, err
	}
	return api.GetOccasion200JSONResponse(toOccasion(o)), nil
}

// ReplaceOccasion rewrites an occasion; its hand additions and removals
// stay, and so do its phrases or colors when the input leaves them out.
func (s *Server) ReplaceOccasion(ctx context.Context, r api.ReplaceOccasionRequestObject) (api.ReplaceOccasionResponseObject, error) {
	if err := sameID(r.Body.Id, r.OccasionId); err != nil {
		return nil, err
	}
	o, err := s.svc.Occasions.Repo().Get(r.OccasionId)
	if err != nil {
		return nil, err
	}
	if err := occasionFrom(r.Body, o); err != nil {
		return nil, err
	}
	if err := s.svc.Occasions.Repo().Update(o, actor(ctx)); err != nil {
		return nil, err
	}
	return api.ReplaceOccasion200JSONResponse(toOccasion(o)), nil
}

// DeleteOccasion removes an occasion.
func (s *Server) DeleteOccasion(_ context.Context, r api.DeleteOccasionRequestObject) (api.DeleteOccasionResponseObject, error) {
	return api.DeleteOccasion204Response{}, s.svc.Occasions.Repo().Delete(r.OccasionId)
}

// ListOccasionMembers pages every member of an occasion by title, with
// whether a keyword brings it.
func (s *Server) ListOccasionMembers(_ context.Context, r api.ListOccasionMembersRequestObject) (api.ListOccasionMembersResponseObject, error) {
	if _, err := s.svc.Occasions.Repo().Get(r.OccasionId); err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Items.Repo().List(item.Filter{OccasionID: &r.OccasionId, Catalog: true, Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	out := api.ListOccasionMembers200JSONResponse{Items: []api.OccasionMember{}}
	rows, out.NextOffset = window(rows, limit, offset)
	ids := make([]uid.ID, len(rows))
	for i, it := range rows {
		ids[i] = it.ID
	}
	keyworded, err := s.svc.Occasions.Repo().Keyworded(r.OccasionId, ids)
	if err != nil {
		return nil, err
	}
	items, err := s.items(rows, true)
	if err != nil {
		return nil, err
	}
	for _, v := range items {
		via := api.OccasionMemberViaAdded
		if keyworded[v.Id] {
			via = api.OccasionMemberViaKeyword
		}
		out.Items = append(out.Items, api.OccasionMember{Item: v, Via: via})
	}
	return out, nil
}

// occasionItem resolves an occasion and an item, each or not found.
func (s *Server) occasionItem(occasionID, itemID uid.ID) (*item.Item, error) {
	if _, err := s.svc.Occasions.Repo().Get(occasionID); err != nil {
		return nil, err
	}
	return s.svc.Items.Repo().Get(itemID)
}

// AddOccasionMember makes a movie or show a member by hand.
func (s *Server) AddOccasionMember(_ context.Context, r api.AddOccasionMemberRequestObject) (api.AddOccasionMemberResponseObject, error) {
	it, err := s.occasionItem(r.OccasionId, r.ItemId)
	if err != nil {
		return nil, err
	}
	if it.Kind != item.Movie && it.Kind != item.Show {
		return nil, invalid("itemId", "only movies and shows are members")
	}
	return api.AddOccasionMember204Response{}, s.svc.Occasions.Repo().AddMember(r.OccasionId, r.ItemId)
}

// RemoveOccasionMember keeps an item out of an occasion.
func (s *Server) RemoveOccasionMember(_ context.Context, r api.RemoveOccasionMemberRequestObject) (api.RemoveOccasionMemberResponseObject, error) {
	if _, err := s.occasionItem(r.OccasionId, r.ItemId); err != nil {
		return nil, err
	}
	return api.RemoveOccasionMember204Response{}, s.svc.Occasions.Repo().RemoveMember(r.OccasionId, r.ItemId)
}

// ListHolidays lists the built-in holidays.
func (s *Server) ListHolidays(context.Context, api.ListHolidaysRequestObject) (api.ListHolidaysResponseObject, error) {
	return api.ListHolidays200JSONResponse{Items: mapAll(occasion.Holidays(), func(h occasion.Holiday) api.Holiday {
		return api.Holiday{Key: h.Key, Name: h.Name, Colors: &h.Colors, Dates: mapAll(h.Dates, func(d time.Time) openapi_types.Date { return openapi_types.Date{Time: d} })}
	})}, nil
}

// ListActiveOccasions lists the occasions in season on the client's day
// with a member the profile browses, or with the given item among them.
func (s *Server) ListActiveOccasions(ctx context.Context, r api.ListActiveOccasionsRequestObject) (api.ListActiveOccasionsResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	y, m, d := r.Params.Date.Date()
	rows, err := s.svc.Occasions.Active(time.Date(y, m, d, 0, 0, 0, 0, time.UTC), service.BrowseAudienceFor(who), r.Params.ItemId)
	if err != nil {
		return nil, err
	}
	return api.ListActiveOccasions200JSONResponse{Items: mapAll(rows, func(v service.ActiveOccasion) api.ActiveOccasion {
		out := api.ActiveOccasion{Id: v.ID, Name: v.Name, AnchorDate: openapi_types.Date{Time: v.Anchor},
			FirstDate: openapi_types.Date{Time: v.First}, LastDate: openapi_types.Date{Time: v.Last}, Colors: colorsOf(v.Occasion)}
		if v.Headline != "" {
			out.Headline = &v.Headline
		}
		return out
	})}, nil
}

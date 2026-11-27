package httpapi

import (
	"path/filepath"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/joincode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

const defaultLimit = 50

func paging(limit, offset *int) (int, int) {
	return or(limit, defaultLimit), or(offset, 0)
}

// window trims rows fetched with limit+1 at offset and reports where the
// next page starts, if there is one.
func window[T any](rows []T, limit, offset int) ([]T, *int) {
	if len(rows) > limit {
		next := offset + limit
		return rows[:limit], &next
	}
	return rows, nil
}

// slicePage pages a list the repository returns whole.
func slicePage[T any](all []T, limit, offset int) ([]T, *int) {
	if offset >= len(all) {
		return nil, nil
	}
	return window(all[offset:], limit, offset)
}

// mapAll never returns nil, so required arrays encode as [] rather than null.
func mapAll[T, U any](in []T, f func(T) U) []U {
	out := make([]U, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

func or[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// optTrue is absent for false, as an optional boolean that means false
// when absent is sent.
func optTrue(b bool) *bool {
	if !b {
		return nil
	}
	return &b
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optEnum[T ~string](s string) *T {
	if s == "" {
		return nil
	}
	v := T(s)
	return &v
}

func enumStr[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func optDate(t *time.Time) *openapi_types.Date {
	if t == nil {
		return nil
	}
	return &openapi_types.Date{Time: *t}
}

func fromDate(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	t := d.UTC()
	return &t
}

func toLibrary(l *library.Library, folders []*library.Folder, st api.LibraryStats) api.Library {
	return api.Library{Id: l.ID, Name: l.Name, Kind: api.LibraryKind(l.Kind), ScanHour: l.ScanHour, WatchFiles: l.WatchFiles,
		Folders: mapAll(folders, toFolder), Stats: st, LastScannedAt: l.LastScannedAt, CreatedAt: l.CreatedAt, ModifiedAt: l.ModifiedAt, CreatedBy: l.CreatedBy, ModifiedBy: l.ModifiedBy}
}

func toFolder(f *library.Folder) api.Folder {
	return api.Folder{Id: f.ID, LibraryId: f.LibraryID, Path: f.Path, CreatedAt: f.CreatedAt, ModifiedAt: f.ModifiedAt, CreatedBy: f.CreatedBy,
		ModifiedBy: f.ModifiedBy}
}

// shownPath is a server path as the caller sees it: whole for an admin, else
// only its base name, since where the media lives is the admin's alone.
func shownPath(path string, admin bool) string {
	if admin || path == "" {
		return path
	}
	return filepath.Base(path)
}

// artPath is an image's path as a client sees it: TMDB's whole, since it
// names TMDB's image, and any other, a file on the server, as shownPath.
func artPath(path, from string, admin bool) string {
	if from == "tmdb" {
		return path
	}
	return shownPath(path, admin)
}

func toItem(it *item.Item, m *item.Metadata, admin bool) api.Item {
	out := api.Item{Id: it.ID, LibraryId: it.LibraryID, ParentId: it.ParentID, Kind: api.ItemKind(it.Kind), Path: optStr(shownPath(it.Path, admin)),
		ParsedTitle: it.ParsedTitle, ParsedYear: it.ParsedYear, SeasonNumber: it.SeasonNumber, EpisodeNumber: it.EpisodeNumber,
		AddedAt: it.AddedAt, CreatedAt: it.CreatedAt, ModifiedAt: it.ModifiedAt}
	if it.Kind == item.Movie || it.Kind == item.Show {
		rating := ""
		if m != nil {
			rating = m.ContentRating
		}
		kids := item.IsKidsContent(it.Kind, rating, it.KidsContent)
		out.KidsContent, out.KidsContentOverride = &kids, it.KidsContent
	}
	if m != nil {
		var release *openapi_types.Date
		if t, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
			release = &openapi_types.Date{Time: t}
		}
		out.Metadata = &api.ItemMetadata{Provider: optEnum[api.ItemMetadataProvider](m.Provider), ProviderId: m.ProviderID,
			Status: api.ItemMetadataStatus(m.Status), Title: optStr(m.Title), Overview: optStr(m.Overview), ReleaseDate: release,
			RuntimeMinutes: m.RuntimeMinutes, ContentRating: optStr(m.ContentRating), PosterPath: optStr(artPath(m.PosterPath, m.PosterFrom, admin)),
			BackdropPath: optStr(artPath(m.BackdropPath, m.BackdropFrom, admin)), FetchedAt: m.FetchedAt}
	}
	return out
}

func toMediaFile(f *mediafile.File, tracks []*mediafile.Track, chapters []*mediafile.Chapter, admin bool) api.MediaFile {
	out := api.MediaFile{Id: f.ID, ItemId: f.ItemID, Path: shownPath(f.Path, admin), SizeBytes: f.SizeBytes, FileModifiedAt: f.FileModifiedAt,
		Container: optStr(f.Container), DurationMs: f.DurationMs, Bitrate: f.Bitrate, ProbedAt: f.ProbedAt,
		Tracks: mapAll(tracks, toTrack), Chapters: mapAll(chapters, toChapter), MissingAt: f.MissingAt, ProbeFailedAt: f.ProbeFailedAt,
		CreatedAt: f.CreatedAt, ModifiedAt: f.ModifiedAt}
	if admin {
		out.ProbeError = optStr(f.ProbeError)
	}
	return out
}

func toTrack(t *mediafile.Track) api.Track {
	return api.Track{Id: t.ID, StreamIndex: t.StreamIndex, Kind: api.TrackKind(t.Kind), Codec: t.Codec, Profile: optStr(t.Profile),
		Level: t.Level, Language: optStr(t.Language), Title: optStr(t.Title), IsDefault: t.IsDefault, IsForced: t.IsForced,
		Bitrate: t.Bitrate, Width: t.Width, Height: t.Height, FrameRate: t.FrameRate, BitDepth: t.BitDepth,
		PixelFormat: optStr(t.PixelFormat), ColorPrimaries: optStr(t.ColorPrimaries), ColorTransfer: optStr(t.ColorTransfer),
		HdrFormat: optEnum[api.HdrFormat](t.HDRFormat), Channels: t.Channels, ChannelLayout: optStr(t.ChannelLayout),
		SampleRate: t.SampleRate, SubtitleIsText: t.SubtitleIsText}
}

func toChapter(c *mediafile.Chapter) api.Chapter {
	return api.Chapter{Id: c.ID, Position: c.Position, StartMs: c.StartMs, EndMs: c.EndMs, Title: optStr(c.Title)}
}

func toAccount(a *account.Account) api.Account {
	locked := a.PINLockedUntil != nil && a.PINLockedUntil.After(time.Now())
	out := api.Account{Id: a.ID, Kind: api.AccountKind(a.Kind), Email: optStr(a.Email), Name: a.Name, IsAdmin: a.IsAdmin,
		HouseholdId: a.HouseholdID, Birthday: optDate(a.Birthday), HasPin: a.PINHash != "", PinLocked: locked, Pending: a.Pending(),
		FilterMode: api.FilterMode(a.FilterMode), MovieCeiling: optEnum[api.MovieRating](a.MovieCeiling),
		TvCeiling: optEnum[api.TvRating](a.TVCeiling), RevokedAt: a.RevokedAt, CreatedAt: a.CreatedAt, ModifiedAt: a.ModifiedAt,
		CreatedBy: a.CreatedBy, ModifiedBy: a.ModifiedBy}
	color, autoplay := a.ShownColor(), !a.NoAutoplay
	out.Color, out.Autoplay = &color, &autoplay
	if a.Kind == account.Adult {
		out.HideKidsContent = &a.HideKidsContent
	}
	return out
}

func toHousehold(h *household.Household) api.Household {
	return api.Household{Id: h.ID, Name: h.Name, AdultId: h.AdultID, CreatedAt: h.CreatedAt, ModifiedAt: h.ModifiedAt,
		CreatedBy: h.CreatedBy, ModifiedBy: h.ModifiedBy}
}

func toDevice(d *device.Device) api.Device {
	return api.Device{Id: d.ID, Name: d.Name, Platform: api.Platform(d.Platform), AccountId: d.AccountID, HouseholdId: d.HouseholdID,
		LastSeenAt: d.LastSeenAt, RevokedAt: d.RevokedAt, CreatedAt: d.CreatedAt, ModifiedAt: d.ModifiedAt}
}

func toJoinCode(j *joincode.JoinCode) api.JoinCode {
	return api.JoinCode{Id: j.ID, Code: j.Code, Link: "/join/" + j.Code, AccountId: j.AccountID, ExpiresAt: j.ExpiresAt,
		UsedAt: j.UsedAt, RevokedAt: j.RevokedAt, CreatedAt: j.CreatedAt, ModifiedAt: j.ModifiedAt, CreatedBy: j.CreatedBy,
		ModifiedBy: j.ModifiedBy}
}

func toPlanEntry(p *session.PlanEntry) api.PlanEntry {
	return api.PlanEntry{Position: p.Position, Verdict: api.Verdict(p.Verdict), VideoAction: api.PlanEntryVideoAction(p.VideoAction),
		AudioAction: api.PlanEntryAudioAction(p.AudioAction), SubtitleAction: api.PlanEntrySubtitleAction(p.SubtitleAction),
		Bad: p.Bad, BadReason: optStr(p.BadReason)}
}

func toSession(s *session.Session, plan []*session.PlanEntry) api.Session {
	out := api.Session{Id: s.ID, DeviceId: s.DeviceID, AccountId: s.AccountID, ItemId: s.ItemID, MediaFileId: s.MediaFileID,
		PositionMs: s.PositionMs, CurrentEntry: s.CurrentEntry, Plan: mapAll(plan, toPlanEntry), Encoder: api.SessionEncoderNone,
		StartedAt: s.StartedAt, CreatedAt: s.CreatedAt, ModifiedAt: s.ModifiedAt, EndedAt: s.EndedAt}
	if s.CurrentEntry < len(plan) && plan[s.CurrentEntry].VideoAction == session.ActionConvert {
		out.Encoder = api.SessionEncoderSoftware
		if plan[s.CurrentEntry].Encoder == session.EncoderHardware {
			out.Encoder = api.SessionEncoderVideotoolbox
		}
	}
	return out
}

func toWatchState(w *watchstate.State) api.WatchState {
	return api.WatchState{ItemId: w.ItemID, PositionMs: w.PositionMs, EditionId: w.EditionID, Watched: w.Watched, PlayCount: w.PlayCount,
		LastPlayedAt: w.LastPlayedAt, ChangedAt: w.ChangedAt}
}

func toTask(t *task.Task, last *task.Run) api.Task {
	out := api.Task{Id: t.ID, Name: t.Name, Kind: t.Kind, IntervalSeconds: t.IntervalSeconds, Enabled: t.Enabled,
		NextRunAt: t.NextRunAt, RunRequestedAt: t.RunRequestedAt, CreatedAt: t.CreatedAt, ModifiedAt: t.ModifiedAt}
	if last != nil {
		r := toRun(last)
		out.LastRun = &r
	}
	return out
}

func toRun(r *task.Run) api.TaskRun {
	return api.TaskRun{Id: r.ID, TaskId: r.TaskID, Status: api.TaskRunStatus(r.Status), Progress: r.Progress,
		Error: optStr(r.Error), Summary: optStr(r.Summary), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
}

func toGenre(g *genre.Genre) api.Genre { return api.Genre{Id: g.ID, Name: g.Name, TmdbId: g.TMDBID} }

func toPerson(p *person.Person) api.Person {
	return api.Person{Id: p.ID, Name: p.Name, TmdbId: p.TMDBID, ProfilePath: optStr(p.ProfilePath)}
}

func toCredits(cs []*person.Credit) api.ItemCredits {
	out := api.ItemCredits{Cast: []api.Credit{}, Crew: []api.Credit{}}
	for _, c := range cs {
		v := api.Credit{PersonId: c.PersonID, Name: c.Name, ProfilePath: optStr(c.ProfilePath), Character: optStr(c.Character),
			Job: optStr(c.Job), Department: optStr(c.Department)}
		if c.Kind == person.Cast {
			out.Cast = append(out.Cast, v)
		} else {
			out.Crew = append(out.Crew, v)
		}
	}
	return out
}

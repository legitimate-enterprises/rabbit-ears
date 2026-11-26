package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// maxDownloadTitles is the most titles one ResolveDownloads covers.
const maxDownloadTitles = 5000

// isTitle is whether an item of kind is a title, what a download is of.
func isTitle(kind string) bool {
	return slices.Contains([]string{item.Movie, item.Episode, item.Song, item.Video}, kind)
}

// ResolveDownloads answers what downloading the targets would keep.
func (s *Server) ResolveDownloads(ctx context.Context, r api.ResolveDownloadsRequestObject) (api.ResolveDownloadsResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	a := service.AudienceFor(who)
	seen := map[int64]bool{}
	var rows []playlist.Row
	for _, t := range r.Body.Targets {
		got, err := s.downloadTarget(ctx, who, a, t)
		if err != nil {
			return nil, err
		}
		for _, row := range got {
			if !seen[row.ItemID] {
				seen[row.ItemID] = true
				rows = append(rows, row)
			}
		}
		if len(rows) > maxDownloadTitles {
			return nil, invalid("targets", fmt.Sprintf("cover more than %d titles", maxDownloadTitles))
		}
	}
	titles, err := s.svc.Downloads.Titles(rows, who.ID, toSnapshot(ctx, r.Body.Snapshot, false), playback.Selection{}, string(r.Body.Quality))
	if err != nil {
		return nil, err
	}
	out := api.ResolveDownloads200JSONResponse{Titles: make([]api.DownloadTitle, 0, len(titles))}
	for _, t := range titles {
		out.Titles = append(out.Titles, toDownloadTitle(t))
		if t.Plan != nil {
			out.SizeBytes += t.Plan.Bytes
		}
	}
	return out, nil
}

// downloadTarget is the rows a target stands for to the audience; a target
// the audience may not see is not found.
func (s *Server) downloadTarget(ctx context.Context, who *account.Account, a item.Audience, t api.DownloadTarget) ([]playlist.Row, error) {
	n := 0
	for _, id := range []*int64{t.ItemId, t.CollectionId, t.PlaylistId} {
		if id != nil {
			n++
		}
	}
	if n != 1 {
		return nil, invalid("targets", "each names exactly one of itemId, collectionId and playlistId")
	}
	if t.EditionId != nil && t.ItemId == nil {
		return nil, invalid("targets", "editionId goes with the itemId of a title")
	}
	lists := s.svc.Playlists.Repo()
	switch {
	case t.ItemId != nil:
		it, err := s.visibleTo(a, *t.ItemId)
		if err != nil {
			return nil, err
		}
		ed := int64(0)
		if t.EditionId != nil {
			if !isTitle(it.Kind) {
				return nil, invalid("targets", "editionId goes with the itemId of a title")
			}
			if _, err := s.edition(it.ID, *t.EditionId); err != nil {
				return nil, err
			}
			ed = *t.EditionId
		}
		return lists.Expand(it.ID, 0, ed, a)
	case t.CollectionId != nil:
		if _, err := s.svc.Collections.Repo().Seen(caller(ctx).device.HouseholdID, *t.CollectionId, a); err != nil {
			return nil, err
		}
		return lists.Expand(0, *t.CollectionId, 0, a)
	default:
		p, err := lists.Get(*t.PlaylistId)
		if err != nil {
			return nil, err
		}
		if p.StationOwned {
			return nil, errNotFound
		}
		if _, err := s.svc.Playlists.Access(p, who.ID); err != nil {
			return nil, playlistError(err)
		}
		return lists.PlayOrder(p.ID, a, -1, 0)
	}
}

// edition is an edition of the item; one of another item is not found.
func (s *Server) edition(itemID, editionID int64) (int64, error) {
	ed, err := s.svc.Editions.Repo().Get(editionID)
	if err == nil && ed.ItemID != itemID {
		err = errNotFound
	}
	if err != nil {
		return 0, err
	}
	return ed.ID, nil
}

func planVerdict(p *playback.DownloadPlan) api.Verdict {
	if p.Original {
		return api.DirectPlay
	}
	return api.Verdict(p.Entry.Verdict)
}

func toDownloadTitle(t *service.DownloadTitle) api.DownloadTitle {
	out := api.DownloadTitle{ItemId: t.ItemID, Kind: api.DownloadTitleKind(t.Kind), LibraryId: t.LibraryID, RuntimeMs: t.RuntimeMs,
		Available: t.Plan != nil, Watched: t.Watched, PositionMs: t.PositionMs}
	if t.EditionID != 0 {
		out.EditionId = &t.EditionID
	}
	if p := t.Plan; p != nil {
		q, v, estimated := api.DownloadQuality(p.Quality), planVerdict(p), !p.Original
		out.Quality, out.Verdict, out.Container, out.SizeBytes, out.Estimated = &q, &v, &p.Container, &p.Bytes, &estimated
	}
	return out
}

// SyncDownloads splits the titles a device keeps into those the profile
// may still see and those it may not.
func (s *Server) SyncDownloads(ctx context.Context, r api.SyncDownloadsRequestObject) (api.SyncDownloadsResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	a := service.AudienceFor(who)
	out := api.SyncDownloads200JSONResponse{Keep: []int64{}, Remove: []int64{}}
	for _, id := range r.Body.ItemIds {
		ok, err := s.svc.Items.Repo().Visible(id, a)
		if err != nil {
			return nil, err
		}
		if ok {
			out.Keep = append(out.Keep, id)
		} else {
			out.Remove = append(out.Remove, id)
		}
	}
	return out, nil
}

// ListDownloads pages this device's downloads for the profile.
func (s *Server) ListDownloads(ctx context.Context, r api.ListDownloadsRequestObject) (api.ListDownloadsResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Downloads.Repo().List(caller(ctx).device.ID, who.ID, service.AudienceFor(who), limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	out := api.ListDownloads200JSONResponse{Items: make([]api.Download, 0, len(rows)), NextOffset: next}
	for _, d := range rows {
		v, err := s.toDownload(d)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreateDownload claims a title's file for the profile on this device.
func (s *Server) CreateDownload(ctx context.Context, r api.CreateDownloadRequestObject) (api.CreateDownloadResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	in := r.Body
	v, err := s.claim(ctx, who, api.DownloadBatchTitle{ItemId: in.ItemId, EditionId: in.EditionId, AudioTrackId: in.AudioTrackId},
		toSnapshot(ctx, in.Snapshot, false), string(in.Quality))
	if err != nil {
		return nil, err
	}
	return api.CreateDownload201JSONResponse(v), nil
}

// CreateDownloads claims many titles' files for the profile on this device,
// in request order; a title's own error answers for it alone.
func (s *Server) CreateDownloads(ctx context.Context, r api.CreateDownloadsRequestObject) (api.CreateDownloadsResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	snap := toSnapshot(ctx, r.Body.Snapshot, false)
	out := api.CreateDownloads200JSONResponse{Results: make([]api.DownloadBatchResult, 0, len(r.Body.Titles))}
	for _, t := range r.Body.Titles {
		res := api.DownloadBatchResult{ItemId: t.ItemId}
		v, err := s.claim(ctx, who, t, snap, string(r.Body.Quality))
		var ae *apiError
		switch {
		case err == nil:
			res.Download = &v
		case errors.As(err, &ae):
			res.Error = &ae.body
		case errors.Is(err, db.ErrNotFound):
			res.Error = &errNotFound.body
		default:
			return nil, err
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// claim claims a title's file for the profile on this device.
func (s *Server) claim(ctx context.Context, who *account.Account, in api.DownloadBatchTitle, snap playback.Snapshot, quality string) (api.Download, error) {
	it, err := s.visibleTo(service.AudienceFor(who), in.ItemId)
	if err != nil {
		return api.Download{}, err
	}
	if !isTitle(it.Kind) {
		return api.Download{}, invalid("itemId", "a download is of a movie, episode, song or video")
	}
	ed := int64(0)
	if in.EditionId != nil {
		if ed, err = s.edition(it.ID, *in.EditionId); err != nil {
			return api.Download{}, err
		}
	}
	row, err := s.svc.Downloads.Row(it, ed)
	if err != nil {
		return api.Download{}, err
	}
	t, err := s.svc.Downloads.Title(row, who.ID, snap, playback.Selection{AudioTrackID: in.AudioTrackId}, quality)
	if err != nil {
		return api.Download{}, err
	}
	d, err := s.svc.Downloads.Create(caller(ctx).device.ID, who.ID, t)
	if errors.Is(err, service.ErrNotDownloadable) {
		return api.Download{}, fail(http.StatusConflict, api.ErrorCodeConflict, err.Error())
	}
	if err != nil {
		return api.Download{}, err
	}
	return s.toDownload(d)
}

// ownDownload is one of this device's downloads for a profile it may act
// for, of a title the profile may still see.
func (s *Server) ownDownload(ctx context.Context, profileID, id int64) (*download.Download, error) {
	who, err := s.profile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return s.svc.Downloads.Repo().Of(id, caller(ctx).device.ID, who.ID, service.AudienceFor(who))
}

// GetDownload answers one download, to poll until it is ready.
func (s *Server) GetDownload(ctx context.Context, r api.GetDownloadRequestObject) (api.GetDownloadResponseObject, error) {
	d, err := s.ownDownload(ctx, r.ProfileId, r.DownloadId)
	if err != nil {
		return nil, err
	}
	v, err := s.toDownload(d)
	return api.GetDownload200JSONResponse(v), err
}

// DeleteDownload forgets a download the device is done with.
func (s *Server) DeleteDownload(ctx context.Context, r api.DeleteDownloadRequestObject) (api.DeleteDownloadResponseObject, error) {
	d, err := s.ownDownload(ctx, r.ProfileId, r.DownloadId)
	if err != nil {
		return nil, err
	}
	return api.DeleteDownload204Response{}, s.svc.Downloads.Release(d)
}

func (s *Server) toDownload(d *download.Download) (api.Download, error) {
	f, m, err := s.svc.Playback.Media(d.MediaFileID)
	if err != nil {
		return api.Download{}, err
	}
	out := api.Download{Id: d.ID, ProfileId: d.AccountID, ItemId: d.ItemID, EditionId: d.EditionID, MediaFileId: d.MediaFileID,
		Quality: api.DownloadQuality(d.Quality), Status: api.DownloadStatusReady, Verdict: api.DirectPlay, Container: f.Container,
		SizeBytes: f.SizeBytes, Progress: 1, Subtitles: []api.DownloadSubtitle{}, CreatedAt: d.CreatedAt}
	var c *download.Conversion
	if d.ConversionID != nil {
		if c, err = s.svc.Downloads.Repo().Conversion(*d.ConversionID); err != nil {
			return api.Download{}, err
		}
		out.Status, out.Verdict, out.Container, out.Progress = api.DownloadStatus(c.Status), api.Verdict(c.Entry.Verdict), c.Container,
			s.svc.Downloads.Progress(c)
		out.SizeBytes, out.Estimated = c.PredictedBytes, c.SizeBytes == nil
		if c.SizeBytes != nil {
			out.SizeBytes = *c.SizeBytes
		}
		out.Error = optStr(c.Error)
	}
	base := fmt.Sprintf("%s/downloads/%d", BasePath, d.ID)
	if out.Status == api.DownloadStatusReady {
		url := base + "/file"
		out.Url = &url
	}
	out.ExpiresAt = s.svc.Downloads.ExpiresAt(d, c)
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Subtitle && t.SubtitleIsText != nil && *t.SubtitleIsText {
			out.Subtitles = append(out.Subtitles, api.DownloadSubtitle{TrackId: t.ID, Language: optStr(t.Language), Title: optStr(t.Title),
				IsForced: t.IsForced, Url: fmt.Sprintf("%s/subtitles/%d.vtt", base, t.ID)})
		}
	}
	return out, nil
}

// ListConversions pages every conversion, or only the active or the failed
// ones, newest first.
func (s *Server) ListConversions(_ context.Context, r api.ListConversionsRequestObject) (api.ListConversionsResponseObject, error) {
	repo := s.svc.Downloads.Repo()
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	var rows []*download.Conversion
	var next *int
	if active, failed := or(r.Params.Active, false), or(r.Params.Failed, false); active || failed {
		list := repo.Active
		if failed {
			list = repo.FailedConversions
		}
		all, err := list()
		if err != nil {
			return nil, err
		}
		rows, next = slicePage(all, limit, offset)
	} else {
		all, err := repo.Conversions(limit+1, offset)
		if err != nil {
			return nil, err
		}
		rows, next = window(all, limit, offset)
	}
	out := api.ListConversions200JSONResponse{Items: make([]api.Conversion, 0, len(rows)), NextOffset: next}
	for _, c := range rows {
		v, err := s.toConversion(c)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// GetConversion answers one conversion.
func (s *Server) GetConversion(_ context.Context, r api.GetConversionRequestObject) (api.GetConversionResponseObject, error) {
	c, err := s.svc.Downloads.Repo().Conversion(r.ConversionId)
	if err != nil {
		return nil, err
	}
	v, err := s.toConversion(c)
	return api.GetConversion200JSONResponse(v), err
}

// DeleteConversion stops and removes a conversion with its downloads.
func (s *Server) DeleteConversion(_ context.Context, r api.DeleteConversionRequestObject) (api.DeleteConversionResponseObject, error) {
	return api.DeleteConversion204Response{}, s.svc.Downloads.DeleteConversion(r.ConversionId)
}

func (s *Server) toConversion(c *download.Conversion) (api.Conversion, error) {
	n, err := s.svc.Downloads.Repo().Count(c.ID)
	if err != nil {
		return api.Conversion{}, err
	}
	e := c.Entry
	out := api.Conversion{Id: c.ID, MediaFileId: c.MediaFileID, Status: api.ConversionStatus(c.Status), Verdict: api.Verdict(e.Verdict),
		Container: api.ConversionContainer(c.Container), Bitrate: e.TopBitrate, SizeBytes: c.PredictedBytes, Estimated: c.SizeBytes == nil,
		Progress: s.svc.Downloads.Progress(c), Downloads: n, Error: optStr(c.Error), CreatedAt: c.CreatedAt, StartedAt: c.StartedAt,
		FinishedAt: c.FinishedAt}
	if c.SizeBytes != nil {
		out.SizeBytes = *c.SizeBytes
	}
	if e.VideoAction == "convert" {
		out.Codec, out.Height = &e.VideoCodec, e.TopHeight
	}
	return out, nil
}

// downloadFiles serves downloads' bytes outside the spec, to the device
// that claimed them with its token, as Download.url describes.
type downloadFiles struct{ svc *service.Services }

func (h *downloadFiles) register(mux *http.ServeMux) {
	p := BasePath + "/downloads/{id}"
	mux.HandleFunc("GET "+p+"/file", h.with(h.file))
	mux.HandleFunc("GET "+p+"/subtitles/{name}", h.with(h.subtitle))
}

// with authenticates the device as the guard does and finds its download
// of a title the download's profile may still see. While a kid is active,
// only the kid's own downloads are found.
func (h *downloadFiles) with(fn func(http.ResponseWriter, *http.Request, *download.Download)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := h.find(r)
		if err != nil {
			responseError(w, r, err)
			return
		}
		fn(w, r, d)
	}
}

func (h *downloadFiles) find(r *http.Request) (*download.Download, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return nil, service.ErrUnauthenticated
	}
	dev, a, err := h.svc.Devices.Authenticate(token)
	if err != nil {
		return nil, err
	}
	p, err := acting(h.svc, dev, a)
	if err != nil {
		return nil, err
	}
	if dev.LockedAt != nil {
		return nil, errLocked
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, errNotFound
	}
	d, err := h.svc.Downloads.Repo().Get(id)
	if err != nil || d.DeviceID != dev.ID || p.asKid() && d.AccountID != p.profile.ID {
		return nil, errNotFound
	}
	who, err := h.svc.Accounts.Repo().Get(d.AccountID)
	if err != nil || who.RevokedAt != nil {
		return nil, errNotFound
	}
	return h.svc.Downloads.Repo().Of(d.ID, dev.ID, who.ID, service.AudienceFor(who))
}

// file serves the download's file in ranges; its ETag lets a client resume
// with If-Range.
func (h *downloadFiles) file(w http.ResponseWriter, r *http.Request, d *download.Download) {
	f, m, err := h.svc.Playback.Media(d.MediaFileID)
	if err != nil {
		responseError(w, r, err)
		return
	}
	path, typ := f.Path, fileTypes[f.Container]
	if f.Container == "mp4" && playback.Song(m) {
		typ = "audio/mp4"
	}
	if d.ConversionID != nil {
		c, err := h.ready(r, d)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			responseError(w, r, err)
			return
		}
		path, typ = h.svc.Downloads.Path(c), "video/mp4"
		if c.Container == "m4a" {
			typ = "audio/mp4"
		}
	}
	fh, err := os.Open(path) //nolint:gosec // a path of the server's own records
	if err != nil {
		responseError(w, r, fail(http.StatusConflict, api.ErrorCodeConflict, "the file is not on disk"))
		return
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		responseError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, st.Size(), st.ModTime().UnixNano()))
	if typ != "" {
		w.Header().Set("Content-Type", typ)
	}
	http.ServeContent(w, r, "", st.ModTime(), fh)
}

// ready is the download's conversion once ready. Asked with wait=1, it
// holds until the conversion is ready or failed, a failure answering 500
// with the error getDownload reports. Each time a conversion settles the
// download is found again, so one gone, or a device signed out, meanwhile
// answers as a new request would. Nothing is held while it waits.
func (h *downloadFiles) ready(r *http.Request, d *download.Download) (*download.Conversion, error) {
	wait := r.URL.Query().Get("wait") == "1"
	for {
		settled := h.svc.Downloads.Settled()
		c, err := h.svc.Downloads.Repo().Conversion(*d.ConversionID)
		switch {
		case err != nil:
			return nil, err
		case c.Status == download.Ready:
			return c, nil
		case !wait:
			return nil, fail(http.StatusConflict, api.ErrorCodeConflict, "the conversion is not ready")
		case c.Status == download.Failed:
			return nil, fail(http.StatusInternalServerError, api.ErrorCodeInternal, c.Error)
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-settled:
		}
		if d, err = h.find(r); err != nil {
			return nil, err
		}
	}
}

// subtitle serves a text subtitle of the download's file as WebVTT.
func (h *downloadFiles) subtitle(w http.ResponseWriter, r *http.Request, d *download.Download) {
	f, m, err := h.svc.Playback.Media(d.MediaFileID)
	if err != nil {
		responseError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSuffix(r.PathValue("name"), ".vtt"), 10, 64)
	t := trackByID(m.Tracks, &id)
	if err != nil || t == nil || t.Kind != mediafile.Subtitle || t.SubtitleIsText == nil || !*t.SubtitleIsText {
		responseError(w, r, db.ErrNotFound)
		return
	}
	path, err := h.svc.Playback.Engine.Sidecar(r.Context(), f.ID, f.Path, t.StreamIndex, m.Tracks)
	if err != nil {
		responseError(w, r, fail(http.StatusServiceUnavailable, api.ErrorCodeInternal, "subtitle unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/vtt")
	http.ServeFile(w, r, path) //nolint:gosec // a path in the engine's own cache, named by a probed track
}

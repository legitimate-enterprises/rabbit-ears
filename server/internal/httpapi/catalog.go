package httpapi

import (
	"context"
	"errors"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// audience is what the device's active profile may see.
func audience(ctx context.Context) item.Audience { return service.AudienceFor(caller(ctx).profile) }

// browse is the audience of the active profile's browsing, which an adult
// hiding kids' content browses without it. An admin operation's caller is
// the admin acting as themselves, who browses everything there.
func browse(ctx context.Context) item.Audience {
	if caller(ctx).adminOp {
		return item.Audience{}
	}
	return service.BrowseAudienceFor(caller(ctx).profile)
}

// seen returns an item only if the active profile may see it; anything
// else is not found.
func (s *Server) seen(ctx context.Context, id uid.ID) (*item.Item, error) {
	return s.visibleTo(audience(ctx), id)
}

// catalogItem is a movie, show, season or episode; music and YouTube items
// are not found through the item operations.
func catalogItem(it *item.Item, err error) (*item.Item, error) {
	if err == nil && !slices.Contains(item.CatalogKinds, it.Kind) {
		return nil, errNotFound
	}
	return it, err
}

func (s *Server) visibleTo(a item.Audience, id uid.ID) (*item.Item, error) {
	ok, err := s.svc.Items.Repo().Visible(id, a)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound
	}
	return s.svc.Items.Repo().Get(id)
}

// library is l with its stats, and its folders for an admin only.
func (s *Server) library(l *library.Library, admin bool) (api.Library, error) {
	var folders []*library.Folder
	if admin {
		var err error
		if folders, err = s.svc.Libraries.Repo().Folders(l.ID); err != nil {
			return api.Library{}, err
		}
	}
	kinds, err := s.svc.Items.Repo().KindCounts(l.ID)
	if err != nil {
		return api.Library{}, err
	}
	unmatched, err := s.svc.Items.Repo().CountUnmatched(l.ID)
	if err != nil {
		return api.Library{}, err
	}
	files, bytes, err := s.svc.MediaFiles.Repo().Totals(l.ID)
	if err != nil {
		return api.Library{}, err
	}
	st := api.LibraryStats{Movies: kinds[item.Movie], Shows: kinds[item.Show], Seasons: kinds[item.Season], Episodes: kinds[item.Episode],
		Files: files, Bytes: bytes, Unmatched: unmatched}
	count := func(kind string) *int { n := kinds[kind]; return &n }
	switch l.Kind {
	case library.Music:
		st.Artists, st.Albums, st.Songs = count(item.Artist), count(item.Album), count(item.Song)
	case library.YouTube:
		st.Channels, st.Videos = count(item.Channel), count(item.Video)
	}
	return toLibrary(l, folders, st), nil
}

func libraryFrom(in *api.LibraryInput, l *library.Library) {
	l.Name, l.Kind, l.ScanHour, l.WatchFiles = in.Name, string(in.Kind), or(in.ScanHour, 3), or(in.WatchFiles, true)
}

// ListLibraries pages libraries by name.
func (s *Server) ListLibraries(ctx context.Context, r api.ListLibrariesRequestObject) (api.ListLibrariesResponseObject, error) {
	all, err := s.svc.Libraries.Repo().List()
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	out := api.ListLibraries200JSONResponse{Items: []api.Library{}, NextOffset: next}
	for _, l := range rows {
		v, err := s.library(l, caller(ctx).asAdmin())
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreateLibrary adds a library with no folders.
func (s *Server) CreateLibrary(ctx context.Context, r api.CreateLibraryRequestObject) (api.CreateLibraryResponseObject, error) {
	l := library.Library{ID: or(r.Body.Id, uid.ID{})}
	libraryFrom(r.Body, &l)
	if err := s.svc.Libraries.Repo().Create(&l, actor(ctx)); err != nil {
		return nil, err
	}
	v, err := s.library(&l, true)
	return api.CreateLibrary201JSONResponse(v), err
}

// GetLibrary returns a library with its folders.
func (s *Server) GetLibrary(ctx context.Context, r api.GetLibraryRequestObject) (api.GetLibraryResponseObject, error) {
	l, err := s.svc.Libraries.Repo().Get(r.LibraryId)
	if err != nil {
		return nil, err
	}
	v, err := s.library(l, caller(ctx).asAdmin())
	return api.GetLibrary200JSONResponse(v), err
}

// ListLibraryFolders lists a library's folders by path.
func (s *Server) ListLibraryFolders(ctx context.Context, r api.ListLibraryFoldersRequestObject) (api.ListLibraryFoldersResponseObject, error) {
	if _, err := s.svc.Libraries.Repo().Get(r.LibraryId); err != nil {
		return nil, err
	}
	fs, err := s.svc.Libraries.Repo().Folders(r.LibraryId)
	return api.ListLibraryFolders200JSONResponse{Items: mapAll(fs, toFolder)}, err
}

// GetLibraryFolder returns one folder of the library.
func (s *Server) GetLibraryFolder(ctx context.Context, r api.GetLibraryFolderRequestObject) (api.GetLibraryFolderResponseObject, error) {
	if _, err := s.svc.Libraries.Repo().Get(r.LibraryId); err != nil {
		return nil, err
	}
	fs, err := s.svc.Libraries.Repo().Folders(r.LibraryId)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if f.ID == r.FolderId {
			return api.GetLibraryFolder200JSONResponse(toFolder(f)), nil
		}
	}
	return nil, errNotFound
}

// ReplaceLibrary rewrites a library's settings; folders are kept.
func (s *Server) ReplaceLibrary(ctx context.Context, r api.ReplaceLibraryRequestObject) (api.ReplaceLibraryResponseObject, error) {
	if err := sameID(r.Body.Id, r.LibraryId); err != nil {
		return nil, err
	}
	l, err := s.svc.Libraries.Repo().Get(r.LibraryId)
	if err != nil {
		return nil, err
	}
	libraryFrom(r.Body, l)
	if err := s.svc.Libraries.Repo().Update(l, actor(ctx)); err != nil {
		return nil, err
	}
	v, err := s.library(l, true)
	return api.ReplaceLibrary200JSONResponse(v), err
}

// DeleteLibrary removes a library and everything scanned from it.
func (s *Server) DeleteLibrary(_ context.Context, r api.DeleteLibraryRequestObject) (api.DeleteLibraryResponseObject, error) {
	return api.DeleteLibrary204Response{}, s.svc.Libraries.Repo().Delete(r.LibraryId)
}

// AddLibraryFolder attaches a root path; a path may belong to one library.
func (s *Server) AddLibraryFolder(ctx context.Context, r api.AddLibraryFolderRequestObject) (api.AddLibraryFolderResponseObject, error) {
	if _, err := s.svc.Libraries.Repo().Get(r.LibraryId); err != nil {
		return nil, err
	}
	f := &library.Folder{ID: or(r.Body.Id, uid.ID{}), LibraryID: r.LibraryId, Path: r.Body.Path}
	if err := s.svc.Libraries.AddFolder(f, actor(ctx)); err != nil {
		return nil, err
	}
	return api.AddLibraryFolder201JSONResponse(toFolder(f)), nil
}

// ReplaceLibraryFolder gives a folder of this library a new path.
func (s *Server) ReplaceLibraryFolder(ctx context.Context, r api.ReplaceLibraryFolderRequestObject) (api.ReplaceLibraryFolderResponseObject, error) {
	if err := sameID(r.Body.Id, r.FolderId); err != nil {
		return nil, err
	}
	f := &library.Folder{ID: r.FolderId, LibraryID: r.LibraryId, Path: r.Body.Path}
	if err := s.svc.Libraries.UpdateFolder(f, actor(ctx)); err != nil {
		return nil, err
	}
	return api.ReplaceLibraryFolder200JSONResponse(toFolder(f)), nil
}

// RemoveLibraryFolder detaches a root path of this library.
func (s *Server) RemoveLibraryFolder(_ context.Context, r api.RemoveLibraryFolderRequestObject) (api.RemoveLibraryFolderResponseObject, error) {
	folders, err := s.svc.Libraries.Repo().Folders(r.LibraryId)
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		if f.ID == r.FolderId {
			return api.RemoveLibraryFolder204Response{}, s.svc.Libraries.RemoveFolder(f)
		}
	}
	return nil, errNotFound
}

func (s *Server) item(it *item.Item, admin bool) (api.Item, error) {
	m, err := s.svc.Items.Repo().Metadata(it.ID)
	if errors.Is(err, db.ErrNotFound) {
		m, err = nil, nil
	}
	if err != nil {
		return api.Item{}, err
	}
	genres, err := s.svc.Genres.Repo().ForItem(it.ID)
	if err != nil {
		return api.Item{}, err
	}
	gone, err := s.svc.MediaFiles.Repo().Unavailable([]uid.ID{it.ID})
	out, gs := toItem(it, m, admin), mapAll(genres, toGenre)
	out.Genres, out.Unavailable = &gs, optTrue(gone[it.ID])
	return out, err
}

// itemFrom copies the input after checking the tree rule the schema cannot
// express: seasons and episodes have a parent, movies and shows do not.
func itemFrom(in *api.ItemInput, it *item.Item) error {
	child := in.Kind == api.ItemKindSeason || in.Kind == api.ItemKindEpisode
	if child != (in.ParentId != nil) {
		return invalid("parentId", "seasons and episodes need a parent; movies and shows have none")
	}
	it.LibraryID, it.ParentID, it.Kind, it.Path = in.LibraryId, in.ParentId, string(in.Kind), or(in.Path, "")
	it.ParsedTitle, it.ParsedYear, it.SeasonNumber, it.EpisodeNumber = in.ParsedTitle, in.ParsedYear, in.SeasonNumber, in.EpisodeNumber
	return nil
}

// ListItems pages the items the active profile may see, optionally under one
// library, parent or kind, matching a title, genre, person or occasion.
func (s *Server) ListItems(ctx context.Context, r api.ListItemsRequestObject) (api.ListItemsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	// The Kids filter and a title's own children are looked at on purpose, so Hide does not apply.
	a, kidsOnly := browse(ctx), or(r.Params.KidsContent, false)
	if kidsOnly || r.Params.ParentId != nil {
		a = audience(ctx)
	}
	rows, err := s.svc.Items.Repo().List(item.Filter{LibraryID: r.Params.LibraryId, ParentID: r.Params.ParentId, KidsOnly: kidsOnly,
		Kind: enumStr(r.Params.Kind), Q: or(r.Params.Q, ""), GenreID: r.Params.GenreId, PersonID: r.Params.PersonId, OccasionID: r.Params.OccasionId,
		Catalog: true, UHD: r.Params.Resolution != nil, Newest: r.Params.Sort != nil && *r.Params.Sort == api.ItemSortAdded, Audience: a, Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	out := api.ListItems200JSONResponse{}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items, err = s.items(rows, caller(ctx).asAdmin())
	return out, err
}

// items converts a page of catalog items with their metadata, genres and
// whether they are unavailable.
func (s *Server) items(rows []*item.Item, admin bool) ([]api.Item, error) {
	ids := make([]uid.ID, len(rows))
	for i, it := range rows {
		ids[i] = it.ID
	}
	metas, err := s.svc.Items.Repo().MetadataFor(ids)
	if err != nil {
		return nil, err
	}
	genres, err := s.svc.Genres.Repo().ForItems(ids)
	if err != nil {
		return nil, err
	}
	gone, err := s.svc.MediaFiles.Repo().Unavailable(ids)
	if err != nil {
		return nil, err
	}
	out := []api.Item{}
	for _, it := range rows {
		v, gs := toItem(it, metas[it.ID], admin), mapAll(genres[it.ID], toGenre)
		v.Genres, v.Unavailable = &gs, optTrue(gone[it.ID])
		out = append(out, v)
	}
	return out, nil
}

// CreateItem adds an item by hand; the scanner is the usual writer.
func (s *Server) CreateItem(_ context.Context, r api.CreateItemRequestObject) (api.CreateItemResponseObject, error) {
	var it item.Item
	if err := itemFrom(r.Body, &it); err != nil {
		return nil, err
	}
	if err := s.svc.Items.Repo().Create(&it); err != nil {
		return nil, err
	}
	return api.CreateItem201JSONResponse(toItem(&it, nil, true)), nil
}

// GetItem returns an item and its metadata, and a movie's collections.
func (s *Server) GetItem(ctx context.Context, r api.GetItemRequestObject) (api.GetItemResponseObject, error) {
	it, err := catalogItem(s.seen(ctx, r.ItemId))
	if err != nil {
		return nil, err
	}
	v, err := s.item(it, caller(ctx).asAdmin())
	if err == nil && it.Kind == item.Movie {
		v.Collections, err = s.movieCollections(ctx, it.ID)
	}
	if err == nil && (it.Kind == item.Movie || it.Kind == item.Episode) {
		v.Editions, err = s.editions(it.ID)
	}
	return api.GetItem200JSONResponse(v), err
}

// editions lists an item's editions that have files, each with its copies.
func (s *Server) editions(itemID uid.ID) (*[]api.Edition, error) {
	eds, err := s.svc.Editions.Repo().ForItem(itemID)
	if err != nil {
		return nil, err
	}
	out := []api.Edition{}
	for _, ed := range eds {
		copies, err := s.svc.Editions.Repo().Copies(ed.ID)
		if err != nil {
			return nil, err
		}
		v := api.Edition{Id: ed.ID, Name: ed.Name, IsDefault: ed.Default, RuntimeMs: ed.RuntimeMs, Copies: []api.EditionCopy{}}
		for _, c := range copies {
			v.Copies = append(v.Copies, api.EditionCopy{MediaFileId: c.MediaFileID, Quality: optStr(c.Quality()), Width: c.Width,
				Height: c.Height, HdrFormat: optEnum[api.HdrFormat](c.HDRFormat), Unavailable: optTrue(c.Missing)})
		}
		out = append(out, v)
	}
	return &out, nil
}

// GetItemArtwork serves an image the active profile may see.
func (s *Server) GetItemArtwork(ctx context.Context, r api.GetItemArtworkRequestObject) (api.GetItemArtworkResponseObject, error) {
	it, err := s.seen(ctx, r.ItemId)
	if err != nil {
		return nil, err
	}
	path, err := s.svc.Items.Artwork(ctx, it, audience(ctx), r.Kind == api.Backdrop)
	if err != nil {
		return nil, err
	}
	f, size, tag, err := openArtwork(path, r.Params.IfNoneMatch)
	if err != nil || f == nil {
		return api.GetItemArtwork304Response{Headers: api.GetItemArtwork304ResponseHeaders{CacheControl: artworkCacheFor(r.Params.V), ETag: tag}}, err
	}
	return api.GetItemArtwork200ImageResponse{Body: f, ContentType: mime.TypeByExtension(filepath.Ext(path)), ContentLength: size,
		Headers: api.GetItemArtwork200ResponseHeaders{CacheControl: artworkCacheFor(r.Params.V), ETag: tag}}, nil
}

const artworkCache = "private, max-age=86400"

// artworkCacheFor keeps an image asked for under a generation until the
// generation moves, which is when the client asks under another address;
// one asked for without is revalidated daily.
func artworkCacheFor(generation *api.ArtworkGeneration) string {
	if generation == nil {
		return artworkCache
	}
	return "private, max-age=31536000, immutable"
}

// openArtwork opens a cached provider image. The provider names each image
// uniquely, so size and name are a strong validator; a new image after a
// rematch changes it. f is nil when ifNoneMatch already names the image.
func openArtwork(path string, ifNoneMatch *string) (f *os.File, size int64, tag string, err error) {
	tag = `"` + filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path) + `"`
	if ifNoneMatch != nil && strings.Contains(*ifNoneMatch, tag) {
		return nil, 0, tag, nil
	}
	f, err = os.Open(path) //nolint:gosec // path is under the image cache, built from a validated provider name
	if err != nil {
		return nil, 0, tag, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, tag, err
	}
	return f, st.Size(), tag, nil
}

// ReplaceItem rewrites an item's file-derived fields.
func (s *Server) ReplaceItem(_ context.Context, r api.ReplaceItemRequestObject) (api.ReplaceItemResponseObject, error) {
	it, err := catalogItem(s.svc.Items.Repo().Get(r.ItemId))
	if err != nil {
		return nil, err
	}
	b := r.Body
	if string(b.Kind) != it.Kind || b.LibraryId != it.LibraryID || (b.ParentId == nil) != (it.ParentID == nil) ||
		b.ParentId != nil && *b.ParentId != *it.ParentID {
		return nil, invalid("kind", "an item keeps its kind, library and parent")
	}
	if err := itemFrom(r.Body, it); err != nil {
		return nil, err
	}
	if err := s.svc.Items.Repo().Update(it); err != nil {
		return nil, err
	}
	v, err := s.item(it, true)
	return api.ReplaceItem200JSONResponse(v), err
}

// DeleteItem removes an item and its subtree.
func (s *Server) DeleteItem(_ context.Context, r api.DeleteItemRequestObject) (api.DeleteItemResponseObject, error) {
	if _, err := catalogItem(s.svc.Items.Repo().Get(r.ItemId)); err != nil {
		return nil, err
	}
	return api.DeleteItem204Response{}, s.svc.Items.Repo().Delete(r.ItemId)
}

// PutItemMetadata stores a manual match as the item's remote row and
// returns the item as the metadata settings show it.
func (s *Server) PutItemMetadata(_ context.Context, r api.PutItemMetadataRequestObject) (api.PutItemMetadataResponseObject, error) {
	it, err := catalogItem(s.svc.Items.Repo().Get(r.ItemId))
	if err != nil {
		return nil, err
	}
	in := r.Body
	now := time.Now().UTC()
	m := &item.Metadata{ItemID: it.ID, Provider: string(or(in.Provider, api.ItemMetadataProviderTmdb)), ProviderID: in.ProviderId, Status: string(in.Status),
		Title: or(in.Title, ""), Overview: or(in.Overview, ""), RuntimeMinutes: in.RuntimeMinutes, ContentRating: or(in.ContentRating, ""),
		PosterPath: or(in.PosterPath, ""), BackdropPath: or(in.BackdropPath, ""), FetchedAt: &now}
	if in.ReleaseDate != nil {
		m.ReleaseDate = in.ReleaseDate.Format(time.DateOnly)
	}
	rematched, err := s.svc.Items.Repo().MatchByHand(m, now)
	if err != nil {
		return nil, err
	}
	if rematched {
		if err := s.svc.Reviews.Repo().Settle(it.ID, "", review.OfIdentity...); err != nil {
			return nil, err
		}
		if err := s.runMetadata(now); err != nil {
			return nil, err
		}
	}
	v, err := s.item(it, true)
	return api.PutItemMetadata200JSONResponse(v), err
}

// DeleteItemMetadata unmatches an item, dropping its remote row, a show's
// episodes' rows and the reviews of the match; what its files say stays.
func (s *Server) DeleteItemMetadata(_ context.Context, r api.DeleteItemMetadataRequestObject) (api.DeleteItemMetadataResponseObject, error) {
	if _, err := catalogItem(s.svc.Items.Repo().Get(r.ItemId)); err != nil {
		return nil, err
	}
	if err := s.svc.Items.Repo().DeleteMetadata(r.ItemId); err != nil {
		return nil, err
	}
	if err := s.svc.Items.Repo().DeleteEpisodeMetadata(r.ItemId); err != nil {
		return nil, err
	}
	return api.DeleteItemMetadata204Response{}, s.svc.Reviews.Repo().Settle(r.ItemId, "", review.OfIdentity...)
}

func (s *Server) mediaFile(f *mediafile.File, admin bool) (api.MediaFile, error) {
	tracks, err := s.svc.MediaFiles.Repo().Tracks(f.ID, "")
	if err != nil {
		return api.MediaFile{}, err
	}
	chapters, err := s.svc.MediaFiles.Repo().Chapters(f.ID)
	return toMediaFile(f, tracks, chapters, admin), err
}

func mediaFileFrom(in *api.MediaFileInput, f *mediafile.File) {
	f.ItemID, f.Path, f.SizeBytes, f.FileModifiedAt = &in.ItemId, in.Path, in.SizeBytes, in.FileModifiedAt
	f.Container, f.DurationMs, f.Bitrate = or(in.Container, ""), in.DurationMs, in.Bitrate
}

// ListMediaFiles pages an item's files, or for an admin the files with a
// problem.
func (s *Server) ListMediaFiles(ctx context.Context, r api.ListMediaFilesRequestObject) (api.ListMediaFilesResponseObject, error) {
	if r.Params.Problem != nil {
		return s.problemFiles(ctx, r.Params)
	}
	if r.Params.ItemId == nil {
		return nil, invalid("itemId", "itemId or problem is required")
	}
	if audience(ctx).Kid {
		if _, err := s.seen(ctx, *r.Params.ItemId); err != nil {
			return nil, err
		}
	}
	all, err := s.svc.MediaFiles.Repo().FilesForItem(*r.Params.ItemId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	out := api.ListMediaFiles200JSONResponse{Items: []api.MediaFile{}, NextOffset: next}
	for _, f := range rows {
		v, err := s.mediaFile(f, caller(ctx).asAdmin())
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

func (s *Server) problemFiles(ctx context.Context, p api.ListMediaFilesParams) (api.ListMediaFilesResponseObject, error) {
	if !caller(ctx).asAdmin() {
		return nil, errForbidden
	}
	all, err := s.svc.MediaFiles.Repo().Problems(string(*p.Problem), p.LibraryId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(p.Limit, p.Offset)
	rows, next := slicePage(all, limit, offset)
	out := api.ListMediaFiles200JSONResponse{Items: []api.MediaFile{}, NextOffset: next}
	for _, f := range rows {
		v, err := s.mediaFile(f.File, true)
		if err != nil {
			return nil, err
		}
		v.LibraryId = f.LibraryID
		v.Title = text(f.Title)
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// ProbeMediaFile is Read again: the file's library scan probes it anew.
func (s *Server) ProbeMediaFile(_ context.Context, r api.ProbeMediaFileRequestObject) (api.ProbeMediaFileResponseObject, error) {
	f, err := s.svc.MediaFiles.Repo().File(r.MediaFileId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.MediaFiles.Repo().ForgetProbe(f.ID); err != nil {
		return nil, err
	}
	if f.ItemID != nil {
		it, err := s.svc.Items.Repo().Get(*f.ItemID)
		if err != nil {
			return nil, err
		}
		if err := s.svc.Libraries.RequestScan(it.LibraryID); err != nil {
			return nil, err
		}
	}
	return api.ProbeMediaFile202Response{}, nil
}

// CreateMediaFile registers a file before it is probed.
func (s *Server) CreateMediaFile(_ context.Context, r api.CreateMediaFileRequestObject) (api.CreateMediaFileResponseObject, error) {
	var f mediafile.File
	mediaFileFrom(r.Body, &f)
	if err := s.svc.MediaFiles.Repo().CreateFile(&f); err != nil {
		return nil, err
	}
	v, err := s.mediaFile(&f, true)
	return api.CreateMediaFile201JSONResponse(v), err
}

// GetMediaFile returns a file with tracks and chapters.
func (s *Server) GetMediaFile(ctx context.Context, r api.GetMediaFileRequestObject) (api.GetMediaFileResponseObject, error) {
	f, err := s.svc.MediaFiles.Repo().File(r.MediaFileId)
	if err != nil {
		return nil, err
	}
	if f.ItemID == nil {
		return nil, errNotFound
	}
	if _, err := s.seen(ctx, *f.ItemID); err != nil {
		return nil, err
	}
	v, err := s.mediaFile(f, caller(ctx).asAdmin())
	return api.GetMediaFile200JSONResponse(v), err
}

// ReplaceMediaFile rewrites a file's stat fields; probe results are kept.
func (s *Server) ReplaceMediaFile(_ context.Context, r api.ReplaceMediaFileRequestObject) (api.ReplaceMediaFileResponseObject, error) {
	f, err := s.svc.MediaFiles.Repo().File(r.MediaFileId)
	if err != nil {
		return nil, err
	}
	mediaFileFrom(r.Body, f)
	if err := s.svc.MediaFiles.Repo().UpdateFile(f); err != nil {
		return nil, err
	}
	v, err := s.mediaFile(f, true)
	return api.ReplaceMediaFile200JSONResponse(v), err
}

// DeleteMediaFile removes a file record and its tracks.
func (s *Server) DeleteMediaFile(_ context.Context, r api.DeleteMediaFileRequestObject) (api.DeleteMediaFileResponseObject, error) {
	return api.DeleteMediaFile204Response{}, s.svc.MediaFiles.Repo().DeleteFile(r.MediaFileId)
}

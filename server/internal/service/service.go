// Package service holds one service per domain. Services take their
// repositories by constructor and know nothing of HTTP.
package service

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/favorite"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/gravatar"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/joincode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/sysinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/together"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// Services wires every domain service over one database.
type Services struct {
	Libraries   *LibraryService
	Items       *ItemService
	Genres      *GenreService
	Occasions   *OccasionService
	People      *PersonService
	MediaFiles  *MediaFileService
	Editions    *EditionService
	Previews    *SeekPreviewService
	Reviews     *ReviewService
	Accounts    *AccountService
	Households  *HouseholdService
	Devices     *DeviceService
	JoinCodes   *JoinCodeService
	DeviceLinks *DeviceLinkService
	AdultLinks  *AdultLinkService
	Offers      *OfferService
	Friends     *FriendService
	Sessions    *SessionService
	Playback    *PlaybackService
	WatchStates *WatchStateService
	Favorites   *FavoriteService
	Tasks       *TaskService
	Auth        *AuthService
	SSO         *SSOService
	Music       *MusicService
	YouTube     *YouTubeService
	Playlists   *PlaylistService
	Collections *CollectionService
	Stations    *StationService
	Covers      *CoverService
	Channels    *ChannelService
	Together    *WatchTogetherService
	Local       *LocalCheckService
	UpNext      *UpNextService
	Related     *RelatedService
	Downloads   *DownloadService
	// Events is the stream every signed-in client follows; EventHold is
	// how long a poll of it waits for something to tell.
	Events    *live.Hub
	EventHold time.Duration
	// Sync serves native clients' replicas; its long poll holds for
	// EventHold too.
	Sync    *replica.Sync
	Server  *serverinfo.Repo
	Logs    *logstore.Repo
	Sampler sysinfo.Sampler
	// DataDir holds caches, artwork and thumbnails; its volume is the one
	// whose storage the admin sees.
	DataDir string
	// Port and DatabasePath are where the server listens and its database
	// file, as resolved at start-up.
	Port         int
	DatabasePath string
	TMDB         *TMDBService
	// Avatars keeps adults' Gravatar pictures; nil shows none.
	Avatars *gravatar.Cache
}

// New builds every service and repository over d.
func New(d *sql.DB) *Services {
	items := NewItemService(item.New(d))
	events := live.NewHub(eventBacklog)
	playback := NewPlaybackService(session.New(d), mediafile.New(d), device.New(d), transcode.New(transcode.Config{}))
	stations := NewStationService(station.New(d), playlist.New(d), item.New(d))
	watching := NewWatchTogetherService(together.New(d), item.New(d), account.New(d))
	playback.Events, stations.Events, watching.Events = events, events, events
	s := &Services{
		Events:      events,
		EventHold:   Hold,
		Sync:        replica.New(d),
		Libraries:   NewLibraryService(library.New(d), task.New(d)),
		Items:       items,
		Genres:      NewGenreService(genre.New(d)),
		Occasions:   NewOccasionService(occasion.New(d), item.New(d)),
		People:      NewPersonService(person.New(d)),
		MediaFiles:  NewMediaFileService(mediafile.New(d)),
		Editions:    NewEditionService(edition.New(d)),
		Previews:    NewSeekPreviewService(seekpreview.New(d)),
		Reviews:     NewReviewService(review.New(d)),
		Accounts:    NewAccountService(account.New(d), d),
		Households:  NewHouseholdService(household.New(d), d),
		Devices:     NewDeviceService(device.New(d), account.New(d)),
		JoinCodes:   NewJoinCodeService(joincode.New(d)),
		DeviceLinks: NewDeviceLinkService(d),
		AdultLinks:  NewAdultLinkService(d),
		Offers:      NewOfferService(d),
		Friends:     NewFriendService(d),
		Sessions:    NewSessionService(session.New(d)),
		Playback:    playback,
		WatchStates: NewWatchStateService(watchstate.New(d)),
		Favorites:   NewFavoriteService(favorite.New(d)),
		Tasks:       NewTaskService(task.New(d)),
		Auth:        NewAuthService(d),
		SSO:         NewSSOService(d),
		Music:       NewMusicService(item.New(d)),
		YouTube:     NewYouTubeService(youtube.New(d)),
		Playlists:   NewPlaylistService(playlist.New(d), edition.New(d), station.New(d), account.New(d)),
		Collections: NewCollectionService(collection.New(d), items),
		Stations:    stations,
		Covers:      NewCoverService(cover.New(d), playlist.New(d), item.New(d)),
		Together:    watching,
		Local:       NewLocalCheckService(),
		Downloads:   NewDownloadService(download.New(d), edition.New(d), mediafile.New(d), watchstate.New(d), playback),
		UpNext:      NewUpNextService(item.New(d), collection.New(d), youtube.New(d), playlist.New(d), mediafile.New(d)),
		Related:     NewRelatedService(item.New(d), collection.New(d)),
		Server:      serverinfo.New(d),
		Logs:        logstore.New(d),
		Sampler:     sysinfo.New(),
		DataDir:     os.TempDir(),
		TMDB:        NewTMDBService(serverinfo.New(d)),
	}
	s.Channels = NewChannelService(s.Stations, s.Playback, playlist.New(d), item.New(d), mediafile.New(d), session.New(d))
	return s
}

// ref is a pointer to a copy of id, which no later change to its source
// reaches.
func ref(id uid.ID) *uid.ID { return &id }

// eventBacklog is how many events the stream keeps; a follower further
// back than that reloads what it shows.
const eventBacklog = 512

// LibraryService manages libraries and their folders.
type LibraryService struct {
	repo  *library.Repo
	tasks *task.Repo
}

// NewLibraryService returns a LibraryService over repo whose folder changes
// request scans through tasks.
func NewLibraryService(repo *library.Repo, tasks *task.Repo) *LibraryService {
	return &LibraryService{repo, tasks}
}

// Repo exposes the repository.
func (s *LibraryService) Repo() *library.Repo { return s.repo }

// AddFolder attaches f's root path to its library and requests its scan.
func (s *LibraryService) AddFolder(f *library.Folder, by *uid.ID) error {
	if err := s.repo.AddFolder(f, by); err != nil {
		return err
	}
	return s.RequestScan(f.LibraryID)
}

// UpdateFolder gives a folder a new path and requests the library's scan,
// which marks the files under the old path missing.
func (s *LibraryService) UpdateFolder(f *library.Folder, by *uid.ID) error {
	if err := s.repo.UpdateFolder(f, by); err != nil {
		return err
	}
	return s.RequestScan(f.LibraryID)
}

// RemoveFolder detaches a root path and requests the library's scan, which
// marks the folder's files missing.
func (s *LibraryService) RemoveFolder(f *library.Folder) error {
	if err := s.repo.RemoveFolder(f.ID); err != nil {
		return err
	}
	return s.RequestScan(f.LibraryID)
}

// RequestScan is Run now on the library's scan task. A library the
// scheduler has not synced yet has none, and its first scan is due at once.
func (s *LibraryService) RequestScan(libraryID uid.ID) error {
	ts, err := s.tasks.List()
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Kind == scan.ScanKind(libraryID) {
			return s.tasks.RequestRun(t.ID, time.Now().UTC())
		}
	}
	return nil
}

// ItemService manages the item tree and its provider metadata.
type ItemService struct {
	repo *item.Repo
	// Images caches provider artwork under ImageDir; nil serves none.
	Images   ImageCache
	ImageDir string
}

// tmdbProvider names TMDB's images, which are paths to download rather
// than files on disk.
const tmdbProvider = "tmdb"

// ImageCache downloads a provider image into dir unless it is already there.
type ImageCache interface {
	CacheImage(ctx context.Context, dir, size, path string) (string, error)
}

// NewItemService returns an ItemService over repo.
func NewItemService(repo *item.Repo) *ItemService { return &ItemService{repo: repo} }

// Repo exposes the repository.
func (s *ItemService) Repo() *item.Repo { return s.repo }

// Artwork returns the local file of the item's poster or backdrop,
// downloading a provider image on first use. An item without one borrows
// its nearest ancestor's, so an episode without a still shows its show and
// a song its album. Music and YouTube art, and local art of movies and TV,
// is a file the scanner recorded. An artist shows its newest album's that
// the audience may hear. db.ErrNotFound when none has one.
func (s *ItemService) Artwork(ctx context.Context, it *item.Item, a item.Audience, backdrop bool) (string, error) {
	if it.Kind == item.Artist {
		return s.artistArt(ctx, it.ID, a, backdrop)
	}
	for {
		m, err := s.repo.Metadata(it.ID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return "", err
		}
		size, path, from := "w500", "", ""
		if m != nil {
			path, from = m.PosterPath, m.PosterFrom
			if backdrop {
				size, path, from = "w1280", m.BackdropPath, m.BackdropFrom
			}
		}
		switch {
		case path != "" && from != tmdbProvider:
			if _, err := os.Stat(path); err != nil {
				return "", db.ErrNotFound
			}
			return path, nil
		case path != "" && s.Images == nil:
			return "", db.ErrNotFound
		case path != "":
			return s.Images.CacheImage(ctx, s.ImageDir, size, path)
		case it.ParentID == nil:
			return "", db.ErrNotFound
		}
		if it, err = s.repo.Get(*it.ParentID); err != nil {
			return "", err
		}
	}
}

// Stored is the file of the image at the address store/name, a TMDB one
// downloaded on first use while the data shows it; db.ErrNotFound
// otherwise.
func (s *ItemService) Stored(ctx context.Context, store, name string) (string, error) {
	var p string
	tmdb := store == "w185" || store == "w500" || store == "w1280"
	switch {
	case (tmdb || store == "caa") && s.ImageDir != "":
		p = filepath.Join(s.ImageDir, store, name)
	case store == "local" || store == "music":
		p = db.DataAbs(filepath.Join("thumbnails", store, name))
	case store == "youtube":
		p = db.DataAbs(filepath.Join("cache", "youtube", name))
	default:
		return "", db.ErrNotFound
	}
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if !tmdb || s.Images == nil {
		return "", db.ErrNotFound
	}
	if shown, err := s.repo.NamesTMDB(store, "/"+name); err != nil || !shown {
		return "", cmp.Or(err, db.ErrNotFound)
	}
	return s.Images.CacheImage(ctx, s.ImageDir, store, "/"+name)
}

// artistArt finds an artist's newest album with art; artists have no
// backdrop.
func (s *ItemService) artistArt(ctx context.Context, id uid.ID, a item.Audience, backdrop bool) (string, error) {
	if backdrop {
		return "", db.ErrNotFound
	}
	albums, err := s.repo.Albums(item.MusicFilter{ArtistID: &id, Sort: "year", Clean: a.Kid, Limit: 1000})
	if err != nil {
		return "", err
	}
	for _, al := range albums {
		if p, err := s.Artwork(ctx, al.Item, a, false); !errors.Is(err, db.ErrNotFound) {
			return p, err
		}
	}
	return "", db.ErrNotFound
}

// GenreService manages genres and their item links.
type GenreService struct{ repo *genre.Repo }

// NewGenreService returns a GenreService over repo.
func NewGenreService(repo *genre.Repo) *GenreService { return &GenreService{repo} }

// Repo exposes the repository.
func (s *GenreService) Repo() *genre.Repo { return s.repo }

// PersonService manages people, their credits and headshots.
type PersonService struct {
	repo *person.Repo
	// Images caches provider headshots under ImageDir; nil serves none.
	Images   ImageCache
	ImageDir string
}

// NewPersonService returns a PersonService over repo.
func NewPersonService(repo *person.Repo) *PersonService { return &PersonService{repo: repo} }

// Repo exposes the repository.
func (s *PersonService) Repo() *person.Repo { return s.repo }

// Artwork returns the local file of the person's headshot, downloading it on
// first use; db.ErrNotFound when they have none.
func (s *PersonService) Artwork(ctx context.Context, p *person.Person) (string, error) {
	if s.Images == nil || p.ProfilePath == "" {
		return "", db.ErrNotFound
	}
	return s.Images.CacheImage(ctx, s.ImageDir, "w185", p.ProfilePath)
}

// MediaFileService manages files, tracks and chapters.
type MediaFileService struct{ repo *mediafile.Repo }

// NewMediaFileService returns a MediaFileService over repo.
func NewMediaFileService(repo *mediafile.Repo) *MediaFileService { return &MediaFileService{repo} }

// Repo exposes the repository.
func (s *MediaFileService) Repo() *mediafile.Repo { return s.repo }

// EditionService reads the editions the scanner sorts files into.
type EditionService struct{ repo *edition.Repo }

// NewEditionService returns an EditionService over repo.
func NewEditionService(repo *edition.Repo) *EditionService { return &EditionService{repo} }

// Repo exposes the repository.
func (s *EditionService) Repo() *edition.Repo { return s.repo }

// ReviewService reads and dismisses the identity reviews scans raise.
type ReviewService struct{ repo *review.Repo }

// NewReviewService returns a ReviewService over repo.
func NewReviewService(repo *review.Repo) *ReviewService { return &ReviewService{repo} }

// Repo exposes the repository.
func (s *ReviewService) Repo() *review.Repo { return s.repo }

// SeekPreviewService reads the seek previews the Seek previews task makes.
type SeekPreviewService struct{ repo *seekpreview.Repo }

// NewSeekPreviewService returns a SeekPreviewService over repo.
func NewSeekPreviewService(repo *seekpreview.Repo) *SeekPreviewService {
	return &SeekPreviewService{repo}
}

// Repo exposes the repository.
func (s *SeekPreviewService) Repo() *seekpreview.Repo { return s.repo }

// AccountService manages adults, kids, PINs and rating rules.
type AccountService struct {
	repo *account.Repo
	d    *sql.DB
}

// NewAccountService returns an AccountService over repo; d runs the delete,
// which touches an account and devices together.
func NewAccountService(repo *account.Repo, d *sql.DB) *AccountService {
	return &AccountService{repo, d}
}

// Repo exposes the repository.
func (s *AccountService) Repo() *account.Repo { return s.repo }

// HouseholdService manages households.
type HouseholdService struct {
	repo *household.Repo
	d    *sql.DB
}

// NewHouseholdService returns a HouseholdService over repo; d runs the
// writes that touch a household and its adult together.
func NewHouseholdService(repo *household.Repo, d *sql.DB) *HouseholdService {
	return &HouseholdService{repo, d}
}

// Repo exposes the repository.
func (s *HouseholdService) Repo() *household.Repo { return s.repo }

// DeviceService manages signed-in devices.
type DeviceService struct {
	repo     *device.Repo
	accounts *account.Repo
}

// NewDeviceService returns a DeviceService over repo; accounts is read to
// refuse tokens of revoked adults.
func NewDeviceService(repo *device.Repo, accounts *account.Repo) *DeviceService {
	return &DeviceService{repo, accounts}
}

// Repo exposes the repository.
func (s *DeviceService) Repo() *device.Repo { return s.repo }

// JoinCodeService manages join codes.
type JoinCodeService struct{ repo *joincode.Repo }

// NewJoinCodeService returns a JoinCodeService over repo.
func NewJoinCodeService(repo *joincode.Repo) *JoinCodeService { return &JoinCodeService{repo} }

// Repo exposes the repository.
func (s *JoinCodeService) Repo() *joincode.Repo { return s.repo }

// SessionService manages playback sessions and their plans.
type SessionService struct{ repo *session.Repo }

// NewSessionService returns a SessionService over repo.
func NewSessionService(repo *session.Repo) *SessionService { return &SessionService{repo} }

// Repo exposes the repository.
func (s *SessionService) Repo() *session.Repo { return s.repo }

// WatchStateService manages per-account watch progress.
type WatchStateService struct{ repo *watchstate.Repo }

// NewWatchStateService returns a WatchStateService over repo.
func NewWatchStateService(repo *watchstate.Repo) *WatchStateService { return &WatchStateService{repo} }

// Repo exposes the repository.
func (s *WatchStateService) Repo() *watchstate.Repo { return s.repo }

// FavoriteService manages the items each profile starred.
type FavoriteService struct{ repo *favorite.Repo }

// NewFavoriteService returns a FavoriteService over repo.
func NewFavoriteService(repo *favorite.Repo) *FavoriteService { return &FavoriteService{repo} }

// Repo exposes the repository.
func (s *FavoriteService) Repo() *favorite.Repo { return s.repo }

// TaskService manages tasks and their runs.
type TaskService struct{ repo *task.Repo }

// NewTaskService returns a TaskService over repo.
func NewTaskService(repo *task.Repo) *TaskService { return &TaskService{repo} }

// Repo exposes the repository.
func (s *TaskService) Repo() *task.Repo { return s.repo }

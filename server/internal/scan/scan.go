// Package scan turns library folders into items, probed tracks, chapter
// thumbnails and provider metadata, and runs that on a schedule and on file
// change. A provider failure never fails a scan.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/parse"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/thumbs"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// Provider is the metadata source; *tmdb.Client implements it.
type Provider interface {
	Enabled() bool
	Find(ctx context.Context, source, id string) (movies, shows []tmdb.Result, err error)
	SearchMovie(ctx context.Context, title string, year *int) ([]tmdb.Result, error)
	SearchTV(ctx context.Context, title string, year *int) ([]tmdb.Result, error)
	Movie(ctx context.Context, id int64) (*tmdb.Details, error)
	TV(ctx context.Context, id int64) (*tmdb.Details, error)
	Season(ctx context.Context, tvID int64, season int) ([]tmdb.Episode, error)
	Collection(ctx context.Context, id int64) (*tmdb.CollectionRef, error)
	CacheImage(ctx context.Context, dir, size, path string) (string, error)
}

// MusicProvider is the music metadata source; *musicbrainz.Client implements it.
type MusicProvider interface {
	SearchRelease(ctx context.Context, artist, album string) ([]musicbrainz.Release, error)
	Release(ctx context.Context, id string) (*musicbrainz.Details, error)
	ReleaseGroup(ctx context.Context, id string) (*musicbrainz.Details, error)
	Artist(ctx context.Context, id string) (*musicbrainz.Details, error)
	CacheCover(ctx context.Context, dir, releaseID string) (string, error)
}

// ChannelAvatars is YouTube's channel pictures; *youtube.Avatars implements it.
type ChannelAvatars interface {
	Fetch(ctx context.Context, channelID string) (string, error)
}

// Config wires a Scanner. Empty ImageDir or ThumbDir disables that output.
type Config struct {
	Libraries *library.Repo
	Items     *item.Repo
	Files     *mediafile.Repo
	Editions  *edition.Repo
	Genres    *genre.Repo
	People    *person.Repo
	// Collections records the TMDB collection each matched movie is in;
	// nil records none.
	Collections *collection.Repo
	Provider    Provider
	// Music looks albums up; nil leaves them with what their files say.
	Music    MusicProvider
	ImageDir string
	ThumbDir string
	// Previews records the seek preview frames made under PreviewDir, one
	// directory per file ID.
	Previews   *seekpreview.Repo
	PreviewDir string
	Now        func() time.Time
	// Probe and Thumb default to ffprobe and ffmpeg; tests replace them.
	Probe func(ctx context.Context, path string) (*probe.Result, error)
	// Fingerprint identifies a file by its media; it defaults to ffmpeg.
	Fingerprint func(ctx context.Context, path string) (string, error)
	Thumb       func(ctx context.Context, input string, position int, startMs, endMs int64, out string) error
	// Cover extracts an audio file's embedded picture; it defaults to ffmpeg.
	Cover func(ctx context.Context, input, out string) error
	// Picture extracts a video's attached picture by stream, and Art a
	// video's frame and the poster cropped from it; both default to ffmpeg.
	Picture func(ctx context.Context, input string, stream int, out string) error
	Art     func(ctx context.Context, input string, durationMs int64, frame, poster string) error
	// YouTube records videos' publish times and sidecar stamps; scanning
	// a YouTube library needs it.
	YouTube *youtube.Repo
	// Avatars gives a channel folder without a square poster its
	// uploader's picture; nil leaves it the art the folder has.
	Avatars ChannelAvatars
	// Reviews records the titles and files whose identity a scan would
	// not decide alone; nil records none.
	Reviews *review.Repo
	// Settings holds the metadata settings; while they use no remote data,
	// and while Settings is nil, no provider is called.
	Settings *serverinfo.Repo
}

// Scanner scans libraries.
type Scanner struct{ Config }

// New returns a Scanner.
func New(c Config) *Scanner {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Probe == nil {
		c.Probe = probe.Run
	}
	if c.Fingerprint == nil {
		c.Fingerprint = probe.Fingerprint
	}
	if c.Thumb == nil {
		c.Thumb = thumbs.Chapter
	}
	if c.Cover == nil {
		c.Cover = thumbs.Cover
	}
	if c.Picture == nil {
		c.Picture = thumbs.Picture
	}
	if c.Art == nil {
		c.Art = thumbs.Art
	}
	return &Scanner{c}
}

// One ffprobe or ffmpeg run gets this long, so a file that never finishes
// fails alone and the task loop reaches the next. Previews read a whole
// file: theirs is twice its runtime, and no less than the floor.
var (
	runLimit      = 2 * time.Minute
	previewsFloor = time.Hour
)

func previewsLimit(f *mediafile.File) time.Duration {
	if f.DurationMs == nil {
		return previewsFloor
	}
	return max(previewsFloor, 2*time.Duration(*f.DurationMs)*time.Millisecond)
}

// remote reports whether the metadata settings let providers be called.
func (s *Scanner) remote() bool {
	if s.Settings == nil {
		return false
	}
	src, err := s.Settings.Sources()
	return err == nil && src.UseRemote()
}

func (s *Scanner) probe(ctx context.Context, path string) (*probe.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, runLimit)
	defer cancel()
	return s.Probe(ctx, path)
}

// fingerprint is the file's fingerprint, "" when it gives none; the next
// scan asks again.
func (s *Scanner) fingerprint(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, runLimit)
	defer cancel()
	fp, err := s.Fingerprint(ctx, path)
	if err != nil {
		logstore.Log("scan").Warn("File not fingerprinted", "path", path, "err", err)
	}
	return fp
}

// backfill gives a file recorded without a fingerprint as they are made
// now its own, without probing it again.
func (s *Scanner) backfill(ctx context.Context, f *mediafile.File) error {
	if probe.Fingerprinted(f.Fingerprint) {
		return nil
	}
	if f.Fingerprint = s.fingerprint(ctx, f.Path); f.Fingerprint == "" {
		return nil
	}
	return s.Files.UpdateFile(f)
}

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".ts": true, ".webm": true, ".wmv": true}

var extrasDirs = map[string]bool{"extras": true, "featurettes": true, "trailers": true, "behind the scenes": true, "sample": true, "samples": true}

// skipDir reports whether scans and watches leave the folder p of a library
// of this kind under root alone: hidden folders and a NAS's index anywhere,
// and the extras of a movie or a show. A TV library's folder directly under
// its root is a show, which may be named for extras; music and YouTube
// folders are named for what they hold.
func skipDir(kind, root, p string) bool {
	name := strings.ToLower(filepath.Base(p))
	switch {
	case strings.HasPrefix(name, "."), name == "@eadir":
		return true
	case kind == library.Movies:
		return extrasDirs[name]
	case kind == library.TV:
		return extrasDirs[name] && filepath.Dir(p) != filepath.Clean(root)
	}
	return false
}

// A downloader's unmerged stream, "name.f137.mp4", or its merge in
// progress, "name.temp.mp4".
var partialDownload = regexp.MustCompile(`\.(f\d+|temp)$`)

func skipFile(name string, exts map[string]bool) bool {
	l := strings.ToLower(name)
	if strings.HasPrefix(l, ".") || !exts[filepath.Ext(l)] {
		return true
	}
	base := strings.TrimSuffix(l, filepath.Ext(l))
	return strings.HasSuffix(base, "-trailer") || strings.HasSuffix(base, "-sample") || strings.HasSuffix(base, "-featurette") ||
		partialDownload.MatchString(base)
}

type candidate struct {
	path string
	root string
	size int64
	mod  time.Time
}

// walk lists the files with these extensions under root, a folder of a
// library of this kind, that it can read. The error names each folder it
// could not read, whose files it did not see.
func walk(root, kind string, exts map[string]bool) ([]candidate, error) {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("library folder %s unreadable", root)
	}
	var out []candidate
	var unread []error
	// The trailing separator makes WalkDir follow a root that is a symlink;
	// children come back joined to the clean root.
	top := root + string(filepath.Separator)
	_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			if d != nil && d.IsDir() {
				unread = append(unread, err)
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			if p != top && skipDir(kind, root, p) {
				return fs.SkipDir
			}
		case !skipFile(d.Name(), exts):
			if info, err := os.Stat(p); err == nil {
				out = append(out, candidate{p, root, info.Size(), info.ModTime().UTC().Truncate(time.Millisecond)})
			}
		}
		return nil
	})
	return out, errors.Join(unread...)
}

// Scan brings one library in line with its folders: new and changed files
// are probed, files it does not see are marked missing or, when gone and
// spare, deleted (see markMissing), and unmatched items get metadata. It
// never deletes a title for not being on disk; a volume that is not
// mounted looks the same. progress, if set, is called with 0 to 1 as files
// are processed. The Summary is never nil and covers what was done even
// when an error ends the scan.
func (s *Scanner) Scan(ctx context.Context, lib *library.Library, progress func(float64)) (*Summary, error) {
	leaf, exts, sync, match := item.Movie, videoExt, s.syncFile, s.matcher(lib)
	switch lib.Kind {
	case library.TV:
		leaf = item.Episode
	case library.Music:
		leaf, exts, sync = item.Song, audioExt, s.syncSong
	case library.YouTube:
		leaf, sync = item.Video, s.syncVideo
	}
	sum := newSummary(leaf)
	folders, err := s.Libraries.Folders(lib.ID)
	if err != nil {
		return sum, err
	}
	var cands []candidate
	var walkErrs []error
	for _, f := range folders {
		c, err := walk(f.Path, lib.Kind, exts)
		if err != nil {
			walkErrs = append(walkErrs, err)
		}
		cands = append(cands, c...)
	}
	sum.Checked, sum.partial = len(cands), len(walkErrs) > 0
	if err := s.reconcileMoves(ctx, lib, folders, cands, sum); err != nil {
		return sum, err
	}
	seen := make(map[string]bool, len(cands))
	for i, c := range cands {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		seen[c.path] = true
		if err := sync(ctx, lib, c, sum); err != nil {
			logstore.Log("scan").Warn("File skipped", "path", c.path, "err", err)
		}
		if progress != nil {
			progress(float64(i+1) / float64(len(cands)+1))
		}
	}
	if err := s.markMissing(lib, folders, seen, sum); err != nil {
		return sum, err
	}
	if err := s.foldSongs(lib, folders, sum); err != nil {
		return sum, err
	}
	if err := s.reviewLoose(lib, sum); err != nil {
		return sum, err
	}
	failed, err := match(ctx, lib)
	sum.LookupFailed = failed
	if err != nil {
		return sum, err
	}
	if err := s.foldMovies(lib.ID, sum); err != nil {
		return sum, err
	}
	return sum, errors.Join(walkErrs...)
}

// matcher is what looks up a library's items after its scan.
func (s *Scanner) matcher(lib *library.Library) func(context.Context, *library.Library) (int, error) {
	switch lib.Kind {
	case library.Music:
		return s.MatchMusic
	case library.YouTube:
		return s.describeChannels
	}
	return s.MatchLibrary
}

// foldMovies folds each of the library's movies matched to an older
// movie's TMDB id into that movie, so a copy in another folder or under
// another name joins the movie everything already points at. sum, if set,
// counts each as a file added to the older.
func (s *Scanner) foldMovies(libraryID int64, sum *Summary) error {
	pairs, err := s.Items.MovieDuplicates(libraryID)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if err := s.Editions.Fold(p[0], p[1]); err != nil {
			return err
		}
		logstore.Log("scan").Info("Duplicate movie folded into the older one", "item_id", p[0], "folded_id", p[1])
		if sum == nil {
			continue
		}
		if slices.Contains(sum.created, p[1]) {
			sum.Added[item.Movie]--
		} else {
			sum.Removed[item.Movie]++
		}
		sum.Updated[item.Movie]++
	}
	return nil
}

// foldSongs folds each of the music library's songs whose files are all
// gone into its one twin with a file on disk (see item.Repo.SongTwins), as
// a file renamed without a fingerprint to tell it by is made again at its
// new path. sum counts each as a file moved to the twin. A scan that could
// not walk every library folder folds nothing.
func (s *Scanner) foldSongs(lib *library.Library, folders []*library.Folder, sum *Summary) error {
	if lib.Kind != library.Music || sum.partial {
		return nil
	}
	pairs, err := s.Items.SongTwins(lib.ID)
	if err != nil {
		return err
	}
	ls := listings{}
	for _, p := range pairs {
		files, err := s.Files.FilesForItem(p[1])
		if err != nil {
			return err
		}
		if slices.ContainsFunc(files, func(f *mediafile.File) bool { return !ls.gone(rootOf(folders, f.Path), f.Path) }) {
			continue
		}
		dead, err := s.Items.Get(p[1])
		if err != nil {
			return err
		}
		if err := s.Editions.Fold(p[0], p[1]); err != nil {
			return err
		}
		logstore.Log("scan").Info("Song with every file gone folded into its twin", "item_id", p[0], "folded_id", p[1])
		if err := s.pruneArtists(dead, sum); err != nil {
			return err
		}
		if slices.Contains(sum.created, p[0]) {
			sum.Added[item.Song]--
		} else {
			sum.Removed[item.Song]++
		}
		sum.Updated[item.Song]++
	}
	return nil
}

func (s *Scanner) syncFile(ctx context.Context, lib *library.Library, c candidate, sum *Summary) error {
	before := sum.added()
	itemIDs, err := s.resolve(lib, c, sum)
	if err != nil || len(itemIDs) == 0 {
		if err == nil {
			sum.loose = append(sum.loose, c.path)
		}
		return err
	}
	f, err := s.Files.FileByPath(c.path)
	switch {
	case errors.Is(err, db.ErrNotFound):
		f = &mediafile.File{Path: c.path, SizeBytes: c.size, ModifiedAt: c.mod}
		if err := s.Files.CreateFile(f); err != nil {
			return err
		}
		if sum.added() == before {
			sum.Updated[sum.leaf]++ // another file for an item we already had
		}
	case err != nil:
		return err
	case f.SizeBytes != c.size || !f.ModifiedAt.Equal(c.mod):
		f.SizeBytes, f.ModifiedAt, f.ProbedAt = c.size, c.mod, nil
		if err := s.Files.UpdateFile(f); err != nil {
			return err
		}
		if err := s.dropPreviews(f.ID); err != nil {
			return err
		}
		if lib.Kind == library.Movies || lib.Kind == library.TV {
			if err := s.Items.DeleteLocal(itemIDs); err != nil {
				return err
			}
		}
		sum.Updated[sum.leaf]++
	}
	fresh := f.ProbedAt == nil
	var probeErr error
	if fresh {
		if probeErr = s.probeFile(ctx, f); probeErr != nil {
			sum.ProbeFailed++ // stays unprobed, so the next scan tries again
		}
	} else if err := s.backfill(ctx, f); err != nil {
		return err
	}
	return errors.Join(s.place(lib, f, itemIDs, fresh, sum), probeErr)
}

// place files f under each of its items, then takes it from items it no
// longer belongs to, pruning them if that empties them. In a Movies library
// the {edition-Name} tag picks the edition and untagged runtimes tell cuts
// apart; every other item has one edition. A file already in exactly these
// items and not just probed stays where it is.
func (s *Scanner) place(lib *library.Library, f *mediafile.File, itemIDs []int64, fresh bool, sum *Summary) error {
	linked, err := s.Editions.ItemIDs(f.ID)
	if err != nil {
		return err
	}
	want := slices.Sorted(slices.Values(itemIDs))
	if !fresh && slices.Equal(linked, want) {
		return nil
	}
	tag, cuts := "", lib.Kind == library.Movies
	if cuts {
		tag = parse.Edition(f.Path)
	}
	for _, id := range want {
		if _, err := s.Editions.Place(f.ID, id, tag, f.DurationMs, cuts); err != nil {
			return err
		}
	}
	for _, id := range linked {
		if slices.Contains(want, id) {
			continue
		}
		if err := s.Editions.Unlink(f.ID, id); err != nil {
			return err
		}
		if err := s.pruneEmpty(id, sum); err != nil {
			return err
		}
	}
	return nil
}

// probeFile records a file's container, tracks and chapters, and makes its
// chapter thumbnails. A failure is recorded on the file unless the scan
// itself was stopped.
func (s *Scanner) probeFile(ctx context.Context, f *mediafile.File) error {
	res, err := s.probe(ctx, f.Path)
	now := s.Now().UTC()
	if err != nil {
		if ctx.Err() == nil {
			f.ProbeFailedAt, f.ProbeError = &now, err.Error()
			err = errors.Join(err, s.Files.UpdateFile(f))
		}
		return err
	}
	f.Container, f.DurationMs, f.Bitrate, f.ProbedAt = res.Container, res.DurationMs, res.Bitrate, &now
	f.ProbeFailedAt, f.ProbeError, f.Fingerprint = nil, "", s.fingerprint(ctx, f.Path)
	if err := s.Files.UpdateFile(f); err != nil {
		return err
	}
	for _, t := range res.Tracks {
		t.MediaFileID = f.ID
	}
	for _, ch := range res.Chapters {
		ch.MediaFileID = f.ID
	}
	if err := s.Files.ReplaceTracks(f.ID, res.Tracks); err != nil {
		return err
	}
	if err := s.Files.ReplaceChapters(f.ID, res.Chapters); err != nil {
		return err
	}
	s.chapterThumbs(ctx, f)
	return nil
}

func (s *Scanner) chapterThumbs(ctx context.Context, f *mediafile.File) {
	if s.ThumbDir == "" {
		return
	}
	chs, err := s.Files.Chapters(f.ID)
	if err != nil {
		logstore.Log("scan").Warn("Chapters unreadable", "path", f.Path, "err", err)
		return
	}
	for _, ch := range chs {
		out := filepath.Join(s.ThumbDir, strconv.FormatInt(f.ID, 10), strconv.Itoa(ch.Position)+".jpg")
		tctx, cancel := context.WithTimeout(ctx, runLimit)
		err := s.Thumb(tctx, f.Path, ch.Position, ch.StartMs, ch.EndMs, out)
		cancel()
		if err != nil {
			logstore.Log("scan").Warn("Chapter thumbnail failed", "path", f.Path, "chapter", ch.Position, "err", err)
			continue
		}
		if err := s.Files.SetChapterThumbnail(ch.ID, out); err != nil {
			logstore.Log("scan").Warn("Chapter thumbnail not stored", "err", err)
		}
	}
}

// resolve finds or creates the items a file belongs to; none means the
// name gave nothing to attach it to.
func (s *Scanner) resolve(lib *library.Library, c candidate, sum *Summary) ([]int64, error) {
	switch lib.Kind {
	case library.Movies:
		return s.resolveMovie(lib, c, sum)
	case library.YouTube:
		id, err := s.resolveVideo(lib, c, sum)
		if id == 0 {
			return nil, err
		}
		return []int64{id}, err
	}
	return s.resolveEpisode(lib, c, sum)
}

// reviewLoose leaves for review the files this scan could file under no
// title, and no others; a scan that could not walk every library folder
// leaves the others too.
func (s *Scanner) reviewLoose(lib *library.Library, sum *Summary) error {
	if s.Reviews == nil {
		return nil
	}
	var keep []int64
	for _, p := range sum.loose {
		rv := &review.Review{LibraryID: lib.ID, Path: p, Reason: review.Unrecognized, RaisedAt: s.Now().UTC()}
		if err := s.Reviews.Raise(rv); err != nil {
			return err
		}
		keep = append(keep, rv.ID)
	}
	if sum.partial {
		return nil
	}
	return s.Reviews.Sweep(lib.ID, review.Unrecognized, keep)
}

// statesAnother reports whether a movie's files or folders state a TMDB
// or IMDb id and none that id shares: the two are then taken for different
// movies, whatever title and year they share. Should they prove to be one,
// foldMovies joins them once both are matched.
func (s *Scanner) statesAnother(it *item.Item, id parse.Identity) (bool, error) {
	if id.TMDB == 0 && id.IMDB == "" {
		return false, nil
	}
	st, err := s.statedVideo(it)
	return another(st.Identity, id), err
}

// another reports whether both state a TMDB or IMDb id and share none.
func another(a, b parse.Identity) bool {
	same := a.TMDB != 0 && a.TMDB == b.TMDB || a.IMDB != "" && a.IMDB == b.IMDB
	return (a.TMDB != 0 || a.IMDB != "") && (b.TMDB != 0 || b.IMDB != "") && !same
}

// ownName reports whether a file in a folder of its own is filed by its
// name rather than the folder's: it states a year the folder does not, or
// an id that none of the folder's is.
func ownName(path string) bool {
	name, folder := filepath.Base(path), filepath.Base(filepath.Dir(path))
	_, year := parse.Title(name)
	_, at := parse.Title(folder)
	return year != nil && !sameYear(year, at) || another(parse.ReadIdentity(name), parse.ReadIdentity(folder))
}

// stayed reports whether any of a movie's files is on disk where it was
// before this scan.
func (s *Scanner) stayed(itemID int64, sum *Summary) (bool, error) {
	files, err := s.Files.FilesForItem(itemID)
	return slices.ContainsFunc(files, func(f *mediafile.File) bool { return !sum.moved[f.Path] && fileExists(f.Path) }), err
}

// resolveMovie keeps a known file's movie. A movie whose files all moved
// takes the title and year of the name they moved to; a file moved out
// from among others that stayed is filed by its new name like a new one.
// A new file in a movie's own folder is that folder's movie unless its
// own name states another; otherwise the library's movie of the same
// title and year, ignoring case, unless the two state different ids, else
// a new movie. Without a year the title alone is not enough.
func (s *Scanner) resolveMovie(lib *library.Library, c candidate, sum *Summary) ([]int64, error) {
	name, dir := filepath.Base(c.path), filepath.Dir(c.path)
	id := parse.ReadIdentity(name)
	folder := dir != c.root && !ownName(c.path)
	if folder {
		name = filepath.Base(dir) // a movie's own folder is usually named more cleanly
		if at := parse.ReadIdentity(name); id.TMDB == 0 && id.IMDB == "" {
			id = at
		}
	}
	title, year := parse.Title(name)
	if f, err := s.Files.FileByPath(c.path); err == nil && f.ItemID != 0 {
		if !sum.moved[c.path] || title == "" {
			return []int64{f.ItemID}, nil
		}
		it, err := s.Items.Get(f.ItemID)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(it.ParsedTitle, title) && sameYear(it.ParsedYear, year) {
			return []int64{it.ID}, nil
		}
		if left, err := s.stayed(it.ID, sum); err != nil {
			return nil, err
		} else if !left {
			it.ParsedTitle, it.ParsedYear = title, year
			return []int64{it.ID}, s.Items.Update(it)
		}
	} else if folder {
		switch it, err := s.Items.MovieInFolder(lib.ID, dir); {
		case err == nil:
			if other, err := s.statesAnother(it, id); err != nil || !other {
				return []int64{it.ID}, err
			}
			title, year = parse.Title(filepath.Base(c.path))
		case !errors.Is(err, db.ErrNotFound):
			return nil, err
		}
	}
	if title == "" {
		return nil, nil
	}
	if year != nil {
		switch it, err := s.Items.Movie(lib.ID, title, *year); {
		case errors.Is(err, db.ErrNotFound):
		case err != nil:
			return nil, err
		default:
			if other, err := s.statesAnother(it, id); err != nil || !other {
				return []int64{it.ID}, err
			}
		}
	}
	it := &item.Item{LibraryID: lib.ID, Kind: item.Movie, ParsedTitle: title, ParsedYear: year}
	if err := s.Items.Create(it); err != nil {
		return nil, err
	}
	sum.Added[item.Movie]++
	sum.created = append(sum.created, it.ID)
	return []int64{it.ID}, nil
}

// renamedShow is the library's one show of this title whose folder is
// gone, moved to dir: a show whose folder was renamed stays the same show
// though none of its files are the ones it had. A year on only one of the
// two names still matches. db.ErrNotFound when none, or when several
// could be it.
func (s *Scanner) renamedShow(lib *library.Library, dir, title string, year *int) (*item.Item, error) {
	shows, err := s.Items.ShowsTitled(lib.ID, title)
	if err != nil {
		return nil, err
	}
	var gone []*item.Item
	ls := listings{}
	for _, sh := range shows {
		if year != nil && sh.ParsedYear != nil && *year != *sh.ParsedYear {
			continue
		}
		if sh.Path == "" || ls.gone(filepath.Dir(sh.Path), sh.Path) {
			gone = append(gone, sh)
		}
	}
	if len(gone) != 1 {
		return nil, db.ErrNotFound
	}
	gone[0].Path, gone[0].ParsedTitle, gone[0].ParsedYear = dir, title, year
	return gone[0], s.Items.Update(gone[0])
}

// splitShow is the library's show whose folder, still there, has dir's
// name under another of the library's folders: a show may keep its
// seasons on several volumes, and stays one show with its own path.
// db.ErrNotFound when none.
func (s *Scanner) splitShow(lib *library.Library, dir string) (*item.Item, error) {
	shows, err := s.Items.ShowsNamed(lib.ID, filepath.Base(dir))
	if err != nil {
		return nil, err
	}
	ls := listings{}
	for _, sh := range shows {
		if !ls.gone(filepath.Dir(sh.Path), sh.Path) {
			return sh, nil
		}
	}
	return nil, db.ErrNotFound
}

func (s *Scanner) resolveEpisode(lib *library.Library, c candidate, sum *Summary) ([]int64, error) {
	rel, err := filepath.Rel(c.root, c.path)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return nil, nil // episodes live in a show folder
	}
	showDir := filepath.Join(c.root, parts[0])
	seasonDir, seasonNum, haveSeason := "", 0, false
	for i, p := range parts[1 : len(parts)-1] {
		if n, ok := parse.SeasonDir(p); ok {
			seasonDir, seasonNum, haveSeason = filepath.Join(append([]string{c.root}, parts[:i+2]...)...), n, true
			break
		}
	}
	file := parts[len(parts)-1]
	sn, ens, ok := parse.Episodes(file)
	if !ok && haveSeason {
		var en int
		sn = seasonNum
		en, ok = parse.EpisodeNumber(file)
		ens = []int{en}
	}
	if !ok {
		return nil, nil
	}
	if haveSeason && sn != seasonNum {
		seasonDir = "" // a stray file in the wrong season folder follows its own name
	}
	title, year := parse.Title(parts[0])
	show, err := s.homed(lib, item.Show, showDir, c.path)
	if err == nil && show.Path == showDir && (show.ParsedTitle != title || !sameYear(show.ParsedYear, year)) && sum.moved[c.path] {
		show.ParsedTitle, show.ParsedYear = title, year
		err = s.Items.Update(show)
	}
	if errors.Is(err, db.ErrNotFound) {
		show, err = s.splitShow(lib, showDir)
	}
	if errors.Is(err, db.ErrNotFound) {
		if show, err = s.renamedShow(lib, showDir, title, year); errors.Is(err, db.ErrNotFound) {
			show = &item.Item{LibraryID: lib.ID, Kind: item.Show, Path: showDir, ParsedTitle: title, ParsedYear: year}
			err = s.Items.Create(show)
			sum.Added[item.Show]++
		}
	}
	if err != nil {
		return nil, err
	}
	season, err := s.Items.Season(show.ID, sn)
	if errors.Is(err, db.ErrNotFound) {
		label := "Season " + strconv.Itoa(sn)
		if sn == 0 {
			label = "Specials"
		}
		season = &item.Item{LibraryID: lib.ID, ParentID: &show.ID, Kind: item.Season, Path: seasonDir, ParsedTitle: label, SeasonNumber: &sn}
		err = s.Items.Create(season)
		sum.Added[item.Season]++
	} else if err == nil && season.Path != seasonDir && (seasonDir != "" && show.Path == showDir || !fileExists(season.Path)) {
		// A season with files in two of a split show's folders keeps the one in the show's own.
		season.Path = seasonDir // its show's folder was renamed, with or without season folders
		err = s.Items.Update(season)
	}
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, en := range ens {
		ep, err := s.Items.Episode(season.ID, en)
		if errors.Is(err, db.ErrNotFound) {
			ep = &item.Item{LibraryID: lib.ID, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: fmt.Sprintf("S%02dE%02d", sn, en),
				SeasonNumber: &sn, EpisodeNumber: &en}
			err = s.Items.Create(ep)
			sum.Added[item.Episode]++
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, ep.ID)
	}
	return ids, nil
}

// markMissing marks the library's files the scan did not see and clears
// the mark of those it did. A file not seen is deleted instead, with what
// was made of it, when it is gone (see listings.gone), the scan saw a file
// in its library folder, and every title it is filed under has a file the
// scan saw: a title keeps its last file's record, and a folder that cannot
// be read or holds no file, as an unmounted volume's, keeps every record
// under it.
func (s *Scanner) markMissing(lib *library.Library, folders []*library.Folder, seen map[string]bool, sum *Summary) error {
	files, err := s.Files.FilesInLibrary(lib.ID)
	if err != nil {
		return err
	}
	now := s.Now().UTC()
	ls, found := listings{}, map[string]bool{}
	for p := range seen {
		found[rootOf(folders, p)] = true
	}
	for _, f := range files {
		var at *time.Time
		if !seen[f.Path] {
			root := rootOf(folders, f.Path)
			if spare, err := s.spare(f, seen); err != nil {
				return err
			} else if spare && found[root] && ls.gone(root, f.Path) {
				if err := s.Files.DeleteFile(f.ID); err != nil {
					return err
				}
				continue
			}
			at = &now
			sum.Missing++
		}
		if err := s.Files.SetMissing(f, at); err != nil {
			return err
		}
	}
	return nil
}

// spare reports whether every title f is filed under has a file seen.
func (s *Scanner) spare(f *mediafile.File, seen map[string]bool) (bool, error) {
	ids, err := s.Editions.ItemIDs(f.ID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		fs, err := s.Files.FilesForItem(id)
		if err != nil || !slices.ContainsFunc(fs, func(o *mediafile.File) bool { return seen[o.Path] }) {
			return false, err
		}
	}
	return true, nil
}

func (s *Scanner) pruneEmpty(id int64, sum *Summary) error {
	for id != 0 {
		it, err := s.Items.Get(id)
		if errors.Is(err, db.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		var n int
		if it.Kind == item.Movie || it.Kind == item.Episode || it.Kind == item.Song || it.Kind == item.Video {
			fs, err := s.Files.FilesForItem(id)
			n = len(fs)
			if err != nil {
				return err
			}
		} else if n, err = s.Items.Count(item.Filter{ParentID: id}); err != nil {
			return err
		}
		if it.Kind == item.Artist && n == 0 {
			if performs, err := s.Items.Performs(id); err != nil || performs {
				return err
			}
		}
		if n > 0 {
			return nil
		}
		if err := s.Items.Delete(id); err != nil {
			return err
		}
		sum.Removed[it.Kind]++
		if err := s.pruneArtists(it, sum); err != nil {
			return err
		}
		id = 0
		if it.ParentID != nil {
			id = *it.ParentID
		}
	}
	return nil
}

// dropPreviews removes a changed file's seek previews and their frames, so
// the Seek previews task makes them again.
func (s *Scanner) dropPreviews(fileID int64) error {
	p, err := s.Previews.ByMediaFile(fileID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if err := s.Previews.Delete(p.ID); err != nil {
		return err
	}
	return os.RemoveAll(p.Dir)
}

func (s *Scanner) gone(fileID int64) bool {
	_, err := s.Files.File(fileID)
	return errors.Is(err, db.ErrNotFound)
}

// MakePreviews makes seek preview frames for every file that has none.
// After each file it stops if yield reports another task waiting, and then
// reports more: the files left are what NeedingPreviews returns next time.
// A file that fails is recorded and left out until it changes; one that is
// not on disk is passed over.
func (s *Scanner) MakePreviews(ctx context.Context, progress func(float64), yield func() bool) (string, bool, error) {
	files, err := s.Files.NeedingPreviews()
	if err != nil {
		return "", false, err
	}
	done, failed, more := 0, 0, false
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if i > 0 && yield != nil && yield() {
			more = true
			break
		}
		if !fileExists(f.Path) {
			continue // a failure recorded now would outlast the outage
		}
		dir := filepath.Join(s.PreviewDir, strconv.FormatInt(f.ID, 10))
		pctx, cancel := context.WithTimeout(ctx, previewsLimit(f))
		fr, err := thumbs.Previews(pctx, f.Path, dir)
		cancel()
		if err == nil {
			err = s.Previews.Put(&seekpreview.SeekPreview{MediaFileID: f.ID, Dir: dir, IntervalMs: fr.IntervalMs, Count: fr.Count, Width: fr.Width, Height: fr.Height})
			// The file's title or library was deleted while its frames were made.
			if err != nil && s.gone(f.ID) {
				_ = os.RemoveAll(dir)
				continue
			}
		} else if ctx.Err() == nil {
			err = errors.Join(err, s.Files.PreviewsFailed(f))
		}
		if err != nil {
			failed++
			logstore.Log("scan").Warn("Seek previews failed", "path", f.Path, "err", err)
		}
		done++
		if progress != nil {
			progress(float64(i+1) / float64(len(files)))
		}
	}
	sum := fmt.Sprintf("%d files", done)
	if done == 1 {
		sum = "1 file"
	}
	if failed > 0 {
		sum += fmt.Sprintf(", %d failed", failed)
	}
	if more {
		sum += ", more to do"
	}
	return sum, more, nil
}

package scan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Tags a title's local fields are read from, first found first.
var (
	overviewTags = []string{"synopsis", "description", "summary"}
	dateTags     = []string{"date_released", "date", "year"}
	ratingTags   = []string{"content_rating", "rating", "law_rating", "itunextc"}
)

// localRatings are the ratings a tag may give each kind: MPAA for movies,
// TV Parental Guidelines for shows and episodes. Any other is no rating.
var localRatings = map[string][]string{
	item.Movie: account.MovieRatings,
	item.Show:  account.TVRatings,
}

func firstTag(tags map[string]string, keys []string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(tags[k]); v != "" {
			return v
		}
	}
	return ""
}

// localDate is a tag's date as an ISO date, or its year alone when that is
// all it says; "" when it is neither.
func localDate(v string) string {
	if len(v) >= 10 {
		if _, err := time.Parse(time.DateOnly, v[:10]); err == nil {
			return v[:10]
		}
	}
	if len(v) >= 4 && (len(v) == 4 || v[4] < '0' || v[4] > '9') {
		if y, err := strconv.Atoi(v[:4]); err == nil && y >= 1870 {
			return v[:4]
		}
	}
	return ""
}

// localRating reads a rating tag, iTunes' "mpaa|PG-13|300|" among them,
// keeping only a rating of kind's system.
func localRating(v, kind string) string {
	if parts := strings.Split(v, "|"); len(parts) > 1 {
		v = parts[1]
	}
	v = strings.ToUpper(strings.TrimSpace(v))
	if kind == item.Episode {
		kind = item.Show
	}
	for _, r := range localRatings[kind] {
		if r == v {
			return v
		}
	}
	return ""
}

// episodeTag is the show and number a title tag may lead with, as in
// "Show - S01E01 - Title"; what follows is the episode's own title.
var episodeTag = regexp.MustCompile(`(?i)^.*\bS\d+E\d+(?:-?E\d+)*\b(?:\s*-\s*|$)`)

// localTitle is a title tag, or "" when it is a release name such as
// "Toy.Story.1995.1080p": one word joined by dots or underscores.
func localTitle(v string) string {
	v = strings.TrimSpace(episodeTag.ReplaceAllString(strings.TrimSpace(v), ""))
	if !strings.Contains(v, " ") && strings.ContainsAny(v, "._") {
		return ""
	}
	return v
}

// localTags is what a file's tags say of a title of kind: its title,
// overview, date, rating and genres.
func localTags(tags map[string]string, kind string) (*item.Metadata, []string) {
	m := &item.Metadata{Title: localTitle(tags["title"]), Overview: firstTag(tags, overviewTags),
		ReleaseDate: localDate(firstTag(tags, dateTags)), ContentRating: localRating(firstTag(tags, ratingTags), kind)}
	var genres []string
	for _, g := range strings.FieldsFunc(tags["genre"], func(r rune) bool { return r == ',' || r == ';' || r == '/' }) {
		if g = strings.TrimSpace(g); g != "" {
			genres = append(genres, g)
		}
	}
	return m, genres
}

// MakeLocal reads the local metadata of each movie, show, season, episode
// and album that has none: a movie's or episode's from its own file, a
// season's from its first episode, a show's from its first episode outside
// the specials and an album's art from its songs (see albumCover). After
// each title it stops if yield reports another task waiting, and then
// reports more. A title with no file on disk is passed over until one is
// back; one whose reading fails keeps what was read, so it is not read
// again until its file changes.
func (s *Scanner) MakeLocal(ctx context.Context, progress func(float64), yield func() bool) (string, bool, error) {
	its, err := s.Items.WithoutLocal(1 << 20)
	if err != nil {
		return "", false, err
	}
	done, more := 0, false
	for i, it := range its {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if i > 0 && yield != nil && yield() {
			more = true
			break
		}
		f, err := s.localFile(it)
		if err != nil {
			return "", false, err
		}
		if f != nil {
			if err := s.describeLocal(ctx, it, f); err != nil {
				// The title or its library was deleted while it was read.
				if _, gone := s.Items.Get(it.ID); errors.Is(gone, db.ErrNotFound) {
					continue
				}
				return "", false, err
			}
			done++
		}
		if progress != nil {
			progress(float64(i+1) / float64(len(its)))
		}
	}
	sum := fmt.Sprintf("%d titles", done)
	if done == 1 {
		sum = "1 title"
	}
	if more {
		sum += ", more to do"
	}
	return sum, more, nil
}

// localFile is the file on disk a title's local metadata is read from,
// nil with none.
func (s *Scanner) localFile(it *item.Item) (*mediafile.File, error) {
	if it.Kind == item.Album {
		songs, err := s.Items.List(item.Filter{ParentID: &it.ID, Kind: item.Song, Limit: 1 << 20})
		for _, sg := range songs {
			if f, err := s.availableFile(sg.ID); err != nil || f != nil {
				return f, err
			}
		}
		return nil, err
	}
	if it.Kind != item.Show && it.Kind != item.Season {
		return s.availableFile(it.ID)
	}
	show := it.ID
	if it.Kind == item.Season {
		show = *it.ParentID
	}
	eps, err := s.Items.Episodes(show)
	if err != nil {
		return nil, err
	}
	var special *mediafile.File
	for _, e := range eps {
		if it.Kind == item.Season && *e.ParentID != it.ID {
			continue
		}
		f, err := s.availableFile(e.ID)
		if err != nil || f == nil {
			continue
		}
		if it.Kind == item.Show && e.SeasonNumber != nil && *e.SeasonNumber == 0 {
			special = cmp.Or(special, f)
			continue
		}
		return f, nil
	}
	return special, nil
}

func (s *Scanner) availableFile(itemID uid.ID) (*mediafile.File, error) {
	files, err := s.Files.FilesForItem(itemID)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.MissingAt == nil && f.ProbedAt != nil && fileExists(f.Path) {
			return f, nil
		}
	}
	return nil, nil
}

// describeLocal stores a title's local row from f: a movie's or episode's
// tags and runtime, a show's rating, and art; an album's is albumCover's.
// Only a database error is returned.
func (s *Scanner) describeLocal(ctx context.Context, it *item.Item, f *mediafile.File) error {
	if it.Kind == item.Album {
		return s.albumCover(ctx, it)
	}
	now := s.Now().UTC()
	m := &item.Metadata{ItemID: it.ID, FetchedAt: &now}
	var genres []string
	res, err := s.probe(ctx, f.Path)
	switch {
	case err != nil:
		logstore.Log("scan").Warn("Tags unreadable", "path", f.Path, "err", err)
	case it.Kind == item.Show:
		tm, _ := localTags(res.Tags, item.Show)
		m.ContentRating = tm.ContentRating
	case it.Kind != item.Season:
		tm, gs := localTags(res.Tags, it.Kind)
		m.Title, m.Overview, m.ReleaseDate, m.ContentRating, genres = tm.Title, tm.Overview, tm.ReleaseDate, tm.ContentRating, gs
		if res.DurationMs != nil && *res.DurationMs >= 60000 {
			rt := int((*res.DurationMs + 30000) / 60000)
			m.RuntimeMinutes = &rt
		}
	}
	s.localArt(ctx, it, f, res, m)
	if err := s.Items.SetLocal(m); err != nil {
		return err
	}
	gs := make([]genre.Genre, len(genres))
	for i, g := range genres {
		gs[i] = genre.Genre{Name: g}
	}
	return s.Genres.SetForItem(it.ID, genre.Local, gs)
}

// localArt gives m the file's attached picture as its poster and a frame
// as its backdrop, or a frame's 2:3 centre as its poster when the file
// has no picture; an episode's poster is its picture or the whole frame,
// as a still is, and it has no backdrop. Images go under ThumbDir/local
// by content hash. A failure is logged and leaves that image out.
func (s *Scanner) localArt(ctx context.Context, it *item.Item, f *mediafile.File, res *probe.Result, m *item.Metadata) {
	if s.ThumbDir == "" {
		return
	}
	dir := filepath.Join(s.ThumbDir, "local")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		logstore.Log("scan").Warn("Local art not stored", "path", f.Path, "err", err)
		return
	}
	tmp, err := os.MkdirTemp(dir, ".art-")
	if err != nil {
		logstore.Log("scan").Warn("Local art not stored", "path", f.Path, "err", err)
		return
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	keep := func(made error, p string) string {
		if made == nil {
			if p, made = storeByHash(p, dir); made == nil {
				return p
			}
		}
		logstore.Log("scan").Warn("Local art not made", "path", f.Path, "err", made)
		return ""
	}
	cover := ""
	if res != nil && res.Cover {
		out := filepath.Join(tmp, "cover.jpg")
		cctx, cancel := context.WithTimeout(ctx, runLimit)
		cover = keep(s.Picture(cctx, f.Path, res.CoverStream, out), out)
		cancel()
	}
	if it.Kind == item.Episode && cover != "" {
		m.PosterPath = cover
		return
	}
	var ms int64
	if f.DurationMs != nil {
		ms = *f.DurationMs
	}
	frame, poster := filepath.Join(tmp, "frame.jpg"), filepath.Join(tmp, "poster.jpg")
	actx, cancel := context.WithTimeout(ctx, runLimit)
	err = s.Art(actx, f.Path, ms, frame, poster)
	cancel()
	frame = keep(err, frame)
	if it.Kind == item.Episode {
		m.PosterPath = frame
		return
	}
	if cover == "" && frame != "" {
		cover = keep(nil, poster)
	}
	m.PosterPath, m.BackdropPath = cover, frame
}

// albumCover stores an album's local row with, as its art, the embedded
// picture of the first of its songs on disk that has one, copied under
// ThumbDir/music by content hash so a new image gets a new name. Only a
// database error is returned; an album left without art keeps its row,
// so it is not read again until a scan finds a picture for it.
func (s *Scanner) albumCover(ctx context.Context, album *item.Item) error {
	now := s.Now().UTC()
	m := &item.Metadata{ItemID: album.ID, FetchedAt: &now}
	songs, err := s.Items.List(item.Filter{ParentID: &album.ID, Kind: item.Song, Limit: 1 << 20})
	if err != nil {
		return err
	}
	dir := filepath.Join(s.ThumbDir, "music")
	for _, sg := range songs {
		f, err := s.availableFile(sg.ID)
		if err != nil {
			return err
		}
		if f == nil || s.ThumbDir == "" {
			continue
		}
		if res, err := s.probe(ctx, f.Path); err != nil || !res.Cover {
			continue
		}
		tmp := filepath.Join(dir, ".cover-"+album.ID.String()+".jpg")
		cctx, cancel := context.WithTimeout(ctx, runLimit)
		err = s.Cover(cctx, f.Path, tmp)
		cancel()
		if err == nil {
			m.PosterPath, err = storeByHash(tmp, dir)
		}
		_ = os.Remove(tmp)
		if err != nil {
			logstore.Log("scan").Warn("Embedded cover unreadable", "path", f.Path, "err", err)
		}
		break
	}
	return s.Items.SetLocal(m)
}

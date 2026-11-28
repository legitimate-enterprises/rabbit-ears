package scan

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"image"
	_ "image/jpeg" // posters
	_ "image/png"  // posters
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// A YouTube library is a folder another program downloads into, such as
// Pinchflat: each top-level folder is a channel, each video file beneath
// it a video. Kodi .nfo, .info.json and -thumb.jpg sidecars, all
// optional, describe them. The scan only reads the folder.

// nfo is a Kodi <tvshow> or <episodedetails>.
type nfo struct {
	Title    string `xml:"title"`
	Plot     string `xml:"plot"`
	Aired    string `xml:"aired"`
	UniqueID []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
}

func (n *nfo) youtubeID() string {
	for _, u := range n.UniqueID {
		if strings.EqualFold(u.Type, "youtube") {
			return strings.TrimSpace(u.Value)
		}
	}
	return ""
}

// readNFO is the .nfo at path, or an empty one when it is missing or
// unreadable.
func readNFO(path string) *nfo {
	var n nfo
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // a sidecar beside a library file
		_ = xml.Unmarshal(b, &n)
	}
	return &n
}

// span is a chapter as an .info.json holds one, in seconds.
type span struct {
	Start float64 `json:"start_time"`
	End   float64 `json:"end_time"`
	Title string  `json:"title"`
}

// infoJSON is the part of an .info.json the scan reads.
type infoJSON struct {
	ID          string  `json:"id"`
	ChannelID   string  `json:"channel_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Timestamp   float64 `json:"timestamp"`
	UploadDate  string  `json:"upload_date"`
	Chapters    []span  `json:"chapters"`
	Sponsor     []span  `json:"sponsorblock_chapters"`
}

func readInfo(path string) *infoJSON {
	var in infoJSON
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // a sidecar beside a library file
		_ = json.Unmarshal(b, &in)
	}
	return &in
}

// sidecars are the files beside a video that describe it.
type sidecars struct{ nfo, info, thumb string }

func sidecarsOf(video string) sidecars {
	base := strings.TrimSuffix(video, filepath.Ext(video))
	return sidecars{base + ".nfo", base + ".info.json", base + "-thumb.jpg"}
}

// stamp sums the modification times of the sidecars present, so one that
// arrives, changes or goes changes it.
func (sc sidecars) stamp() int64 {
	var n int64
	for _, p := range []string{sc.nfo, sc.info, sc.thumb} {
		if st, err := os.Stat(p); err == nil {
			n += st.ModTime().UnixMilli()
		}
	}
	return n
}

// published is when a video went up: the .info.json timestamp, else the
// .nfo air date, else the .info.json upload date, else modified.
func published(in *infoJSON, n *nfo, modified time.Time) time.Time {
	if in.Timestamp > 0 {
		return time.Unix(int64(in.Timestamp), 0).UTC()
	}
	for _, layout := range []string{"2006-01-02 15:04:05Z07:00", time.DateOnly} {
		if t, err := time.Parse(layout, strings.TrimSpace(n.Aired)); err == nil {
			return t.UTC()
		}
	}
	if t, err := time.Parse("20060102", in.UploadDate); err == nil {
		return t
	}
	return modified.UTC()
}

// chaptersFrom turns .info.json chapters into a file's, cutting each
// SponsorBlock segment out of the chapters it overlaps so none overlap.
func chaptersFrom(chapters, sponsor []span) []*mediafile.Chapter {
	var spans []span
	for _, c := range chapters {
		pieces := []span{c}
		for _, sp := range sponsor {
			var kept []span
			for _, p := range pieces {
				if sp.End <= p.Start || sp.Start >= p.End {
					kept = append(kept, p)
					continue
				}
				if sp.Start > p.Start {
					kept = append(kept, span{p.Start, sp.Start, p.Title})
				}
				if sp.End < p.End {
					kept = append(kept, span{sp.End, p.End, p.Title})
				}
			}
			pieces = kept
		}
		spans = append(spans, pieces...)
	}
	spans = append(spans, sponsor...)
	slices.SortStableFunc(spans, func(a, b span) int { return cmp.Compare(a.Start, b.Start) })
	var out []*mediafile.Chapter
	for _, s := range spans {
		start, end := int64(s.Start*1000+.5), int64(s.End*1000+.5)
		if end > start {
			out = append(out, &mediafile.Chapter{Position: len(out), StartMs: start, EndMs: end, Title: s.Title})
		}
	}
	return out
}

// resolveVideo finds or creates a video file's channel, named by its
// top-level folder, and the video, named by its file until a sidecar names
// it. A video directly in the library folder belongs to no channel.
func (s *Scanner) resolveVideo(lib *library.Library, c candidate, sum *Summary) ([]uid.ID, error) {
	rel, err := filepath.Rel(c.root, c.path)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return nil, nil
	}
	dir := filepath.Join(c.root, parts[0])
	ch, err := s.homed(lib, item.Channel, dir, c.path)
	if err == nil && sum.moved[c.path] && ch.ParsedTitle != parts[0] {
		ch.ParsedTitle = parts[0]
		err = s.Items.Update(ch)
	}
	if errors.Is(err, db.ErrNotFound) {
		ch = &item.Item{LibraryID: lib.ID, Kind: item.Channel, Path: dir, ParsedTitle: parts[0]}
		err = s.Items.Create(ch)
		sum.Added[item.Channel]++
	}
	if err != nil {
		return nil, err
	}
	v, err := s.Items.ByPath(lib.ID, item.Video, c.path)
	if errors.Is(err, db.ErrNotFound) {
		v = &item.Item{LibraryID: lib.ID, ParentID: &ch.ID, Kind: item.Video, Path: c.path,
			ParsedTitle: strings.TrimSuffix(filepath.Base(c.path), filepath.Ext(c.path))}
		err = s.Items.Create(v)
		sum.Added[item.Video]++
	} else if err == nil && sum.moved[c.path] {
		// A video moved with its file is named, and belongs, as its path says.
		v.ParsedTitle = strings.TrimSuffix(filepath.Base(c.path), filepath.Ext(c.path))
		if err = s.Items.Update(v); err == nil && *v.ParentID != ch.ID {
			err = s.Items.SetParent(v.ID, ch.ID)
		}
	}
	if err != nil {
		return nil, err
	}
	return []uid.ID{v.ID}, nil
}

// syncVideo brings one video file in, and reads its sidecars whenever the
// file is new or changed or they are. Reading them follows a fresh probe,
// so chapters the file embeds win over theirs.
func (s *Scanner) syncVideo(ctx context.Context, lib *library.Library, c candidate, sum *Summary) error {
	side := sidecarsOf(c.path)
	stamp := side.stamp()
	f, err := s.Files.FileByPath(c.path)
	switch {
	case errors.Is(err, db.ErrNotFound):
	case err != nil:
		return err
	case f.ProbedAt != nil && f.SizeBytes == c.size && f.FileModifiedAt.Equal(c.mod) && f.ItemID != nil:
		if err := s.backfill(ctx, f); err != nil {
			return err
		}
		old, err := s.YouTube.Stamp(*f.ItemID)
		if err != nil || old == stamp {
			return err
		}
		f.ProbedAt = nil
		if err := s.Files.UpdateFile(f); err != nil {
			return err
		}
		sum.Updated[item.Video]++
	}
	if err := s.syncFile(ctx, lib, c, sum); err != nil {
		return err
	}
	if f, err = s.Files.FileByPath(c.path); errors.Is(err, db.ErrNotFound) || err == nil && f.ItemID == nil {
		return nil // the file belongs to no channel
	} else if err != nil {
		return err
	}
	return s.describeVideo(f, c.mod, side, stamp)
}

// describeVideo records what a video's sidecars say about it: title and
// description from the .nfo, else the .info.json; its id, publish time,
// thumbnail, and .info.json chapters when the file has none.
func (s *Scanner) describeVideo(f *mediafile.File, modified time.Time, side sidecars, stamp int64) error {
	n, in := readNFO(side.nfo), readInfo(side.info)
	it, err := s.Items.Get(*f.ItemID)
	if err != nil {
		return err
	}
	if title := cmp.Or(strings.TrimSpace(n.Title), strings.TrimSpace(in.Title)); title != "" && title != it.ParsedTitle {
		it.ParsedTitle = title
		if err := s.Items.Update(it); err != nil {
			return err
		}
	}
	now := s.Now().UTC()
	m := &item.Metadata{ItemID: it.ID, Status: item.Matched, Title: it.ParsedTitle,
		Overview: cmp.Or(strings.TrimSpace(n.Plot), strings.TrimSpace(in.Description)), ExternalID: cmp.Or(n.youtubeID(), in.ID), FetchedAt: &now}
	m.PosterPath = s.stored(side.thumb)
	if err := s.Items.SetLocal(m); err != nil {
		return err
	}
	if len(in.Chapters)+len(in.Sponsor) > 0 {
		have, err := s.Files.Chapters(f.ID)
		if err != nil {
			return err
		}
		if len(have) == 0 {
			if err := s.Files.ReplaceChapters(f.ID, chaptersFrom(in.Chapters, in.Sponsor)); err != nil {
				return err
			}
		}
	}
	return s.YouTube.SetSource(it.ID, published(in, n, modified), stamp)
}

// describeChannels records each channel folder's tvshow.nfo and poster.jpg
// as its local data; a channel whose folder is not on disk keeps what it
// has. With avatars, while the metadata settings use remote data, a folder
// without a square poster also gets its uploader's YouTube avatar as its
// remote data, returning how many of those lookups failed and whether it
// gave way (see run); a failure never fails it.
func (s *Scanner) describeChannels(ctx context.Context, lib *library.Library, avatars bool, yield func() bool) (int, bool, error) {
	chs, err := s.Items.List(item.Filter{LibraryID: &lib.ID, Kind: item.Channel, Limit: 1 << 20})
	if err != nil {
		return 0, false, err
	}
	r := &run{yield: yield}
	for _, ch := range chs {
		if !fileExists(ch.Path) {
			continue
		}
		n := readNFO(filepath.Join(ch.Path, "tvshow.nfo"))
		l := &item.Metadata{ItemID: ch.ID, Status: item.Matched, Title: ch.ParsedTitle, Overview: strings.TrimSpace(n.Plot), ExternalID: n.youtubeID()}
		poster := filepath.Join(ch.Path, "poster.jpg")
		l.PosterPath = s.stored(poster)
		if err := s.putIfChanged(l, s.Items.LocalRow, s.Items.SetLocal); err != nil {
			return r.failed, false, err
		}
		if !avatars || square(poster) || s.Avatars == nil || !s.remote() {
			continue
		}
		if r.gaveWay() {
			return r.failed, true, nil
		}
		old, err := s.Items.Remote(ch.ID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return r.failed, false, err
		}
		m := &item.Metadata{ItemID: ch.ID, Provider: youtube.Provider, Status: item.Matched}
		if err := s.channelAvatar(ctx, r, ch, m, old); err != nil {
			return r.failed, false, err
		}
		if err := s.putIfChanged(m, s.Items.Remote, s.Items.SetMetadata); err != nil {
			return r.failed, false, err
		}
	}
	return r.failed, false, nil
}

// stored is a copy of the library image p under ThumbDir/local, named by
// its content, so a picture replaced under its name gets a new one; "" for
// none or one not copied.
func (s *Scanner) stored(p string) string {
	if s.ThumbDir == "" || !fileExists(p) {
		return ""
	}
	dest, err := storeByHash(p, filepath.Join(s.ThumbDir, "local"))
	if err != nil {
		logstore.Log("scan").Warn("YouTube art not stored", "path", p, "err", err)
	}
	return dest
}

// putIfChanged writes m with put, stamped now, unless get finds the row
// already says the same.
func (s *Scanner) putIfChanged(m *item.Metadata, get func(uid.ID) (*item.Metadata, error), put func(*item.Metadata) error) error {
	old, err := get(m.ItemID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	if old != nil && old.Overview == m.Overview && old.ExternalID == m.ExternalID && old.PosterPath == m.PosterPath && old.Title == m.Title &&
		equalTime(old.RetryAt, m.RetryAt) {
		return nil
	}
	now := s.Now().UTC()
	m.FetchedAt = &now
	return put(m)
}

// channelAvatar sets m to the channel's avatar: the copy fetched before
// until its refresh is due, else one fetched now.
func (s *Scanner) channelAvatar(ctx context.Context, r *run, ch *item.Item, m, old *item.Metadata) error {
	if old != nil && old.RetryAt != nil {
		if old.PosterPath != "" && fileExists(old.PosterPath) {
			m.PosterPath = old.PosterPath
		}
		if m.RetryAt = old.RetryAt; old.RetryAt.After(s.Now()) {
			return nil
		}
	}
	return s.fetchAvatar(ctx, r, ch, m)
}

// fetchAvatar fetches into m the avatar of the channel ch's videos came
// from, due again on the metadata schedule. A failure keeps m's art and
// retries after RetryDelay; only cancellation is returned.
func (s *Scanner) fetchAvatar(ctx context.Context, r *run, ch *item.Item, m *item.Metadata) error {
	now := s.Now().UTC()
	retry, refresh := now.Add(RetryDelay), tmdb.RefreshAt(now)
	if r.outage {
		r.failed++
		m.RetryAt = &retry
		return nil
	}
	p, err := s.Avatars.Fetch(ctx, uploader(ch.Path))
	switch {
	case err == nil:
		m.PosterPath, m.RetryAt = p, &refresh
	case errors.Is(err, youtube.ErrNotFound):
		m.RetryAt = &refresh
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		logstore.Log("youtube").Warn("Channel avatar not fetched; will retry", "item_id", ch.ID, "title", ch.ParsedTitle, "err", err)
		r.outage = true
		r.failed++
		m.RetryAt = &retry
	}
	return nil
}

// uploader is the channel id the first .info.json under dir names, "" with none.
func uploader(dir string) string {
	var id string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".info.json") {
			id = readInfo(p).ChannelID
		}
		if id != "" {
			return fs.SkipAll
		}
		return nil
	})
	return id
}

// square reports an image at p as wide as it is tall; one missing or
// unreadable is not.
func square(p string) bool {
	f, err := os.Open(p) //nolint:gosec // a poster in a library folder
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	c, _, err := image.DecodeConfig(f)
	return err == nil && c.Width > 0 && c.Width == c.Height
}

func equalTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

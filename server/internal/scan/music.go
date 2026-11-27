package scan

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/atomicfile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/parse"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

var audioExt = map[string]bool{".flac": true, ".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".oga": true, ".opus": true,
	".wav": true, ".aif": true, ".aiff": true, ".wv": true, ".wma": true}

// songInfo is what a song's tags, or failing them its folders and file
// name, say it is.
type songInfo struct {
	Artist      string // who performs it
	AlbumArtist string
	Album       string
	Title       string
	Disc, Track *int
	Year        *int
	Genres      []string
	Explicit    bool
}

var (
	// A file name's leading "1-04. ", "01 - " or "01 " is disc and track.
	leadTrack = regexp.MustCompile(`^(?:(\d{1,2})[-.])?(\d{1,3})(?:\s*[-.]\s*|\s+|_+)(.+)$`)
	discDir   = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*(\d{1,2})$`)
	leadNum   = regexp.MustCompile(`^\d+`)
	leadYear  = regexp.MustCompile(`^\d{4}`)
)

func tag(tags map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(tags[k]); v != "" {
			return v
		}
	}
	return ""
}

// number reads "3" or "3/12" as 3.
func number(s string) *int {
	if n, err := strconv.Atoi(leadNum.FindString(s)); err == nil && n > 0 {
		return &n
	}
	return nil
}

func year(s string) *int {
	if n, err := strconv.Atoi(leadYear.FindString(s)); err == nil {
		return &n
	}
	return nil
}

// explicit reads a parental advisory from tags: iTunes's rtng atom (1, or
// 4 in older files; only MP4 has it, elsewhere "rating" is a star rating),
// the ITUNESADVISORY tag of ID3 and Vorbis comments, or a plain EXPLICIT.
func explicit(tags map[string]string, container string) bool {
	if r := tag(tags, "rating"); (container == "mp4" || container == "mov") && (r == "1" || r == "4") {
		return true
	}
	if tag(tags, "itunesadvisory") == "1" {
		return true
	}
	switch strings.ToLower(tag(tags, "explicit")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// readSong files a song by its tags (ffprobe's lower-case keys), falling
// back to Artist/Album/NN Title under root for anything they lack.
func readSong(tags map[string]string, root, path string) songInfo {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	dirs := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	if dirs[0] == "." {
		dirs = nil
	}
	var folderDisc *int
	if n := len(dirs); n > 0 {
		if m := discDir.FindStringSubmatch(dirs[n-1]); m != nil {
			folderDisc, dirs = number(m[1]), dirs[:n-1]
		}
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	in := songInfo{Title: tag(tags, "title"), Track: number(tag(tags, "track", "tracknumber")),
		Disc: number(tag(tags, "disc", "discnumber")), Year: year(tag(tags, "date", "year", "originaldate")),
		Album: tag(tags, "album"), Artist: tag(tags, "artist"),
		AlbumArtist: tag(tags, "album_artist", "albumartist", "album artist")}
	if m := leadTrack.FindStringSubmatch(base); m != nil {
		if in.Title == "" {
			in.Title = strings.TrimSpace(m[3])
		}
		if in.Track == nil {
			in.Track = number(m[2])
		}
		if in.Disc == nil && m[1] != "" {
			in.Disc = number(m[1])
		}
	}
	if in.Title == "" {
		in.Title = base
	}
	if in.Disc == nil {
		in.Disc = folderDisc
	}
	if in.Album == "" {
		in.Album = "Unknown Album"
		if n := len(dirs); n > 0 {
			t, y := parse.Title(dirs[n-1])
			in.Album = cmp.Or(t, dirs[n-1])
			if in.Year == nil {
				in.Year = y
			}
		}
	}
	if in.AlbumArtist == "" && tag(tags, "compilation") == "1" {
		in.AlbumArtist = "Various Artists"
	}
	if in.AlbumArtist == "" {
		in.AlbumArtist = in.Artist
	}
	if in.AlbumArtist == "" {
		in.AlbumArtist = "Unknown Artist"
		if n := len(dirs); n > 1 {
			in.AlbumArtist = dirs[n-2]
		}
	}
	if in.Artist == "" {
		in.Artist = in.AlbumArtist
	}
	for _, g := range strings.Split(tag(tags, "genre"), ";") {
		if g = strings.TrimSpace(g); g != "" {
			in.Genres = append(in.Genres, g)
		}
	}
	return in
}

// syncSong brings one audio file in: a new or changed file is probed, and
// its tags place it under an artist and album. Tags come only from the
// probe, so an unchanged probed file is left alone once its song's
// advisory tag and MusicBrainz ids have been read.
func (s *Scanner) syncSong(ctx context.Context, lib *library.Library, c candidate, sum *Summary) error {
	f, err := s.Files.FileByPath(c.path)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	if f != nil && f.ProbedAt != nil && f.SizeBytes == c.size && f.FileModifiedAt.Equal(c.mod) {
		if err := s.backfill(ctx, f); err != nil {
			return err
		}
		song, err := s.Items.ByPath(lib.ID, item.Song, c.path)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
		if err != nil {
			return nil
		}
		if read, err := s.Items.SongReleaseRead(song.ID); err != nil || read && song.Explicit != nil {
			return err
		}
	}
	res, err := s.probe(ctx, c.path)
	if err != nil {
		sum.ProbeFailed++
		return err
	}
	info := readSong(res.Tags, c.root, c.path)
	info.Explicit = explicit(res.Tags, res.Container)
	song, album, err := s.resolveSong(lib, c, info, sum)
	if err != nil {
		return err
	}
	if err := s.Items.SetSongRelease(song.ID, tag(res.Tags, "musicbrainz_albumid", "musicbrainz album id"),
		tag(res.Tags, "musicbrainz_releasegroupid", "musicbrainz release group id")); err != nil {
		return err
	}
	// A file replaced in place may hold another picture than its album shows.
	replaced := f != nil && (f.SizeBytes != c.size || !f.FileModifiedAt.Equal(c.mod))
	if f == nil {
		f = &mediafile.File{ItemID: &song.ID, Path: c.path}
		if err := s.Files.CreateFile(f); err != nil {
			return err
		}
	} else {
		sum.Updated[item.Song]++
	}
	now := s.Now().UTC()
	f.ItemID, f.SizeBytes, f.FileModifiedAt = &song.ID, c.size, c.mod
	f.Container, f.DurationMs, f.Bitrate, f.ProbedAt = res.Container, res.DurationMs, res.Bitrate, &now
	f.Fingerprint = s.fingerprint(ctx, c.path)
	if err := s.Files.UpdateFile(f); err != nil {
		return err
	}
	for _, t := range res.Tracks {
		t.MediaFileID = f.ID
	}
	if err := s.Files.ReplaceTracks(f.ID, res.Tracks); err != nil {
		return err
	}
	for _, id := range []uid.ID{album.ID, *album.ParentID} {
		if err := s.addGenres(id, info.Genres); err != nil {
			return err
		}
	}
	s.albumArt(ctx, album, c.path, res.Cover, replaced)
	return nil
}

// resolveSong finds or creates the song's album artist, album, performer
// and the song itself. A song whose tags now name another album moves there.
func (s *Scanner) resolveSong(lib *library.Library, c candidate, in songInfo, sum *Summary) (*item.Item, *item.Item, error) {
	if sum.moved[c.path] {
		if err := s.renameAbove(lib, c.path, in, sum); err != nil {
			return nil, nil, err
		}
	}
	artist, err := s.musicItem(lib, nil, item.Artist, in.AlbumArtist, nil, sum)
	if err != nil {
		return nil, nil, err
	}
	album, err := s.musicItem(lib, &artist.ID, item.Album, in.Album, in.Year, sum)
	if err != nil {
		return nil, nil, err
	}
	var performer *uid.ID
	if !strings.EqualFold(in.Artist, in.AlbumArtist) {
		p, err := s.musicItem(lib, nil, item.Artist, in.Artist, nil, sum)
		if err != nil {
			return nil, nil, err
		}
		performer = &p.ID
	}
	song, err := s.Items.ByPath(lib.ID, item.Song, c.path)
	switch {
	case errors.Is(err, db.ErrNotFound):
		song = &item.Item{LibraryID: lib.ID, ParentID: &album.ID, Kind: item.Song, Path: c.path}
		sum.Added[item.Song]++
	case err != nil:
		return nil, nil, err
	}
	was := *song
	song.ParsedTitle, song.SortTitle, song.DiscNumber, song.TrackNumber = in.Title, item.SortName(in.Title), in.Disc, in.Track
	song.ParsedYear, song.ArtistID, song.Explicit = in.Year, performer, &in.Explicit
	if song.ID.IsZero() {
		err := s.Items.Create(song)
		sum.created = append(sum.created, song.ID)
		return song, album, err
	}
	if err := s.Items.Update(song); err != nil {
		return nil, nil, err
	}
	if err := s.pruneArtists(&was, sum); err != nil {
		return nil, nil, err
	}
	if *was.ParentID == album.ID {
		return song, album, nil
	}
	if err := s.Items.SetParent(song.ID, album.ID); err != nil {
		return nil, nil, err
	}
	song.ParentID = &album.ID
	return song, album, s.pruneEmpty(*was.ParentID, sum)
}

// renameAbove gives the album and the album artist of a song found moved
// the names its new path gives them, when all their songs moved with it
// and none has the name yet: a renamed folder is the album or artist it
// was. A moved file's tags are as they were, so only a folder renames one.
func (s *Scanner) renameAbove(lib *library.Library, path string, in songInfo, sum *Summary) error {
	song, err := s.Items.ByPath(lib.ID, item.Song, path)
	if err != nil {
		return err
	}
	album, err := s.Items.Get(*song.ParentID)
	if err != nil {
		return err
	}
	artist, err := s.Items.Get(*album.ParentID)
	if err != nil {
		return err
	}
	albums, err := s.Items.List(item.Filter{ParentID: &artist.ID, Kind: item.Album, Limit: 100000})
	if err != nil {
		return err
	}
	if err := s.renameMoved(artist, in.AlbumArtist, nil, albums, sum); err != nil {
		return err
	}
	return s.renameMoved(album, in.Album, in.Year, []*item.Item{album}, sum)
}

func (s *Scanner) renameMoved(it *item.Item, name string, year *int, albums []*item.Item, sum *Summary) error {
	if strings.EqualFold(it.ParsedTitle, name) {
		return nil
	}
	if _, err := s.Items.Named(it.LibraryID, it.ParentID, it.Kind, name); !errors.Is(err, db.ErrNotFound) {
		return err
	}
	for _, al := range albums {
		songs, err := s.Items.List(item.Filter{ParentID: &al.ID, Kind: item.Song, Limit: 100000})
		if err != nil {
			return err
		}
		if slices.ContainsFunc(songs, func(sg *item.Item) bool { return !sum.moved[sg.Path] }) {
			return nil
		}
	}
	it.ParsedTitle, it.SortTitle = name, item.SortName(name)
	if it.Kind == item.Album {
		it.ParsedYear = year
	}
	return s.Items.Update(it)
}

// musicItem finds an artist or album by name under parent, or creates it.
// An album takes the first year a song gives it.
func (s *Scanner) musicItem(lib *library.Library, parent *uid.ID, kind, name string, year *int, sum *Summary) (*item.Item, error) {
	it, err := s.Items.Named(lib.ID, parent, kind, name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		it = &item.Item{LibraryID: lib.ID, ParentID: parent, Kind: kind, ParsedTitle: name, SortTitle: item.SortName(name), ParsedYear: year}
		sum.Added[kind]++
		return it, s.Items.Create(it)
	case err != nil:
		return nil, err
	case it.ParsedYear == nil && year != nil:
		it.ParsedYear = year
		return it, s.Items.Update(it)
	}
	return it, nil
}

// addGenres adds tag genres to an item's, keeping those it has.
func (s *Scanner) addGenres(itemID uid.ID, names []string) error {
	if len(names) == 0 {
		return nil
	}
	have, err := s.Genres.From(itemID, genre.Local)
	if err != nil {
		return err
	}
	var gs []genre.Genre
	known := map[string]bool{}
	for _, g := range have {
		gs, known[strings.ToLower(g.Name)] = append(gs, *g), true
	}
	added := false
	for _, n := range names {
		if !known[strings.ToLower(n)] {
			gs, known[strings.ToLower(n)], added = append(gs, genre.Genre{Name: n}), true, true
		}
	}
	if !added {
		return nil
	}
	return s.Genres.SetForItem(itemID, genre.Local, gs)
}

var folderCovers = []string{"cover.jpg", "folder.jpg", "front.jpg", "cover.png", "folder.png", "front.png"}

// albumArt gives an album without artwork, or one whose song was replaced,
// the folder cover beside this song, else the song's embedded picture,
// copied under ThumbDir/music by content hash so a new image gets a new
// name. Failures are logged; art never fails a scan.
func (s *Scanner) albumArt(ctx context.Context, album *item.Item, songPath string, embedded, replaced bool) {
	if s.ThumbDir == "" {
		return
	}
	m, err := s.Items.LocalRow(album.ID)
	if err == nil && m.PosterPath != "" && !replaced {
		if _, err := os.Stat(m.PosterPath); err == nil {
			return
		}
	}
	if m == nil {
		m = &item.Metadata{ItemID: album.ID}
	}
	src := ""
	if entries, err := os.ReadDir(filepath.Dir(songPath)); err == nil {
		for _, want := range folderCovers {
			for _, e := range entries {
				if src == "" && strings.EqualFold(e.Name(), want) {
					src = filepath.Join(filepath.Dir(songPath), e.Name())
				}
			}
		}
	}
	dir := filepath.Join(s.ThumbDir, "music")
	tmp := filepath.Join(dir, ".cover-"+album.ID.String()+".jpg")
	defer func() { _ = os.Remove(tmp) }()
	if src == "" && embedded {
		cctx, cancel := context.WithTimeout(ctx, runLimit)
		err := s.Cover(cctx, songPath, tmp)
		cancel()
		if err != nil {
			logstore.Log("scan").Warn("Embedded cover unreadable", "path", songPath, "err", err)
			return
		}
		src = tmp
	}
	if src == "" {
		return
	}
	dest, err := storeByHash(src, dir)
	if err == nil {
		m.PosterPath = dest
		err = s.Items.SetLocal(m)
	}
	if err != nil {
		logstore.Log("scan").Warn("Album art not stored", "album_id", album.ID, "err", err)
	}
}

// storeByHash copies src into dir named by its content hash.
func storeByHash(src, dir string) (string, error) {
	b, err := os.ReadFile(src) //nolint:gosec // a cover beside a library file, or one ffmpeg wrote
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	dest := filepath.Join(dir, hex.EncodeToString(h[:8])+strings.ToLower(filepath.Ext(src)))
	if _, err := os.Stat(dest); err == nil { //nolint:gosec // dest is dir plus a hex hash
		return dest, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return dest, atomicfile.Write(dest, bytes.NewReader(b))
}

// pruneArtists removes the performer a song had, before it left them or
// was itself removed, when they no longer perform or own anything.
func (s *Scanner) pruneArtists(song *item.Item, sum *Summary) error {
	if song.ArtistID == nil {
		return nil
	}
	return s.pruneEmpty(*song.ArtistID, sum)
}

package mediafile_test

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func i(n int) *int { return &n }

func setup(t *testing.T) (*mediafile.Repo, uid.ID, uid.ID) {
	d := dbtest.Open(t)
	l := &library.Library{Name: "m", Kind: library.Movies}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "x"}
	if err := item.New(d).Create(it); err != nil {
		t.Fatal(err)
	}
	return mediafile.New(d), l.ID, it.ID
}

func TestFileCRUDAndQueries(t *testing.T) {
	r, lib, it := setup(t)
	mod := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("x", 7200))
	f := &mediafile.File{ItemID: &it, Path: "/m/a.mkv", SizeBytes: 100, FileModifiedAt: mod}
	if err := r.CreateFile(f); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateFile(&mediafile.File{ItemID: &it, Path: "/m/a.mkv"}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("dup path: %v", err)
	}
	bad := uid.N(999)
	if err := r.CreateFile(&mediafile.File{ItemID: &bad, Path: "/m/z.mkv"}); !errors.Is(err, db.ErrReference) {
		t.Fatalf("bad item: %v", err)
	}
	probed := mod.Add(time.Hour)
	dur := int64(7200000)
	f.Container, f.DurationMs, f.ProbedAt = "matroska", &dur, &probed
	if err := r.UpdateFile(f); err != nil {
		t.Fatal(err)
	}
	g, err := r.File(f.ID)
	if err != nil || g.Container != "matroska" || *g.DurationMs != dur || !g.FileModifiedAt.Equal(mod) || g.FileModifiedAt.Location() != time.UTC ||
		g.CreatedAt.IsZero() || g.ModifiedAt.Before(g.CreatedAt) || *g.ItemID != it {
		t.Fatalf("%+v %v", g, err)
	}
	if p, err := r.FileByPath("/m/a.mkv"); err != nil || p.ID != f.ID {
		t.Fatal(err)
	}
	if fs, _ := r.FilesForItem(it); len(fs) != 1 {
		t.Fatal(len(fs))
	}
	if fs, _ := r.FilesInLibrary(lib); len(fs) != 1 {
		t.Fatal(len(fs))
	}
	if err := r.DeleteFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteFile(f.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

// A file marked missing keeps the instant it first went and is left out of
// the previews work until the mark is cleared.
func TestMissingMark(t *testing.T) {
	r, _, it := setup(t)
	probed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if u, err := r.Unavailable([]uid.ID{it}); err != nil || len(u) != 0 {
		t.Fatalf("an item without files is unavailable: %v %v", u, err)
	}
	f := &mediafile.File{ItemID: &it, Path: "/m/a.mkv", ProbedAt: &probed}
	if err := r.CreateFile(f); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceTracks(f.ID, []*mediafile.Track{{Kind: mediafile.Video, Codec: "h264"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.NeedingPreviews(); len(n) != 1 || n[0].MissingAt != nil {
		t.Fatalf("a present file needs previews: %+v", n)
	}
	first, later := probed.Add(time.Hour), probed.Add(2*time.Hour)
	for _, at := range []time.Time{first, later} {
		if err := r.SetMissing(f, &at); err != nil {
			t.Fatal(err)
		}
	}
	if g, err := r.File(f.ID); err != nil || g.MissingAt == nil || !g.MissingAt.Equal(first) {
		t.Fatalf("missing since %v %v, want %v", g.MissingAt, err, first)
	}
	if n, _ := r.NeedingPreviews(); len(n) != 0 {
		t.Fatal("a missing file is offered for previews")
	}
	// Its item is unavailable until one of the item's files is present.
	if u, err := r.Unavailable([]uid.ID{it}); err != nil || !u[it] {
		t.Fatalf("unavailable %v %v", u, err)
	}
	copied := &mediafile.File{ItemID: &it, Path: "/m/b.mkv"}
	if err := r.CreateFile(copied); err != nil {
		t.Fatal(err)
	}
	if u, err := r.Unavailable([]uid.ID{it}); err != nil || len(u) != 0 {
		t.Fatalf("unavailable with a copy present: %v %v", u, err)
	}
	if err := r.DeleteFile(copied.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMissing(f, nil); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.File(f.ID); g.MissingAt != nil {
		t.Fatalf("mark not cleared: %v", g.MissingAt)
	}
	if n, _ := r.NeedingPreviews(); len(n) != 1 {
		t.Fatal("a file that is back needs previews again")
	}
}

func TestTracksRoundTripAndReplace(t *testing.T) {
	r, _, it := setup(t)
	f := &mediafile.File{ItemID: &it, Path: "/m/a.mkv"}
	if err := r.CreateFile(f); err != nil {
		t.Fatal(err)
	}
	fps := 23.976
	text := false
	br := int64(38_000_000)
	tracks := []*mediafile.Track{
		{StreamIndex: 0, Kind: mediafile.Video, Codec: "hevc", Profile: "Main 10", Level: i(153), Width: i(3840), Height: i(2160),
			FrameRate: &fps, BitDepth: i(10), PixelFormat: "yuv420p10le", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", HDRFormat: "hdr10", Bitrate: &br, IsDefault: true, DVProfile: i(8)},
		{StreamIndex: 1, Kind: mediafile.Audio, Codec: "truehd", Language: "eng", Channels: i(8), ChannelLayout: "7.1", SampleRate: i(48000), IsDefault: true},
		{StreamIndex: 2, Kind: mediafile.Audio, Codec: "aac", Language: "eng", Channels: i(2)},
		{StreamIndex: 3, Kind: mediafile.Subtitle, Codec: "hdmv_pgs_subtitle", Language: "eng", IsForced: true, SubtitleIsText: &text},
	}
	if err := r.ReplaceTracks(f.ID, tracks); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Tracks(f.ID, "")
	if len(got) != 4 {
		t.Fatal(len(got))
	}
	v := got[0]
	if v.Profile != "Main 10" || *v.Level != 153 || *v.FrameRate != fps || v.HDRFormat != "hdr10" || *v.DVProfile != 8 || *v.Bitrate != br || !v.IsDefault || v.Channels != nil || v.SubtitleIsText != nil {
		t.Fatalf("video: %+v", v)
	}
	if s := got[3]; !s.IsForced || s.SubtitleIsText == nil || *s.SubtitleIsText {
		t.Fatalf("sub: %+v", s)
	}
	if a, _ := r.Tracks(f.ID, mediafile.Audio); len(a) != 2 || *a[0].Channels != 8 {
		t.Fatalf("%+v", a)
	}
	if one, err := r.Track(got[1].ID); err != nil || one.Codec != "truehd" {
		t.Fatal(err)
	}

	dup := []*mediafile.Track{{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264"}, {StreamIndex: 0, Kind: mediafile.Audio, Codec: "aac"}}
	if err := r.ReplaceTracks(f.ID, dup); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("dup stream index: %v", err)
	}
	if still, _ := r.Tracks(f.ID, ""); len(still) != 4 {
		t.Fatalf("failed replace must roll back, have %d", len(still))
	}
	if err := r.ReplaceTracks(f.ID, []*mediafile.Track{{StreamIndex: 0, Kind: "data", Codec: "x"}}); err == nil {
		t.Fatal("bad kind accepted")
	}
	if err := r.ReplaceTracks(f.ID, tracks[:1]); err != nil {
		t.Fatal(err)
	}
	if one, _ := r.Tracks(f.ID, ""); len(one) != 1 {
		t.Fatal(len(one))
	}
	_ = r.DeleteFile(f.ID)
	if gone, _ := r.Tracks(f.ID, ""); len(gone) != 0 {
		t.Fatal("tracks should cascade")
	}
}

func TestChapters(t *testing.T) {
	r, _, it := setup(t)
	f := &mediafile.File{ItemID: &it, Path: "/m/a.mkv"}
	_ = r.CreateFile(f)
	cs := []*mediafile.Chapter{{Position: 0, StartMs: 0, EndMs: 1000, Title: "One"}, {Position: 1, StartMs: 1000, EndMs: 2000}}
	if err := r.ReplaceChapters(f.ID, cs); err != nil {
		t.Fatal(err)
	}
	if err := r.SetChapterThumbnail(cs[1].ID, "/cache/c1.jpg"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Chapters(f.ID)
	if len(got) != 2 || got[0].Title != "One" || got[1].ThumbnailPath != "/cache/c1.jpg" || got[1].Title != "" {
		t.Fatalf("%+v", got)
	}
	if err := r.ReplaceChapters(f.ID, []*mediafile.Chapter{{Position: 0}, {Position: 0}}); !errors.Is(err, db.ErrConflict) {
		t.Fatal(err)
	}
	if got, _ := r.Chapters(f.ID); len(got) != 2 {
		t.Fatal("rollback expected")
	}
	if err := r.SetChapterThumbnail(uid.N(999), "x"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

// A file needs chapter thumbnails while a chapter's was neither made nor
// failed since the file's chapters were last stored, whether or not the
// file is probed again.
func TestNeedingChapterThumbnails(t *testing.T) {
	r, _, it := setup(t)
	probed := time.Now().UTC()
	f := &mediafile.File{ItemID: &it, Path: "/m/a.mkv", ProbedAt: &probed}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.CreateFile(f))
	must(r.UpdateFile(f))
	need := func() int {
		t.Helper()
		fs, err := r.NeedingChapterThumbnails()
		must(err)
		return len(fs)
	}
	if need() != 0 {
		t.Fatal("a file without chapters")
	}
	cs := []*mediafile.Chapter{{Position: 0, EndMs: 1000}, {Position: 1, StartMs: 1000, EndMs: 2000}}
	must(r.ReplaceChapters(f.ID, cs))
	must(r.SetChapterThumbnail(cs[0].ID, "/cache/c0.jpg"))
	if need() != 1 {
		t.Fatal("one chapter untried")
	}
	must(r.ChapterThumbnailFailed(cs[1].ID))
	got, _ := r.Chapters(f.ID)
	if need() != 0 || !got[0].ThumbnailTried() || !got[1].ThumbnailTried() || got[1].ThumbnailPath != "" {
		t.Fatalf("both tried: %+v", got)
	}
	must(r.ReplaceChapters(f.ID, []*mediafile.Chapter{{Position: 0, EndMs: 1000}}))
	if got, _ := r.Chapters(f.ID); need() != 1 || got[0].ThumbnailTried() {
		t.Fatalf("chapters stored again are untried: %+v", got)
	}
	must(r.SetMissing(f, &probed))
	if need() != 0 {
		t.Fatal("a missing file")
	}
}

// made gives a file seek preview frames and a chapter thumbnail on disk
// under the data folder root, as the scan does, and returns the preview and
// thumbnail folders.
func made(t *testing.T, d *sql.DB, r *mediafile.Repo, root string, fileID uid.ID) (string, string) {
	t.Helper()
	id := fileID.String()
	previews, thumbs := filepath.Join(root, "previews", id), filepath.Join(root, "thumbnails", id)
	for _, p := range []string{filepath.Join(previews, "0.jpg"), filepath.Join(thumbs, "0.jpg")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := seekpreview.New(d).Put(&seekpreview.SeekPreview{MediaFileID: fileID, Dir: previews, IntervalMs: 1, Count: 1, Width: 1, Height: 1}); err != nil {
		t.Fatal(err)
	}
	cs := []*mediafile.Chapter{{Position: 0, StartMs: 0, EndMs: 1000}}
	if err := r.ReplaceChapters(fileID, cs); err != nil {
		t.Fatal(err)
	}
	if err := r.SetChapterThumbnail(cs[0].ID, filepath.Join(thumbs, "0.jpg")); err != nil {
		t.Fatal(err)
	}
	return previews, thumbs
}

func gone(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// A deleted file's seek preview frames and chapter thumbnails go with it.
func TestDeleteFileRemovesWhatWasMadeFromIt(t *testing.T) {
	d := dbtest.Open(t)
	l := &library.Library{Name: "m", Kind: library.Movies}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "x"}
	if err := item.New(d).Create(it); err != nil {
		t.Fatal(err)
	}
	r := mediafile.New(d)
	f := &mediafile.File{ItemID: &it.ID, Path: "/m/a.mkv"}
	if err := r.CreateFile(f); err != nil {
		t.Fatal(err)
	}
	previews, thumbs := made(t, d, r, t.TempDir(), f.ID)
	if err := r.DeleteFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if !gone(previews, thumbs) {
		t.Fatal("previews or thumbnails left on disk")
	}
}

// What was made from a file is found where the data folder is now, so a
// file deleted after the folder moved still takes it off disk.
func TestDeleteFileAfterTheDataFolderMoved(t *testing.T) {
	d := dbtest.Open(t)
	l := &library.Library{Name: "m", Kind: library.Movies}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "x"}
	if err := item.New(d).Create(it); err != nil {
		t.Fatal(err)
	}
	r := mediafile.New(d)
	f := &mediafile.File{ItemID: &it.ID, Path: "/m/a.mkv"}
	if err := r.CreateFile(f); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	was, now := filepath.Join(root, "was"), filepath.Join(root, "now")
	dbtest.DataDir(t, was)
	made(t, d, r, was, f.ID)
	if err := os.Rename(was, now); err != nil {
		t.Fatal(err)
	}
	dbtest.DataDir(t, now)
	id := f.ID.String()
	if cs, err := r.Chapters(f.ID); err != nil || cs[0].ThumbnailPath != filepath.Join(now, "thumbnails", id, "0.jpg") {
		t.Fatalf("%v %+v", err, cs)
	}
	if err := r.DeleteFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if !gone(filepath.Join(now, "previews", id), filepath.Join(now, "thumbnails", id)) {
		t.Fatal("previews or thumbnails left on disk")
	}
}

// Deleting a title takes off disk what was made from the files that go
// with it and its descendants, and keeps what another title's file has.
func TestDeleteItemRemovesWhatWasMadeFromItsFiles(t *testing.T) {
	d := dbtest.Open(t)
	l := &library.Library{Name: "t", Kind: library.TV}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	items := item.New(d)
	show := &item.Item{LibraryID: l.ID, Kind: item.Show, ParsedTitle: "s"}
	if err := items.Create(show); err != nil {
		t.Fatal(err)
	}
	season := &item.Item{LibraryID: l.ID, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "1", SeasonNumber: i(1)}
	if err := items.Create(season); err != nil {
		t.Fatal(err)
	}
	ep := &item.Item{LibraryID: l.ID, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: "e", EpisodeNumber: i(1)}
	other := &item.Item{LibraryID: l.ID, Kind: item.Show, ParsedTitle: "o"}
	for _, it := range []*item.Item{ep, other} {
		if err := items.Create(it); err != nil {
			t.Fatal(err)
		}
	}
	r := mediafile.New(d)
	only, shared := &mediafile.File{ItemID: &ep.ID, Path: "/t/a.mkv"}, &mediafile.File{ItemID: &ep.ID, Path: "/t/b.mkv"}
	for _, f := range []*mediafile.File{only, shared} {
		if err := r.CreateFile(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := edition.New(d).Place(shared.ID, other.ID, "", nil, false); err != nil {
		t.Fatal(err)
	}
	onlyPreviews, onlyThumbs := made(t, d, r, t.TempDir(), only.ID)
	sharedPreviews, sharedThumbs := made(t, d, r, t.TempDir(), shared.ID)
	if err := items.Delete(show.ID); err != nil {
		t.Fatal(err)
	}
	if !gone(onlyPreviews, onlyThumbs) {
		t.Fatal("previews or thumbnails of a deleted file left on disk")
	}
	if gone(sharedPreviews) || gone(sharedThumbs) {
		t.Fatal("previews or thumbnails of a file another title holds removed")
	}
}

// Deleting a library takes its files' records and what was made from them,
// keeps the media itself on disk, and leaves every other library alone.
func TestDeleteLibraryRemovesWhatWasMadeFromItsFiles(t *testing.T) {
	d := dbtest.Open(t)
	libs, items, r := library.New(d), item.New(d), mediafile.New(d)
	media := filepath.Join(t.TempDir(), "a.mkv")
	if err := os.WriteFile(media, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var files []*mediafile.File
	var libIDs []uid.ID
	for n, p := range []string{media, "/b/b.mkv"} {
		l := &library.Library{Name: strconv.Itoa(n), Kind: library.Movies}
		if err := libs.Create(l, nil); err != nil {
			t.Fatal(err)
		}
		it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "x"}
		if err := items.Create(it); err != nil {
			t.Fatal(err)
		}
		f := &mediafile.File{ItemID: &it.ID, Path: p}
		if err := r.CreateFile(f); err != nil {
			t.Fatal(err)
		}
		files, libIDs = append(files, f), append(libIDs, l.ID)
	}
	deletedPreviews, deletedThumbs := made(t, d, r, t.TempDir(), files[0].ID)
	keptPreviews, keptThumbs := made(t, d, r, t.TempDir(), files[1].ID)
	if err := libs.Delete(libIDs[0]); err != nil {
		t.Fatal(err)
	}
	if !gone(deletedPreviews, deletedThumbs) {
		t.Fatal("previews or thumbnails of a deleted library's file left on disk")
	}
	if gone(media) {
		t.Fatal("media file deleted from disk")
	}
	if _, err := r.File(files[0].ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted library's file record: %v", err)
	}
	if gone(keptPreviews) || gone(keptThumbs) {
		t.Fatal("previews or thumbnails of another library's file removed")
	}
	if fs, err := r.FilesInLibrary(libIDs[1]); err != nil || len(fs) != 1 {
		t.Fatalf("other library's files: %v %v", fs, err)
	}
}

func TestLibraryTotals(t *testing.T) {
	r, lib, it := setup(t)
	if n, b, err := r.Totals(lib); err != nil || n != 0 || b != 0 {
		t.Fatalf("empty library: %d %d %v", n, b, err)
	}
	for i, size := range []int64{100, 2_000_000_000, 3_000_000_000} {
		if err := r.CreateFile(&mediafile.File{ItemID: &it, Path: "/m/" + string(rune('a'+i)) + ".mkv", SizeBytes: size}); err != nil {
			t.Fatal(err)
		}
	}
	if n, b, err := r.Totals(lib); err != nil || n != 3 || b != 5_000_000_100 {
		t.Fatalf("totals: %d %d %v", n, b, err)
	}
	if n, b, _ := r.Totals(uid.New()); n != 0 || b != 0 {
		t.Fatalf("other library: %d %d", n, b)
	}
}

// A library's files are found from its items, never by walking every file
// of every library.
func TestLibraryFilesStartFromItsItems(t *testing.T) {
	p := &dbtest.Planner{DBTX: dbtest.Open(t)}
	r := mediafile.New(p)
	if _, _, err := r.Totals(uid.N(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FilesInLibrary(uid.N(1)); err != nil {
		t.Fatal(err)
	}
	for _, plan := range p.Plans {
		for line := range strings.SplitSeq(plan, "\n") {
			if line == "SCAN f" || strings.HasPrefix(line, "SCAN f ") {
				t.Error(plan)
			}
		}
	}
}

// Each problem lists its files with their item's library and title, a file
// of no item too, and counts what it lists; forgetting a probe clears it.
func TestProblems(t *testing.T) {
	r, lib, it := setup(t)
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	zero := int64(0)
	stated := &mediafile.File{ItemID: &it, Path: "/m/a.mkv", ProbedAt: &at, DurationMs: &zero}
	loose := &mediafile.File{Path: "/m/b.mkv", ProbeFailedAt: &at, ProbeError: "no"}
	gone := &mediafile.File{ItemID: &it, Path: "/m/c.mkv", ProbedAt: &at}
	for _, f := range []*mediafile.File{stated, loose, gone} {
		if err := r.CreateFile(f); err != nil {
			t.Fatal(err)
		}
		if err := r.UpdateFile(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetMissing(gone, &at); err != nil {
		t.Fatal(err)
	}
	for problem, want := range map[string]*mediafile.File{mediafile.NoDuration: stated, mediafile.Unreadable: loose, mediafile.Missing: gone} {
		got, err := r.Problems(problem, nil)
		if n, _ := r.ProblemCount(problem); err != nil || len(got) != 1 || n != 1 || got[0].ID != want.ID {
			t.Fatalf("%s: %+v %d %v", problem, got, n, err)
		}
	}
	if got, _ := r.Problems(mediafile.NoDuration, &lib); len(got) != 1 || got[0].LibraryID == nil || *got[0].LibraryID != lib || got[0].Title != "x" {
		t.Fatalf("in its library %+v", got)
	}
	if got, _ := r.Problems(mediafile.Unreadable, nil); got[0].LibraryID != nil || got[0].Title != "" || got[0].ProbeError != "no" || got[0].ItemID != nil {
		t.Fatalf("of no item %+v", got[0])
	}
	other := uid.New()
	if got, _ := r.Problems(mediafile.NoDuration, &other); len(got) != 0 {
		t.Fatalf("another library %+v", got)
	}
	if err := r.ForgetProbe(stated.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.ProblemCount(mediafile.NoDuration); n != 0 {
		t.Fatalf("a forgotten probe still counts: %d", n)
	}
}

// A file's keyframes hold for the modification time they were probed at; a
// failure there leaves the file out of NeedingKeyframes until it changes.
func TestKeyframesFollowTheFilesModificationTime(t *testing.T) {
	r, _, it := setup(t)
	mod := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	probed := mod.Add(time.Hour)
	f := &mediafile.File{ItemID: &it, Path: "/m/a.mkv", FileModifiedAt: mod, ProbedAt: &probed}
	audio := &mediafile.File{ItemID: &it, Path: "/m/a.flac", FileModifiedAt: mod, ProbedAt: &probed}
	for _, x := range []*mediafile.File{f, audio} {
		if err := r.CreateFile(x); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ReplaceTracks(f.ID, []*mediafile.Track{{Kind: mediafile.Video, Codec: "h264"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceTracks(audio.ID, []*mediafile.Track{{Kind: mediafile.Audio, Codec: "flac"}}); err != nil {
		t.Fatal(err)
	}
	need := func() int {
		t.Helper()
		n, err := r.NeedingKeyframes()
		if err != nil || len(n) > 0 && n[0].ID != f.ID {
			t.Fatalf("%+v %v", n, err)
		}
		return len(n)
	}
	if _, err := r.Keyframes(f.ID, mod); !errors.Is(err, db.ErrNotFound) || need() != 1 {
		t.Fatalf("never probed: %v", err)
	}
	at := []float64{0.042, 4.3, 7199.091999}
	if err := r.ReplaceKeyframes(f.ID, mod, []float64{9, 10}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceKeyframes(f.ID, mod, at); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Keyframes(f.ID, mod); err != nil || !slices.Equal(got, at) || need() != 0 {
		t.Fatalf("%v %v, want %v", got, err, at)
	}
	changed := mod.Add(time.Second)
	if _, err := r.Keyframes(f.ID, changed); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("another modification time: %v", err)
	}
	f.FileModifiedAt = changed
	if err := r.UpdateFile(f); err != nil || need() != 1 {
		t.Fatalf("a changed file needs keyframes again: %v", err)
	}
	if err := r.KeyframesFailed(f.ID, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Keyframes(f.ID, mod); !errors.Is(err, db.ErrNotFound) || need() != 0 {
		t.Fatalf("a failure replaces what was probed before: %v", err)
	}
	if err := r.ReplaceKeyframes(uid.N(999), mod, at); !errors.Is(err, db.ErrReference) {
		t.Fatalf("unknown file: %v", err)
	}
	if err := r.ReplaceKeyframes(f.ID, changed, at); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Keyframes(f.ID, changed); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("keyframes outlived their file: %v", err)
	}
}

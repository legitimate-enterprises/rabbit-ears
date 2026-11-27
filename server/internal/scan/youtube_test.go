package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

func youtubeEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, library.YouTube, "")
	e.s.YouTube = youtube.New(e.d)
	src, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", "youtube"))
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("youtube fixtures missing; run make deps")
	}
	if err := os.CopyFS(e.root, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) videos() map[string]*youtube.VideoView {
	e.t.Helper()
	vs, err := e.s.YouTube.Videos(youtube.VideoFilter{LibraryID: &e.lib.ID, Limit: -1})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*youtube.VideoView{}
	for _, v := range vs {
		out[v.Title] = v
	}
	return out
}

func (e *env) chapters(videoID uid.ID) []string {
	e.t.Helper()
	fs, err := e.files.FilesForItem(videoID)
	if err != nil || len(fs) != 1 {
		e.t.Fatalf("files %v %v", fs, err)
	}
	cs, err := e.files.Chapters(fs[0].ID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.Title+" "+strconv.FormatInt(c.StartMs, 10)+"-"+strconv.FormatInt(c.EndMs, 10))
	}
	return out
}

// Scanning a folder Pinchflat filled makes a channel of each top-level
// folder and a video of each video file beneath it, from whichever
// sidecars it has, and skips download strays. Nothing in the folder is
// written or deleted.
func TestScanYouTubeFolder(t *testing.T) {
	e := youtubeEnv(t)
	gears := filepath.Join(e.root, "Clockwork Corner", "Gear Trains.mp4")
	downloaded := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	must(t, os.Chtimes(gears, downloaded, downloaded))
	// A download caught mid-merge: one stream's file and the merge's own.
	for _, part := range []string{"Gear Trains.f137.mp4", "Gear Trains.temp.mp4"} {
		must(t, os.Link(gears, filepath.Join(filepath.Dir(gears), part)))
	}
	before := tree(t, e.root)

	sum, err := e.s.Scan(context.Background(), e.lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.String(); got != "2 channels, 3 videos added" {
		t.Fatalf("summary %q", got)
	}
	if after := tree(t, e.root); !slices.Equal(after, before) {
		t.Fatalf("the scan changed the folder:\n%v\n%v", before, after)
	}

	chs, err := e.s.YouTube.Channels(youtube.ChannelFilter{LibraryID: &e.lib.ID})
	if err != nil || len(chs) != 2 {
		t.Fatalf("channels %+v %v", chs, err)
	}
	clock, bench := chs[0], chs[1]
	if bench.Title != "Workbench Weekly" || bench.Overview != "Projects from a small shop." || bench.ExternalID != "UCworkbenchweekly00000a" ||
		bench.VideoCount != 2 || bench.LastVideoAt.Format(time.DateOnly) != "2020-03-01" {
		t.Errorf("bench %+v", bench)
	}
	if clock.Title != "Clockwork Corner" || clock.Overview != "Gears, one at a time." || clock.ExternalID != "PLclockworkcorner00000b" || clock.VideoCount != 1 {
		t.Errorf("clock %+v", clock)
	}
	if m, err := e.items.Metadata(bench.ID); err != nil || m.PosterPath != filepath.Join(e.root, "Workbench Weekly", "poster.jpg") || m.PosterFrom != item.Local {
		t.Errorf("bench art %+v %v", m, err)
	}
	if m, err := e.items.Metadata(clock.ID); err != nil || m.PosterPath != "" {
		t.Errorf("clock art %+v %v", m, err)
	}

	vs := e.videos()
	if len(vs) != 3 {
		t.Fatalf("videos %v", vs)
	}
	// The .nfo beats the .info.json; its timestamp beats the air date.
	bb := vs["Building a Bench"]
	if bb == nil || bb.ChannelID != bench.ID || bb.Overview != "A bench from one board." || bb.ExternalID != "Wkbnch19801" ||
		!bb.PublishedAt.Equal(time.Date(2019, 8, 2, 14, 0, 0, 0, time.UTC)) || bb.Height == nil || *bb.Height != 180 {
		t.Fatalf("bench video %+v", bb)
	}
	if m, _ := e.items.Metadata(bb.ID); m.PosterPath != filepath.Join(e.root, "Workbench Weekly", "Season 2019", "s2019e080200 - Building a Bench-thumb.jpg") {
		t.Errorf("thumbnail %q", m.PosterPath)
	}
	if got := e.chapters(bb.ID); !slices.Equal(got, []string{"Intro 0-500", "Sponsor 500-1500", "Build 1500-3000"}) {
		t.Errorf("info.json chapters with the sponsor cut out %v", got)
	}
	// Without a timestamp the air date stands; embedded chapters win.
	sc := vs["Sharpening Chisels"]
	if sc == nil || sc.ExternalID != "Wkbnch20301" || !sc.PublishedAt.Equal(time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("chisels %+v", sc)
	}
	if got := e.chapters(sc.ID); !slices.Equal(got, []string{"Grind 0-1500", "Hone 1500-3000"}) {
		t.Errorf("embedded chapters %v", got)
	}
	// No sidecars: the file names it and its modification time dates it.
	gt := vs["Gear Trains"]
	if gt == nil || gt.ChannelID != clock.ID || gt.ExternalID != "" || !gt.PublishedAt.Equal(downloaded) {
		t.Fatalf("gears %+v", gt)
	}

	probes := *e.probes
	if sum, err = e.s.Scan(context.Background(), e.lib, nil); err != nil || *e.probes != probes || sum.String() != "No changes; 3 videos checked" {
		t.Fatalf("rescan probed %d: %q %v", *e.probes-probes, sum, err)
	}

	// Sidecars that arrive later are read by the next scan.
	base := filepath.Join(e.root, "Clockwork Corner", "Gear Trains")
	must(t, os.WriteFile(base+".info.json", []byte(`{"id":"Clock000001","title":"Gear Trains Explained","description":"Spur and helical.",
		"timestamp":1700000000,"chapters":[{"start_time":0,"end_time":2,"title":"Spur"},{"start_time":2,"end_time":3,"title":"Helical"}]}`), 0o600))
	fixtureCopy(t, filepath.Join("youtube", "Workbench Weekly", "poster.jpg"), base+"-thumb.jpg")
	if sum, err = e.s.Scan(context.Background(), e.lib, nil); err != nil || sum.String() != "1 video updated" {
		t.Fatalf("after sidecars %q %v", sum, err)
	}
	gt = e.videos()["Gear Trains Explained"]
	if gt == nil || gt.ID != vs["Gear Trains"].ID || gt.Overview != "Spur and helical." || gt.ExternalID != "Clock000001" ||
		!gt.PublishedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("gears after sidecars %+v", gt)
	}
	if m, _ := e.items.Metadata(gt.ID); m.PosterPath != base+"-thumb.jpg" {
		t.Errorf("late thumbnail %q", m.PosterPath)
	}
	if got := e.chapters(gt.ID); !slices.Equal(got, []string{"Spur 0-2000", "Helical 2000-3000"}) {
		t.Errorf("late chapters %v", got)
	}

	// A channel folder gone from disk keeps the channel, what its sidecars
	// said and its video, whose file is marked.
	must(t, os.RemoveAll(filepath.Dir(base)))
	if sum, err = e.s.Scan(context.Background(), e.lib, nil); err != nil || sum.String() != "No changes; 2 videos checked; 1 file missing" {
		t.Fatalf("after removal %q %v", sum, err)
	}
	chs, _ = e.s.YouTube.Channels(youtube.ChannelFilter{LibraryID: &e.lib.ID})
	if len(chs) != 2 || chs[0].ID != clock.ID || chs[0].Overview != "Gears, one at a time." || chs[0].ExternalID != "PLclockworkcorner00000b" ||
		chs[0].VideoCount != 1 || !e.missing(base+".mp4") {
		t.Fatalf("channels after removal %+v, missing %v", chs, e.missing(base+".mp4"))
	}
}

// tree lists every path under root with its size and modification time.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	must(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			out = append(out, p+" "+strconv.FormatInt(info.Size(), 10)+" "+info.ModTime().String())
		}
		return err
	}))
	return out
}

func TestYouTubeChapters(t *testing.T) {
	sp := func(start, end float64, title string) span { return span{Start: start, End: end, Title: title} }
	got := func(cs []*mediafile.Chapter) []string {
		var out []string
		for i, c := range cs {
			if c.Position != i {
				t.Errorf("position %d at %d", c.Position, i)
			}
			out = append(out, c.Title+" "+strconv.FormatInt(c.StartMs, 10)+"-"+strconv.FormatInt(c.EndMs, 10))
		}
		return out
	}
	cases := []struct {
		name              string
		chapters, sponsor []span
		want              []string
	}{
		{"none", nil, nil, nil},
		{"chapters alone", []span{sp(0, 60.5, "A"), sp(60.5, 120, "B")}, nil, []string{"A 0-60500", "B 60500-120000"}},
		{"a segment inside one chapter splits it", []span{sp(0, 100, "A")}, []span{sp(10, 20, "Sponsor")},
			[]string{"A 0-10000", "Sponsor 10000-20000", "A 20000-100000"}},
		{"a segment across a boundary trims both", []span{sp(0, 50, "A"), sp(50, 100, "B")}, []span{sp(40, 60, "Sponsor")},
			[]string{"A 0-40000", "Sponsor 40000-60000", "B 60000-100000"}},
		{"a segment covering a chapter drops it", []span{sp(0, 10, "A"), sp(10, 20, "B")}, []span{sp(0, 10, "Intro")},
			[]string{"Intro 0-10000", "B 10000-20000"}},
		{"segments without chapters", nil, []span{sp(30, 40, "Outro"), sp(0, 5, "Intro")}, []string{"Intro 0-5000", "Outro 30000-40000"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if g := got(chaptersFrom(c.chapters, c.sponsor)); !slices.Equal(g, c.want) {
				t.Errorf("got %v, want %v", g, c.want)
			}
		})
	}
}

// YouTube libraries scan nightly like every other kind.
func TestSyncSchedulesYouTubeScans(t *testing.T) {
	e := newEnv(t, library.YouTube, "")
	r, tasks := newRunner(t, e)
	must(t, r.Sync())
	ts, _ := tasks.List()
	var kinds []string
	for _, x := range ts {
		kinds = append(kinds, x.Kind)
	}
	slices.Sort(kinds)
	if want := []string{"local", "metadata", "previews", "scan:" + e.lib.ID.String()}; !slices.Equal(kinds, want) {
		t.Fatalf("tasks %v, want %v", kinds, want)
	}
}

// A video recorded without a fingerprint gets one on the next scan even
// though its file and sidecars are unchanged, so a move can be recognized.
func TestUnchangedVideosAreFingerprinted(t *testing.T) {
	e := youtubeEnv(t)
	e.mustScan()
	_, err := e.d.Exec(`UPDATE media_file SET fingerprint = NULL`)
	must(t, err)
	e.mustScan()
	var n, with int
	must(t, e.d.QueryRow(`SELECT COUNT(*), COUNT(fingerprint) FROM media_file`).Scan(&n, &with))
	if n == 0 || with != n {
		t.Fatalf("%d files, %d fingerprinted", n, with)
	}
}

// A probed file filed under no item is filed under its video and its
// sidecars read, without probing it again; the next scan changes nothing.
func TestAProbedFileWithNoItemIsFiledWithItsSidecars(t *testing.T) {
	e := youtubeEnv(t)
	base := filepath.Join(e.root, "Clockwork Corner", "Gear Trains")
	must(t, os.WriteFile(base+".info.json", []byte(`{"id":"Clock000001","title":"Gear Trains Explained","timestamp":1700000000}`), 0o600))
	st, err := os.Stat(base + ".mp4")
	must(t, err)
	probed := time.Now()
	f := &mediafile.File{Path: base + ".mp4", SizeBytes: st.Size(), FileModifiedAt: st.ModTime(), Container: "mp4", ProbedAt: &probed}
	must(t, e.files.CreateFile(f))
	e.mustScan()
	if *e.probes != 2 {
		t.Errorf("probed %d files, want the 2 unprobed", *e.probes)
	}
	gt := e.videos()["Gear Trains Explained"]
	if gt == nil || gt.ExternalID != "Clock000001" {
		t.Fatalf("sidecars not read: %+v", gt)
	}
	if fs, err := e.files.FilesForItem(gt.ID); err != nil || len(fs) != 1 || fs[0].ID != f.ID {
		t.Fatalf("files %v %v", fs, err)
	}
	if sum, err := e.s.Scan(context.Background(), e.lib, nil); err != nil || *e.probes != 2 || sum.String() != "No changes; 3 videos checked" {
		t.Fatalf("rescan %q %v", sum, err)
	}
}

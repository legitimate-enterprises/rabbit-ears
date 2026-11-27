package youtube_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

type world struct {
	r       *youtube.Repo
	items   *item.Repo
	files   *mediafile.Repo
	states  *watchstate.Repo
	library uid.ID
	viewer  uid.ID
}

func newWorld(t *testing.T) world {
	t.Helper()
	d := dbtest.OpenPreferLocal(t)
	l := &library.Library{Name: "YouTube", Kind: library.YouTube}
	must(t, library.New(d).Create(l, nil))
	w := world{r: youtube.New(d), items: item.New(d), files: mediafile.New(d), states: watchstate.New(d), library: l.ID}
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "Adult", PasswordHash: "x"}
	must(t, account.New(d).Create(a, nil))
	w.viewer = a.ID
	return w
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (w world) item(t *testing.T, kind, title string, parent *uid.ID) uid.ID {
	t.Helper()
	it := &item.Item{LibraryID: w.library, Kind: kind, ParsedTitle: title, ParentID: parent}
	must(t, w.items.Create(it))
	return it.ID
}

// video adds a scanned video published at the RFC 3339 instant published.
func (w world) video(t *testing.T, channel uid.ID, external, title, published string) uid.ID {
	t.Helper()
	id := w.item(t, item.Video, title, &channel)
	must(t, w.items.SetLocal(&item.Metadata{ItemID: id, Status: item.Matched, Title: title, ExternalID: external}))
	at, err := time.Parse(time.RFC3339, published)
	must(t, err)
	must(t, w.r.SetSource(id, at, 1))
	must(t, w.files.CreateFile(&mediafile.File{ItemID: &id, Path: "/yt/" + external + ".mp4", SizeBytes: 1, FileModifiedAt: time.Now()}))
	return id
}

func (w world) channel(t *testing.T, title, external string) uid.ID {
	t.Helper()
	id := w.item(t, item.Channel, title, nil)
	must(t, w.items.SetLocal(&item.Metadata{ItemID: id, Status: item.Matched, ExternalID: external}))
	return id
}

func (w world) watch(t *testing.T, id uid.ID, watched bool, pos int64) {
	t.Helper()
	now := time.Now()
	s := &watchstate.State{AccountID: w.viewer, ItemID: id, LastPlayedAt: &now}
	s.Mark(watched, pos)
	must(t, w.states.Save(s))
}

func ids(vs []*youtube.VideoView) []uid.ID {
	var out []uid.ID
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func TestChannelAndVideoViews(t *testing.T) {
	w := newWorld(t)
	a := w.channel(t, "Workbench Weekly", "UCa")
	b := w.channel(t, "Clockwork Corner", "PLb")
	empty := w.channel(t, "Quiet Folder", "")
	// Two videos of one day order by their publish instants.
	a1 := w.video(t, a, "a1", "Dishwashers", "2026-01-01T09:00:00Z")
	a2 := w.video(t, a, "a2", "Toasters", "2026-03-01T08:00:00Z")
	a3 := w.video(t, a, "a3", "Kettles", "2026-03-01T20:30:00Z")
	b1 := w.video(t, b, "b1", "Clock", "2026-02-01T12:00:00Z")
	w.watch(t, a1, true, 0)
	w.watch(t, a2, false, 9000)

	chs, err := w.r.Channels(youtube.ChannelFilter{AccountID: &w.viewer})
	if err != nil || len(chs) != 2 || chs[0].ID != b || chs[1].ID != a {
		t.Fatalf("A to Z, only with videos: %+v %v", chs, err)
	}
	if c := chs[1]; c.VideoCount != 3 || c.UnwatchedCount != 2 || c.LastVideoAt.Format(time.DateOnly) != "2026-03-01" || c.ExternalID != "UCa" {
		t.Fatalf("%+v", c)
	}
	if latest, _ := w.r.Channels(youtube.ChannelFilter{Sort: "latest"}); latest[0].ID != a {
		t.Fatalf("latest first: %+v", latest)
	}
	if one, _ := w.r.Channels(youtube.ChannelFilter{ID: &empty}); len(one) != 1 || one[0].VideoCount != 0 {
		t.Fatalf("by id: %+v", one)
	}
	if q, _ := w.r.Channels(youtube.ChannelFilter{Q: "clock"}); len(q) != 1 || q[0].ID != b {
		t.Fatalf("q: %+v", q)
	}

	vs, err := w.r.Videos(youtube.VideoFilter{AccountID: &w.viewer})
	if err != nil || !slices.Equal(ids(vs), []uid.ID{a3, a2, b1, a1}) {
		t.Fatalf("newest: %v %v", ids(vs), err)
	}
	if v := vs[1]; v.ChannelTitle != "Workbench Weekly" || v.Watched || v.PositionMs != 9000 || v.LastPlayedAt == nil ||
		v.PublishedAt.Format(time.DateOnly) != "2026-03-01" || v.ExternalID != "a2" {
		t.Fatalf("%+v", v)
	}
	if vs, _ := w.r.Videos(youtube.VideoFilter{AccountID: &w.viewer, Unwatched: true, Sort: "oldest"}); !slices.Equal(ids(vs), []uid.ID{b1, a2, a3}) {
		t.Fatalf("unwatched oldest: %v", ids(vs))
	}
	if vs, _ := w.r.Videos(youtube.VideoFilter{Sort: "channel"}); !slices.Equal(ids(vs), []uid.ID{b1, a3, a2, a1}) {
		t.Fatalf("by channel: %v", ids(vs))
	}
	if vs, _ := w.r.Videos(youtube.VideoFilter{ChannelID: &a, Q: "TOAST"}); !slices.Equal(ids(vs), []uid.ID{a2}) {
		t.Fatalf("q: %v", ids(vs))
	}

	// A watched video played again partway is in progress too.
	time.Sleep(2 * time.Millisecond)
	w.watch(t, b1, true, 4000)
	if vs, _ := w.r.Videos(youtube.VideoFilter{AccountID: &w.viewer, InProgress: true, Sort: "played"}); !slices.Equal(ids(vs), []uid.ID{b1, a2}) {
		t.Fatalf("in progress, latest played first: %v", ids(vs))
	}
}

// A video's stamp reads back as written; one never read reads as -1.
func TestStamp(t *testing.T) {
	w := newWorld(t)
	ch := w.channel(t, "Workbench Weekly", "")
	v := w.item(t, item.Video, "Unread", &ch)
	if s, err := w.r.Stamp(v); err != nil || s != -1 {
		t.Fatalf("unread %d %v", s, err)
	}
	must(t, w.items.SetLocal(&item.Metadata{ItemID: v, Status: item.Matched}))
	if s, err := w.r.Stamp(v); err != nil || s != -1 {
		t.Fatalf("metadata without a source %d %v", s, err)
	}
	must(t, w.r.SetSource(v, time.Unix(1564754400, 0), 42))
	if s, err := w.r.Stamp(v); err != nil || s != 42 {
		t.Fatalf("stamp %d %v", s, err)
	}
	// Rewriting the metadata keeps the source columns.
	must(t, w.items.SetLocal(&item.Metadata{ItemID: v, Status: item.Matched, Title: "Read"}))
	if s, _ := w.r.Stamp(v); s != 42 {
		t.Fatalf("stamp after metadata %d", s)
	}
}

// Channel and video pages read each row's shown metadata by its key;
// building every title's shown metadata first costs a whole-library pass.
func TestViewsLookUpEachRowsMetadata(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	p := &dbtest.Planner{DBTX: d}
	r := youtube.New(p)
	if _, err := r.Channels(youtube.ChannelFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Videos(youtube.VideoFilter{}); err != nil {
		t.Fatal(err)
	}
	for _, plan := range p.Plans {
		if strings.Contains(plan, "MATERIALIZE item_shown") {
			t.Error(plan)
		}
	}
}

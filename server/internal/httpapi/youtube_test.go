package httpapi_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

type tube struct {
	lib          int64
	channel      int64
	playlist     int64 // a channel folder that follows a playlist
	newer, older int64
}

// youtubeLibrary makes a YouTube library holding a channel folder with two
// scanned videos and a playlist folder with one.
func (e *env) youtubeLibrary() tube {
	e.t.Helper()
	tb := tube{lib: e.library("youtube")}
	items, files, yt := e.svc.Items.Repo(), e.svc.MediaFiles.Repo(), e.svc.YouTube.Repo()
	dir := e.t.TempDir()
	folder := func(title, external string) int64 {
		ch := &item.Item{LibraryID: tb.lib, Kind: item.Channel, Path: filepath.Join(dir, title), ParsedTitle: title}
		must(e.t, items.Create(ch))
		must(e.t, items.SetLocal(&item.Metadata{ItemID: ch.ID, Status: item.Matched,
			Overview: "Projects from a small shop.", ExternalID: external}))
		return ch.ID
	}
	tb.channel, tb.playlist = folder("Workbench Weekly", "UCworkbenchweekly000000a"), folder("Clockwork Corner", "PLclockworkcorner00000b")
	add := func(channel int64, external, title string, published time.Time, height int) int64 {
		p := filepath.Join(dir, external+".mp4")
		v := &item.Item{LibraryID: tb.lib, ParentID: &channel, Kind: item.Video, Path: p, ParsedTitle: title}
		must(e.t, items.Create(v))
		must(e.t, items.SetLocal(&item.Metadata{ItemID: v.ID, Status: item.Matched, Title: title, ExternalID: external}))
		must(e.t, yt.SetSource(v.ID, published, 0))
		must(e.t, os.WriteFile(p, []byte("video"), 0o600))
		dur := int64(612000)
		f := &mediafile.File{ItemID: v.ID, Path: p, SizeBytes: 5, ModifiedAt: time.Now(), DurationMs: &dur}
		must(e.t, files.CreateFile(f))
		must(e.t, files.ReplaceTracks(f.ID, []*mediafile.Track{{MediaFileID: f.ID, Kind: mediafile.Video, Codec: "h264", Width: ptr(1920), Height: &height}}))
		return v.ID
	}
	// Late on 1 March UTC: its publish date is the UTC one in any server zone.
	tb.newer = add(tb.channel, "aaaaaaaaaaa", "Building a bench", time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC), 1080)
	tb.older = add(tb.channel, "bbbbbbbbbbb", "Sharpening chisels", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC), 720)
	add(tb.playlist, "ccccccccccc", "Gear trains", time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC), 720)
	return tb
}

func ptr[T any](v T) *T { return &v }

func TestYouTubeBrowsing(t *testing.T) {
	e := newEnv(t)
	tb := e.youtubeLibrary()
	tok := e.plain.token

	var chs api.YouTubeChannelPage
	e.expect(200, "GET", "/youtube/channels?sort=latest&libraryId="+itoa(tb.lib), tok, nil, &chs)
	if len(chs.Items) != 2 || chs.Items[1].Id != tb.playlist {
		t.Fatalf("%+v", chs)
	}
	if pl := chs.Items[1]; *pl.SourceUrl != "https://www.youtube.com/playlist?list=PLclockworkcorner00000b" || *pl.ExternalId != "PLclockworkcorner00000b" {
		t.Fatalf("playlist folder %+v", pl)
	}
	c := chs.Items[0]
	if c.Title != "Workbench Weekly" || c.VideoCount != 2 || c.UnwatchedCount != 2 || c.LastVideoOn.String() != "2026-03-01" ||
		*c.SourceUrl != "https://www.youtube.com/channel/UCworkbenchweekly000000a" || *c.Overview != "Projects from a small shop." {
		t.Fatalf("%+v", c)
	}
	e.expect(200, "GET", "/youtube/channels/"+itoa(tb.channel), tok, nil, &c)
	e.expect(404, "GET", "/youtube/channels/"+itoa(tb.newer), tok, nil, nil)

	// Home's New from your channels.
	var vids api.YouTubeVideoPage
	e.expect(200, "GET", "/youtube/videos?sort=newest&unwatched=true&limit=1", tok, nil, &vids)
	if len(vids.Items) != 1 || vids.NextOffset == nil || vids.Items[0].Id != tb.newer {
		t.Fatalf("%+v", vids)
	}
	v := vids.Items[0]
	if v.ChannelTitle != "Workbench Weekly" || *v.Height != 1080 || *v.DurationMs != 612000 || v.PublishedOn.String() != "2026-03-01" ||
		*v.SourceUrl != "https://www.youtube.com/watch?v=aaaaaaaaaaa" || v.Watched {
		t.Fatalf("%+v", v)
	}
	e.expect(200, "GET", "/youtube/videos?sort=oldest&channelId="+itoa(tb.channel), tok, nil, &vids)
	if len(vids.Items) != 2 || vids.Items[0].Id != tb.older {
		t.Fatalf("oldest: %+v", vids)
	}
	e.expect(200, "GET", "/youtube/videos?q=chisels", tok, nil, &vids)
	if len(vids.Items) != 1 || vids.Items[0].Id != tb.older {
		t.Fatalf("q: %+v", vids)
	}
	e.expect(200, "GET", "/youtube/videos/"+itoa(tb.newer), tok, nil, &v)
	e.expect(404, "GET", "/youtube/videos/"+itoa(tb.channel), tok, nil, nil)

	// Mark all watched, then the per-video watch state reads it back.
	p := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(204, "PUT", p+"/youtube/channels/"+itoa(tb.channel)+"/watch-state", tok, map[string]any{"watched": true}, nil)
	e.expect(200, "GET", "/youtube/videos?unwatched=true&channelId="+itoa(tb.channel), tok, nil, &vids)
	if len(vids.Items) != 0 {
		t.Fatalf("unwatched after Mark all watched: %+v", vids)
	}
	e.expect(200, "GET", "/youtube/channels/"+itoa(tb.channel), tok, nil, &c)
	if c.UnwatchedCount != 0 {
		t.Fatalf("%+v", c)
	}
	// Another adult's watch state is their own.
	e.expect(200, "GET", "/youtube/videos?unwatched=true", e.admin.token, nil, &vids)
	if len(vids.Items) != 3 {
		t.Fatalf("other adult: %+v", vids)
	}
	e.expect(404, "PUT", p+"/youtube/channels/"+itoa(tb.newer)+"/watch-state", tok, map[string]any{"watched": true}, nil)
	e.expect(404, "PUT", "/profiles/"+itoa(e.admin.account.ID)+"/youtube/channels/"+itoa(tb.channel)+"/watch-state", tok, map[string]any{"watched": true}, nil)
}

// Marking a channel's videos as of an instant leaves a video changed after
// it.
func TestPutYouTubeChannelWatchStateAt(t *testing.T) {
	e := newEnv(t)
	tb := e.youtubeLibrary()
	p := "/profiles/" + itoa(e.plain.account.ID)
	minuteAgo := time.Now().UTC().Add(-time.Minute)
	e.expect(200, "PUT", p+"/watch-state/"+itoa(tb.newer), e.plain.token, map[string]any{"watched": false, "positionMs": 5000, "at": minuteAgo}, nil)
	e.expect(204, "PUT", p+"/youtube/channels/"+itoa(tb.channel)+"/watch-state", e.plain.token,
		map[string]any{"watched": true, "at": minuteAgo.Add(-time.Hour)}, nil)
	if watched(t, e, e.plain.account.ID, tb.newer) || !watched(t, e, e.plain.account.ID, tb.older) {
		t.Fatal("an older mark overwrote a newer state, or skipped one without")
	}
}

// Marking a channel's videos watched is all or nothing.
func TestPutYouTubeChannelWatchStateFailsWhole(t *testing.T) {
	e := newEnv(t)
	tb := e.youtubeLibrary()
	me := e.plain.account.ID
	_, err := e.db.Exec(`CREATE TRIGGER fail_second BEFORE INSERT ON watch_state WHEN (SELECT COUNT(*) FROM watch_state) >= 1
		BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	must(t, err)
	e.expect(500, "PUT", "/profiles/"+itoa(me)+"/youtube/channels/"+itoa(tb.channel)+"/watch-state", e.plain.token, map[string]any{"watched": true}, nil)
	for _, id := range []int64{tb.newer, tb.older} {
		if watched(t, e, me, id) {
			t.Errorf("video %d marked by a failed request", id)
		}
	}
}

package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// Under Remote preferred, a channel folder whose poster is a video's
// frame, as Pinchflat leaves a playlist's, shows its uploader's YouTube
// avatar once fetched: the channel named in its videos' .info.json. An
// outage keeps the folder's art and retries an hour later; the avatar is
// then refreshed on the metadata schedule, not by every scan. A square
// poster is left alone.
func TestChannelAvatarForAPlaylistFolder(t *testing.T) {
	e := youtubeEnv(t)
	must(t, e.s.Settings.SetSources(serverinfo.Sources{Source: serverinfo.Remote, Mode: serverinfo.Preferred}))
	clock := filepath.Join(e.root, "Clockwork Corner")
	fixtureCopy(t, filepath.Join("youtube", "Workbench Weekly", "Season 2019", "s2019e080200 - Building a Bench-thumb.jpg"), filepath.Join(clock, "poster.jpg"))
	must(t, os.WriteFile(filepath.Join(clock, "Gear Trains.info.json"), []byte(`{"id":"Clock000001","channel_id":"UCclockworkcorner0000000"}`), 0o600))

	var calls int32
	down := &atomic.Bool{}
	down.Store(true)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch {
		case down.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/channel/UCclockworkcorner0000000":
			_, _ = w.Write([]byte(`<html><head><meta property="og:image" content="` + srv.URL + `/avatar.jpg"></head></html>`))
		case r.URL.Path == "/avatar.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("avatar"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	avatars := youtube.NewAvatars(t.TempDir())
	avatars.Base = srv.URL
	e.s.Avatars = avatars
	avatar := filepath.Join(avatars.Dir, "UCclockworkcorner0000000.jpg")

	art := func(title string) *item.Metadata {
		t.Helper()
		chs, err := e.s.YouTube.Channels(youtube.ChannelFilter{LibraryID: &e.lib.ID})
		must(t, err)
		for _, ch := range chs {
			if ch.Title == title {
				m, err := e.items.Metadata(ch.ID)
				must(t, err)
				return m
			}
		}
		t.Fatalf("no channel %s", title)
		return nil
	}

	_, err := e.s.Scan(context.Background(), e.lib, nil)
	must(t, err)
	if failed, _, err := e.s.lookUp(context.Background(), e.lib, nil); err != nil || failed != 1 {
		t.Fatalf("lookup during an outage: %d failed, %v", failed, err)
	}
	m := art("Clockwork Corner")
	if m.PosterPath != filepath.Join(clock, "poster.jpg") || m.RetryAt == nil || !m.RetryAt.Equal(e.clock.Add(RetryDelay)) {
		t.Fatalf("during the outage %+v", m)
	}
	if m := art("Workbench Weekly"); m.PosterPath != filepath.Join(e.root, "Workbench Weekly", "poster.jpg") || m.RetryAt != nil {
		t.Fatalf("square poster %+v", m)
	}

	down.Store(false)
	*e.clock = e.clock.Add(RetryDelay)
	if _, _, err := e.s.RetryDue(context.Background(), 10, nil); err != nil {
		t.Fatal(err)
	}
	m = art("Clockwork Corner")
	if b, err := os.ReadFile(m.PosterPath); err != nil || m.PosterPath != avatar || string(b) != "avatar" {
		t.Fatalf("after the outage %+v %q %v", m, b, err)
	}
	if lo, hi := e.clock.Add(tmdb.RefreshEvery*8/10), e.clock.Add(tmdb.RefreshEvery*12/10); m.RetryAt == nil || m.RetryAt.Before(lo) || m.RetryAt.After(hi) {
		t.Fatalf("refresh at %v", m.RetryAt)
	}

	before := atomic.LoadInt32(&calls)
	e.mustScan()
	if n := atomic.LoadInt32(&calls); n != before || art("Clockwork Corner").PosterPath != avatar {
		t.Fatalf("a rescan asked YouTube %d times: %+v", n-before, art("Clockwork Corner"))
	}

	down.Store(true)
	*e.clock = *m.RetryAt
	if _, _, err := e.s.RetryDue(context.Background(), 10, nil); err != nil {
		t.Fatal(err)
	}
	if m := art("Clockwork Corner"); m.PosterPath != avatar || !m.RetryAt.Equal(e.clock.Add(RetryDelay)) {
		t.Fatalf("a failed refresh %+v", m)
	}
}

package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// active=true lists only the conversions still to finish, newest first,
// however many finished ones are newer: a failed one asked for again keeps
// its place.
func TestListActiveConversions(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	f := &mediafile.File{ItemID: e.movie(e.library("movies"), "M"), Path: "/m.mkv", SizeBytes: 1, ModifiedAt: now}
	must(t, e.svc.MediaFiles.Repo().CreateFile(f))
	repo := e.svc.Downloads.Repo()
	conv := func(height int) *download.Conversion {
		c := &download.Conversion{MediaFileID: f.ID, SourceModifiedAt: now, Container: "mp4",
			Entry: &session.PlanEntry{Verdict: "video_transcode", VideoAction: "convert", AudioAction: "copy", TopHeight: height}}
		must(t, repo.Request(c))
		return c
	}
	retried, converting, queued := conv(480), conv(720), conv(1080)
	must(t, repo.Fail(retried.ID, "no", now))
	must(t, repo.Start(converting.ID, now))
	ready := conv(360)
	must(t, repo.Start(ready.ID, now))
	must(t, repo.Finish(ready.ID, 1, now))
	must(t, repo.Fail(conv(240).ID, "no", now))
	conv(480)

	var page api.ConversionPage
	e.expect(200, "GET", "/conversions?active=true", e.admin.token, nil, &page)
	var got []int64
	for _, c := range page.Items {
		got = append(got, c.Id)
	}
	if want := []int64{queued.ID, converting.ID, retried.ID}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
	e.expect(200, "GET", "/conversions", e.admin.token, nil, &page)
	if len(page.Items) != 5 {
		t.Fatalf("all: %d", len(page.Items))
	}
}

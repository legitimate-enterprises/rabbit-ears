package httpapi_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/thumbs"
)

// previews makes an item's seek previews the way the Seek previews task does.
func (e *env) previews(item int64) *seekpreview.SeekPreview {
	e.t.Helper()
	return e.previewsIn(e.t.TempDir(), item)
}

// previewsIn is previews made under the data folder data.
func (e *env) previewsIn(data string, item int64) *seekpreview.SeekPreview {
	e.t.Helper()
	files, err := e.svc.MediaFiles.Repo().FilesForItem(item)
	must(e.t, err)
	dir := filepath.Join(data, "previews", strconv.FormatInt(files[0].ID, 10))
	fr, err := thumbs.Previews(context.Background(), files[0].Path, dir)
	must(e.t, err)
	p := &seekpreview.SeekPreview{MediaFileID: files[0].ID, Dir: dir, IntervalMs: fr.IntervalMs, Count: fr.Count, Width: fr.Width, Height: fr.Height}
	must(e.t, e.svc.Previews.Repo().Put(p))
	return p
}

func frame(pf *api.PreviewFrames, n string) string { return strings.Replace(pf.FrameUrl, "{n}", n, 1) }

func TestSeekPreviewsBesideTheStream(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	p := e.previews(m)
	st := e.start(m, sdrWeb())
	pf := st.Previews
	base := strings.TrimSuffix(st.StreamUrl, "/file")
	if pf == nil || pf.Count != 1 || pf.Width != 320 || pf.Height != 180 || pf.IntervalMs != 10000 ||
		pf.FrameUrl != base+"/previews/{n}.jpg" || pf.BifUrl != base+"/previews.bif" {
		t.Fatalf("%+v", pf)
	}
	res, b := e.get(frame(pf, "0"))
	want, _ := os.ReadFile(thumbs.FramePath(p.Dir, 0))
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(b, want) {
		t.Fatalf("frame 0: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	for _, n := range []string{"1", "-1", "x", "../0"} {
		if res, _ := e.get(frame(pf, n)); res.StatusCode != 404 {
			t.Errorf("frame %q: %d", n, res.StatusCode)
		}
	}
	res, b = e.get(pf.BifUrl)
	want, _ = os.ReadFile(filepath.Join(p.Dir, thumbs.BIFName))
	if res.StatusCode != 200 || !bytes.Equal(b, want) || !bytes.HasPrefix(b, []byte("\x89BIF")) {
		t.Fatalf("bif: %d %d bytes", res.StatusCode, len(b))
	}
	if res, _ := e.get(strings.Replace(pf.BifUrl, "/streams/", "/streams/x", 1)); res.StatusCode != 404 {
		t.Fatal("a wrong stream key must not serve previews")
	}

	other := e.probed("hdr10-main10.mkv", "")
	if st := e.start(other, sdrWeb()); st.Previews != nil {
		t.Fatalf("a file without previews plays without them: %+v", st.Previews)
	}
}

// Seek previews are served from where the data folder is now, not where it
// was when they were made.
func TestSeekPreviewsFollowTheDataFolder(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	root := t.TempDir()
	was, now := filepath.Join(root, "was"), filepath.Join(root, "now")
	dbtest.DataDir(t, was)
	p := e.previewsIn(was, m)
	must(t, os.Rename(was, now))
	dbtest.DataDir(t, now)
	st := e.start(m, sdrWeb())
	res, b := e.get(frame(st.Previews, "0"))
	want, err := os.ReadFile(thumbs.FramePath(filepath.Join(now, "previews", strconv.FormatInt(p.MediaFileID, 10)), 0))
	must(t, err)
	if res.StatusCode != 200 || !bytes.Equal(b, want) {
		t.Fatalf("frame 0: %d", res.StatusCode)
	}
	if res, b = e.get(st.Previews.BifUrl); res.StatusCode != 200 || !bytes.HasPrefix(b, []byte("\x89BIF")) {
		t.Fatalf("bif: %d", res.StatusCode)
	}
}

// Previews are reached only through a stream key, which only playback hands
// out, so a kid never sees frames of a title outside their filter.
func TestKidCannotReachPreviewsOutsideTheFilter(t *testing.T) {
	e := newEnv(t)
	r := e.probed("h264-high41.mp4", "")
	e.rate(r, "R")
	e.previews(r)
	g := e.probed("hdr10-main10.mkv", "")
	gp := e.previews(g)
	k := e.kid(e.plain, "Kid") // PG
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	play := func(item int64) map[string]any {
		return map[string]any{"profileId": k.Id, "itemId": item, "snapshot": sdrWeb()}
	}
	e.expect(404, "POST", "/playback/sessions", e.plain.token, play(r), nil)
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, play(g), &st)
	res, b := e.get(frame(st.Previews, "0"))
	want, _ := os.ReadFile(thumbs.FramePath(gp.Dir, 0))
	if res.StatusCode != 200 || !bytes.Equal(b, want) {
		t.Fatalf("the kid's own title: %d", res.StatusCode)
	}
}

// Every mid-playback answer carries the stream and its previews, so a kid
// made active on a device cannot drive an adult's session still open there.
func TestKidCannotDriveAnAdultsSession(t *testing.T) {
	e := newEnv(t)
	r := e.probed("h264-high41.mp4", "")
	e.rate(r, "R")
	e.previews(r)
	adult := e.start(r, sdrWeb())
	k := e.kid(e.plain, "Kid")
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	sid := itoa(adult.SessionId)
	e.expect(404, "PUT", "/playback/sessions/"+sid+"/throughput", e.plain.token, map[string]any{"measuredBitrate": 1, "bufferMs": 0, "stallCount": 0}, nil)
	e.expect(404, "PUT", "/playback/sessions/"+sid+"/position", e.plain.token, map[string]any{"positionMs": 0}, nil)
}

func TestSeekPreviewsAdminRead(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	p := e.previews(m)
	var page api.SeekPreviewPage
	e.expect(200, "GET", "/seek-previews?mediaFileId="+itoa(p.MediaFileID), e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != p.ID || page.Items[0].Count != 1 || page.Items[0].Width != 320 {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", "/seek-previews?mediaFileId=999", e.admin.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", "/seek-previews?limit=1", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.NextOffset != nil {
		t.Fatalf("%+v", page)
	}
	var one api.SeekPreview
	e.expect(200, "GET", "/seek-previews/"+itoa(p.ID), e.admin.token, nil, &one)
	if one.MediaFileId != p.MediaFileID || one.IntervalMs != 10000 || one.Height != 180 || one.CreatedAt.IsZero() {
		t.Fatalf("%+v", one)
	}
	e.expect(404, "GET", "/seek-previews/999", e.admin.token, nil, nil)
	e.expect(403, "GET", "/seek-previews/"+itoa(p.ID), e.plain.token, nil, nil)
}

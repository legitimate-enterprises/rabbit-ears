package httpapi_test

import (
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestLogsListFilterAndDownload(t *testing.T) {
	e := newEnv(t)
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sid := uid.N(7)
	for _, l := range []*logstore.Entry{
		{At: t0, Level: "info", Source: "scan", Message: "Scan finished: 3 movies added"},
		{At: t0.Add(time.Minute), Level: "warn", Source: "tmdb", Message: "TMDB returned 429"},
		{At: t0.Add(2 * time.Minute), Level: "info", Source: "playback", Message: "Session 7 started: video transcode",
			SessionID: &sid, FFmpegCommand: "ffmpeg -i /m/a.mkv out.m3u8", TranscodeReason: "HEVC Main10 is not decodable here"},
	} {
		must(t, e.svc.Logs.Insert(l))
	}
	var page api.LogEntryPage
	e.expect(200, "GET", "/logs", e.admin.token, nil, &page)
	if len(page.Items) != 3 || page.Items[0].Source != "playback" || page.Items[2].Source != "scan" {
		t.Fatalf("newest first: %+v", page.Items)
	}
	top := page.Items[0]
	if top.SessionId == nil || *top.SessionId != sid || top.FfmpegCommand == nil || *top.FfmpegCommand != "ffmpeg -i /m/a.mkv out.m3u8" ||
		top.TranscodeReason == nil || top.Level != api.LogLevelInfo || !top.At.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("detail fields: %+v", top)
	}
	for path, want := range map[string]int{
		"/logs?level=warn":                       1,
		"/logs?source=scan":                      1,
		"/logs?q=429":                            1,
		"/logs?since=2026-03-01T12:01:00Z":       2,
		"/logs?until=2026-03-01T12:00:00Z":       1,
		"/logs?level=info&source=playback&q=SES": 1,
	} {
		var p api.LogEntryPage
		e.expect(200, "GET", path, e.admin.token, nil, &p)
		if len(p.Items) != want {
			t.Errorf("%s: got %d want %d", path, len(p.Items), want)
		}
	}
	var p api.LogEntryPage
	e.expect(200, "GET", "/logs?limit=2", e.admin.token, nil, &p)
	if len(p.Items) != 2 || p.NextOffset == nil || *p.NextOffset != 2 {
		t.Fatalf("paging: %+v", p)
	}
	e.expect(400, "GET", "/logs?level=loud", e.admin.token, nil, nil)

	status, raw := e.do("GET", "/logs/download", e.admin.token, nil, nil)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if status != 200 || len(lines) != 3 || !strings.HasPrefix(lines[0], "2026-03-01T12:00:00Z INFO scan Scan finished") ||
		!strings.Contains(lines[2], "ffmpeg -i /m/a.mkv out.m3u8") || !strings.Contains(lines[2], "HEVC Main10") {
		t.Fatalf("download %d: %q", status, raw)
	}
}

func TestLogsAreAdminOnly(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/logs", "/logs/download"} {
		e.expect(403, "GET", path, e.plain.token, nil, nil)
		e.expect(401, "GET", path, "", nil, nil)
	}
}

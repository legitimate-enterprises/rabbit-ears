package httpapi_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

// awayWeb is sdrWeb() away from home. A nil auto leaves the field out, as a
// v1 client does; a zero maxBitrate leaves the cap out.
func awayWeb(auto any, maxBitrate int64) map[string]any {
	s := sdrWeb()
	n := map[string]any{"discoveredLocally": false, "metered": false, "lowData": false, "vpn": false}
	if auto != nil {
		n["auto"] = auto
	}
	if maxBitrate > 0 {
		n["maxBitrate"] = maxBitrate
	}
	s["network"] = n
	return s
}

var resolution = regexp.MustCompile(`RESOLUTION=(\d+x\d+)`)

// variants lists the resolutions a stream's master playlist offers.
func (e *env) variants(st api.PlaybackStream) []string {
	e.t.Helper()
	res, b := e.get(st.StreamUrl)
	if res.StatusCode != 200 {
		e.t.Fatalf("master: %d", res.StatusCode)
	}
	out := []string{}
	for _, m := range resolution.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	return out
}

func TestAdaptiveStreams(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	end := func(st api.PlaybackStream) {
		e.expect(204, "DELETE", "/playback/sessions/"+itoa(st.SessionId), e.plain.token, nil, nil)
	}

	st := e.start(m, awayWeb(true, 0))
	if st.Entry.Verdict != api.VideoTranscode || st.Auto == nil || !*st.Auto || st.Away == nil || !*st.Away ||
		st.Rungs[0].Verdict != api.Remux || st.Rungs[0].Blocked != nil {
		t.Fatalf("Auto away from home transcodes the ladder and lists Original as a remux: %+v", st)
	}
	if v := e.variants(st); len(v) != 1 || v[0] != "640x360" {
		t.Fatalf("the ladder never climbs above the 360p source: %v", v)
	}
	end(st)

	st = e.start(m, awayWeb(false, 0))
	if st.Entry.Verdict != api.Remux || st.Auto != nil || st.Away == nil || !*st.Away {
		t.Fatalf("Original away from home stays a remux: %+v", st)
	}
	end(st)

	st = e.start(m, awayWeb(false, 400_000))
	if st.Entry.Verdict != api.VideoTranscode || st.Auto != nil || st.Negotiated.Bitrate != 400_000 || len(e.variants(st)) != 1 {
		t.Fatalf("a fixed pick is one rung: %+v", st)
	}
	end(st)

	home := awayWeb(true, 0)
	home["network"].(map[string]any)["discoveredLocally"] = true
	st = e.start(m, home)
	if st.Entry.Verdict != api.DirectPlay || st.Auto != nil || st.Away != nil {
		t.Fatalf("at home Auto plays as Original: %+v", st)
	}
	end(st)

	files, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	big := int64(38e6)
	files[0].Bitrate = &big
	must(t, e.svc.MediaFiles.Repo().UpdateFile(files[0]))
	metered := awayWeb(true, 0)
	metered["network"].(map[string]any)["metered"] = true
	st = e.start(m, metered)
	if st.Rungs[0].Verdict != api.Remux || st.Rungs[0].Blocked == nil || !*st.Rungs[0].Blocked {
		t.Fatalf("on a metered path Original above 4 Mb/s is listed, blocked: %+v", st.Rungs)
	}
	end(st)

	both := awayWeb(true, 4_000_000)
	e.expect(400, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": both}, nil)
	e.expect(400, "POST", "/items/"+itoa(m)+"/playability", e.plain.token, both, nil)
	st = e.start(m, awayWeb(true, 0))
	e.expect(400, "PUT", "/playback/sessions/"+itoa(st.SessionId)+"/network", e.plain.token, both["network"], nil)
}

func throughput(rungHeight, stalls int) map[string]any {
	return map[string]any{"measuredBitrate": 600_000, "bufferMs": 1_000, "stallCount": stalls, "rungHeight": rungHeight, "rungBitrate": 800_000}
}

// The slow loop: three samples in a row on Auto's bottom rung with stalls
// rising restart the stream once, with a 240p floor rung under the ladder.
func TestFloorRungRestart(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	st := e.start(m, awayWeb(true, 0))
	sid := "/playback/sessions/" + itoa(st.SessionId)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 4000}, nil)
	var up api.PlaybackUpdate
	for i, c := range []struct {
		height, stalls int
		want           api.PlaybackUpdateAction
	}{
		{360, 0, api.Continue},
		{360, 1, api.Continue},
		{480, 2, api.Continue}, // off the bottom rung, so the run starts again
		{360, 3, api.Continue},
		{360, 3, api.Continue},
		{360, 3, api.Continue}, // no new stall across the last three
		{360, 4, api.Restart},
		{360, 5, api.Continue},
		{360, 6, api.Continue},
		{360, 7, api.Continue}, // once per session
	} {
		e.expect(200, "PUT", sid+"/throughput", e.plain.token, throughput(c.height, c.stalls), &up)
		if up.Action != c.want {
			t.Fatalf("sample %d: %s, want %s", i, up.Action, c.want)
		}
		if up.Action == api.Restart {
			if v := e.variants(up.Stream); len(v) != 2 || v[1] != "426x240" || up.Stream.PositionMs != 4000 || up.Stream.Auto == nil {
				t.Fatalf("restart at the same position with the floor rung: %v %+v", v, up.Stream)
			}
		}
	}
	e.expect(204, "DELETE", sid, e.plain.token, nil, nil)

	st = e.start(m, awayWeb(false, 400_000))
	sid = "/playback/sessions/" + itoa(st.SessionId)
	for stalls := range 5 {
		e.expect(200, "PUT", sid+"/throughput", e.plain.token, throughput(360, stalls), &up)
		if up.Action != api.Continue {
			t.Fatal("an explicit pick is never overridden")
		}
	}
}

// A v1 client sends none of the v2 fields and gets v1: no away, Auto or
// blocked marks, fMP4, and telemetry that never restarts.
func TestV1ClientKeepsV1Behavior(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	files, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	big := int64(38e6)
	files[0].Bitrate = &big
	must(t, e.svc.MediaFiles.Repo().UpdateFile(files[0]))
	v1 := awayWeb(nil, 0)
	v1["network"].(map[string]any)["metered"] = true
	var st api.PlaybackStream
	raw := e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": v1}, &st)
	for _, key := range []string{`"away"`, `"auto"`, `"blocked"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("v1 reply carries %s: %s", key, raw)
		}
	}
	if st.Entry.Verdict != api.VideoTranscode || st.Rungs[0].Verdict != api.VideoTranscode || st.Rungs[0].Bitrate != 800_000 {
		t.Fatalf("metered caps Original to the transcode a v1 menu shows: %+v", st)
	}
	_, master := e.get(st.StreamUrl)
	base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1]
	_, media := e.get(base + "0/index.m3u8")
	if !strings.Contains(string(master), "0/index.m3u8") || !strings.Contains(string(media), "#EXT-X-MAP") || !strings.Contains(string(media), "0.m4s") {
		t.Fatalf("v1 is fMP4:\n%s\n%s", master, media)
	}
	var up api.PlaybackUpdate
	for stalls := range 5 {
		e.expect(200, "PUT", "/playback/sessions/"+itoa(st.SessionId)+"/throughput", e.plain.token,
			map[string]any{"measuredBitrate": 600_000, "bufferMs": 0, "stallCount": stalls, "currentRung": 0}, &up)
		if up.Action != api.Continue {
			t.Fatal("telemetry never restarts a v1 client")
		}
	}
}

// roku plays H.264 and AAC, AC-3 or E-AC-3 in MPEG-TS HLS only.
func roku() map[string]any {
	s := awayWeb(false, 0)
	s["decoder"] = map[string]any{
		"protocols":       []string{"hls"},
		"containers":      []string{},
		"videoCodecs":     []map[string]any{{"codec": "h264", "hardware": true, "maxWidth": 1920, "maxHeight": 1080, "maxFrameRate": 60, "maxBitDepth": 8}},
		"audioCodecs":     []string{"aac", "ac3", "eac3"},
		"subtitleFormats": []string{"webvtt"},
		"segmentFormats":  []string{"ts"},
	}
	return s
}

// probeTS decodes a served MPEG-TS segment and lists its format and its
// streams' codecs, sorted, each once: ffprobe repeats a program's streams.
func probeTS(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.ts")
	must(t, os.WriteFile(p, b, 0o600))
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=format_name:stream=codec_name", "-of", "csv=p=0", p).Output() //nolint:gosec // test temp path
	if err != nil {
		t.Fatalf("segment does not decode: %v", err)
	}
	f := strings.Fields(string(out))
	slices.Sort(f)
	return strings.Join(slices.Compact(f), " ")
}

func TestTSSegments(t *testing.T) {
	for _, c := range []struct {
		fixture string
		verdict api.Verdict
		seg     string
		want    string
	}{
		{"h264-high41.mp4", api.Remux, "0.ts", "aac h264 mpegts"},
		{"hdr10-main10.mkv", api.VideoTranscode, "1.ts", "aac h264 mpegts"},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			e := newEnv(t)
			st := e.start(e.probed(c.fixture, ""), roku())
			if st.Entry.Verdict != c.verdict {
				t.Fatalf("%+v", st.Entry)
			}
			base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1] + "0/"
			_, media := e.get(base + "index.m3u8")
			if strings.Contains(string(media), "#EXT-X-MAP") || !strings.Contains(string(media), c.seg) || strings.Contains(string(media), ".m4s") {
				t.Fatalf("a TS playlist has no init segment:\n%s", media)
			}
			res, b := e.get(base + c.seg)
			if res.StatusCode != 200 || res.Header.Get("Content-Type") != "video/mp2t" {
				t.Fatalf("%d %q", res.StatusCode, res.Header.Get("Content-Type"))
			}
			if got := probeTS(t, b); got != c.want {
				t.Fatalf("segment streams: %s", got)
			}
			for _, name := range []string{"init.mp4", "0.m4s"} {
				if res, _ := e.get(base + name); res.StatusCode != 404 {
					t.Errorf("%s: %d", name, res.StatusCode)
				}
			}
		})
	}
}

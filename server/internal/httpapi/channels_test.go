package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
)

// tracks gives an item's file the tracks a probe would find, H.264 and
// stereo AAC in MP4, so playback decides on it with no probe.
func (e *env) tracks(itemID int64) {
	e.t.Helper()
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(itemID)
	must(e.t, err)
	fs[0].Container = "mp4"
	must(e.t, e.svc.MediaFiles.Repo().UpdateFile(fs[0]))
	must(e.t, e.svc.MediaFiles.Repo().ReplaceTracks(fs[0].ID, []*mediafile.Track{
		{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264", Profile: "High", Level: ptr(41), Width: ptr(1920), Height: ptr(1080),
			FrameRate: ptr(24.0), BitDepth: ptr(8), PixelFormat: "yuv420p"},
		{StreamIndex: 1, Kind: mediafile.Audio, Codec: "aac", Channels: ptr(2), IsDefault: true},
	}))
}

// air is one program of a schedule written by hand: an item from start
// for its file's length, both in seconds after the schedule's origin.
type air struct {
	item  int64
	start float64
}

// lineup makes plain's station Nights over a movies playlist of the items
// and replaces its schedule with programs, which start at the given
// seconds after 16:00 and run for their files' lengths.
func (e *env) lineup(programs ...air) (api.Station, time.Time) {
	e.t.Helper()
	origin := time.Date(2026, 11, 13, 16, 0, 0, 0, time.Local)
	var pl api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Nights", "kind": "movies"}, &pl)
	added := map[int64]bool{}
	var ps []station.Program
	for i, p := range programs {
		if !added[p.item] {
			e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(pl.Id)+"/entries", e.plain.token, map[string]any{"itemId": p.item}, nil)
			added[p.item] = true
		}
		ed, err := e.svc.Editions.Repo().Default(p.item)
		must(e.t, err)
		fs, err := e.svc.MediaFiles.Repo().FilesForEdition(ed.ID)
		must(e.t, err)
		start := origin.Add(time.Duration(p.start * float64(time.Second)))
		ps = append(ps, station.Program{Row: station.Row{Key: station.Key{ItemID: p.item}, EditionID: ed.ID}, Position: i, Start: start,
			End: start.Add(time.Duration(*fs[0].DurationMs) * time.Millisecond)})
	}
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl.Id, "visibility": "household"}, &st)
	must(e.t, e.svc.Stations.Repo().Unstart(st.Id, time.Unix(0, 0)))
	must(e.t, e.svc.Stations.Repo().Prune(origin.Add(24*time.Hour)))
	must(e.t, e.svc.Stations.Repo().Write(st.Id, ps, &station.Loop{}))
	return st, origin
}

func (e *env) tune(st api.Station, token string, snap map[string]any, subtitle ...int64) api.StationPlayback {
	e.t.Helper()
	body := map[string]any{"snapshot": snap, "live": true}
	if len(subtitle) > 0 {
		body["subtitleTrackId"] = subtitle[0]
	}
	var got api.StationPlayback
	e.expect(201, "POST", "/stations/"+itoa(st.Id)+"/playback", token, body, &got)
	if got.Stream.Live == nil || !*got.Stream.Live {
		e.t.Fatalf("not live: %+v", got.Stream)
	}
	if got.Stream.BufferGoalSeconds != nil {
		e.t.Fatalf("a live stream names a buffer goal: %+v", got.Stream)
	}
	return got
}

// playlist fetches a station stream's playlist until it holds want, which
// a file whose keyframes are still being probed delays.
func (e *env) playlist(url, want string) string {
	e.t.Helper()
	var list []byte
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		res, body := e.get(url)
		if list = body; res.StatusCode != 200 {
			e.t.Fatalf("playlist: %d %s", res.StatusCode, list)
		}
		if strings.Contains(string(list), want) {
			return string(list)
		}
	}
	e.t.Fatalf("the playlist never held %q:\n%s", want, list)
	return ""
}

var inf = regexp.MustCompile(`(?m)^#EXTINF:([0-9.]+),\n(([^/\n]+)/[0-9]+\.(?:m4s|ts))$`)

// pieces reads a playlist's segments in order, grouped by the piece they
// belong to: each piece's id, its segments' URIs and their durations
// added up.
func pieces(list string) (ids []string, uris map[string][]string, seconds map[string]float64) {
	uris, seconds = map[string][]string{}, map[string]float64{}
	for _, m := range inf.FindAllStringSubmatch(list, -1) {
		if len(ids) == 0 || ids[len(ids)-1] != m[3] {
			ids = append(ids, m[3])
		}
		var d float64
		_, _ = fmt.Sscanf(m[1], "%f", &d)
		uris[m[3]], seconds[m[3]] = append(uris[m[3]], m[2]), seconds[m[3]]+d
	}
	return ids, uris, seconds
}

// fetched downloads a piece's init segment and the segments named, and
// probes them joined as one file.
func (e *env) fetched(base, piece string, uris []string) *probe.Result {
	e.t.Helper()
	res, joined := e.get(base + piece + "/init.mp4")
	if res.StatusCode != 200 {
		e.t.Fatalf("%s init: %d %s", piece, res.StatusCode, joined)
	}
	for _, u := range uris {
		res, seg := e.get(base + u)
		if res.StatusCode != 200 || len(seg) == 0 {
			e.t.Fatalf("%s: %d %s", u, res.StatusCode, seg)
		}
		joined = append(joined, seg...)
	}
	out := filepath.Join(e.t.TempDir(), "joined.mp4")
	must(e.t, os.WriteFile(out, joined, 0o600))
	r, err := probe.Run(context.Background(), out)
	must(e.t, err)
	return r
}

// With live a movies station answers its continuous stream, joined at the
// segment on the air; without it, the one program on now.
func TestTuneInLive(t *testing.T) {
	e := newEnv(t)
	at := e.stationClock()
	lantern := e.film(e.library("movies"), "Lantern", "2001-01-01", "PG")
	pl := e.nightsPlaylist(map[int64]int64{lantern: 90})
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl}, &st)
	tune := "/stations/" + itoa(st.Id) + "/playback"
	live := map[string]any{"snapshot": snapshot(), "live": true}

	// More than an hour before the next program the station is off the air.
	at(14, 30)
	raw := e.expect(409, "POST", tune, e.plain.token, live, nil)
	var off api.Error
	must(t, json.Unmarshal(raw, &off))
	if off.Code != api.ErrorCodeOffAir || off.ResumesAt == nil || !off.ResumesAt.Equal(at(16, 0)) {
		t.Fatalf("%s", raw)
	}
	// A file the scan has not probed can be handed over whole and not
	// streamed, and no ident runs ahead of a program that cannot play.
	at(15, 5)
	if raw = e.expect(409, "POST", tune, e.plain.token, live, nil); errorCode(t, raw) != "off_air" {
		t.Fatalf("%s", raw)
	}
	at(16, 10)
	if raw = e.expect(409, "POST", tune, e.plain.token, live, nil); errorCode(t, raw) != "conflict" {
		t.Fatalf("%s", raw)
	}
	var legacy api.StationPlayback
	e.expect(201, "POST", tune, e.plain.token, map[string]any{"snapshot": snapshot()}, &legacy)
	if legacy.Stream.Live != nil || legacy.Stream.PositionMs != 600000 || legacy.Stream.Entry.Verdict != api.DirectPlay {
		t.Fatalf("without live: %+v", legacy.Stream)
	}

	e.tracks(lantern)
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(lantern)
	must(t, err)
	must(t, os.Rename(fs[0].Path, fs[0].Path+".away"))
	if raw = e.expect(409, "POST", tune, e.plain.token, live, nil); errorCode(t, raw) != "conflict" || !strings.Contains(string(raw), "unavailable") {
		t.Fatalf("%s", raw)
	}
	must(t, os.Rename(fs[0].Path+".away", fs[0].Path))
	var got api.StationPlayback
	e.expect(201, "POST", tune, e.plain.token, live, &got)
	if got.Stream.Live == nil || !*got.Stream.Live || got.Stream.PositionMs != 0 || got.Stream.Entry.Verdict == api.DirectPlay ||
		!strings.Contains(got.Stream.StreamUrl, "/channels/") || len(got.Stream.Rungs) != 1 || len(got.Stream.Sidecars) != 0 || got.Stream.Previews != nil ||
		got.OnAir.Status != api.OnAir || got.OnAir.Now == nil || got.OnAir.Now.ItemId != lantern {
		t.Fatalf("%+v", got)
	}
	// Ten minutes in, the stream starts at the segment on the air.
	res, list := e.get(got.Stream.StreamUrl)
	if res.StatusCode != 200 || !strings.Contains(string(list), "#EXT-X-MEDIA-SEQUENCE:0\n") || !strings.Contains(string(list), "/100.m4s\n") ||
		strings.Contains(string(list), "/99.m4s") || strings.Contains(string(list), "#EXT-X-ENDLIST") {
		t.Fatalf("%d %s", res.StatusCode, list)
	}
	var sess api.Session
	if e.expect(200, "GET", "/sessions/"+itoa(got.Stream.SessionId), e.admin.token, nil, &sess); sess.ItemId != lantern || sess.AccountId != e.plain.account.ID {
		t.Fatalf("an ordinary session: %+v", sess)
	}
	// The client refusing what it was sent moves down the plan on a new
	// stream, still live; a plan with nothing left says so.
	sid := "/playback/sessions/" + itoa(got.Stream.SessionId)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 5000}, nil)
	e.expect(404, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(lantern), e.plain.token, nil, nil)
	for left := len(sess.Plan) - 1; left > 0; left-- {
		var up api.PlaybackUpdate
		e.expect(200, "POST", sid+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 0}, &up)
		if up.Action != api.Restart || up.Stream.Live == nil || up.Stream.StreamUrl == got.Stream.StreamUrl {
			t.Fatalf("%+v", up)
		}
		if res, _ := e.get(got.Stream.StreamUrl); res.StatusCode != 404 {
			t.Fatalf("the stream left behind: %d", res.StatusCode)
		}
		if res, list := e.get(up.Stream.StreamUrl); res.StatusCode != 200 || !strings.Contains(string(list), "#EXT-X-MEDIA-SEQUENCE:0\n") {
			t.Fatalf("%d %s", res.StatusCode, list)
		}
		got.Stream = up.Stream
	}
	if raw = e.expect(409, "POST", sid+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 0}, nil); errorCode(t, raw) != "conflict" {
		t.Fatalf("%s", raw)
	}
}

// A station's stream is one playlist that runs from a program through the
// ident in the gap after it into the next program, each piece set off by a
// discontinuity and its own init segment, sliding as the clock moves and
// closing when nothing more is on.
func TestChannelRunsProgramsAndIdentsAsOneStream(t *testing.T) {
	e := newEnv(t)
	first, second := e.probed("h264-high41.mp4", ""), e.probed("chapters.mkv", "")
	st, origin := e.lineup(air{first, 0}, air{second, 30})
	now := origin.Add(2 * time.Second)
	e.svc.Stations.Now = func() time.Time { return now }

	got := e.tune(st, e.plain.token, sdrWeb())
	if got.OnAir.Now == nil || got.OnAir.Now.ItemId != first || got.Stream.Entry.Verdict != api.Remux || got.Stream.Entry.VideoAction != api.PlanEntryVideoActionCopy {
		t.Fatalf("%+v", got)
	}
	base := strings.TrimSuffix(got.Stream.StreamUrl, "live.m3u8")

	// Two seconds in: the program from its start, then the ident as far
	// as the playlist looks ahead. A probe measures a piece to its last
	// timestamp, and a program's sit a second after its source's.
	list := e.playlist(got.Stream.StreamUrl, "#EXT-X-DISCONTINUITY\n")
	ids, uris, secs := pieces(list)
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "p") || !strings.HasPrefix(ids[1], "i") || math.Abs(secs[ids[0]]-10) > 0.05 || secs[ids[1]] != 12 ||
		!strings.Contains(list, "#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-DISCONTINUITY-SEQUENCE:0\n") || strings.Contains(list, "#EXT-X-ENDLIST") ||
		strings.Count(list, "#EXT-X-MAP:") != 2 || !strings.Contains(list, "#EXT-X-DISCONTINUITY\n#EXT-X-MAP:URI=\""+ids[1]+"/init.mp4\"\n") {
		t.Fatalf("%v %v\n%s", ids, secs, list)
	}
	program, ident := ids[0], ids[1]
	if r := e.fetched(base, program, uris[program]); len(r.Tracks) != 2 || r.Tracks[0].Codec != "h264" || *r.Tracks[0].Height != 360 ||
		r.DurationMs == nil || math.Abs(float64(*r.DurationMs)-11000) > 150 {
		t.Fatalf("the program: %+v", r)
	}

	// At 25 seconds: the rest of the ident, 20 seconds in all, then the
	// next program and the end of the schedule.
	now = origin.Add(25 * time.Second)
	list = e.playlist(got.Stream.StreamUrl, "#EXT-X-ENDLIST")
	ids, uris2, secs := pieces(list)
	if len(ids) != 3 || ids[0] != program || ids[1] != ident || !strings.HasPrefix(ids[2], "p") || ids[2] == program || secs[ident] != 20 ||
		math.Abs(secs[ids[2]]-10.021) > 0.05 || !strings.Contains(list, "#EXT-X-MEDIA-SEQUENCE:0\n") || strings.Count(list, "#EXT-X-DISCONTINUITY\n") != 2 {
		t.Fatalf("%v %v\n%s", ids, secs, list)
	}
	if r := e.fetched(base, ident, uris2[ident]); len(r.Tracks) != 2 || r.Tracks[0].Codec != "h264" || *r.Tracks[0].Height != 1080 ||
		r.Tracks[1].Codec != "aac" || r.DurationMs == nil || math.Abs(float64(*r.DurationMs)-20000) > 1000 {
		t.Fatalf("the ident: %+v", r)
	}
	if r := e.fetched(base, ids[2], uris2[ids[2]]); len(r.Tracks) != 2 || r.Tracks[0].Codec != "h264" || r.DurationMs == nil ||
		math.Abs(float64(*r.DurationMs)-11021) > 150 {
		t.Fatalf("the next program: %+v", r)
	}
	var on api.Station
	if e.expect(200, "GET", "/stations/"+itoa(st.Id), e.plain.token, nil, &on); on.OnAir.Now != nil || on.OnAir.Next == nil || on.OnAir.Next.ItemId != second {
		t.Fatalf("during the ident: %+v", on.OnAir)
	}
	var sess api.Session
	if e.expect(200, "GET", "/sessions/"+itoa(got.Stream.SessionId), e.admin.token, nil, &sess); sess.ItemId != first {
		t.Fatalf("during the ident the session stays on the last program: %+v", sess)
	}

	// At 35 seconds the session is on the second program; at 47 the
	// first program and the first of the ident have slid out.
	now = origin.Add(35 * time.Second)
	e.playlist(got.Stream.StreamUrl, "#EXT-X-ENDLIST")
	if e.expect(200, "GET", "/sessions/"+itoa(got.Stream.SessionId), e.admin.token, nil, &sess); sess.ItemId != second {
		t.Fatalf("the session follows the program on: %+v", sess)
	}
	now = origin.Add(47 * time.Second)
	list = e.playlist(got.Stream.StreamUrl, "#EXT-X-ENDLIST")
	if ids, _, _ = pieces(list); len(ids) != 2 || ids[0] != ident ||
		!strings.Contains(list, fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-DISCONTINUITY-SEQUENCE:1\n", len(uris[program])+1)) {
		t.Fatalf("%v\n%s", ids, list)
	}

	// A segment no playlist of the session lists, and another session's key.
	for _, path := range []string{base + ident + "/9.m4s", base + "nope/0.m4s", strings.Replace(base, "/channels/", "/channels/x", 1) + "live.m3u8"} {
		if res, _ := e.get(path); res.StatusCode != 404 {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(got.Stream.SessionId), e.plain.token, nil, nil)
	if res, _ := e.get(got.Stream.StreamUrl); res.StatusCode != 404 {
		t.Fatalf("an ended session's stream: %d", res.StatusCode)
	}
}

// Tuning in while the ident is on joins it where it is, as MPEG-TS for a
// client that plays only that.
func TestChannelJoinsAnIdentInProgress(t *testing.T) {
	e := newEnv(t)
	first, second := e.probed("h264-high41.mp4", ""), e.probed("chapters.mkv", "")
	st, origin := e.lineup(air{first, 0}, air{second, 40})
	now := origin.Add(25 * time.Second)
	e.svc.Stations.Now = func() time.Time { return now }
	snap := sdrWeb()
	snap["decoder"].(map[string]any)["segmentFormats"] = []string{"ts"}

	got := e.tune(st, e.plain.token, snap)
	if got.OnAir.Now != nil || got.OnAir.Next == nil || got.OnAir.Next.ItemId != second {
		t.Fatalf("%+v", got)
	}
	// The ident runs from 10 to 40 seconds; 25 is in its third segment.
	list := e.playlist(got.Stream.StreamUrl, "/2.ts\n")
	ids, uris, _ := pieces(list)
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "i") || uris[ids[0]][0] != ids[0]+"/2.ts" || strings.Contains(list, "#EXT-X-MAP") ||
		!strings.Contains(list, "#EXT-X-MEDIA-SEQUENCE:0\n") {
		t.Fatalf("%v\n%s", ids, list)
	}
	res, seg := e.get(strings.TrimSuffix(got.Stream.StreamUrl, "live.m3u8") + uris[ids[0]][0])
	if res.StatusCode != 200 || len(seg) < 188 || seg[0] != 0x47 {
		t.Fatalf("an MPEG-TS segment: %d, %d bytes", res.StatusCode, len(seg))
	}
}

// A long gap is filled by idents of ten minutes each, so one not rendered
// yet is soon there; the last takes what is left.
func TestChannelFillsALongGapInParts(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	gentle := e.film(films, "Gentle", "2001-01-01", "G")
	e.file(gentle, 60000)
	e.tracks(gentle)
	// The gap runs 25 minutes, from 1 to 26 minutes past.
	st, origin := e.lineup(air{gentle, 0}, air{gentle, 26 * 60})
	now := origin.Add(13 * time.Minute)
	e.svc.Stations.Now = func() time.Time { return now }

	got := e.tune(st, e.plain.token, snapshot())
	list := e.playlist(got.Stream.StreamUrl, "#EXTINF")
	ids, uris, _ := pieces(list)
	if len(ids) != 1 || !strings.HasPrefix(ids[0], "i") || uris[ids[0]][0] != ids[0]+"/20.m4s" {
		t.Fatalf("two minutes into the second part: %v\n%s", ids, list)
	}
	second := ids[0]
	now = origin.Add(20*time.Minute + 50*time.Second)
	list = e.playlist(got.Stream.StreamUrl, "#EXT-X-DISCONTINUITY")
	ids, uris, _ = pieces(list)
	if len(ids) != 2 || ids[0] != second || !strings.HasPrefix(ids[1], "i") || uris[ids[1]][0] != ids[1]+"/0.m4s" {
		t.Fatalf("into the third part: %v\n%s", ids, list)
	}
	now = origin.Add(25*time.Minute + 50*time.Second)
	list = e.playlist(got.Stream.StreamUrl, "#EXT-X-DISCONTINUITY")
	if ids, _, secs := pieces(list); len(ids) != 2 || !strings.HasPrefix(ids[0], "i") || !strings.HasPrefix(ids[1], "p") || secs[ids[0]] > 40 {
		t.Fatalf("the third part ends at the program: %v %v\n%s", ids, secs, list)
	}
}

// Devices given the same decision share one encoder and one transcode
// slot, which the last of them to leave frees; a device given another
// decision has its own.
func TestChannelListenersShareAnEncoder(t *testing.T) {
	e := newEnv(t)
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir(), MaxVideo: 1})
	t.Cleanup(e.svc.Playback.Engine.Close)
	hdr := e.probed("hdr10-main10.mkv", "")
	st, origin := e.lineup(air{hdr, 0})
	now := origin.Add(time.Second)
	e.svc.Stations.Now = func() time.Time { return now }
	engine := e.svc.Playback.Engine

	one := e.tune(st, e.plain.token, sdrWeb())
	neighbor := e.neighbor(e.plain)
	two := e.tune(st, neighbor, sdrWeb())
	if one.Stream.Entry.Verdict != api.VideoTranscode || two.Stream.Entry.Verdict != api.VideoTranscode || one.Stream.StreamUrl == two.Stream.StreamUrl {
		t.Fatalf("%+v %+v", one.Stream, two.Stream)
	}
	for _, s := range []api.StationPlayback{one, two} {
		ids, uris, _ := pieces(e.playlist(s.Stream.StreamUrl, "/0.m4s\n"))
		if res, _ := e.get(strings.TrimSuffix(s.Stream.StreamUrl, "live.m3u8") + uris[ids[0]][0]); res.StatusCode != 200 {
			t.Fatalf("segment: %d", res.StatusCode)
		}
	}
	// The clip is short: its encoder may reach the end and rest its slot at
	// any moment, so only more than one slot is wrong while they listen.
	if len(engine.Transcoding()) > 1 || len(engine.Encoding()) != 1 {
		t.Fatalf("one slot and one encoder for both: %v %v", engine.Transcoding(), engine.Encoding())
	}
	// A third device that plays the file's HEVC as it is, with a text
	// subtitle where the others had the forced bitmap one burned in,
	// takes no slot.
	hdrSnap := sdrWeb()
	hdrSnap["display"] = map[string]any{"width": 3840, "height": 2160, "refreshRate": 60, "hdrFormats": []string{"hdr10"}}
	tv, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "TV", "web")
	must(t, err)
	three := e.tune(st, tv, hdrSnap, e.trackID(hdr, "subrip"))
	if three.Stream.Entry.VideoAction != api.PlanEntryVideoActionCopy || len(engine.Transcoding()) > 1 {
		t.Fatalf("%+v, slots %v", three.Stream.Entry, engine.Transcoding())
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(three.Stream.SessionId), tv, nil, nil)

	e.expect(204, "DELETE", "/playback/sessions/"+itoa(one.Stream.SessionId), e.plain.token, nil, nil)
	if len(engine.Transcoding()) > 1 || len(engine.Encoding()) != 1 {
		t.Fatalf("the other listener keeps the encoder: %v %v", engine.Transcoding(), engine.Encoding())
	}
	if res, _ := e.get(strings.TrimSuffix(two.Stream.StreamUrl, "live.m3u8") + "nope/0.m4s"); res.StatusCode != 404 {
		t.Fatalf("%d", res.StatusCode)
	}
	// Reaping leaves a channel's encoder to its listener.
	must(t, e.svc.Playback.Reap(time.Now().Add(-time.Hour)))
	if len(engine.Encoding()) != 1 {
		t.Fatal("reaped the encoder a listener is on")
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(two.Stream.SessionId), neighbor, nil, nil)
	if len(engine.Transcoding()) != 0 || len(engine.Encoding()) != 0 {
		t.Fatalf("the last listener left: slots %v, encoders %v", engine.Transcoding(), engine.Encoding())
	}
}

// A kid's stream ends before a program they may not see, and tuning in
// then is refused; the ident before it is not theirs either.
func TestChannelEndsBeforeAProgramAKidMayNotSee(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	gentle, rough := e.film(films, "Gentle", "2001-01-01", "G"), e.film(films, "Rough", "2002-01-01", "R")
	e.file(gentle, 60000)
	e.file(rough, 60000)
	e.tracks(gentle)
	e.tracks(rough)
	st, origin := e.lineup(air{gentle, 0})
	ed, err := e.svc.Editions.Repo().Default(rough)
	must(t, err)
	// The schedule still holds a program its playlist has lost.
	must(t, e.svc.Stations.Repo().Write(st.Id, []station.Program{{Row: station.Row{Key: station.Key{ItemID: rough}, EditionID: ed.ID}, Position: 1,
		Start: origin.Add(90 * time.Second), End: origin.Add(150 * time.Second)}}, &station.Loop{}))
	token := e.kidDevice(e.plain, e.kid(e.plain, "Kid").Id) // PG ceiling
	now := origin.Add(45 * time.Second)
	e.svc.Stations.Now = func() time.Time { return now }

	got := e.tune(st, token, snapshot())
	list := e.playlist(got.Stream.StreamUrl, "#EXT-X-ENDLIST")
	if ids, _, _ := pieces(list); len(ids) != 1 || !strings.HasPrefix(ids[0], "p") {
		t.Fatalf("%v\n%s", ids, list)
	}
	adult := e.tune(st, e.plain.token, snapshot())
	if ids, _, _ := pieces(e.playlist(adult.Stream.StreamUrl, "#EXT-X-DISCONTINUITY")); len(ids) != 2 || !strings.HasPrefix(ids[1], "i") {
		t.Fatalf("an adult gets the ident: %v", ids)
	}
	now = origin.Add(70 * time.Second)
	if raw := e.expect(409, "POST", "/stations/"+itoa(st.Id)+"/playback", token, map[string]any{"snapshot": snapshot(), "live": true}, nil); errorCode(t, raw) != "off_air" {
		t.Fatalf("%s", raw)
	}
	now = origin.Add(100 * time.Second)
	e.expect(403, "POST", "/stations/"+itoa(st.Id)+"/playback", token, map[string]any{"snapshot": snapshot(), "live": true}, nil)
}

// A program whose file has no known length and must be converted has no
// segments to list: the stream ends before it.
func TestChannelEndsBeforeAProgramOfUnknownLength(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	known, unknown := e.film(films, "Known", "2001-01-01", "G"), e.film(films, "Unknown", "2002-01-01", "G")
	e.file(known, 10000)
	e.file(unknown, 10000)
	e.tracks(known)
	st, origin := e.lineup(air{known, 0}, air{unknown, 10.5})
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(unknown)
	must(t, err)
	fs[0].Container, fs[0].DurationMs = "mp4", nil
	must(t, e.svc.MediaFiles.Repo().UpdateFile(fs[0]))
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(fs[0].ID, []*mediafile.Track{
		{StreamIndex: 0, Kind: mediafile.Video, Codec: "hevc", Profile: "Main", Width: ptr(1920), Height: ptr(1080), FrameRate: ptr(24.0), BitDepth: ptr(8), PixelFormat: "yuv420p"},
		{StreamIndex: 1, Kind: mediafile.Audio, Codec: "aac", Channels: ptr(2), IsDefault: true},
	}))
	now := origin.Add(2 * time.Second)
	e.svc.Stations.Now = func() time.Time { return now }

	got := e.tune(st, e.plain.token, snapshot())
	list := e.playlist(got.Stream.StreamUrl, "#EXT-X-ENDLIST")
	if ids, _, secs := pieces(list); len(ids) != 1 || secs[ids[0]] != 10 {
		t.Fatalf("%v %v\n%s", ids, secs, list)
	}
}

// Tuning in to a program whose file has no known length is refused, even
// where its video would be copied.
func TestTuneInToAProgramOfUnknownLength(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	st, origin := e.lineup(air{m, 0})
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	fs[0].DurationMs = nil
	must(t, e.svc.MediaFiles.Repo().UpdateFile(fs[0]))
	e.svc.Stations.Now = func() time.Time { return origin.Add(2 * time.Second) }

	raw := e.expect(409, "POST", "/stations/"+itoa(st.Id)+"/playback", e.plain.token, map[string]any{"snapshot": sdrWeb(), "live": true}, nil)
	if errorCode(t, raw) != "conflict" {
		t.Fatalf("%s", raw)
	}
}

// A converted program lists only segments its encoder writes: a file whose
// length passes a boundary by less than its last frame's span ends with the
// segment before.
func TestChannelProgramListsOnlyWhatItsEncoderWrites(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	src := filepath.Join(t.TempDir(), "sliver.mp4")
	//nolint:gosec // a temp path
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24000/1001", "-frames:v", "144", "-c:v", "mpeg4",
		src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	e := newEnv(t)
	r, err := probe.Run(context.Background(), src)
	must(t, err)
	m := e.movie(e.library("movies"), "Sliver")
	e.rate(m, "G")
	now := time.Now().UTC()
	f := &mediafile.File{ItemID: m, Path: src, SizeBytes: 1, ModifiedAt: now, Container: r.Container, DurationMs: r.DurationMs, Bitrate: r.Bitrate, ProbedAt: &now}
	must(t, e.svc.MediaFiles.Repo().CreateFile(f))
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, r.Tracks))
	st, origin := e.lineup(air{m, 0})
	e.svc.Stations.Now = func() time.Time { return origin.Add(time.Second) }
	got := e.tune(st, e.plain.token, sdrWeb())
	if got.Stream.Entry.VideoAction != api.PlanEntryVideoActionConvert {
		t.Fatalf("%+v", got.Stream.Entry)
	}
	list := e.playlist(got.Stream.StreamUrl, "#EXT-X-ENDLIST")
	if ids, uris, _ := pieces(list); len(ids) == 0 || len(uris[ids[0]]) != 1 {
		t.Fatalf("%v\n%s", ids, list)
	}
}

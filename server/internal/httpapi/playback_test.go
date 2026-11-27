package httpapi_test

import (
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func snapshot() map[string]any {
	return map[string]any{
		"decoder": map[string]any{
			"protocols":  []string{"http", "hls"},
			"containers": []string{"mp4"},
			"videoCodecs": []map[string]any{{"codec": "h264", "profiles": []string{"High"}, "maxLevel": 51, "hardware": true,
				"maxWidth": 3840, "maxHeight": 2160, "maxFrameRate": 60, "maxBitDepth": 8}},
			"audioCodecs":     []string{"aac"},
			"subtitleFormats": []string{"webvtt"},
		},
		"display":    map[string]any{"width": 2560, "height": 1440, "refreshRate": 60, "hdrFormats": []string{}},
		"audioRoute": map[string]any{"channels": 2, "passthroughCodecs": []string{}},
		"network":    map[string]any{"discoveredLocally": true, "metered": false, "lowData": false, "vpn": false},
	}
}

// playable returns a movie with one file the profile may play.
func (e *env) playable() (uid.ID, uid.ID) {
	m := e.movie(e.library("movies"), "Film")
	e.rate(m, "G")
	var f api.MediaFile
	e.expect(201, "POST", "/media-files", e.admin.token, map[string]any{"itemId": m, "path": e.onDisk("/films/film.mp4"), "sizeBytes": 1, "fileModifiedAt": "2026-01-01T00:00:00Z"}, &f)
	return m, f.Id
}

func TestPlaybackSession(t *testing.T) {
	e := newEnv(t)
	m, f := e.playable()
	k := e.kid(e.plain, "Kid")
	req := map[string]any{"profileId": k.Id, "itemId": m, "snapshot": snapshot()}
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, req, &st)
	if st.MediaFileId != f || st.Entry.Verdict != api.DirectPlay || st.StreamUrl == "" || st.Negotiated.Verdict != api.DirectPlay || len(st.Rungs) == 0 || st.Sidecars == nil {
		t.Fatalf("%+v", st)
	}
	sid := "/playback/sessions/" + itoa(st.SessionId)

	var up api.PlaybackUpdate
	for _, c := range []struct {
		path string
		body any
	}{
		{"/display", map[string]any{"width": 1920, "height": 1080, "refreshRate": 60, "hdrFormats": []string{}}},
		{"/audio-route", map[string]any{"channels": 6, "passthroughCodecs": []string{"ac3"}}},
		{"/network", map[string]any{"discoveredLocally": false, "metered": true, "lowData": false, "vpn": false, "maxBitrate": 4000000}},
		{"/throughput", map[string]any{"measuredBitrate": 50000000, "bufferMs": 12000, "stallCount": 0}},
		{"/tracks", map[string]any{}},
	} {
		e.expect(200, "PUT", sid+c.path, e.plain.token, c.body, &up)
		if up.Action != api.Continue || up.Stream.SessionId != st.SessionId {
			t.Fatalf("%s: %+v", c.path, up)
		}
	}
	e.expect(400, "PUT", sid+"/display", e.plain.token, map[string]any{"width": 0, "height": 1080, "refreshRate": 60, "hdrFormats": []string{}}, nil)
	e.expect(400, "PUT", sid+"/display", e.plain.token, map[string]any{"width": 1920, "height": 1080, "refreshRate": 0, "hdrFormats": []string{}}, nil)
	e.expect(400, "PUT", sid+"/network", e.plain.token, map[string]any{"metered": true}, nil)

	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 90000}, nil)
	var ws api.WatchState
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/watch-state/"+itoa(m), e.plain.token, nil, &ws)
	if ws.PositionMs != 90000 {
		t.Fatalf("position must reach the profile's watch state: %+v", ws)
	}

	var sess api.Session
	e.expect(200, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, &sess)
	if sess.PositionMs != 90000 || sess.MediaFileId != f || len(sess.Plan) != 1 || sess.AccountId != k.Id ||
		sess.Encoder != api.SessionEncoderNone || sess.TranscodeReason != nil {
		t.Fatalf("%+v", sess)
	}

	e.expect(200, "POST", sid+"/events", e.plain.token, map[string]any{"kind": "stall", "positionMs": 90000}, &up)
	if up.Action != api.Continue {
		t.Fatalf("%+v", up)
	}
	raw := e.expect(409, "POST", sid+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 90000, "reason": "decode error"}, nil)
	if !strings.Contains(string(raw), "conflict") {
		t.Fatalf("an exhausted plan is a conflict: %s", raw)
	}
	e.expect(200, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, &sess)
	if !sess.Plan[0].Bad || sess.Plan[0].BadReason == nil {
		t.Fatalf("failed entry must be marked bad: %+v", sess.Plan[0])
	}

	// Another device cannot touch this session.
	e.expect(404, "PUT", sid+"/position", e.admin.token, map[string]any{"positionMs": 1}, nil)
	e.expect(404, "DELETE", sid, e.admin.token, nil, nil)

	e.expect(204, "DELETE", sid, e.plain.token, nil, nil)
	e.expect(404, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 1}, nil)
	var active api.SessionPage
	e.expect(200, "GET", "/sessions?active=true", e.admin.token, nil, &active)
	if len(active.Items) != 0 {
		t.Fatalf("%+v", active)
	}
	var all api.SessionPage
	e.expect(200, "GET", "/sessions", e.admin.token, nil, &all)
	if len(all.Items) != 1 || all.Items[0].EndedAt == nil {
		t.Fatalf("%+v", all)
	}
	e.expect(204, "DELETE", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, nil)
	e.expect(404, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, nil)
}

func TestStartPlaybackRefusals(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	k := e.kid(e.plain, "Kid")
	e.expect(400, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": k.Id, "itemId": m}, nil)
	missing := snapshot()
	delete(missing, "display")
	e.expect(400, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": k.Id, "itemId": m, "snapshot": missing}, nil)
	other := e.kid(e.admin, "Other")
	e.expect(404, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": other.Id, "itemId": m, "snapshot": snapshot()}, nil)
	e.expect(404, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": k.Id, "itemId": uid.N(999), "snapshot": snapshot()}, nil)
	fileless := e.movie(e.library("tv"), "Empty")
	e.rate(fileless, "G")
	if raw := e.expect(409, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": k.Id, "itemId": fileless, "snapshot": snapshot()}, nil); len(raw) == 0 {
		t.Fatal("no body")
	}
}

func TestPlayabilityOnlyForWhatTheProfileMayPlay(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	var got api.Playability
	e.expect(200, "POST", "/items/"+itoa(m)+"/playability", e.plain.token, snapshot(), &got)
	if got.Verdict != api.DirectPlay {
		t.Fatalf("%+v", got)
	}
	var sessions api.SessionPage
	e.expect(200, "GET", "/sessions", e.admin.token, nil, &sessions)
	if len(sessions.Items) != 0 {
		t.Fatalf("no session is opened: %+v", sessions)
	}
	e.expect(400, "POST", "/items/"+itoa(m)+"/playability", e.plain.token, map[string]any{"decoder": map[string]any{}}, nil)

	show, _ := e.show(e.library("tv"), "Show", "TV-Y")
	e.expect(404, "POST", "/items/"+itoa(show)+"/playability", e.plain.token, snapshot(), nil)
	var film api.Item
	e.expect(200, "GET", "/items/"+itoa(m), e.plain.token, nil, &film)
	r := e.movie(film.LibraryId, "R film")
	e.rate(r, "R")
	k := e.kid(e.plain, "Kid")
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(404, "POST", "/items/"+itoa(r)+"/playability", e.plain.token, snapshot(), nil)
	e.expect(200, "POST", "/items/"+itoa(m)+"/playability", e.plain.token, snapshot(), nil)
}

package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

// timed returns a movie with one file of the given duration (nil for
// unknown) and a session playing it as the plain adult.
func (e *env) timed(durationMs *int64) (movie int64, sid string, id int64) {
	e.t.Helper()
	m := e.movie(e.library("movies"), "Film")
	e.rate(m, "G")
	f := map[string]any{"itemId": m, "path": e.onDisk("/films/film.mp4"), "sizeBytes": 1, "modifiedAt": "2026-01-01T00:00:00Z"}
	if durationMs != nil {
		f["durationMs"] = *durationMs
	}
	e.expect(201, "POST", "/media-files", e.admin.token, f, nil)
	st := e.start(m, snapshot())
	return m, "/playback/sessions/" + itoa(st.SessionId), st.SessionId
}

// play starts a new session on movie, ending the device's last one.
func (e *env) play(movie int64) string {
	e.t.Helper()
	return "/playback/sessions/" + itoa(e.start(movie, snapshot()).SessionId)
}

func (e *env) watchState(item int64) api.WatchState {
	e.t.Helper()
	var ws api.WatchState
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(item), e.plain.token, nil, &ws)
	return ws
}

// A recorded position at 90% of the file marks the item watched as the
// manual mark does: one play counted, position back to the start. Further
// reports near the end count no more plays, and a rewatch from the start
// leaves it watched.
func TestNearTheEndMarksWatched(t *testing.T) {
	e := newEnv(t)
	dur := int64(100_000)
	m, sid, _ := e.timed(&dur)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 89_999}, nil)
	if ws := e.watchState(m); ws.Watched || ws.PositionMs != 89_999 || ws.PlayCount != 0 {
		t.Fatalf("short of 90%%: %+v", ws)
	}
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 90_000}, nil)
	if ws := e.watchState(m); !ws.Watched || ws.PositionMs != 0 || ws.PlayCount != 1 {
		t.Fatalf("at 90%%: %+v", ws)
	}
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 99_000}, nil)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 5_000}, nil)
	if ws := e.watchState(m); !ws.Watched || ws.PositionMs != 5_000 || ws.PlayCount != 1 {
		t.Fatalf("credits, then a rewatch: %+v", ws)
	}
}

func TestUnknownDurationNeverMarksWatched(t *testing.T) {
	e := newEnv(t)
	m, sid, _ := e.timed(nil)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 10_000_000}, nil)
	if ws := e.watchState(m); ws.Watched || ws.PositionMs != 10_000_000 {
		t.Fatalf("%+v", ws)
	}
}

// Ending with a position records it as a position report would, the 90%
// rule included, before the session ends.
func TestEndingRecordsThePosition(t *testing.T) {
	e := newEnv(t)
	dur := int64(100_000)
	m, sid, id := e.timed(&dur)
	e.expect(204, "DELETE", sid+"?positionMs=42000", e.plain.token, nil, nil)
	if ws := e.watchState(m); ws.Watched || ws.PositionMs != 42_000 {
		t.Fatalf("%+v", ws)
	}
	var sess api.Session
	e.expect(200, "GET", "/sessions/"+itoa(id), e.admin.token, nil, &sess)
	if sess.PositionMs != 42_000 || sess.EndedAt == nil {
		t.Fatalf("%+v", sess)
	}
	sid = e.play(m)
	e.expect(204, "DELETE", sid+"?positionMs=95000", e.plain.token, nil, nil)
	if ws := e.watchState(m); !ws.Watched || ws.PlayCount != 1 {
		t.Fatalf("%+v", ws)
	}
	sid = e.play(m)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 7_000}, nil)
	e.expect(204, "DELETE", sid, e.plain.token, nil, nil)
	if ws := e.watchState(m); ws.PositionMs != 7_000 {
		t.Fatalf("no position leaves the last report: %+v", ws)
	}
}

// Telemetry may carry the position; it is recorded as a position report,
// and telemetry with or without one keeps the session from being reaped.
func TestTelemetryCarriesThePositionAndKeepsTheSessionLive(t *testing.T) {
	e := newEnv(t)
	dur := int64(100_000)
	m, sid, id := e.timed(&dur)
	e.expect(200, "PUT", sid+"/throughput", e.plain.token,
		map[string]any{"measuredBitrate": 9_000_000, "bufferMs": 20_000, "stallCount": 0, "positionMs": 30_000}, nil)
	if ws := e.watchState(m); ws.PositionMs != 30_000 {
		t.Fatalf("%+v", ws)
	}
	if v, err := e.svc.Sessions.Repo().Get(id); err != nil || v.PositionMs != 30_000 {
		t.Fatalf("%+v %v", v, err)
	}
	before := time.Now().UTC()
	time.Sleep(2 * time.Millisecond)
	e.expect(200, "PUT", sid+"/throughput", e.plain.token, map[string]any{"measuredBitrate": 9_000_000, "bufferMs": 20_000, "stallCount": 0}, nil)
	must(t, e.svc.Playback.Reap(before))
	if v, err := e.svc.Sessions.Repo().Get(id); err != nil || v.EndedAt != nil || v.PositionMs != 30_000 {
		t.Fatalf("telemetry is a sign of life: %+v %v", v, err)
	}
}

package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
)

func (e *env) playing(token string, item int64) []api.PlaybackSession {
	e.t.Helper()
	var p api.PlaybackSessionPage
	path := "/playback/sessions"
	if item != 0 {
		path += "?itemId=" + itoa(item)
	}
	e.expect(200, "GET", path, token, nil, &p)
	return p.Items
}

func TestPlayingOnAnotherDeviceAndMoveHere(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	m, _ := e.playable()
	me := e.plain.account.ID
	tv, tvID := e.secondDevice(e.plain, "Living Room", "roku")
	neighbor := e.neighbor(e.plain)
	st := e.startOn(tv, me, m)
	e.position(tv, st.SessionId, 2_472_000)
	one := "/playback/sessions/" + itoa(st.SessionId)

	got := e.playing(e.plain.token, m)
	if len(got) != 1 || got[0].Id != st.SessionId || got[0].ItemId != m || got[0].DeviceId != tvID || got[0].DeviceName != "Living Room" ||
		got[0].Platform != api.Roku || got[0].ThisDevice || got[0].PositionMs != 2_472_000 || got[0].EndedAt != nil || got[0].MovedToDeviceName != nil {
		t.Fatalf("playing on the TV, asked from the laptop: %+v", got)
	}
	if got = e.playing(tv, 0); len(got) != 1 || !got[0].ThisDevice {
		t.Fatalf("asked from the TV itself: %+v", got)
	}
	if got = e.playing(e.plain.token, m+1); len(got) != 0 {
		t.Fatalf("another title: %+v", got)
	}
	if got = e.playing(neighbor, m); len(got) != 0 {
		t.Fatalf("another profile's session: %+v", got)
	}
	e.expect(404, "GET", one, neighbor, nil, nil)
	e.expect(404, "POST", one+"/move", neighbor, nil, nil)
	e.expect(409, "POST", one+"/move", tv, nil, nil)

	// A session that stopped reporting is not playing; one that reports again is.
	must(t, e.svc.Sessions.Repo().SetPosition(st.SessionId, 2_472_000, time.Now().Add(-31*time.Second)))
	if got = e.playing(e.plain.token, m); len(got) != 0 {
		t.Fatalf("quiet for 31 seconds: %+v", got)
	}
	e.position(tv, st.SessionId, 2_480_000)

	at := e.cursor(tv)
	var moved api.PlaybackSession
	e.expect(200, "POST", one+"/move", e.plain.token, nil, &moved)
	if moved.Id != st.SessionId || moved.PositionMs != 2_480_000 || moved.EndedAt == nil || moved.MovedToDeviceName == nil ||
		*moved.MovedToDeviceName != "Laptop" {
		t.Fatalf("moved: %+v", moved)
	}
	heard := e.heard(tv, &at)
	if len(heard) != 1 || !is(heard[0], api.EventKindPlayback, live.Moved) || *heard[0].SessionId != st.SessionId || *heard[0].DeviceId != tvID {
		t.Fatalf("the TV hears it: %+v", heard)
	}
	var there api.PlaybackSession
	e.expect(200, "GET", one, tv, nil, &there)
	if there.EndedAt == nil || there.MovedToDeviceName == nil || *there.MovedToDeviceName != "Laptop" || !there.ThisDevice {
		t.Fatalf("the TV reads where it went: %+v", there)
	}
	e.expect(404, "PUT", one+"/position", tv, map[string]any{"positionMs": 1}, nil)
	e.expect(409, "POST", one+"/move", e.plain.token, nil, nil)
	if got = e.playing(e.plain.token, m); len(got) != 0 {
		t.Fatalf("nothing plays there now: %+v", got)
	}

	// Here it starts where the TV was, and the TV may take it back.
	here := e.startOn(e.plain.token, me, m)
	if here.PositionMs != 2_480_000 {
		t.Fatalf("resumed at %d", here.PositionMs)
	}
	e.expect(200, "POST", "/playback/sessions/"+itoa(here.SessionId)+"/move", tv, nil, &moved)
	if *moved.MovedToDeviceName != "Living Room" {
		t.Fatalf("%+v", moved)
	}
}

func TestStationsAndSongsAreNotOfferedToMove(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	film := e.film(e.library("movies"), "Lantern", "2001-01-01", "PG")
	pl := e.nightsPlaylist(map[int64]int64{film: 600})
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl, "timing": "continuous"}, &st)
	tv, _ := e.secondDevice(e.plain, "TV", "roku")
	var tuned api.StationPlayback
	e.expect(201, "POST", "/stations/"+itoa(st.Id)+"/playback", tv, map[string]any{"snapshot": snapshot()}, &tuned)
	if got := e.playing(e.plain.token, 0); len(got) != 0 {
		t.Fatalf("a station session: %+v", got)
	}
	e.expect(409, "POST", "/playback/sessions/"+itoa(tuned.Stream.SessionId)+"/move", e.plain.token, nil, nil)

	// Nor is a station's one live stream, whose session follows the
	// program on; tuning in to it tells of no playback.
	e.quiet()
	at := e.cursor(tv)
	e.tracks(film)
	live := e.tune(st, e.plain.token, snapshot())
	if got := append(e.playing(tv, 0), e.playing(tv, film)...); len(got) != 0 {
		t.Fatalf("a live station session: %+v", got)
	}
	e.expect(409, "POST", "/playback/sessions/"+itoa(live.Stream.SessionId)+"/move", tv, nil, nil)
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(live.Stream.SessionId), e.plain.token, nil, nil)
	if got := e.heard(tv, &at); len(got) != 0 {
		t.Fatalf("a station tune-in was told as playback: %+v", got)
	}
}

func TestWatchTogetherFromAnotherDevice(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	m, _ := e.playable()
	other := e.movie(e.library("tv"), "Other")
	e.file(other, 60_000)
	me := e.plain.account.ID
	tv, _ := e.secondDevice(e.plain, "Living Room", "roku")
	st := e.startOn(tv, me, m)
	e.position(tv, st.SessionId, 2_472_000)
	at := e.cursor(tv)

	mine := e.startOn(e.plain.token, me, other)
	for name, body := range map[string]map[string]any{
		"this device's session": {"itemId": other, "fromSessionId": mine.SessionId},
		"another title":         {"itemId": other, "fromSessionId": st.SessionId},
		"no such session":       {"itemId": m, "fromSessionId": 999},
	} {
		if code, raw := e.do("POST", "/watch-together", e.plain.token, body, nil); code != 400 {
			t.Fatalf("%s = %d: %s", name, code, raw)
		}
	}
	e.heard(tv, &at)

	var w api.WatchTogether
	e.expect(201, "POST", "/watch-together", e.plain.token, map[string]any{"itemId": m, "fromSessionId": st.SessionId}, &w)
	if w.Status != api.Waiting || w.PositionMs != 2_472_000 || len(w.Members) != 1 || w.Members[0].ThisDevice == nil || !*w.Members[0].ThisDevice {
		t.Fatalf("opened where the TV is: %+v", w)
	}
	got := e.heard(tv, &at)
	if len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Opened) || got[0].SessionId == nil || *got[0].SessionId != st.SessionId ||
		*got[0].WatchTogetherId != w.Id {
		t.Fatalf("the TV is told its session is asked for: %+v", got)
	}

	e.expect(409, "POST", togetherPath(w)+"/join", e.plain.token, nil, nil)
	e.expect(200, "POST", togetherPath(w)+"/join", tv, nil, &w)
	if w.Status != api.Joined || len(w.Members) != 2 || w.Members[0].AccountId != me || w.Members[1].AccountId != me ||
		*w.Members[0].ThisDevice || !*w.Members[1].ThisDevice || w.Members[0].DeviceName != "Laptop" || w.Members[1].DeviceName != "Living Room" {
		t.Fatalf("one profile, two devices: %+v", w)
	}
	e.expect(200, "PUT", togetherPath(w)+"/state", tv, map[string]any{"playing": true, "positionMs": 2_475_000}, &w)
	if !w.Playing {
		t.Fatalf("%+v", w)
	}
	e.expect(200, "GET", togetherPath(w), e.plain.token, nil, &w)
	if !w.Playing || !w.Members[0].Present || !w.Members[1].Present {
		t.Fatalf("the laptop follows: %+v", w)
	}
	// Starting the title here keeps the TV's session and the Watch together.
	e.startOn(e.plain.token, me, m)
	e.expect(200, "GET", togetherPath(w), tv, nil, &w)
	if got := e.playing(e.plain.token, m); w.Status != api.Joined || len(got) != 2 {
		t.Fatalf("both play: %+v %+v", w, got)
	}
}

// A kid hands a title to their other device as an adult does, while their
// filter allows the title. Once it does not, the title's Watch together is
// not there for the kid, by its session or by an event.
func TestAKidsWatchTogetherFromAnotherDeviceFollowsTheirFilter(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	m, _ := e.playable()
	kid := e.kid(e.plain, "Kid") // PG ceiling
	tv, tablet := e.kidDevice(e.plain, kid.Id), e.kidDevice(e.plain, kid.Id)
	st := e.startOn(tv, kid.Id, m)
	e.position(tv, st.SessionId, 90_000)
	at := e.cursor(tv)
	from := map[string]any{"itemId": m, "fromSessionId": st.SessionId}

	var w api.WatchTogether
	e.expect(201, "POST", "/watch-together", tablet, from, &w)
	if w.PositionMs != 90_000 || len(w.Members) != 1 || w.Members[0].AccountId != kid.Id {
		t.Fatalf("a kid opens where their other device is: %+v", w)
	}
	if got := e.heard(tv, &at); len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Opened) || got[0].SessionId == nil ||
		*got[0].SessionId != st.SessionId {
		t.Fatalf("the kid's other device is asked: %+v", got)
	}
	e.expect(200, "POST", togetherPath(w)+"/join", tv, nil, &w)
	if w.Status != api.Joined || w.Members[1].AccountId != kid.Id {
		t.Fatalf("one kid, two devices: %+v", w)
	}
	e.heard(tv, &at)

	e.rate(m, "R")
	adult := e.cursor(e.plain.token)
	e.expect(404, "POST", "/watch-together", tablet, from, nil)
	e.expect(404, "GET", togetherPath(w), tv, nil, nil)
	if got := e.heard(tv, &at); len(got) != 0 {
		t.Fatalf("a kid heard of a Watch together on a title now hidden: %+v", got)
	}
	if got := e.heard(e.plain.token, &adult); len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Ended) || *got[0].WatchTogetherId != w.Id {
		t.Fatalf("it ended, which an adult hears: %+v", got)
	}
}

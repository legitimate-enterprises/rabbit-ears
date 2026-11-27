package httpapi_test

import (
	"fmt"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func watched(t *testing.T, e *env, profile, id uid.ID) bool {
	t.Helper()
	var w api.WatchState
	if st, _ := e.do("GET", "/profiles/"+itoa(profile)+"/watch-state/"+itoa(id), e.plain.token, nil, &w); st == 404 {
		return false
	}
	return w.Watched
}

// One request marks a selection: movies themselves, a show its episodes, a
// channel its videos. An item the profile may not see, or of a kind with
// no watch state, marks nothing.
func TestPutWatchStatesMarksASelection(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	tb := e.youtubeLibrary()
	me := e.plain.account.ID
	path := "/profiles/" + itoa(me) + "/watch-state/batch"

	e.expect(204, "PUT", path, e.plain.token, map[string]any{"itemIds": []uid.ID{c.g, c.adultShow, tb.channel}, "watched": true}, nil)
	for _, id := range []uid.ID{c.g, c.adultEp, tb.newer, tb.older} {
		if !watched(t, e, me, id) {
			t.Errorf("item %s not marked watched", id)
		}
	}
	e.expect(204, "PUT", path, e.plain.token, map[string]any{"itemIds": []uid.ID{c.adultShow}, "watched": false}, nil)
	if watched(t, e, me, c.adultEp) {
		t.Error("the show's episode is still watched")
	}

	song := e.song(c.music, "Tune")
	e.expect(400, "PUT", path, e.plain.token, map[string]any{"itemIds": []uid.ID{c.pg, song}, "watched": true}, nil)
	e.expect(404, "PUT", path, e.plain.token, map[string]any{"itemIds": []uid.ID{c.pg, uid.N(999)}, "watched": true}, nil)
	if watched(t, e, me, c.pg) {
		t.Error("a rejected selection marked an item")
	}
	e.expect(400, "PUT", path, e.plain.token, map[string]any{"itemIds": []uid.ID{}, "watched": true}, nil)

	// A write that fails midway leaves the whole selection as it was.
	_, err := e.db.Exec(`CREATE TRIGGER fail_mark BEFORE INSERT ON watch_state WHEN NEW.item_id = ` + fmt.Sprintf("x'%x'", c.pg[:]) + //nolint:gosec // a DDL statement binds no parameters; the id is the test's own
		` BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	must(t, err)
	e.expect(500, "PUT", path, e.plain.token, map[string]any{"itemIds": []uid.ID{c.adultShow, c.pg}, "watched": true}, nil)
	if watched(t, e, me, c.adultEp) {
		t.Error("a failed selection marked an item")
	}
}

// One request sets kids' content on movies and shows and for kids on
// channels; anything else, or a kid active, sets nothing.
func TestSetKidsContentsSetsASelection(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	tb := e.youtubeLibrary()
	e.expect(204, "PUT", "/items/kids-content/batch", e.plain.token, map[string]any{"itemIds": []uid.ID{c.pg, c.adultShow, tb.channel}, "kidsContent": true}, nil)
	for _, id := range []uid.ID{c.pg, c.adultShow} {
		if got, over := kidsContent(itemOf(t, e, id)); !got || over == nil {
			t.Errorf("item %s: kidsContent %v override %v", id, got, over)
		}
	}
	var ch api.YouTubeChannel
	e.expect(200, "GET", "/youtube/channels/"+itoa(tb.channel), e.plain.token, nil, &ch)
	if ch.ForKids == nil || !*ch.ForKids {
		t.Error("the channel is not for kids")
	}

	e.expect(404, "PUT", "/items/kids-content/batch", e.plain.token, map[string]any{"itemIds": []uid.ID{c.r, c.adultEp}, "kidsContent": true}, nil)
	if got, _ := kidsContent(itemOf(t, e, c.r)); got {
		t.Error("a rejected selection set an item")
	}
	k := e.kid(e.plain, "Kid")
	e.expect(403, "PUT", "/items/kids-content/batch", e.kidDevice(e.plain, k.Id), map[string]any{"itemIds": []uid.ID{c.g}, "kidsContent": false}, nil)
}

// One request appends a selection to a playlist, a station's own among
// them; a title of several editions takes its default one. A wrong kind
// adds nothing.
func TestAddPlaylistEntriesAppendsASelection(t *testing.T) {
	e := newEnv(t)
	tok, base := e.plain.token, "/profiles/"+itoa(e.plain.account.ID)+"/playlists"
	m := e.probed("h264-high41.mp4", e.onDisk("/films/a.mp4"))
	e.copyOf(m, "chapters.mkv", "Extended")
	single := e.probed("chapters.mkv", e.onDisk("/films/single.mkv"))
	var col api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Kites"}, &col)

	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "P", "kind": "movies"}, &pl)
	one := base + "/" + itoa(pl.Id)
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": single}, nil)
	eid := uid.New()
	e.expect(204, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": m, "id": eid}, {"collectionId": col.Id}, {"itemId": single}}}, nil)
	var page api.PlaylistEntryPage
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if len(page.Items) != 4 || page.Items[1].Id != eid || *page.Items[1].ItemId != m || page.Items[1].EditionId == nil ||
		*page.Items[2].CollectionId != col.Id || *page.Items[3].ItemId != single {
		t.Fatalf("entries %+v", page.Items)
	}
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(m), tok, nil, &it)
	for _, ed := range *it.Editions {
		if ed.Id == *page.Items[1].EditionId && !ed.IsDefault {
			t.Fatal("the entry plays a non-default edition")
		}
	}

	tv := e.library("tv")
	show, _ := e.show(tv, "Show", "TV-PG")
	e.expect(400, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": single}, {"itemId": show}}}, nil)
	e.expect(404, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": single}, {"itemId": uid.N(999)}}}, nil)
	e.expect(409, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": single}, {"itemId": single, "id": eid}}}, nil)
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if len(page.Items) != 4 {
		t.Fatalf("a rejected selection added: %d entries", len(page.Items))
	}

	// A write that fails midway leaves the whole selection out.
	_, err := e.db.Exec(`CREATE TRIGGER fail_add BEFORE INSERT ON playlist_entry WHEN NEW.item_id = ` + fmt.Sprintf("x'%x'", m[:]) + //nolint:gosec // a DDL statement binds no parameters; the id is the test's own
		` BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	must(t, err)
	e.expect(500, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": single}, {"itemId": m}}}, nil)
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if len(page.Items) != 4 {
		t.Fatalf("a failed selection added: %d entries", len(page.Items))
	}
	_, err = e.db.Exec(`DROP TRIGGER fail_add`)
	must(t, err)

	var st api.Station
	e.expect(201, "POST", "/stations", tok, map[string]any{"name": "Shows", "kind": "tv", "order": "playlist"}, &st)
	e.expect(204, "POST", base+"/"+itoa(st.PlaylistId)+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": show}}}, nil)
}

package serverinfo_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
)

func TestArtworkGenerationAdvancesWhenAnImageChangesAndSettles(t *testing.T) {
	d := dbtest.Open(t)
	r := serverinfo.New(d)
	gen := func(after time.Duration) int64 {
		t.Helper()
		g, err := r.ArtworkGeneration(time.Now().UTC().Add(after), 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	exec := func(q string) {
		t.Helper()
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	start := gen(time.Minute)
	exec(`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1, 'M', 'movies', 0, 0)`)
	exec(`INSERT INTO item (id, library_id, kind, parsed_title, added_at, updated_at) VALUES (1, 1, 'movie', 'a', 0, 0)`)
	exec(`INSERT INTO item_metadata (item_id, poster_path) VALUES (1, '/a.jpg')`)
	if g := gen(time.Minute); g != start+1 {
		t.Fatalf("an image arriving advances the generation: %d, want %d", g, start+1)
	}
	exec(`UPDATE item_metadata SET title = 'x', fetched_at = 5 WHERE item_id = 1`)
	if g := gen(2 * time.Minute); g != start+1 {
		t.Fatalf("a refresh that keeps the image keeps the generation: %d", g)
	}
	exec(`UPDATE item_metadata SET poster_path = '/b.jpg' WHERE item_id = 1`)
	exec(`UPDATE item_metadata SET backdrop_path = '/w.jpg' WHERE item_id = 1`)
	if g := gen(0); g != start+1 {
		t.Fatalf("changes made just now are not announced, so a scan does not move every client at each title: %d", g)
	}
	if g := gen(time.Minute); g != start+3 {
		t.Fatalf("they are once the changes stop: %d, want %d", g, start+3)
	}
	exec(`DELETE FROM item_metadata WHERE item_id = 1`)
	if g := gen(2 * time.Minute); g != start+4 {
		t.Fatalf("an unmatched title loses its image: %d, want %d", g, start+4)
	}
	exec(`INSERT INTO item_metadata (item_id, title) VALUES (1, 'no art')`)
	exec(`DELETE FROM item_metadata WHERE item_id = 1`)
	if g := gen(3 * time.Minute); g != start+4 {
		t.Fatalf("a row without an image changes nothing: %d", g)
	}
	exec(`INSERT INTO collection (source, tmdb_id, name, poster_path, created_at, updated_at) VALUES ('tmdb', 1, 'C', '/c.jpg', 0, 0)`)
	exec(`INSERT INTO person (tmdb_id, name, profile_path) VALUES (9, 'P', '/p.jpg')`)
	if g := gen(4 * time.Minute); g != start+6 {
		t.Fatalf("collection and headshot images count too: %d, want %d", g, start+6)
	}
}

// The metadata settings pick which source's image every title shows, so
// changing either moves the generation; saving them unchanged does not.
func TestArtworkGenerationAdvancesWithTheMetadataSettings(t *testing.T) {
	d := dbtest.Open(t)
	r := serverinfo.New(d)
	dbtest.Sources(t, d, "local", "only")
	gen := func(after time.Duration) int64 {
		t.Helper()
		g, err := r.ArtworkGeneration(time.Now().UTC().Add(after), 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	start := gen(time.Minute)
	dbtest.Sources(t, d, "local", "only")
	if g := gen(2 * time.Minute); g != start {
		t.Fatalf("unchanged settings keep the generation: %d, want %d", g, start)
	}
	dbtest.Sources(t, d, "remote", "only")
	if g := gen(3 * time.Minute); g != start+1 {
		t.Fatalf("a new source: %d, want %d", g, start+1)
	}
	dbtest.Sources(t, d, "remote", "preferred")
	if g := gen(4 * time.Minute); g != start+2 {
		t.Fatalf("a new mode: %d, want %d", g, start+2)
	}
}

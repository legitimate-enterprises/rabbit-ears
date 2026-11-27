package edition_test

import (
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

type fx struct {
	t     *testing.T
	d     *sql.DB
	eds   *edition.Repo
	files *mediafile.Repo
	items *item.Repo
	lib   uid.ID
	n     int
}

func setup(t *testing.T) *fx {
	d := dbtest.Open(t)
	l := &library.Library{Name: "m", Kind: library.Movies}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	return &fx{t: t, d: d, eds: edition.New(d), files: mediafile.New(d), items: item.New(d), lib: l.ID}
}

func (x *fx) movie() uid.ID {
	it := &item.Item{LibraryID: x.lib, Kind: item.Movie, ParsedTitle: "Lantern"}
	if err := x.items.Create(it); err != nil {
		x.t.Fatal(err)
	}
	return it.ID
}

// file makes an unlinked file of minutes, with one video track of height h
// (none for 0).
func (x *fx) file(minutes, h int) uid.ID {
	x.t.Helper()
	x.n++
	ms := int64(minutes) * 60000
	f := &mediafile.File{Path: "/m/" + strconv.Itoa(x.n) + ".mkv", SizeBytes: int64(h), FileModifiedAt: time.Now(), DurationMs: &ms}
	if err := x.files.CreateFile(f); err != nil {
		x.t.Fatal(err)
	}
	if h > 0 {
		w := h * 16 / 9
		if err := x.files.ReplaceTracks(f.ID, []*mediafile.Track{{Kind: mediafile.Video, Codec: "h264", Width: &w, Height: &h}}); err != nil {
			x.t.Fatal(err)
		}
	}
	return f.ID
}

func (x *fx) place(file, it uid.ID, tag string, cuts bool) uid.ID {
	x.t.Helper()
	f, err := x.files.File(file)
	if err != nil {
		x.t.Fatal(err)
	}
	id, err := x.eds.Place(file, it, tag, f.DurationMs, cuts)
	if err != nil {
		x.t.Fatal(err)
	}
	return id
}

func names(es []*edition.Edition) string {
	s := ""
	for _, e := range es {
		if e.Default {
			s += "*"
		}
		s += e.Name + ";"
	}
	return s
}

// Tags name editions case-insensitively; untagged files within CutGapMs
// of a cut are copies of it, further apart a cut of their own, named by
// its runtime; the untagged cut first recorded is the default.
func TestPlaceSortsFilesIntoEditions(t *testing.T) {
	x := setup(t)
	m := x.movie()
	std := x.place(x.file(100, 1080), m, "", true)
	if again := x.place(x.file(102, 2160), m, "", true); again != std {
		t.Fatal("a copy two minutes off made a new edition")
	}
	long := x.place(x.file(110, 1080), m, "", true)
	ext := x.place(x.file(130, 720), m, "Extended", true)
	if x.place(x.file(131, 1080), m, "extended", true) != ext || long == std {
		t.Fatal("tags or cuts mixed up")
	}
	es, err := x.eds.ForItem(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(es); got != "*100-minute cut;110-minute cut;Extended;" {
		t.Fatalf("editions %s", got)
	}
	if es[0].RuntimeMs == nil || *es[0].RuntimeMs != 100*60000 {
		t.Fatalf("runtime %v", es[0].RuntimeMs)
	}
	cs, err := x.eds.Copies(std)
	if err != nil || len(cs) != 2 || cs[0].Quality() != "4K" || cs[1].Quality() != "1080p" {
		t.Fatalf("copies %+v %v", cs, err)
	}
	// Without cuts every untagged file is one edition.
	ep := x.movie()
	a := x.place(x.file(40, 720), ep, "", false)
	if x.place(x.file(80, 0), ep, "", false) != a {
		t.Fatal("episode split by runtime")
	}
	if es, _ := x.eds.ForItem(ep); names(es) != "*Standard;" {
		t.Fatalf("episode editions %s", names(es))
	}
}

// A title with only tagged editions defaults to the first; an edition
// whose files are gone is kept but not listed, and a file moved within a
// title leaves the edition it left.
func TestDefaultsAndEmptyEditions(t *testing.T) {
	x := setup(t)
	m := x.movie()
	f := x.file(100, 1080)
	dc := x.place(f, m, "Director's Cut", true)
	x.place(x.file(90, 1080), m, "Theatrical", true)
	d, err := x.eds.Default(m)
	if err != nil || d.ID != dc {
		t.Fatalf("default %+v %v", d, err)
	}
	moved := x.place(f, m, "", true)
	if es, _ := x.eds.ForItem(m); names(es) != "*Standard;Theatrical;" {
		t.Fatalf("editions %s", names(es))
	}
	if _, err := x.files.File(f); err != nil {
		t.Fatalf("moving a file lost it: %v", err)
	}
	if got, err := x.eds.Get(dc); err != nil || got.ItemID != m || got.Name != "Director's Cut" {
		t.Fatalf("empty edition %+v %v", got, err)
	}
	if id, err := x.eds.Of(m, f); err != nil || id != moved {
		t.Fatalf("edition of file %s %v", id, err)
	}
}

// The default is the first edition with a file on disk: a cut whose only
// file is gone yields to one that can play, and when every file is gone
// the choice is the one it would be were they all there.
func TestDefaultPrefersFilesOnDisk(t *testing.T) {
	x := setup(t)
	at := time.Now()
	for name, c := range map[string]struct{ goneOld, goneNew bool }{
		"old gone": {true, false}, "both gone": {true, true}, "both present": {false, false},
	} {
		m := x.movie()
		oldF, newF := x.file(100, 1080), x.file(120, 1080)
		old, newer := x.place(oldF, m, "", true), x.place(newF, m, "", true)
		for id, gone := range map[uid.ID]bool{oldF: c.goneOld, newF: c.goneNew} {
			if !gone {
				continue
			}
			f, err := x.files.File(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := x.files.SetMissing(f, &at); err != nil {
				t.Fatal(err)
			}
		}
		want := old
		if c.goneOld && !c.goneNew {
			want = newer
		}
		if d, err := x.eds.Default(m); err != nil || d.ID != want {
			t.Fatalf("%s: default %+v %v, want %s", name, d, err, want)
		}
	}
}

// A file holding two episodes sits in both; it goes with the last of them.
func TestFileInTwoTitles(t *testing.T) {
	x := setup(t)
	a, b := x.movie(), x.movie()
	f := x.file(50, 1080)
	x.place(f, a, "", false)
	x.place(f, b, "", false)
	ids, err := x.eds.ItemIDs(f)
	if err != nil || len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("items %v %v", ids, err)
	}
	if fs, _ := x.files.FilesForItem(b); len(fs) != 1 {
		t.Fatalf("files of b %d", len(fs))
	}
	if err := x.items.Delete(a); err != nil {
		t.Fatal(err)
	}
	if _, err := x.files.File(f); err != nil {
		t.Fatalf("file went with one of its titles: %v", err)
	}
	if err := x.items.Delete(b); err != nil {
		t.Fatal(err)
	}
	if _, err := x.files.File(f); err == nil {
		t.Fatal("file outlived its titles")
	}
}

// A fold moves the folded movie's downloads, onto the edition its own
// joined, and the covers showing its artwork to the movie kept.
func TestFoldKeepsDownloadsAndCovers(t *testing.T) {
	x := setup(t)
	a, b := x.movie(), x.movie()
	ea := x.place(x.file(100, 1080), a, "", true)
	fb, fc := x.file(100, 720), x.file(150, 1080)
	eb, ec := x.place(fb, b, "", true), x.place(fc, b, "", true)
	for _, q := range []string{
		`INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (?7,'adult','A',0,0)`,
		`INSERT INTO household (id, name, adult_id, created_at, modified_at) VALUES (?7,'H',?7,0,0)`,
		`INSERT INTO device (id, token_hash, name, platform, account_id, household_id, created_at, modified_at) VALUES (?7,'t','D','web',?7,?7,0,0)`,
		`INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (?7,?7,'P','movies',0,0)`,
		`INSERT INTO cover (id, playlist_id, item_id, created_at, modified_at) VALUES (?8,?7,?2,0,0)`,
		`INSERT INTO download (id, device_id, account_id, item_id, edition_id, media_file_id, quality, created_at, modified_at)
			VALUES (?8,?7,?7,?2,?3,?4,'original',0,0), (?9,?7,?7,?2,?5,?6,'original',0,0)`,
		`INSERT INTO playback_session (id, device_id, account_id, item_id, media_file_id, started_at, created_at, modified_at)
			VALUES (?7,?7,?7,?2,?4,0,0,0)`,
	} {
		if _, err := x.d.Exec(q, a, b, eb, fb, ec, fc, uid.N(1), uid.N(2), uid.N(3)); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.eds.Fold(a, b); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := x.d.QueryRow(`SELECT
		ifnull((SELECT group_concat(hex(item_id) || '/' || hex(edition_id), ' ') FROM (SELECT * FROM download ORDER BY id)), '') || ' ' ||
		ifnull((SELECT group_concat(hex(item_id)) FROM cover), '')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%[1]X/%[2]X %[1]X/%[3]X %[1]X", a[:], ea[:], ec[:]); got != want {
		t.Fatalf("downloads and cover %q, want %q", got, want)
	}
	// A session follows its title without counting as the device reporting.
	var item uid.ID
	var reported int64
	if err := x.d.QueryRow(`SELECT item_id, modified_at FROM playback_session`).Scan(&item, &reported); err != nil || item != a || reported != 0 {
		t.Fatalf("session on %v last reported %d: %v", item, reported, err)
	}
}

func TestQuality(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want string
	}{{3840, 2160, "4K"}, {3840, 1600, "4K"}, {1920, 1080, "1080p"}, {1920, 800, "1080p"}, {1280, 720, "720p"}, {720, 576, "SD"}, {0, 0, ""}} {
		if got := (edition.Copy{Width: &c.w, Height: &c.h}).Quality(); got != c.want {
			t.Errorf("%dx%d = %q, want %q", c.w, c.h, got, c.want)
		}
	}
}

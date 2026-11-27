package seekpreview_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestPutReplacesPerFileAndCascades(t *testing.T) {
	d := dbtest.Open(t)
	l := &library.Library{Name: "L", Kind: library.Movies, ScanHour: 3}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "F"}
	if err := item.New(d).Create(it); err != nil {
		t.Fatal(err)
	}
	files := mediafile.New(d)
	var fs []*mediafile.File
	for _, p := range []string{"/a.mkv", "/b.mkv"} {
		f := &mediafile.File{ItemID: &it.ID, Path: p, FileModifiedAt: time.Now().UTC()}
		if err := files.CreateFile(f); err != nil {
			t.Fatal(err)
		}
		fs = append(fs, f)
	}
	r := seekpreview.New(d)
	a := &seekpreview.SeekPreview{MediaFileID: fs[0].ID, Dir: "/data/previews/1", IntervalMs: 10000, Count: 3, Width: 320, Height: 180}
	if err := r.Put(a); err != nil || a.ID.IsZero() {
		t.Fatal(a, err)
	}
	b := &seekpreview.SeekPreview{MediaFileID: fs[1].ID, Dir: "/data/previews/2", IntervalMs: 10000, Count: 9, Width: 320, Height: 136}
	if err := r.Put(b); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // a later modified_at
	again := &seekpreview.SeekPreview{MediaFileID: fs[0].ID, Dir: "/data/previews/1", IntervalMs: 10000, Count: 4, Width: 320, Height: 180}
	if err := r.Put(again); err != nil {
		t.Fatal(err)
	}
	got, err := r.ByMediaFile(fs[0].ID)
	if err != nil || got.Count != 4 || got.ID != again.ID || again.ID != a.ID || got.Dir != "/data/previews/1" ||
		!again.CreatedAt.Equal(a.CreatedAt) || !again.ModifiedAt.After(a.ModifiedAt) {
		t.Fatalf("%+v %v", got, err)
	}
	if g, err := r.Get(b.ID); err != nil || g.Height != 136 || g.MediaFileID != fs[1].ID {
		t.Fatalf("%+v %v", g, err)
	}
	all, err := r.List(nil)
	if err != nil || len(all) != 2 || all[0].ID != again.ID {
		t.Fatalf("newest first: %+v %v", all, err)
	}
	if one, _ := r.List(&fs[1].ID); len(one) != 1 || one[0].ID != b.ID {
		t.Fatalf("%+v", one)
	}
	if err := r.Put(&seekpreview.SeekPreview{MediaFileID: uid.N(999), Dir: "x", IntervalMs: 1, Count: 1, Width: 1, Height: 1}); !errors.Is(err, db.ErrReference) {
		t.Fatalf("unknown file: %v", err)
	}
	if err := files.DeleteFile(fs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByMediaFile(fs[0].ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("previews outlived their file: %v", err)
	}
}

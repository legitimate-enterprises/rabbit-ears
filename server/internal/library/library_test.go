package library_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func folder(r *library.Repo, libraryID uid.ID, path string) (*library.Folder, error) {
	f := &library.Folder{LibraryID: libraryID, Path: path}
	return f, r.AddFolder(f, nil)
}

func TestCRUD(t *testing.T) {
	r := library.New(dbtest.Open(t))
	l := &library.Library{Name: "Films", Kind: library.Movies, ScanHour: 3, WatchFiles: true}
	if err := r.Create(l, nil); err != nil || l.ID.IsZero() {
		t.Fatal(err)
	}
	got, err := r.Get(l.ID)
	if err != nil || got.Name != "Films" || !got.WatchFiles || got.CreatedAt.Location() != time.UTC || got.LastScannedAt != nil {
		t.Fatalf("%+v %v", got, err)
	}
	got.Name, got.ScanHour = "Movies", 5
	if err := r.Update(got, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 3600))
	if err := r.MarkScanned(l.ID, at); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(l.ID)
	if got.Name != "Movies" || got.ScanHour != 5 || got.LastScannedAt == nil || !got.LastScannedAt.Equal(at) {
		t.Fatalf("%+v", got)
	}
	all, _ := r.List()
	if len(all) != 1 {
		t.Fatal(len(all))
	}
	if err := r.Delete(l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(l.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Update(l, nil); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestConstraints(t *testing.T) {
	r := library.New(dbtest.Open(t))
	if err := r.Create(&library.Library{Name: "a", Kind: "bogus"}, nil); err == nil {
		t.Fatal("bad kind accepted")
	}
	if err := r.Create(&library.Library{Name: "a", Kind: library.TV, ScanHour: 24}, nil); err == nil {
		t.Fatal("bad hour accepted")
	}
	a := &library.Library{Name: "a", Kind: library.TV}
	if err := r.Create(a, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&library.Library{Name: "a", Kind: library.TV}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("dup name: %v", err)
	}
}

// A path is kept as the folder it names, so one spelling cannot stand for
// two and a stored trailing slash reads back clean.
func TestFolderPathsAreClean(t *testing.T) {
	d := dbtest.Open(t)
	r := library.New(d)
	a := &library.Library{Name: "a", Kind: library.Movies}
	_ = r.Create(a, nil)
	f, err := folder(r, a.ID, "/media/movies/")
	if err != nil || f.Path != "/media/movies" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := folder(r, a.ID, "/media/movies"); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("the same folder twice: %v", err)
	}
	f.Path = "/media/film//"
	if err := r.UpdateFolder(f, nil); err != nil || f.Path != "/media/film" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := d.Exec(`UPDATE library_folder SET path = '/media/old/'`); err != nil {
		t.Fatal(err)
	}
	if fs, _ := r.Folders(a.ID); len(fs) != 1 || fs[0].Path != "/media/old" {
		t.Fatalf("a stored trailing slash: %+v", fs)
	}
}

func TestFolders(t *testing.T) {
	r := library.New(dbtest.Open(t))
	a := &library.Library{Name: "a", Kind: library.TV}
	b := &library.Library{Name: "b", Kind: library.Movies}
	_ = r.Create(a, nil)
	_ = r.Create(b, nil)
	f, err := folder(r, a.ID, "/media/tv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := folder(r, b.ID, "/media/tv"); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("shared path: %v", err)
	}
	if _, err := folder(r, uid.N(999), "/x"); !errors.Is(err, db.ErrReference) {
		t.Fatalf("bad library: %v", err)
	}
	if _, err := folder(r, b.ID, "/media/tv/kids"); err != nil {
		t.Fatal(err)
	}
	if fs, _ := r.Folders(a.ID); len(fs) != 1 || fs[0].Path != "/media/tv" {
		t.Fatalf("%+v", fs)
	}
	if l, err := r.ForPath("/media/tv/kids/x.mkv"); err != nil || l.ID != b.ID {
		t.Fatalf("longest root should win: %+v %v", l, err)
	}
	if l, err := r.ForPath("/media/tv/show/x.mkv"); err != nil || l.ID != a.ID {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := r.ForPath("/media/tvx/x.mkv"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("prefix must respect path boundary: %v", err)
	}
	f.Path = "/media/kids"
	if err := r.UpdateFolder(f, nil); err != nil {
		t.Fatal(err)
	}
	if fs, _ := r.Folders(a.ID); len(fs) != 1 || fs[0].Path != "/media/kids" {
		t.Fatalf("changed path: %+v", fs)
	}
	if err := r.UpdateFolder(&library.Folder{ID: f.ID, LibraryID: a.ID, Path: "/media/tv/kids"}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("path of another library: %v", err)
	}
	if err := r.UpdateFolder(&library.Folder{ID: f.ID, LibraryID: b.ID, Path: "/media/y"}, nil); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("folder of another library: %v", err)
	}
	if err := r.RemoveFolder(f.ID); err != nil {
		t.Fatal(err)
	}
	_ = r.Delete(b.ID)
	if fs, _ := r.Folders(b.ID); len(fs) != 0 {
		t.Fatal("folders should cascade")
	}
}

// A library and a folder keep the id a client chose, carry who made and
// last changed them, and a second row with the same id conflicts.
func TestClientIDsAndAuthors(t *testing.T) {
	d := dbtest.Open(t)
	r := library.New(d)
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A"}
	b := &account.Account{Kind: account.Adult, Email: "b@example.test", Name: "B"}
	for _, x := range []*account.Account{a, b} {
		if err := account.New(d).Create(x, nil); err != nil {
			t.Fatal(err)
		}
	}
	id := uid.New()
	l := &library.Library{ID: id, Name: "Films", Kind: library.Movies}
	if err := r.Create(l, &a.ID); err != nil || l.ID != id {
		t.Fatalf("%v %v", l.ID, err)
	}
	if err := r.Create(&library.Library{ID: id, Name: "Other", Kind: library.Movies}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("the same id twice: %v", err)
	}
	l.Name = "Movies"
	if err := r.Update(l, &b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(id)
	if err != nil || *got.CreatedBy != a.ID || *got.ModifiedBy != b.ID || got.ModifiedAt.Before(got.CreatedAt) {
		t.Fatalf("%+v %v", got, err)
	}
	fid := uid.New()
	f := &library.Folder{ID: fid, LibraryID: id, Path: "/media"}
	if err := r.AddFolder(f, &a.ID); err != nil {
		t.Fatal(err)
	}
	f.Path = "/films"
	if err := r.UpdateFolder(f, &b.ID); err != nil {
		t.Fatal(err)
	}
	if fs, err := r.Folders(id); err != nil || len(fs) != 1 || fs[0].ID != fid || *fs[0].CreatedBy != a.ID || *fs[0].ModifiedBy != b.ID {
		t.Fatalf("%+v %v", fs, err)
	}
}

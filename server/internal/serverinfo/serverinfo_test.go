package serverinfo_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
)

func TestSaveAndRename(t *testing.T) {
	r := serverinfo.New(dbtest.Open(t))
	if _, err := r.Get(); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Save("Den"); err != nil {
		t.Fatal(err)
	}
	if err := r.Save("Attic"); err != nil {
		t.Fatal(err)
	}
	i, err := r.Get()
	if err != nil || i.Name != "Attic" || i.UpdatedAt.Before(i.CreatedAt) {
		t.Fatalf("%+v %v", i, err)
	}
}

func TestUpdateRenamesAndPausesTasks(t *testing.T) {
	r := serverinfo.New(dbtest.Open(t))
	if err := r.Update(&serverinfo.Info{Name: "x"}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("no row before first run: %v", err)
	}
	if err := r.Save("Den"); err != nil {
		t.Fatal(err)
	}
	if i, _ := r.Get(); i.TasksPaused {
		t.Fatal("tasks start unpaused")
	}
	if err := r.Update(&serverinfo.Info{Name: "Attic", TasksPaused: true}); err != nil {
		t.Fatal(err)
	}
	if i, err := r.Get(); err != nil || i.Name != "Attic" || !i.TasksPaused {
		t.Fatalf("%+v %v", i, err)
	}
	if err := r.Save("Loft"); err != nil {
		t.Fatal(err)
	}
	if i, _ := r.Get(); !i.TasksPaused {
		t.Fatal("renaming keeps the pause")
	}
}

func TestTMDBKeyIsStoredKeptAndRemoved(t *testing.T) {
	r := serverinfo.New(dbtest.Open(t))
	if err := r.SetTMDBKey("abc"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("no row before first run: %v", err)
	}
	if err := r.Save("Den"); err != nil {
		t.Fatal(err)
	}
	if i, _ := r.Get(); i.TMDBKey != "" {
		t.Fatal("no key at first run")
	}
	if err := r.SetTMDBKey("abc"); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(&serverinfo.Info{Name: "Attic", TasksPaused: true}); err != nil {
		t.Fatal(err)
	}
	if i, err := r.Get(); err != nil || i.TMDBKey != "abc" {
		t.Fatalf("editing the row keeps the key: %+v %v", i, err)
	}
	if err := r.SetTMDBKey(""); err != nil {
		t.Fatal(err)
	}
	if i, _ := r.Get(); i.TMDBKey != "" {
		t.Fatalf("empty removes the key: %+v", i)
	}
}

// Every server starts Local only, before and after first run, and saving
// or removing a TMDB key leaves the settings as they are.
func TestMetadataSourcesStartLocalOnly(t *testing.T) {
	r := serverinfo.New(dbtest.Open(t))
	if s, err := r.Sources(); err != nil || s != serverinfo.Default || s.UseRemote() {
		t.Fatalf("%+v %v", s, err)
	}
	if err := r.SetSources(serverinfo.Sources{Source: serverinfo.Remote, Mode: serverinfo.Only}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("no row before first run: %v", err)
	}
	if err := r.Save("Den"); err != nil {
		t.Fatal(err)
	}
	if s, err := r.Sources(); err != nil || s != serverinfo.Default {
		t.Fatalf("%+v %v", s, err)
	}
	want := serverinfo.Sources{Source: serverinfo.Remote, Mode: serverinfo.Preferred}
	if err := r.SetSources(want); err != nil {
		t.Fatal(err)
	}
	if err := r.SetTMDBKey("k"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetTMDBKey(""); err != nil {
		t.Fatal(err)
	}
	if s, err := r.Sources(); err != nil || s != want || !s.UseRemote() {
		t.Fatalf("%+v %v", s, err)
	}
	if (serverinfo.Sources{Source: "web", Mode: serverinfo.Only}).Valid() || !want.Valid() {
		t.Fatal("validity")
	}
}

// The read-ahead starts at five minutes, before and after first run; the
// row keeps it through other edits and refuses one outside its bounds.
func TestReadAheadStartsAtFiveMinutes(t *testing.T) {
	r := serverinfo.New(dbtest.Open(t))
	if s, err := r.ReadAhead(); err != nil || s != 300 {
		t.Fatalf("%d %v", s, err)
	}
	if err := r.SetReadAhead(120); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("no row before first run: %v", err)
	}
	if err := r.Save("Den"); err != nil {
		t.Fatal(err)
	}
	if s, err := r.ReadAhead(); err != nil || s != 300 {
		t.Fatalf("%d %v", s, err)
	}
	if err := r.SetReadAhead(120); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(&serverinfo.Info{Name: "Attic"}); err != nil {
		t.Fatal(err)
	}
	if s, err := r.ReadAhead(); err != nil || s != 120 {
		t.Fatalf("editing the row keeps the read-ahead: %d %v", s, err)
	}
	for _, bad := range []int{29, 601} {
		if err := r.SetReadAhead(bad); err == nil {
			t.Fatalf("saved a read-ahead of %d seconds", bad)
		}
	}
}

// The transcode limits start at the engine's defaults, before and after
// first run; the row keeps them through other edits and refuses ones outside
// their bounds.
func TestTranscodeLimitsStartAtTheEngineDefaults(t *testing.T) {
	r := serverinfo.New(dbtest.Open(t))
	if n, c, err := r.TranscodeLimits(); err != nil || n != 3 || c != 20_000_000_000 {
		t.Fatalf("%d %d %v", n, c, err)
	}
	if err := r.SetTranscodeLimits(2, 4_000_000_000); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("no row before first run: %v", err)
	}
	if err := r.Save("Den"); err != nil {
		t.Fatal(err)
	}
	if n, c, err := r.TranscodeLimits(); err != nil || n != 3 || c != 20_000_000_000 {
		t.Fatalf("%d %d %v", n, c, err)
	}
	if err := r.SetTranscodeLimits(2, 4_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(&serverinfo.Info{Name: "Attic"}); err != nil {
		t.Fatal(err)
	}
	if i, err := r.Get(); err != nil || i.MaxTranscodes != 2 || i.TranscodeCacheCapBytes != 4_000_000_000 {
		t.Fatalf("editing the row keeps the limits: %+v %v", i, err)
	}
	for _, bad := range []struct {
		n int
		c int64
	}{{0, 4_000_000_000}, {9, 4_000_000_000}, {2, 999_999_999}, {2, 1_000_000_000_001}} {
		if err := r.SetTranscodeLimits(bad.n, bad.c); err == nil {
			t.Fatalf("saved limits %d and %d", bad.n, bad.c)
		}
	}
}

package scan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
)

// unprint takes every file's fingerprint away, as records made before
// fingerprints were have none, so no rename is told by it.
func (e *env) unprint() {
	e.t.Helper()
	_, err := e.d.Exec(`UPDATE media_file SET fingerprint = NULL`)
	must(e.t, err)
}

// A song whose file was renamed without a fingerprint to tell it by is
// folded into the song made at the new path, which takes its plays.
func TestRenamedUnprintedSongFoldsIntoItsTwin(t *testing.T) {
	e := newEnv(t, library.Music, "")
	old := filepath.Join(e.root, "Artist", "Album")
	fixtureCopy(t, "sample.flac", filepath.Join(old, "01 - One.flac"))
	e.mustScan()
	songs := e.list(item.Song)
	e.play(songs[0].ID, 5000, 100)
	e.unprint()

	rename(t, old, filepath.Join(e.root, "Artist", "Album (1999)"))
	sum, err := e.s.Scan(context.Background(), e.lib, nil)
	must(t, err)
	songs = e.list(item.Song)
	if len(songs) != 1 || e.plays() != fmt.Sprintf("%s:5000", songs[0].ID) {
		t.Fatalf("state %q, plays %q", e.state(), e.plays())
	}
	if got := sum.String(); got != "1 song updated; 1 file missing" {
		t.Errorf("summary %q", got)
	}
	// The gone file's record, now spare under the twin, goes with the next scan.
	e.mustScan()
	settled := e.state()
	if strings.Contains(settled, "?") {
		t.Errorf("state %q keeps a missing file", settled)
	}
	e.mustScan()
	if e.state() != settled {
		t.Errorf("state %q was %q", e.state(), settled)
	}
}

// A song on a volume that is not mounted is not gone, so it keeps its
// records beside its twin.
func TestUnmountedSongDoesNotFold(t *testing.T) {
	e := newEnv(t, library.Music, "")
	volume := filepath.Join(t.TempDir(), "volume")
	err := library.New(e.d).AddFolder(&library.Folder{LibraryID: e.lib.ID, Path: volume}, nil)
	must(t, err)
	fixtureCopy(t, "sample.flac", filepath.Join(volume, "Artist", "Album", "01 - One.flac"))
	fixtureCopy(t, "sample.flac", filepath.Join(e.root, "Artist", "Album", "01 - One.flac"))
	e.mustScan()
	e.unprint()

	rename(t, volume, filepath.Join(t.TempDir(), "elsewhere"))
	_ = e.scan()
	if n := len(e.list(item.Song)); n != 2 {
		t.Fatalf("%d songs, state %q", n, e.state())
	}
}

// Live copies of one track are each kept, and gone ones with more than
// one twin are folded into none.
func TestLiveCopiesOfASongStay(t *testing.T) {
	e := newEnv(t, library.Music, "")
	old := filepath.Join(e.root, "Artist", "Album")
	fixtureCopy(t, "sample.flac", filepath.Join(old, "01 - One.flac"))
	fixtureCopy(t, "sample.flac", filepath.Join(old, "01 - Two.flac"))
	e.mustScan()
	e.unprint()

	rename(t, old, filepath.Join(e.root, "Artist", "Album (1999)"))
	e.mustScan()
	if n := len(e.list(item.Song)); n != 4 {
		t.Fatalf("%d songs, state %q", n, e.state())
	}
}

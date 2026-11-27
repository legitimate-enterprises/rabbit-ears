package scan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
)

// An episode with two files keeps the one on disk and loses the record of
// the one deleted, with its chapter thumbnails; the scan after changes
// nothing.
func TestGoneFileOfATitleWithAnotherIsDeleted(t *testing.T) {
	e := newEnv(t, library.TV, "")
	kept := filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E01.mp4")
	gone := filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E01 - 1080p.mkv")
	fixtureCopy(t, "h264-high41.mp4", kept)
	fixtureCopy(t, "chapters.mkv", gone)
	e.mustScan()
	ep := e.list(item.Episode)
	if fs, _ := e.files.FilesForItem(ep[0].ID); len(ep) != 1 || len(fs) != 2 {
		t.Fatalf("episodes %+v, files %+v", ep, fs)
	}
	f, err := e.files.FileByPath(gone)
	must(t, err)
	thumbs := filepath.Join(e.s.ThumbDir, f.ID.String())
	if _, err := os.Stat(thumbs); err != nil {
		t.Fatal(err)
	}

	must(t, os.Remove(gone))
	if got := scanSummary(t, e); got != "No changes; 1 episode checked" {
		t.Errorf("summary %q", got)
	}
	if _, err := e.files.FileByPath(gone); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("gone file's record: %v", err)
	}
	if _, err := os.Stat(thumbs); !os.IsNotExist(err) {
		t.Errorf("chapter thumbnails: %v", err)
	}
	if got := e.list(item.Episode); len(got) != 1 || got[0].ID != ep[0].ID || e.missing(kept) {
		t.Fatalf("episodes %+v, kept file missing %v", got, e.missing(kept))
	}
	before := e.state()
	if got := scanSummary(t, e); got != "No changes; 1 episode checked" || e.state() != before {
		t.Fatalf("summary %q, state %q was %q", got, e.state(), before)
	}
}

// An episode whose only file is deleted keeps its title and the file's
// record, marked missing.
func TestGoneOnlyFileOfATitleIsMarked(t *testing.T) {
	e := newEnv(t, library.TV, "")
	only := filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E01.mp4")
	fixtureCopy(t, "h264-high41.mp4", only)
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E02.mkv"))
	e.mustScan()
	before := e.list(item.Episode)

	must(t, os.Remove(only))
	if got := scanSummary(t, e); got != "No changes; 1 episode checked; 1 file missing" {
		t.Errorf("summary %q", got)
	}
	if got := e.list(item.Episode); len(got) != len(before) || !e.missing(only) {
		t.Fatalf("episodes %+v were %+v, missing %v", got, before, e.missing(only))
	}
}

// A file under a folder that cannot be read, or is not there, is not taken
// for gone, though its title has another file on disk.
func TestFileInAnUnreadableFolderIsNotDeleted(t *testing.T) {
	lock := func(t *testing.T, dir string) {
		must(t, os.Chmod(dir, 0))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // a folder needs its execute bit to be removed
		if _, err := os.ReadDir(dir); err == nil {
			t.Skip("permissions do not stop this user reading")
		}
	}
	for _, c := range []struct {
		name string
		hide func(t *testing.T, root, season string)
	}{
		{"unreadable library folder", func(t *testing.T, root, _ string) { lock(t, root) }},
		{"absent library folder", func(t *testing.T, root, _ string) {
			must(t, os.Rename(root, filepath.Join(t.TempDir(), "volume")))
		}},
		{"unreadable season folder", func(t *testing.T, _, season string) { lock(t, season) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, library.TV, "")
			season := filepath.Join(e.root, "Lamp Street", "Season 01")
			away := filepath.Join(season, "Lamp Street - S01E01.mkv")
			fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E01.mp4"))
			fixtureCopy(t, "chapters.mkv", away)
			e.mustScan()
			if got := e.list(item.Episode); len(got) != 1 {
				t.Fatalf("episodes %+v", got)
			}

			c.hide(t, e.root, season)
			if err := e.scan(); err == nil {
				t.Fatal("a scan of an unreadable folder reported no error")
			}
			if !e.missing(away) {
				t.Fatal("file under the unreadable folder not marked missing")
			}
		})
	}
}

// A library folder left empty, as the mount point of a volume not mounted
// is, keeps its files' records, though their titles have files in another
// folder.
func TestFileInAnEmptyLibraryFolderIsNotDeleted(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	other := t.TempDir()
	err := e.s.Libraries.AddFolder(&library.Folder{LibraryID: e.lib.ID, Path: other}, nil)
	must(t, err)
	away := filepath.Join(other, "Lantern (2001)", "Lantern (2001) 2160p.mkv")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001)", "Lantern (2001).mp4"))
	fixtureCopy(t, "chapters.mkv", away)
	e.mustScan()
	if fs, _ := e.files.FilesForItem(e.onlyMovie().ID); len(fs) != 2 {
		t.Fatalf("files %+v", fs)
	}

	must(t, os.RemoveAll(filepath.Dir(away)))
	e.mustScan()
	if !e.missing(away) {
		t.Fatal("file under the empty folder not marked missing")
	}
}

package scan

import (
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
)

// splitEnv is a TV library of two folders, the second returned.
func splitEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t, library.TV, "")
	second := t.TempDir()
	_, err := e.s.Libraries.AddFolder(e.lib.ID, second)
	must(t, err)
	return e, second
}

// paths is the path of every show and season of the library, in id order.
func (e *env) paths() string {
	e.t.Helper()
	var s string
	must(e.t, e.d.QueryRow(`SELECT group_concat(id || '=' || COALESCE(path, ''), ' ') FROM
		(SELECT * FROM item WHERE kind IN ('show','season') ORDER BY id)`).Scan(&s))
	return s
}

// stable scans again and fails if that made, lost or moved anything.
func (e *env) stable() {
	e.t.Helper()
	state, paths := e.state(), e.paths()
	e.mustScan()
	if got := e.state(); got != state {
		e.t.Errorf("state %s, then %s", state, got)
	}
	if got := e.paths(); got != paths {
		e.t.Errorf("paths %s, then %s", paths, got)
	}
}

func TestShowInTwoLibraryFoldersIsOneShow(t *testing.T) {
	e, second := splitEnv(t)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lamp Street (2010)", "Season 01", "Lamp Street - S01E01.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(second, "Lamp Street (2010)", "Season 02", "Lamp Street - S02E01.mkv"))
	e.mustScan()

	if shows, seasons, eps := e.list(item.Show), e.list(item.Season), e.list(item.Episode); len(shows) != 1 || len(seasons) != 2 || len(eps) != 2 {
		t.Fatalf("shows=%d seasons=%d episodes=%d", len(shows), len(seasons), len(eps))
	}
	e.stable()
}

func TestSeasonInTwoLibraryFoldersIsOneSeason(t *testing.T) {
	e, second := splitEnv(t)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lamp Street (2010)", "Season 01", "Lamp Street - S01E01.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(second, "Lamp Street (2010)", "Season 01", "Lamp Street - S01E02.mkv"))
	e.mustScan()

	shows, seasons, eps := e.list(item.Show), e.list(item.Season), e.list(item.Episode)
	if len(shows) != 1 || len(seasons) != 1 || len(eps) != 2 {
		t.Fatalf("shows=%d seasons=%d episodes=%d", len(shows), len(seasons), len(eps))
	}
	if want := filepath.Join(shows[0].Path, "Season 01"); seasons[0].Path != want {
		t.Errorf("season path %s, want %s", seasons[0].Path, want)
	}
	e.stable()
}

func TestShowsOfOneTitleInTwoLibraryFoldersStayApart(t *testing.T) {
	e, second := splitEnv(t)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lamp Street (2010)", "Season 01", "Lamp Street - S01E01.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(second, "Lamp Street (2019)", "Season 01", "Lamp Street - S01E01.mkv"))
	e.mustScan()

	if shows := e.list(item.Show); len(shows) != 2 {
		t.Fatalf("shows %d", len(shows))
	}
	e.stable()
}

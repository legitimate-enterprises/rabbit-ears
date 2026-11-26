package parse

import (
	"fmt"
	"testing"
)

func ip(n int) *int { return &n }

func TestTitle(t *testing.T) {
	cases := []struct {
		in    string
		title string
		year  *int
	}{
		{"The.Matrix.1999.1080p.BluRay.x264-GRP.mkv", "The Matrix", ip(1999)},
		{"Blade Runner 2049 (2017).mkv", "Blade Runner 2049", ip(2017)},
		{"2001 A Space Odyssey (1968)", "2001 A Space Odyssey", ip(1968)},
		{"Amélie (2001) [2160p] {tmdb-194}", "Amélie", ip(2001)},
		{"Mad_Max_Fury_Road_2015_720p_WEB-DL.mp4", "Mad Max Fury Road", ip(2015)},
		{"Heat", "Heat", nil},
		{"Dune.Part.Two.2160p.UHD.BluRay.REMUX.HDR.HEVC", "Dune Part Two", nil},
		{"Spider-Man.No.Way.Home.2021.mkv", "Spider-Man No Way Home", ip(2021)},
		{"1917 (2019).mkv", "1917", ip(2019)},
		{"Movie.Name.2010.1080p-GRP", "Movie Name", ip(2010)},
		{"Mr. Robot (2015)", "Mr. Robot", ip(2015)},
		{"Blade.Runner.2049.2017.1080p.BluRay.x264-GRP.mkv", "Blade Runner 2049", ip(2017)},
		{"Blade Runner 2049 2017 1080p.mkv", "Blade Runner 2049", ip(2017)},
		{"Wonder.Woman.1984.2020.2160p.WEB-DL.mkv", "Wonder Woman 1984", ip(2020)},
		{"Wonder Woman 1984 2020 2160p.mkv", "Wonder Woman 1984", ip(2020)},
		{"Death.Race.2000.1975.mkv", "Death Race 2000", ip(1975)},
		{"Death Race 2000 1975.mkv", "Death Race 2000", ip(1975)},
		{"1917.2019.1080p.mkv", "1917", ip(2019)},
		{"1917 2019 1080p.mkv", "1917", ip(2019)},
		{"(500) Days of Summer (2009).mkv", "(500) Days of Summer", ip(2009)},
		{"[REC] (2007).mkv", "REC", ip(2007)},
		{"[REC] (2007) [1080p].mkv", "REC", ip(2007)},
		{"[SubsPlease] 2012 (1080p).mkv", "2012", nil},
		{"Blade Runner 2049.mkv", "Blade Runner 2049", nil},
		{"Blade.Runner.2049.1080p.BluRay.mkv", "Blade Runner 2049", nil},
		{"Movie (2099).mkv", "Movie 2099", nil},
	}
	for _, c := range cases {
		title, year := Title(c.in)
		if title != c.title || (year == nil) != (c.year == nil) || (year != nil && *year != *c.year) {
			t.Errorf("Title(%q) = %q, %v; want %q, %v", c.in, title, year, c.title, c.year)
		}
	}
}

func TestEpisode(t *testing.T) {
	cases := []struct {
		in     string
		season int
		ep     int
		ok     bool
	}{
		{"Show.Name.S01E02.720p.HDTV.mkv", 1, 2, true},
		{"show name - s02e10 - Title.mkv", 2, 10, true},
		{"Show Name 1x05.mp4", 1, 5, true},
		{"Show S01E01E02.mkv", 1, 1, true},
		{"Show.S00E03.Special.mkv", 0, 3, true},
		{"Season 3 Episode 7.mkv", 3, 7, true},
		{"Show (2010) - S10E100.mkv", 10, 100, true},
		{"Movie.2019.1080p.mkv", 0, 0, false},
		{"Show.Name.1080p.x264.mkv", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := Episode(c.in)
		if ok != c.ok || s != c.season || e != c.ep {
			t.Errorf("Episode(%q) = %d, %d, %v; want %d, %d, %v", c.in, s, e, ok, c.season, c.ep, c.ok)
		}
	}
}

func TestSeasonDir(t *testing.T) {
	cases := map[string]struct {
		n  int
		ok bool
	}{"Season 01": {1, true}, "season 2": {2, true}, "S03": {3, true}, "Specials": {0, true}, "Season 0": {0, true}, "Extras": {0, false}, "Show Name": {0, false}}
	for in, want := range cases {
		n, ok := SeasonDir(in)
		if n != want.n || ok != want.ok {
			t.Errorf("SeasonDir(%q) = %d, %v", in, n, ok)
		}
	}
}

func TestEpisodeNumber(t *testing.T) {
	cases := map[string]struct {
		n  int
		ok bool
	}{"E05.mkv": {5, true}, "Show - Episode 12.mp4": {12, true}, "05 - Title.mkv": {5, true}, "Title.mkv": {0, false}, "Ep.3.mkv": {3, true}}
	for in, want := range cases {
		n, ok := EpisodeNumber(in)
		if n != want.n || ok != want.ok {
			t.Errorf("EpisodeNumber(%q) = %d, %v", in, n, ok)
		}
	}
}

// The library's own naming: Title (Year) {edition-Name} [Quality]-Group.ext
// for movies, Show (Year) - S01E02 - Episode Title [Quality]-Group.ext for
// episodes, a double episode as S10E17-E18.
func TestLibraryNames(t *testing.T) {
	if title, year := Title("Night Harbor (1987) {edition-Director's Cut} [Bluray-1080p]-GRP.mkv"); title != "Night Harbor" || year == nil || *year != 1987 {
		t.Errorf("Title = %q, %v", title, year)
	}
	for in, want := range map[string]string{
		"Night Harbor (1987) {edition-Director's Cut} [Bluray-1080p]-GRP.mkv": "Director's Cut",
		"Night Harbor (1987) {Edition-extended} [WEBDL-2160p]-GRP.mkv":        "extended",
		"Night Harbor (1987) [Bluray-1080p]-GRP.mkv":                          "",
		"Night Harbor (1987) {tmdb-123}.mkv":                                  "",
	} {
		if got := Edition(in); got != want {
			t.Errorf("Edition(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string][]int{
		"Lamp Street (2010) - S01E02 - The Key [HDTV-720p]-GRP.mkv":          {2},
		"Lamp Street (2010) - S10E17-E18 - Two Parts [WEBDL-1080p]-GRP.mkv":  {17, 18},
		"Lamp Street (2010) - S01E01E02 - Repeat [WEBDL-1080p]-GRP.mkv":      {1, 2},
		"Lamp Street (2010) - S01E01-03 - Range [WEBDL-1080p]-GRP.mkv":       {1, 2, 3},
		"Lamp Street (2010) - S01E01-E02-E03 - Scene [WEBDL-1080p]-GRP.mkv":  {1, 2, 3},
		"Lamp Street (2010) - S01E05-720p.mkv":                               {5},
		"Lamp Street (2010) - S00E01 - A Special [WEBDL-1080p]-GRP.mkv":      {1},
		"Lamp Street (2010) - S01E01-S01E02 - Two [WEBDL-1080p]-GRP.mkv":     {1, 2},
		"Lamp Street (2010) - S01E01.S01E02 - Two [WEBDL-1080p]-GRP.mkv":     {1, 2},
		"Lamp Street (2010) - S01E01-S02E01 - Not Two [WEBDL-1080p]-GRP.mkv": {1},
	} {
		s, eps, ok := Episodes(in)
		if !ok || fmt.Sprint(eps) != fmt.Sprint(want) {
			t.Errorf("Episodes(%q) = %d, %v, %v; want %v", in, s, eps, ok, want)
		}
	}
	if s, _, _ := Episodes("Lamp Street (2010) - S10E17-E18 - Two Parts.mkv"); s != 10 {
		t.Errorf("season %d", s)
	}
	if n, ok := SeasonDir("Specials"); !ok || n != 0 {
		t.Errorf("Specials = %d, %v", n, ok)
	}
}

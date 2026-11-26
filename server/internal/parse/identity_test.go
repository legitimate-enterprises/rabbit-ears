package parse

import (
	"fmt"
	"testing"
)

// The producers' folder and file names state identity in tags; each
// namespace is read on its own, and a tag that names a provider without
// an id of its form is reported, not skipped.
func TestReadIdentity(t *testing.T) {
	const release, group = "0a1b2c3d-1111-2222-3333-444455556666", "9f8e7d6c-aaaa-bbbb-cccc-ddddeeeeffff"
	cases := []struct {
		in   string
		want Identity
	}{
		{"The Office (2001) {tvdb-78107}", Identity{TVDB: 78107}},
		{"The Office US (2005) {tvdb-73244}", Identity{TVDB: 73244}},
		{"The Office (US)", Identity{Country: "US"}},
		{"The Office (UK) (2001)", Identity{Country: "GB"}},
		{"Iron Man (2008) {tmdb-1726}", Identity{TMDB: 1726}},
		{"Iron Man (2008) {tmdb-1726} {edition-Extended} [Bluray-1080p]-GRP.mkv", Identity{TMDB: 1726}},
		{"Heat (1995) {imdb-tt0113277}", Identity{IMDB: "tt0113277"}},
		{"Heat (1995) [tmdbid=949] [imdbid-tt0113277]", Identity{TMDB: 949, IMDB: "tt0113277"}},
		{"First Light (2024) (Deluxe) [mb-release-" + release + "]", Identity{Release: release}},
		{"First Light (2024) [mb-release-group-" + group + "]", Identity{ReleaseGroup: group}},
		{"First Light [MB-Release-0A1B2C3D-1111-2222-3333-444455556666]", Identity{Release: release}},
		{"Heat (1995) {tmdb-abc}", Identity{Invalid: []string{"{tmdb-abc}"}}},
		{"Heat (1995) {tmdb-0}", Identity{Invalid: []string{"{tmdb-0}"}}},
		{"Heat (1995) {imdb-113277}", Identity{Invalid: []string{"{imdb-113277}"}}},
		{"Heat (1995) {tvdb-}", Identity{Invalid: []string{"{tvdb-}"}}},
		{"First Light [mb-release-1234]", Identity{Invalid: []string{"[mb-release-1234]"}}},
		{"Heat (1995) {tmdb-949} {tmdb-950}", Identity{Invalid: []string{"{tmdb-949}", "{tmdb-950}"}}},
		{"Heat (HD) (TV) [1080p]", Identity{}},
		{"Plain Name", Identity{}},
	}
	for _, c := range cases {
		if got := ReadIdentity(c.in); fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", c.want) {
			t.Errorf("ReadIdentity(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	titles := map[string]string{
		"The Office (UK) (2001) {tvdb-78107}":                      "The Office",
		"The Office US (2005) {tvdb-73244}":                        "The Office US",
		"First Light (2024) (Deluxe) [mb-release-" + release + "]": "First Light",
		"Heat [tmdbid=949] (1995)":                                 "Heat",
	}
	for name, want := range titles {
		if title, _ := Title(name); title != want {
			t.Errorf("Title(%q) = %q, want %q", name, title, want)
		}
	}
}

// An episode file's premiere year stands before its season and episode
// marker; a year after it is an air date or part of the episode's title.
func TestEpisodeYear(t *testing.T) {
	cases := []struct {
		in   string
		want *int
	}{
		{"The Office (2001) - S01E01 - Downsize [WEBDL-1080p]-GRP.mkv", ip(2001)},
		{"The.Office.2005.S02E03.720p.mkv", ip(2005)},
		{"The Office - S01E01 - Pilot.mkv", nil},
		{"The Office - S01E01 - 2005-03-24 - Pilot.mkv", nil},
		{"Lamp Street - 1x02 - Summer of 1999.mkv", nil},
		{"1999 - S01E01.mkv", nil},
		{"E05.mkv", nil},
	}
	for _, c := range cases {
		if got := EpisodeYear(c.in); (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("EpisodeYear(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

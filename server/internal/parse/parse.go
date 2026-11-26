// Package parse extracts titles, years, seasons and episodes from the file
// and folder names people give media.
package parse

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	braces  = regexp.MustCompile(`[\[{(][^\]})]*[\]})]`)
	yearTok = regexp.MustCompile(`(?:^|[\s.(\[_-])((?:19|20)\d\d)(?:$|[\s.)\]_-])`)
	// Release tokens that end the title when no year precedes them.
	junk    = regexp.MustCompile(`(?i)(?:^|[\s.])(?:\d{3,4}p|4k|uhd|bluray|bdrip|brrip|web-?dl|webrip|hdtv|dvdrip|remux|hdr10?|hevc|x26[45]|h\.?26[45])(?:$|[\s.])`)
	sxe     = regexp.MustCompile(`(?i)s(\d{1,2})[\s._-]*e(\d{1,3})`)
	nxn     = regexp.MustCompile(`(?i)(?:^|[\s._-])(\d{1,2})x(\d{1,3})(?:$|[\s._-])`)
	words   = regexp.MustCompile(`(?i)season[\s._-]*(\d{1,2})[\s._-]*episode[\s._-]*(\d{1,3})`)
	epOnly  = regexp.MustCompile(`(?i)(?:^|[\s._-])(?:e|ep|episode)[\s._-]*(\d{1,3})(?:$|[\s._-])`)
	leadNum = regexp.MustCompile(`^(\d{1,3})(?:[\s._-]|$)`)
	seasDir = regexp.MustCompile(`(?i)^(?:season[\s._-]*|s)(\d{1,2})$`)
	// sxeRun is SxxEyy and the further episodes a multi-episode name chains
	// on: E02, -E02, -02, or another SxxEyy after a dot or a dash.
	sxeRun  = regexp.MustCompile(`(?i)s(\d{1,2})[\s._-]*e(\d{1,3})((?:-?e\d{1,3}|-\d{1,3}\b|[.-]s\d{1,2}[\s._-]*e\d{1,3})*)`)
	runPart = regexp.MustCompile(`(?i)(-?)(s(\d{1,2})[\s._-]*)?e?(\d{1,3})`)
	// A movie named for a bracketed word and its year, "[REC] (2007)", and a
	// title that opens with a number in parentheses, "(500) Days of Summer".
	wordYear = regexp.MustCompile(`^[\[{(]([^\]})]+)[\]})][\s._-]*\(((?:19|20)\d\d)\)`)
	numLead  = regexp.MustCompile(`^\(\d{1,3}\)`)
	edition  = regexp.MustCompile(`(?i)\{edition-([^}]+)\}`)
)

// Title returns the title and optional year of a movie or show name, with or
// without extension, dropping release tags, bracketed notes and separators.
// A number beyond next year is part of the title, not its year, and a
// bracketed word with a year, "[REC] (2007)", is the title.
func Title(name string) (string, *int) {
	s := stripExt(name)
	if m := wordYear.FindStringSubmatch(s); m != nil && !yearTok.MatchString(m[1]) && !junk.MatchString(m[1]) {
		if y, _ := strconv.Atoi(m[2]); y <= time.Now().Year()+1 {
			return strings.TrimSpace(m[1]), &y
		}
	}
	prefix := ""
	if m := numLead.FindString(s); m != "" && strings.TrimSpace(s[len(m):]) != "" {
		prefix, s = m+" ", s[len(m):]
	}
	s = braces.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) == 6 && yearTok.MatchString(m) { // "(2019)" survives for the year scan
			return " " + m[1:5] + " "
		}
		return " "
	})
	s = strings.NewReplacer("_", " ").Replace(s)
	var year *int
	// The last year that is not the whole title (1917, 2001) is the release year.
	cut := -1
	// Each search resumes at the year's trailing separator, which doubles as
	// the leading separator of an adjacent year ("2049.2017"); the suffix's
	// start is never the name's start, so `^` must not match there.
	for pos := 0; pos < len(s); {
		m := yearTok.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		lead, end := pos+m[2], pos+m[3]
		bare := pos > 0 && m[0] == m[2] // matched `^` of the suffix, not a separator
		pos = end
		if bare || strings.TrimSpace(s[:lead]) == "" {
			continue
		}
		y, _ := strconv.Atoi(s[lead:end])
		if y > time.Now().Year()+1 { // "Blade Runner 2049" names no release year
			continue
		}
		year, cut = &y, lead
	}
	if cut >= 0 {
		s = s[:cut]
	}
	if year == nil {
		if loc := junk.FindStringIndex(s); loc != nil && strings.TrimSpace(s[:loc[0]]) != "" {
			s = s[:loc[0]]
		}
	}
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = strings.ReplaceAll(s, ".", " ")
	} else if strings.Count(s, ".") > strings.Count(s, " ") {
		s = strings.ReplaceAll(s, ".", " ")
	}
	title := strings.Join(strings.Fields(strings.Trim(s, " .-")), " ")
	if title != "" {
		title = prefix + title
	}
	return title, year
}

// Episode reads season and first episode number from a file name. Season 0
// is specials.
func Episode(name string) (season, episode int, ok bool) {
	name = stripExt(name)
	for _, re := range []*regexp.Regexp{sxe, words, nxn} {
		if m := re.FindStringSubmatch(name); m != nil {
			season, _ = strconv.Atoi(m[1])
			episode, _ = strconv.Atoi(m[2])
			return season, episode, true
		}
	}
	return 0, 0, false
}

// Episodes reads the season and every episode a file name holds: S01E02
// is one, S01E01E02 and S01E01-E02 are two, and a dash between two numbers
// is a range, so S01E01-03 is three. A dash spanning more than 10 is read as
// the two numbers alone. Another SxxEyy of the same season after a dot or a
// dash, S01E01-S01E02, is a further episode.
func Episodes(name string) (season int, episodes []int, ok bool) {
	m := sxeRun.FindStringSubmatch(stripExt(name))
	if m == nil {
		s, e, ok := Episode(name)
		if !ok {
			return 0, nil, false
		}
		return s, []int{e}, true
	}
	season, _ = strconv.Atoi(m[1])
	first, _ := strconv.Atoi(m[2])
	episodes = []int{first}
	for _, p := range runPart.FindAllStringSubmatch(m[3], -1) {
		n, _ := strconv.Atoi(p[4])
		last := episodes[len(episodes)-1]
		if n <= last {
			continue
		}
		// A chained SxxEyy of another season is not this file's episode.
		if sn, _ := strconv.Atoi(p[3]); p[2] != "" && sn != season {
			continue
		}
		from := n
		if p[1] == "-" && p[2] == "" && n-last <= 10 {
			from = last + 1
		}
		for k := from; k <= n; k++ {
			episodes = append(episodes, k)
		}
	}
	return season, episodes, true
}

// Edition is the name in a file name's {edition-Name} tag, or "" for none.
func Edition(name string) string {
	if m := edition.FindStringSubmatch(filepath.Base(name)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// EpisodeNumber reads an episode number from a file inside a season folder
// that carries no season marker: "E05", "Episode 5", "05 - Title".
func EpisodeNumber(name string) (int, bool) {
	name = stripExt(name)
	for _, re := range []*regexp.Regexp{epOnly, leadNum} {
		if m := re.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, true
		}
	}
	return 0, false
}

// SeasonDir reads a season folder name; "Specials" is season 0.
func SeasonDir(name string) (int, bool) {
	if strings.EqualFold(strings.TrimSpace(name), "specials") {
		return 0, true
	}
	if m := seasDir.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	return 0, false
}

func stripExt(name string) string {
	name = filepath.Base(name)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".ts", ".webm", ".wmv":
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name
}

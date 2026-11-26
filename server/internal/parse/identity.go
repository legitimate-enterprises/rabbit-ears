package parse

import (
	"regexp"
	"strconv"
	"strings"
)

// Identity is what a folder or file name states outright about which title
// it holds. Each provider's id is its own namespace: a TMDB id is a movie's
// or a show's by the library it stands in.
type Identity struct {
	TMDB, TVDB int64
	IMDB       string // "tt" and its digits
	// Release and ReleaseGroup are MusicBrainz ids. A group is an album in
	// all its editions, never one of them.
	Release, ReleaseGroup string
	// Country is the ISO 3166-1 alpha-2 code of a "(US)" or "(UK)" qualifier.
	Country string
	// Invalid lists the tags that name a provider without one id of its
	// form, so the name states no identity that may be trusted.
	Invalid []string
}

var (
	idTag   = regexp.MustCompile(`(?i)[\[{](tmdb|tvdb|imdb|mb-release-group|mb-release)(?:id)?[-=]([^\]}]*)[\]}]`)
	number  = regexp.MustCompile(`^[1-9]\d{0,11}$`)
	imdbID  = regexp.MustCompile(`^tt\d{7,10}$`)
	mbID    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	country = regexp.MustCompile(`\(([A-Z]{2})\)`)
	// Two capitals in parentheses are a country only when they are one of
	// these; "(HD)" and "(TV)" are not.
	countries = map[string]string{"UK": "GB"}
)

func init() {
	for _, c := range strings.Fields("US GB AU CA NZ IE JP KR CN IN DE FR ES IT SE NO DK FI NL BE BR MX RU ZA IL TR") {
		countries[c] = c
	}
}

// ReadIdentity reads the identity tags and country qualifier of a name:
// {tmdb-1}, {tvdb-1}, {imdb-tt0000001}, [mb-release-uuid] and
// [mb-release-group-uuid], in braces or brackets, with "-" or "=" and an
// optional "id" after the provider.
func ReadIdentity(name string) Identity {
	var id Identity
	tags := idTag.FindAllStringSubmatch(name, -1)
	seen, bad := map[string]string{}, map[string]bool{}
	for _, m := range tags {
		ns, v := strings.ToLower(m[1]), strings.ToLower(strings.TrimSpace(m[2]))
		ok := false
		switch ns {
		case "tmdb", "tvdb":
			ok = number.MatchString(v)
		case "imdb":
			ok = imdbID.MatchString(v)
		default:
			ok = mbID.MatchString(v)
		}
		// Two ids of one provider in one name state neither.
		if prev, twice := seen[ns]; !ok || twice && prev != v {
			bad[ns] = true
		}
		seen[ns] = v
	}
	for _, m := range tags {
		if bad[strings.ToLower(m[1])] {
			id.Invalid = append(id.Invalid, m[0])
		}
	}
	if len(id.Invalid) == 0 {
		id.TMDB, _ = strconv.ParseInt(seen["tmdb"], 10, 64)
		id.TVDB, _ = strconv.ParseInt(seen["tvdb"], 10, 64)
		id.IMDB, id.Release, id.ReleaseGroup = seen["imdb"], seen["mb-release"], seen["mb-release-group"]
	}
	for _, m := range country.FindAllStringSubmatch(name, -1) {
		if c, ok := countries[m[1]]; ok {
			id.Country = c
		}
	}
	return id
}

// EpisodeYear is the premiere year an episode file's name gives before its
// season and episode marker, where the show's name and year stand. A year
// after the marker is an air date or part of the episode's title.
func EpisodeYear(name string) *int {
	name = stripExt(name)
	for _, re := range []*regexp.Regexp{sxe, words, nxn} {
		if loc := re.FindStringIndex(name); loc != nil {
			_, year := Title(name[:loc[0]])
			return year
		}
	}
	return nil
}

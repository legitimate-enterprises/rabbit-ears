package account

import (
	"slices"
	"time"
)

// MovieRatings is the MPAA scale, least to most restrictive audience.
var MovieRatings = []string{"G", "PG", "PG-13", "R", "NC-17"}

// TVRatings is the TV Parental Guidelines scale in the same order.
var TVRatings = []string{"TV-Y", "TV-Y7", "TV-G", "TV-PG", "TV-14", "TV-MA"}

// AtOrBelow returns the ratings of scale up to and including ceiling. An
// unknown or empty ceiling allows nothing.
func AtOrBelow(scale []string, ceiling string) []string {
	i := slices.Index(scale, ceiling)
	return scale[:i+1]
}

// Age is whole years from birthday to now, both read as calendar dates.
func Age(birthday, now time.Time) int {
	y := now.Year() - birthday.Year()
	if now.Month() < birthday.Month() || now.Month() == birthday.Month() && now.Day() < birthday.Day() {
		y--
	}
	return max(y, 0)
}

// SuggestCeilings maps an age onto both scales. NC-17 is never suggested.
func SuggestCeilings(age int) (movie, tv string) {
	switch {
	case age < 7:
		return "G", "TV-Y"
	case age < 10:
		return "PG", "TV-Y7"
	case age < 12:
		return "PG", "TV-G"
	case age < 14:
		return "PG-13", "TV-PG"
	case age < 17:
		return "PG-13", "TV-14"
	default:
		return "R", "TV-MA"
	}
}

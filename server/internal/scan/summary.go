package scan

import (
	"fmt"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// Summary is what one library scan did. Seasons are structure, not content,
// so they are tracked but never reported.
type Summary struct {
	Added, Updated, Removed map[string]int // by item kind; Updated counts changed files by the kind they belong to
	Checked                 int            // media files seen on disk
	Missing                 int            // the library's files not seen on disk
	ProbeFailed             int
	LookupFailed            int
	leaf                    string  // the kind a file stands for: movie, episode, song or video
	created                 []int64 // movies and songs made in this scan
	// moved are the paths this scan found a known file moved to; loose
	// are the files it could file under no title.
	moved map[string]bool
	loose []string
	// partial is whether a library folder could not be walked, so what
	// the scan did not see may still be there.
	partial bool
}

func newSummary(leaf string) *Summary {
	return &Summary{Added: map[string]int{}, Updated: map[string]int{}, Removed: map[string]int{}, leaf: leaf, moved: map[string]bool{}}
}

func (s *Summary) added() int {
	n := 0
	for _, v := range s.Added {
		n += v
	}
	return n
}

// changed reports whether the scan added, updated or removed anything.
func (s *Summary) changed() bool {
	for _, m := range []map[string]int{s.Added, s.Updated, s.Removed} {
		for _, n := range m {
			if n > 0 {
				return true
			}
		}
	}
	return false
}

func plural(n int, kind string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", kind)
	}
	return fmt.Sprintf("%d %ss", n, kind)
}

// kinds renders counts as "2 shows, 6 episodes", "1 album, 9 songs" or
// "1 channel, 4 videos".
func kinds(m map[string]int) string {
	var parts []string
	for _, k := range []string{item.Movie, item.Show, item.Episode, item.Artist, item.Album, item.Song, item.Channel, item.Video} {
		if m[k] > 0 {
			parts = append(parts, plural(m[k], k))
		}
	}
	return strings.Join(parts, ", ")
}

// String is the line stored on the task run: the changes by kind, or "No
// changes; N checked", then any missing files and probe and lookup failures.
func (s *Summary) String() string {
	var parts []string
	for _, c := range []struct {
		m    map[string]int
		verb string
	}{{s.Added, "added"}, {s.Updated, "updated"}, {s.Removed, "removed"}} {
		if t := kinds(c.m); t != "" {
			parts = append(parts, t+" "+c.verb)
		}
	}
	if len(parts) == 0 {
		parts = []string{"No changes; " + plural(s.Checked, s.leaf) + " checked"}
	}
	out := strings.Join(parts, "; ")
	if s.Missing > 0 {
		out += "; " + plural(s.Missing, "file") + " missing"
	}
	if s.ProbeFailed > 0 {
		out += "; " + plural(s.ProbeFailed, "file") + " failed to probe"
	}
	if s.LookupFailed > 0 {
		out += "; " + plural(s.LookupFailed, "metadata lookup") + " failed"
	}
	return out
}

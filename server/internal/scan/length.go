package scan

import "sort"

var lengthBuckets = []int{22, 30, 44, 60}

// TypicalLength is a show's typical episode length: the mode of the episode
// runtimes (minutes) rounded to 5, coerced to 22, 30, 44 or 60 so broadcast
// shows with commercials and streaming shows without share one scale. The
// caller leaves specials out. Zero means no data.
func TypicalLength(runtimes []int) int {
	counts := map[int]int{}
	for _, r := range runtimes {
		if r > 0 {
			counts[(r+2)/5*5]++
		}
	}
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return 0
	}
	sort.Ints(keys) // ascending, so a tie keeps the shorter length
	mode := keys[0]
	for _, k := range keys {
		if counts[k] > counts[mode] {
			mode = k
		}
	}
	best := lengthBuckets[0]
	for _, b := range lengthBuckets {
		if abs(b-mode) < abs(best-mode) {
			best = b
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

package scan

import "testing"

func TestTypicalLength(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want int
	}{
		{"hour drama with commercials", []int{43, 44, 42, 45, 44, 60}, 44},
		{"streaming hour", []int{58, 61, 59, 62, 55}, 60},
		{"sitcom", []int{21, 22, 22, 23, 24}, 22},
		{"half hour", []int{28, 30, 31, 29}, 30},
		{"mode beats mean", []int{22, 22, 22, 90}, 22},
		{"zeros ignored", []int{0, 0, 47, 46}, 44},
		{"empty", nil, 0},
		{"tie goes shorter", []int{22, 22, 60, 60}, 22},
	}
	for _, c := range cases {
		if got := TypicalLength(c.in); got != c.want {
			t.Errorf("%s: %v = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

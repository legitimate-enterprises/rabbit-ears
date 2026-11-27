package playlist_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
)

// The default-alphabet vectors of rocicorp/fractional-indexing's
// src/test.js at 648d2e6; "" is a null bound. Clients port the same
// vectors, so both sides make identical keys.
func TestKeyBetweenUpstreamVectors(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{"", "", "a0"},
		{"", "a0", "Zz"},
		{"", "Zz", "Zy"},
		{"a0", "", "a1"},
		{"a1", "", "a2"},
		{"a0", "a1", "a0V"},
		{"a1", "a2", "a1V"},
		{"a0V", "a1", "a0l"},
		{"Zz", "a0", "ZzV"},
		{"Zz", "a1", "a0"},
		{"", "Y00", "Xzzz"},
		{"bzz", "", "c000"},
		{"a0", "a0V", "a0G"},
		{"a0", "a0G", "a08"},
		{"b125", "b129", "b127"},
		{"a0", "a1V", "a1"},
		{"Zz", "a01", "a0"},
		{"", "a0V", "a0"},
		{"", "b999", "b99"},
		{"", "A000000000000000000000000001", "A000000000000000000000000000V"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzy", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzz", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzzV"},
		{"a1", "a0", "a0V"},
		{"az", "b00", "azV"},
	} {
		got, err := playlist.KeyBetween(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("KeyBetween(%q, %q) = %q, %v; want %q", c.a, c.b, got, err, c.want)
		}
	}
}

// Upstream throws on these; a key outside 0-9A-Za-z, which upstream reads
// as digit 0, is refused too.
func TestKeyBetweenRefusesInvalidKeys(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{"", "A00000000000000000000000000"},
		{"a00", ""},
		{"a00", "a1"},
		{"0", "1"},
		{"a0", "a0"},
		{"a0", "a!"},
		{"a0", "a0x!"},
		{"a0 ", ""},
		{"a", ""},
	} {
		if got, err := playlist.KeyBetween(c.a, c.b); err == nil {
			t.Errorf("KeyBetween(%q, %q) = %q, want an error", c.a, c.b, got)
		}
	}
	for _, k := range []string{"", "a!", "a0é", "a00", "b0", "A00000000000000000000000000"} {
		if playlist.ValidKey(k) == nil {
			t.Errorf("ValidKey(%q) passed", k)
		}
	}
	for _, k := range []string{"a0", "Zz", "a0V", "b00", "A000000000000000000000000000V"} {
		if err := playlist.ValidKey(k); err != nil {
			t.Errorf("ValidKey(%q): %v", k, err)
		}
	}
}

// Taken from upstream run with its default alphabet; upstream's own
// generateNKeysBetween vectors are base 10.
func TestNKeysBetween(t *testing.T) {
	for _, c := range []struct {
		a, b string
		n    int
		want string
	}{
		{"", "", 5, "a0 a1 a2 a3 a4"},
		{"a0", "a1", 5, "a08 a0G a0V a0d a0l"},
		{"", "a0", 5, "Zv Zw Zx Zy Zz"},
		{"a0", "", 5, "a1 a2 a3 a4 a5"},
		{"az", "", 3, "b00 b01 b02"},
		{"a5", "a0", 3, "a2 a1 a0V"},
		{"a0", "a1", 20, "a02 a04 a08 a0A a0C a0G a0I a0K a0O a0S a0V a0X a0Z a0d a0h a0l a0n a0p a0t a0x"},
		{"a0", "a1", 0, ""},
	} {
		got, err := playlist.NKeysBetween(c.a, c.b, c.n)
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("NKeysBetween(%q, %q, %d) = %v, %v; want %q", c.a, c.b, c.n, got, err, c.want)
		}
	}
	for _, c := range [][2]string{{"", ""}, {"a0", ""}, {"", "a1"}, {"a0", "a5"}} {
		if _, err := playlist.NKeysBetween(c[0], c[1], -1); err == nil {
			t.Errorf("NKeysBetween(%q, %q, -1) passed", c[0], c[1])
		}
	}
}

// As upstream's testOrdering, on the default alphabet: keys made at random
// places stay between their neighbours and sort bytewise, as SQLite's BINARY
// collation sorts them.
func TestKeysSortBytewise(t *testing.T) {
	seed := int64(1)
	rnd := func() float64 {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		return float64(seed) / 0x7fffffff
	}
	var list []string
	for range 1000 {
		pos := int(rnd() * float64(len(list)+1))
		var a, b string
		if pos > 0 {
			a = list[pos-1]
		}
		if pos < len(list) {
			b = list[pos]
		}
		k, err := playlist.KeyBetween(a, b)
		if err != nil || (a != "" && a >= k) || (b != "" && k >= b) {
			t.Fatalf("KeyBetween(%q, %q) = %q, %v", a, b, k, err)
		}
		list = slices.Insert(list, pos, k)
	}
	if !slices.IsSorted(list) {
		t.Fatal("keys out of order")
	}
}

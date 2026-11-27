package playlist

// An entry's rank is a fractional-indexing key: a port of
// rocicorp/fractional-indexing (CC0) at 648d2e6 with its default alphabets,
// base-62 digits behind an A-Z/a-z head, so keys sort bytewise. Clients make
// the same keys; "" stands for a missing bound.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	rankHeads  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// smallestInteger is the lowest integer part, which no key may be.
	smallestInteger = "A00000000000000000000000000"
)

func digit(c byte) int { return strings.IndexByte(rankDigits, c) }

// from is s[n:], empty past its end.
func from(s string, n int) string { return s[min(n, len(s)):] }

// midpoint is a fraction between a and b ("" for none above); a < b, and
// neither ends in a zero.
func midpoint(a, b string) (string, error) {
	if b != "" && a >= b {
		return "", fmt.Errorf("%s >= %s", a, b)
	}
	if strings.HasSuffix(a, "0") || strings.HasSuffix(b, "0") {
		return "", errors.New("trailing zero")
	}
	if b != "" {
		n := 0
		for n < len(b) && (n < len(a) && a[n] == b[n] || n >= len(a) && b[n] == '0') {
			n++
		}
		if n > 0 {
			m, err := midpoint(from(a, n), from(b, n))
			return b[:n] + m, err
		}
	}
	digitA, digitB := 0, len(rankDigits)
	if a != "" {
		digitA = digit(a[0])
	}
	if b != "" {
		digitB = digit(b[0])
	}
	if digitB-digitA > 1 {
		return string(rankDigits[(digitA+digitB+1)/2]), nil
	}
	if len(b) > 1 {
		return b[:1], nil
	}
	m, err := midpoint(from(a, 1), "")
	return string(rankDigits[digitA]) + m, err
}

func integerLength(head byte) (int, error) {
	i := strings.IndexByte(rankHeads, head)
	if i < 0 {
		return 0, fmt.Errorf("invalid order key head: %c", head)
	}
	half := len(rankHeads) / 2
	if i < half {
		return half - i + 1, nil
	}
	return i - half + 2, nil
}

func integerPart(key string) (string, error) {
	n, err := integerLength(key[0])
	if err != nil {
		return "", err
	}
	if n > len(key) {
		return "", fmt.Errorf("invalid order key: %s", key)
	}
	return key[:n], nil
}

// ValidKey refuses what is not a key: also, unlike upstream, any character
// outside 0-9A-Za-z.
func ValidKey(key string) error {
	if key == "" || key == smallestInteger || strings.ContainsFunc(key, func(r rune) bool { return !strings.ContainsRune(rankDigits, r) }) {
		return fmt.Errorf("invalid order key: %s", key)
	}
	i, err := integerPart(key)
	if err != nil {
		return err
	}
	if strings.HasSuffix(from(key, len(i)), "0") {
		return fmt.Errorf("invalid order key: %s", key)
	}
	return nil
}

// step moves integer part x one up or down, false past the end of the range.
func step(x string, up bool) (string, bool) {
	fill, edge, carry := byte('0'), byte('z'), 1
	if !up {
		fill, edge, carry = 'z', '0', -1
	}
	trailing := ""
	for i := len(x) - 1; i >= 1; i-- {
		if x[i] == edge {
			trailing = string(fill) + trailing
			continue
		}
		return x[:i] + string(rankDigits[digit(x[i])+carry]) + trailing, true
	}
	hi := strings.IndexByte(rankHeads, x[0]) + carry
	if hi < 0 || hi == len(rankHeads) {
		return "", false
	}
	h := rankHeads[hi]
	was, _ := integerLength(x[0])
	now, _ := integerLength(h)
	switch {
	case now > was:
		return string(h) + trailing + string(fill), true
	case now < was:
		return string(h) + trailing[1:], true
	}
	return string(h) + trailing, true
}

// KeyBetween is a key that sorts between keys a and b, either "" for no
// bound; they may come in either order.
func KeyBetween(a, b string) (string, error) {
	for _, k := range []string{a, b} {
		if k != "" {
			if err := ValidKey(k); err != nil {
				return "", err
			}
		}
	}
	if a != "" && b != "" && a > b {
		a, b = b, a
	}
	if a == "" && b == "" {
		return "a0", nil
	}
	if a == "" {
		ib, _ := integerPart(b)
		if ib == smallestInteger {
			m, err := midpoint("", from(b, len(ib)))
			return ib + m, err
		}
		if ib < b {
			return ib, nil
		}
		if res, ok := step(ib, false); ok {
			return res, nil
		}
		return "", errors.New("cannot decrement any more")
	}
	ia, _ := integerPart(a)
	fa := from(a, len(ia))
	if b == "" {
		if i, ok := step(ia, true); ok {
			return i, nil
		}
		m, err := midpoint(fa, "")
		return ia + m, err
	}
	if a == b {
		return "", fmt.Errorf("%s >= %s", a, b)
	}
	ib, _ := integerPart(b)
	if ia == ib {
		m, err := midpoint(fa, from(b, len(ib)))
		return ia + m, err
	}
	i, ok := step(ia, true)
	if !ok {
		return "", errors.New("cannot increment any more")
	}
	if i < b {
		return i, nil
	}
	m, err := midpoint(fa, "")
	return ia + m, err
}

// NKeysBetween is n keys in order between a and b, as KeyBetween bounds
// them: consecutive integers beside a missing bound, else short keys
// between.
func NKeysBetween(a, b string, n int) ([]string, error) {
	switch {
	case n < 0:
		return nil, fmt.Errorf("n must be >= 0: %d", n)
	case n == 0:
		return []string{}, nil
	case n == 1:
		k, err := KeyBetween(a, b)
		return []string{k}, err
	}
	if b == "" || a == "" {
		out := make([]string, 0, n)
		c := ""
		for range n {
			var err error
			if b == "" {
				c, err = KeyBetween(or(c, a), b)
			} else {
				c, err = KeyBetween(a, or(c, b))
			}
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		if b != "" {
			slices.Reverse(out)
		}
		return out, nil
	}
	mid := n / 2
	c, err := KeyBetween(a, b)
	if err != nil {
		return nil, err
	}
	lo, err := NKeysBetween(a, c, mid)
	if err != nil {
		return nil, err
	}
	hi, err := NKeysBetween(c, b, n-mid-1)
	return append(append(lo, c), hi...), err
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

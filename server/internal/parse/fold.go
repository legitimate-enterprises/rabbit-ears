package parse

import (
	"strings"
	"unicode"

	unorm "golang.org/x/text/unicode/norm"
)

func lettersAndDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// Norm composes s first, since a combining accent is neither letter nor
// digit and would otherwise be dropped from its letter.
func Norm(s string) string { return lettersAndDigits(unorm.NFC.String(s)) }

// Fold is Norm without accents, which decomposing drops, reading "&" as "and".
func Fold(s string) string {
	return lettersAndDigits(unorm.NFD.String(strings.ReplaceAll(s, "&", " and ")))
}

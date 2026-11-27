package httpapi_test

import (
	"fmt"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// itoa writes an id (canonical UUID) or an integer as a path segment.
func itoa(v any) string { return fmt.Sprint(v) }

// ordered is a pair of ids lower first, as SQLite orders BLOBs.
func ordered(a, b uid.ID) (uid.ID, uid.ID) {
	if b.Compare(a) < 0 {
		return b, a
	}
	return a, b
}

package httpapi

import (
	"testing"
	"time"
)

// SetBoundsWait sets how long a copied stream's playlist waits for its
// keyframes, until t ends.
func SetBoundsWait(t testing.TB, d time.Duration) {
	old := boundsWait
	boundsWait = d
	t.Cleanup(func() { boundsWait = old })
}

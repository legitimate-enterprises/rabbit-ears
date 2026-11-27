package db

import (
	"io/fs"
	"testing"
)

// UseMigrations makes set the migrations for the rest of t.
func UseMigrations(t *testing.T, set fs.FS) {
	was := migrationFS
	migrationFS = set
	t.Cleanup(func() { migrationFS = was })
}

package atomicfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/atomicfile"
)

func TestWriteReplacesTheFileWhole(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(p, strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "new" { //nolint:gosec // test temp path
		t.Errorf("file = %q, %v; want new", b, err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("%d files left in the folder, want 1", len(es))
	}
}

func TestAFailedWriteLeavesTheFileAndNoTemporary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := atomicfile.Write(p, iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "old" { //nolint:gosec // test temp path
		t.Errorf("file = %q, want old", b)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("%d files left in the folder, want 1", len(es))
	}
}

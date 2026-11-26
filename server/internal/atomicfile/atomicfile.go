// Package atomicfile replaces files whole, so a reader never sees half of
// one.
package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Write replaces p with what r holds, through a hidden temporary beside p
// so the rename stays on one filesystem. A failed write leaves p as it was.
func Write(p string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := io.Copy(tmp, r)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

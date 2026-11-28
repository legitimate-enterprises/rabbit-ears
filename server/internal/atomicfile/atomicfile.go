// Package atomicfile replaces files whole, so a reader never sees half of
// one.
package atomicfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Hashed stores b in dir, named by its content hash with ext, and returns
// its path; a file of that name is already b. A name never holds other
// bytes, so whoever caches a file by its name never needs to look again.
func Hashed(dir, ext string, b []byte) (string, error) {
	h := sha256.Sum256(b)
	dest := filepath.Join(dir, hex.EncodeToString(h[:8])+ext)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return dest, Write(dest, bytes.NewReader(b))
}

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

// Package uid is the row id: a UUIDv7 stored as a 16-byte BLOB and written as
// its canonical lowercase string. A nullable id is a *ID (nil is NULL).
package uid

import (
	"bytes"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type ID [16]byte

var errZero = errors.New("uid: zero id")

// New mints a UUIDv7. Ids from one process strictly increase: uuid.NewV7
// keeps a sequence under a mutex.
func New() ID { return ID(uuid.Must(uuid.NewV7())) }

// Parse accepts only the canonical form the API's Id pattern allows:
// lowercase hex, dashes at 8/13/18/23, version 7, variant 10xx.
func Parse(s string) (ID, error) {
	var id ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return id, fmt.Errorf("uid: malformed id %q", s)
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if strings.ToLower(h) != h {
		return id, fmt.Errorf("uid: malformed id %q", s)
	}
	if _, err := hex.Decode(id[:], []byte(h)); err != nil {
		return ID{}, fmt.Errorf("uid: malformed id %q", s)
	}
	if id[6]>>4 != 7 || id[8]>>6 != 2 {
		return ID{}, fmt.Errorf("uid: not a UUIDv7 %q", s)
	}
	return id, nil
}

func (id ID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], id[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], id[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], id[8:10])
	b[23] = '-'
	hex.Encode(b[24:], id[10:])
	return string(b[:])
}

func (id ID) IsZero() bool { return id == ID{} }

// Compare orders bytewise, as SQLite orders BLOBs.
func (id ID) Compare(o ID) int { return bytes.Compare(id[:], o[:]) }

func (id ID) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, errZero
	}
	return id[:], nil
}

// Scan accepts only a 16-byte BLOB; scan a nullable column into a *ID.
func (id *ID) Scan(src any) error {
	b, ok := src.([]byte)
	if !ok || len(b) != 16 {
		return fmt.Errorf("uid: cannot scan %T (%v) into an id", src, src)
	}
	copy(id[:], b)
	return nil
}

func (id ID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, errZero
	}
	return []byte(id.String()), nil
}

func (id *ID) UnmarshalText(b []byte) error {
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*id = p
	return nil
}

// N is a fixture id for tests: 00000000-0000-7000-8000-<n as 12 hex digits>.
// N ids sort before every New id.
func N(n int) ID {
	if n < 0 || n >= 1<<48 {
		panic(fmt.Sprintf("uid.N(%d) out of range", n))
	}
	id := ID{6: 0x70, 8: 0x80}
	for i := 15; i >= 10; i-- {
		id[i] = byte(n & 0xff)
		n >>= 8
	}
	return id
}

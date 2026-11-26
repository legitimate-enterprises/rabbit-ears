// Package gravatar keeps a copy of each address's Gravatar picture on disk,
// so clients are served the picture without contacting gravatar.com.
package gravatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/atomicfile"
)

// Cache fetches pictures from Base and keeps them in Dir, asking again
// once a copy, or the record that there is none, is older than Refresh.
type Cache struct {
	Dir     string
	Base    string
	Client  *http.Client
	Refresh time.Duration
	Now     func() time.Time
	// locks holds a *sync.Mutex per hash, so one address is asked once at
	// a time while others are asked alongside it. Entries stay: there is
	// one per address ever asked, which is one per account.
	locks sync.Map
}

// lock takes the hash's lock and returns its unlock.
func (c *Cache) lock(h string) func() {
	m, _ := c.locks.LoadOrStore(h, new(sync.Mutex))
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// New returns a Cache over dir asking gravatar.com daily.
func New(dir string) *Cache {
	return &Cache{Dir: dir, Base: "https://gravatar.com/avatar", Client: &http.Client{Timeout: 5 * time.Second},
		Refresh: 24 * time.Hour, Now: time.Now}
}

// Hash is Gravatar's name for an address: SHA-256 of it trimmed and
// lowercased.
func Hash(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// Picture is the path of the address's picture, "" when it has none. A
// copy past Refresh is fetched again; when that fails the old copy, or
// its absence, stands until the next period, so an outage costs one
// request a period.
func (c *Cache) Picture(ctx context.Context, email string) (string, error) {
	h := Hash(email)
	defer c.lock(h)()
	pic, none := filepath.Join(c.Dir, h), filepath.Join(c.Dir, h+".none")
	have := exists(pic)
	if c.fresh(pic) || c.fresh(none) {
		return found(pic, have), nil
	}
	if err := os.MkdirAll(c.Dir, 0o750); err != nil {
		return "", err
	}
	body, status, err := c.fetch(ctx, h)
	switch {
	case err == nil && status == http.StatusOK:
		return pic, errors.Join(c.write(pic, body), remove(none))
	case err == nil && status == http.StatusNotFound:
		return "", errors.Join(c.write(none, nil), remove(pic))
	case have:
		return pic, c.stamp(pic)
	}
	return "", c.write(none, nil)
}

// Forget removes the copy of the address's picture, or the record that
// there is none.
func (c *Cache) Forget(email string) error {
	h := Hash(email)
	defer c.lock(h)()
	pic := filepath.Join(c.Dir, h)
	return errors.Join(remove(pic), remove(pic+".none"))
}

func found(pic string, have bool) string {
	if have {
		return pic
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// fresh reports a file written within the refresh period.
func (c *Cache) fresh(p string) bool {
	st, err := os.Stat(p)
	return err == nil && c.Now().Sub(st.ModTime()) < c.Refresh
}

func (c *Cache) fetch(ctx context.Context, hash string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/"+hash+"?d=404&s=256", http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

// stamp dates p now by the cache's clock, which fresh reads it against.
func (c *Cache) stamp(p string) error {
	now := c.Now()
	return os.Chtimes(p, now, now)
}

// write replaces p whole and stamps it.
func (c *Cache) write(p string, b []byte) error {
	if err := atomicfile.Write(p, bytes.NewReader(b)); err != nil {
		return err
	}
	return c.stamp(p)
}

func remove(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

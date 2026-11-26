package gravatar_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/gravatar"
)

// fake is gravatar.com for one address: its picture, else 404 as d=404
// asks; down answers 500.
func fake(t *testing.T, email string, calls *int32, down *atomic.Bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(email))
	known := "/avatar/" + hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		switch {
		case down.Load():
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Query().Get("d") != "404":
			t.Errorf("asked without d=404: %s", r.URL)
		case r.URL.Path == known:
			_, _ = w.Write([]byte("\x89PNG picture"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPictureIsFetchedOnceAndRefreshed(t *testing.T) {
	var calls int32
	var down atomic.Bool
	srv := fake(t, "ann@example.test", &calls, &down)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	c := gravatar.New(t.TempDir())
	c.Base, c.Client, c.Now = srv.URL+"/avatar", srv.Client(), func() time.Time { return now }
	ctx := context.Background()

	path, err := c.Picture(ctx, "  Ann@Example.TEST ")
	if err != nil || path == "" {
		t.Fatalf("%q %v", path, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "\x89PNG picture" { //nolint:gosec // the cache's own file
		t.Fatalf("%q", b)
	}
	if p, err := c.Picture(ctx, "ann@example.test"); p != path || err != nil || calls != 1 {
		t.Fatalf("a fresh copy is served without asking: %q %v, %d calls", p, err, calls)
	}
	if p, err := c.Picture(ctx, "bob@example.test"); p != "" || err != nil || calls != 2 {
		t.Fatalf("no picture: %q %v", p, err)
	}
	if p, _ := c.Picture(ctx, "bob@example.test"); p != "" || calls != 2 {
		t.Fatalf("no picture is remembered too, %d calls", calls)
	}

	// Past the refresh period it asks again; an outage keeps the old copy
	// until the next period.
	now = now.Add(c.Refresh + time.Minute)
	down.Store(true)
	if p, err := c.Picture(ctx, "ann@example.test"); p != path || err != nil || calls != 3 {
		t.Fatalf("outage: %q %v, %d calls", p, err, calls)
	}
	if p, _ := c.Picture(ctx, "ann@example.test"); p != path || calls != 3 {
		t.Fatalf("an outage waits a period too, %d calls", calls)
	}
	now = now.Add(c.Refresh + time.Minute)
	down.Store(false)
	if p, _ := c.Picture(ctx, "bob@example.test"); p != "" || calls != 4 {
		t.Fatalf("%d calls", calls)
	}
}

// held is gravatar.com answering 404 to every address, each request
// reported on arrived and held until release closes.
func held(t *testing.T, calls *int32) (srv *httptest.Server, arrived chan string, release chan struct{}) {
	t.Helper()
	arrived, release = make(chan string, 4), make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		arrived <- r.URL.Path
		<-release
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, arrived, release
}

func TestDifferentAddressesFetchAtOnceAndOneAddressOnce(t *testing.T) {
	var calls int32
	srv, arrived, release := held(t, &calls)
	c := gravatar.New(t.TempDir())
	c.Base, c.Client = srv.URL+"/avatar", srv.Client()
	ctx := context.Background()

	done := make(chan error, 3)
	for _, email := range []string{"ann@example.test", "ann@example.test", "bob@example.test"} {
		go func() {
			_, err := c.Picture(ctx, email)
			done <- err
		}()
	}
	paths := map[string]bool{}
	for len(paths) < 2 {
		select {
		case p := <-arrived:
			if paths[p] {
				t.Fatalf("%s asked twice at once", p)
			}
			paths[p] = true
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("one address's fetch waits on another's: %v", paths)
		}
	}
	select {
	case p := <-arrived:
		t.Errorf("%s asked while its first fetch is in flight", p)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("%d calls, want one per address", n)
	}
}

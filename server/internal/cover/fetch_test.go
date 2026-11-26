package cover_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
)

func TestRefusedAddresses(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "127.9.9.9", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"fe80::1", "fc00::1", "100.64.0.1", "0.0.0.0", "::", "224.0.0.1", "255.255.255.255", "::ffff:10.0.0.1", "64:ff9b::a00:1"} {
		if !cover.Refused(net.ParseIP(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "151.101.1.69", "2606:4700::1111", "64:ff9b::808:808"} {
		if cover.Refused(net.ParseIP(s)) {
			t.Errorf("%s refused", s)
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !cover.Refused(n.IP) {
			t.Errorf("own address %s allowed", n.IP)
		}
	}
}

// The real guard refuses a link to this host however it is named,
// resolved at dial time.
func TestFetchRefusesLocalLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("img")) }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	f := cover.NewFetcher()
	for _, u := range []string{srv.URL, "http://localhost" + port, "http://[::1]" + port, "http://169.254.169.254/latest/meta-data"} {
		if _, err := f.Fetch(context.Background(), u); !errors.Is(err, cover.ErrRefused) {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"ftp://example.com/a.jpg", "file:///etc/passwd", "gopher://example.com", "//example.com/a.jpg", "not a url", "http:///a.jpg"} {
		if _, err := f.Fetch(context.Background(), u); !errors.Is(err, cover.ErrLink) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

// loopbackOnly lets a test reach its own server at 127.0.0.1, and refuses
// everything else the real guard refuses.
func loopbackOnly() *cover.Fetcher {
	f := cover.NewFetcher()
	f.Refuse = func(ip net.IP) bool { return !ip.Equal(net.IPv4(127, 0, 0, 1)) && cover.Refused(ip) }
	return f
}

func TestFetchFollowsOnlySafeRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/img", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("img")) })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/img", http.StatusFound) })
	mux.HandleFunc("/inside", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/admin", http.StatusFound)
	})
	mux.HandleFunc("/v6", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://[::1]:80/", http.StatusFound)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/missing", http.NotFound)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := loopbackOnly()
	if b, err := f.Fetch(context.Background(), srv.URL+"/ok"); err != nil || string(b) != "img" {
		t.Fatalf("%q %v", b, err)
	}
	for path, want := range map[string]error{"/inside": cover.ErrRefused, "/v6": cover.ErrRefused, "/file": cover.ErrLink,
		"/loop": cover.ErrLink, "/missing": cover.ErrLink} {
		if _, err := f.Fetch(context.Background(), srv.URL+path); !errors.Is(err, want) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestFetchCapsSizeAndTime(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, 2048)) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := loopbackOnly()
	f.Max, f.Client.Timeout = 1024, 200*time.Millisecond
	if _, err := f.Fetch(context.Background(), srv.URL+"/big"); !errors.Is(err, cover.ErrTooBig) {
		t.Errorf("big: %v", err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/slow"); !errors.Is(err, cover.ErrLink) {
		t.Errorf("slow: %v", err)
	}
}

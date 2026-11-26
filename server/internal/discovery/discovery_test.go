package discovery

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// serve answers on an ephemeral loopback port and returns a client socket.
func serve(t *testing.T, name string) (net.PacketConn, net.Addr) {
	t.Helper()
	return serveTo(t, name, func(net.IP) bool { return true })
}

func serveTo(t *testing.T, name string, local func(net.IP) bool) (net.PacketConn, net.Addr) {
	t.Helper()
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Serve(ctx, srv, 18477, func() string { return name }, local) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, srv.LocalAddr()
}

func ask(t *testing.T, c net.PacketConn, to net.Addr, payload string) (string, bool) {
	t.Helper()
	if _, err := c.WriteTo([]byte(payload), to); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	b := make([]byte, 2048)
	n, from, err := c.ReadFrom(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	if from.String() != to.String() {
		t.Fatalf("reply from %v", from)
	}
	return string(b[:n]), true
}

func TestRepliesToTheDiscoverDatagram(t *testing.T) {
	c, to := serve(t, "Den")
	got, ok := ask(t, c, to, Request)
	if !ok || got != "RABBIT-EARS SERVER 1 18477 Den" {
		t.Fatalf("%q %v", got, ok)
	}
	if len(got) > 4*len(Request) {
		t.Fatalf("reply of %d bytes amplifies a %d-byte request", len(got), len(Request))
	}
}

func TestIgnoresAnythingElse(t *testing.T) {
	c, to := serve(t, "Den")
	for _, p := range []string{"", "RABBIT-EARS DISCOVER 2", "RABBIT-EARS DISCOVER 1\n", "rabbit-ears discover 1", "RABBIT-EARS DISCOVER 1" + strings.Repeat("x", 1000)} {
		if got, ok := ask(t, c, to, p); ok {
			t.Errorf("%q answered %q", p, got)
		}
	}
	if _, ok := ask(t, c, to, Request); !ok {
		t.Fatal("stopped answering after junk")
	}
}

func TestLongNamesAreCutAndLinesKeptSingle(t *testing.T) {
	c, to := serve(t, "Living room\r\n"+strings.Repeat("é", 100))
	got, ok := ask(t, c, to, Request)
	if !ok || len(got) > 4*len(Request) || strings.ContainsAny(got, "\r\n") || !strings.HasPrefix(got, "RABBIT-EARS SERVER 1 18477 Living room  é") {
		t.Fatalf("%q", got)
	}
	if !strings.HasSuffix(got, "é") {
		t.Fatalf("cut inside a character: %q", got)
	}
}

func TestNoNameBeforeFirstRun(t *testing.T) {
	c, to := serve(t, "")
	if got, _ := ask(t, c, to, Request); got != "RABBIT-EARS SERVER 1 18477" {
		t.Fatalf("%q", got)
	}
}

// A reply to a sender off the LAN could only reflect a spoofed request.
func TestIgnoresSendersOffTheLAN(t *testing.T) {
	asked := make(chan net.IP, 1)
	c, to := serveTo(t, "Den", func(ip net.IP) bool { asked <- ip; return false })
	if got, ok := ask(t, c, to, Request); ok {
		t.Fatalf("answered %q", got)
	}
	if ip := <-asked; !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("asked about %v", ip)
	}
}

// Discovery is unauthenticated and per datagram, so the name and the
// interface list behind each reply are read at most once per interval.
func TestCachedReadsOncePerInterval(t *testing.T) {
	reads := 0
	now := time.Unix(0, 0)
	get := cached(time.Minute, func() int { reads++; return reads }, func() time.Time { return now })
	for range 100 {
		if v := get(); v != 1 {
			t.Fatalf("got %d", v)
		}
	}
	now = now.Add(time.Minute)
	if v := get(); v != 2 || reads != 2 {
		t.Fatalf("got %d after %d reads", v, reads)
	}
}

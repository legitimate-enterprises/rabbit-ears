// Package discovery lets clients find the server: a UDP responder for those
// without Bonjour, such as Roku, which broadcast on the HTTP port, and a
// Bonjour advertisement for the rest.
package discovery

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
)

// Request is the whole payload of a discovery datagram.
const Request = "RABBIT-EARS DISCOVER 1"

// maxReply caps a reply at four times the request, bounding what a spoofed
// request could reflect; long server names are cut to fit.
const maxReply = 4 * len(Request)

// Reply is the one line answered to Request: "RABBIT-EARS SERVER 1", the
// HTTP port and the server name, separated by single spaces. The name runs
// to the end, has no line breaks and is absent before first run.
func Reply(port int, name string) string {
	out := "RABBIT-EARS SERVER 1 " + strconv.Itoa(port)
	name = strings.NewReplacer("\r", " ", "\n", " ").Replace(name)
	if name == "" {
		return out
	}
	out += " " + name
	for len(out) > maxReply {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return out
}

// Serve answers Request from a sender local reports on the server's own
// network, until ctx ends, then closes conn. Any other datagram is dropped
// without a reply, so the responder reflects nothing off the LAN. name is
// read per request; wrap it in Cached to bound its cost.
func Serve(ctx context.Context, conn net.PacketConn, port int, name func() string, local func(net.IP) bool) {
	go func() { <-ctx.Done(); _ = conn.Close() }()
	// Larger than Request, so a longer datagram reads longer and never
	// passes for it when the buffer truncates it.
	buf := make([]byte, 2*len(Request))
	for {
		n, from, err := conn.ReadFrom(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			logstore.Log("discovery").Warn("Discovery read failed", "err", err)
			continue
		}
		if string(buf[:n]) != Request {
			continue
		}
		if a, ok := from.(*net.UDPAddr); !ok || !local(a.IP) {
			continue
		}
		if _, err := conn.WriteTo([]byte(Reply(port, name())), from); err != nil {
			logstore.Log("discovery").Warn("Discovery reply failed", "to", from.String(), "err", err)
		}
	}
}

// Cached returns f's value, calling f again only once ttl has passed since
// the last call. It is safe for concurrent use.
func Cached[T any](ttl time.Duration, f func() T) func() T {
	return cached(ttl, f, time.Now)
}

func cached[T any](ttl time.Duration, f func() T, now func() time.Time) func() T {
	var mu sync.Mutex
	var v T
	var at time.Time
	var ok bool
	return func() T {
		mu.Lock()
		defer mu.Unlock()
		if t := now(); !ok || t.Sub(at) >= ttl {
			v, at, ok = f(), t, true
		}
		return v
	}
}

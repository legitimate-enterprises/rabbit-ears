package cover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// Errors Fetch answers for a link it will not keep.
var (
	ErrLink    = errors.New("the link gave no image")
	ErrRefused = errors.New("the link is to a local or private address")
	ErrTooBig  = errors.New("the link's image is over 16 MiB")
)

// MaxFetch is the most Fetch reads, as much as an upload may hold.
const MaxFetch = 16 << 20

// Fetcher gets an image link once for a cover. Every connection it makes,
// redirects included, is checked against Refuse after the name resolves,
// so neither a redirect nor a name that resolves inward reaches this host
// or its networks.
type Fetcher struct {
	Client *http.Client
	Refuse func(net.IP) bool
	Max    int64
}

// NewFetcher returns a Fetcher refusing what Refused does.
func NewFetcher() *Fetcher {
	f := &Fetcher{Refuse: Refused, Max: MaxFetch}
	dial := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if ip := net.ParseIP(host); err != nil || ip == nil || f.Refuse(ip) {
			return ErrRefused
		}
		return nil
	}}
	f.Client = &http.Client{
		Timeout: 10 * time.Second,
		// No proxy: a proxy would make the connection this guard checks.
		Transport: &http.Transport{DialContext: dial.DialContext, TLSHandshakeTimeout: 5 * time.Second,
			MaxResponseHeaderBytes: 64 << 10, DisableKeepAlives: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("%w: too many redirects", ErrLink)
			}
			return web(req.URL)
		},
	}
	return f
}

// web accepts an absolute http or https URL.
func web(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("%w: not an http or https link", ErrLink)
	}
	return nil
}

// Fetch reads the body raw links to, at most Max bytes.
func (f *Fetcher) Fetch(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: not an http or https link", ErrLink)
	}
	if err := web(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLink, err)
	}
	req.Header.Set("Accept", "image/*")
	resp, err := f.Client.Do(req)
	switch {
	case errors.Is(err, ErrRefused), errors.Is(err, ErrLink):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrLink, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: it answered %s", ErrLink, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, f.Max+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrLink, err)
	case int64(len(b)) > f.Max:
		return nil, ErrTooBig
	}
	return b, nil
}

// special lists ranges no image link is fetched from beyond those net.IP
// names: shared address space, "this network", IETF protocol assignments,
// benchmarking, reserved and NAT64, whose embedded address is checked.
var special = func() (out []*net.IPNet) {
	for _, c := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

var nat64 = func() *net.IPNet { _, n, _ := net.ParseCIDR("64:ff9b::/96"); return n }()

// Refused reports an address an image link may not reach: loopback,
// private, link-local, multicast, unspecified, special-purpose, or one of
// the server's own.
func Refused(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if nat64.Contains(ip) {
		return Refused(ip[12:])
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range special {
		if n.Contains(ip) {
			return true
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

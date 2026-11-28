package youtube

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/atomicfile"
)

// ErrNotFound means YouTube has no such channel, or no picture for it.
var ErrNotFound = errors.New("youtube: channel or avatar not found")

// Avatars fetches channels' pictures from their channel pages on Base,
// which need no API key, and keeps them in Dir by channel id.
type Avatars struct {
	Dir    string
	Base   string
	Client *http.Client
}

// NewAvatars returns Avatars over dir asking youtube.com.
func NewAvatars(dir string) *Avatars {
	return &Avatars{Dir: dir, Base: "https://www.youtube.com", Client: &http.Client{Timeout: 10 * time.Second}}
}

var (
	channelID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	ogImage   = regexp.MustCompile(`<meta[^>]*\sproperty=["']og:image["'][^>]*>`)
	content   = regexp.MustCompile(`\scontent=["']([^"']+)["']`)
	imageExt  = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}
)

// Fetch downloads the channel's avatar, its page's og:image, and returns
// the path it was stored at, named by its content.
func (a *Avatars) Fetch(ctx context.Context, id string) (string, error) {
	if !channelID.MatchString(id) {
		return "", ErrNotFound
	}
	page, _, err := a.get(ctx, a.Base+"/channel/"+id, 8<<20)
	if err != nil {
		return "", err
	}
	tag := ogImage.Find(page)
	m := content.FindSubmatch(tag)
	if m == nil {
		return "", ErrNotFound
	}
	src, err := url.Parse(html.UnescapeString(string(m[1])))
	if err != nil || src.Scheme != "https" && src.Scheme != "http" {
		return "", ErrNotFound
	}
	img, kind, err := a.get(ctx, src.String(), 5<<20)
	if err != nil {
		return "", err
	}
	ext, ok := imageExt[strings.TrimSpace(strings.Split(kind, ";")[0])]
	if !ok {
		return "", fmt.Errorf("youtube: avatar of %s is %q, not an image", id, kind)
	}
	return atomicfile.Hashed(a.Dir, ext, img)
}

// get reads at most limit bytes of u, with its content type.
func (a *Avatars) get(ctx context.Context, u string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15")
	req.Header.Set("Accept-Language", "en")
	// Skips the cookie consent page YouTube shows some regions in place of the channel.
	req.Header.Set("Cookie", "SOCS=CAI")
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, "", ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("youtube: %s answered %s", req.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, resp.Header.Get("Content-Type"), err
}

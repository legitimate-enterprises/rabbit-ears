package httpapi_test

import (
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

func TestAttributionIsPublicAndNamesTMDB(t *testing.T) {
	e := newEnv(t)
	var a api.Attribution
	e.expect(200, "GET", "/attribution", "", nil, &a)
	if a.Provider != "TMDB" || !strings.Contains(a.Text, "TMDB") || a.Url == "" {
		t.Errorf("%+v", a)
	}
}

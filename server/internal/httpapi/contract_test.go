package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Every operation of the spec is served under /api/v2 and none under
// /api/v1; TestSpecSecurityIsEnforced calls each under /api/v2.
func TestEveryOperationIsServedUnderV2Only(t *testing.T) {
	e := newEnv(t)
	if httpapi.BasePath != "/api/v2" {
		t.Fatalf("BasePath %q", httpapi.BasePath)
	}
	doc, err := api.GetSpec()
	must(t, err)
	router, err := gorillamux.NewRouter(doc)
	must(t, err)
	root := strings.TrimSuffix(e.url, httpapi.BasePath)
	n := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			n++
			concrete := path
			for _, p := range append(item.Parameters, op.Parameters...) {
				if p.Value.In != "path" {
					continue
				}
				v := "1"
				if s := p.Value.Schema.Value; s != nil && s.Format == "uuid" {
					v = uid.N(1).String()
				}
				concrete = strings.ReplaceAll(concrete, "{"+p.Value.Name+"}", v)
			}
			req, err := http.NewRequest(method, root+httpapi.BasePath+concrete, nil)
			must(t, err)
			if _, _, err := router.FindRoute(req); err != nil {
				t.Errorf("%s %s is not routed under %s: %v", method, path, httpapi.BasePath, err)
			}
			req, err = http.NewRequest(method, root+"/api/v1"+concrete, nil)
			must(t, err)
			res, err := http.DefaultClient.Do(req)
			must(t, err)
			_ = res.Body.Close()
			if res.StatusCode != 404 {
				t.Errorf("%s /api/v1%s = %d, want 404", method, path, res.StatusCode)
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d operations in the spec", n)
	}
}

// The v2 operations carry their security and validation at the boundary.
func TestV2OperationsGuarded(t *testing.T) {
	e := newEnv(t)
	e.expect(400, "POST", "/device-links", "", map[string]any{"deviceName": "TV"}, nil)
	e.expect(400, "POST", "/device-links", "", map[string]any{"deviceName": "TV", "platform": "toaster"}, nil)
	link := "/device-links/" + itoa(uid.N(1))
	e.expect(400, "GET", link, "", nil, nil)
	e.expect(400, "GET", link+"/qr", "", nil, nil)
	e.expect(401, "GET", "/link/7KQ4-MNP2", "", nil, nil)
	e.expect(400, "GET", "/link/7kq4mnp2", e.plain.token, nil, nil)
	e.expect(401, "POST", "/link/7KQ4-MNP2/approve", "", nil, nil)
	e.expect(401, "GET", "/household/device-links", "", nil, nil)
	e.expect(403, "GET", "/device-links", e.plain.token, nil, nil)
	e.expect(403, "DELETE", link, e.plain.token, nil, nil)
	e.expect(403, "GET", "/seek-previews", e.plain.token, nil, nil)
	e.expect(403, "GET", "/seek-previews/"+itoa(uid.N(1)), e.plain.token, nil, nil)
}

// v2 snapshot fields are optional: a v1 snapshot still decides, and a v2 one
// is accepted with its new fields checked at the boundary.
func TestV2SnapshotFields(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	path := "/items/" + itoa(m) + "/playability"
	e.expect(200, "POST", path, e.plain.token, snapshot(), nil)

	v2 := snapshot()
	v2["decoder"].(map[string]any)["segmentFormats"] = []string{"ts", "fmp4"}
	v2["network"].(map[string]any)["auto"] = true
	v2["throughput"] = map[string]any{"measuredBitrate": 9_000_000, "bufferMs": 12_000, "stallCount": 0, "rungHeight": 1080, "rungBitrate": 8_000_000}
	e.expect(200, "POST", path, e.plain.token, v2, nil)

	bad := snapshot()
	bad["decoder"].(map[string]any)["segmentFormats"] = []string{"mkv"}
	e.expect(400, "POST", path, e.plain.token, bad, nil)
	empty := snapshot()
	empty["decoder"].(map[string]any)["segmentFormats"] = []string{}
	e.expect(400, "POST", path, e.plain.token, empty, nil)
}

package httpapi_test

import (
	"testing"
)

// The v2 operations carry their security and validation at the boundary.
func TestV2OperationsGuarded(t *testing.T) {
	e := newEnv(t)
	e.expect(400, "POST", "/device-links", "", map[string]any{"deviceName": "TV"}, nil)
	e.expect(400, "POST", "/device-links", "", map[string]any{"deviceName": "TV", "platform": "toaster"}, nil)
	e.expect(400, "GET", "/device-links/1", "", nil, nil)
	e.expect(400, "GET", "/device-links/1/qr", "", nil, nil)
	e.expect(401, "GET", "/link/7KQ4-MNP2", "", nil, nil)
	e.expect(400, "GET", "/link/7kq4mnp2", e.plain.token, nil, nil)
	e.expect(401, "POST", "/link/7KQ4-MNP2/approve", "", nil, nil)
	e.expect(401, "GET", "/household/device-links", "", nil, nil)
	e.expect(403, "GET", "/device-links", e.plain.token, nil, nil)
	e.expect(403, "DELETE", "/device-links/1", e.plain.token, nil, nil)
	e.expect(403, "GET", "/seek-previews", e.plain.token, nil, nil)
	e.expect(403, "GET", "/seek-previews/1", e.plain.token, nil, nil)
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

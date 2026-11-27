package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/oapi-codegen/runtime"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestSpecValidates(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q", doc.OpenAPI)
	}
}

func TestEveryOperationHasAnID(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
			}
		}
	}
}

// Queued offline writes replay only through operations marked x-replayable;
// none of them may need admin powers or an upload body, and each is sent by
// a method SyncAction allows.
func TestReplayableOperations(t *testing.T) {
	want := []string{
		"putWatchState", "deleteWatchState", "putWatchStates", "putYouTubeChannelWatchState", "recordPlay",
		"createPlaylist", "replacePlaylist", "deletePlaylist", "addPlaylistEntry", "addPlaylistEntries", "replacePlaylistEntry",
		"removePlaylistEntry", "createPlaylistShare", "replacePlaylistShare", "deletePlaylistShare", "createPlaylistHouseholdShare",
		"replacePlaylistHouseholdShare", "deletePlaylistHouseholdShare",
		"createCollection", "replaceCollection", "deleteCollection", "addCollectionMovie", "removeCollectionMovie",
		"replaceCollectionMoviePosition", "resetCollectionOrder",
		"createStation", "replaceStation", "deleteStation", "rescheduleStation",
		"allowItem", "disallowItem", "setKidsContent", "clearKidsContent", "setKidsContents", "setYouTubeChannelForKids",
		"setProfilePreferences",
	}
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, m := range doc.Components.Schemas["SyncAction"].Value.Properties["method"].Value.Enum {
		methods = append(methods, m.(string))
	}
	var got []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Extensions["x-replayable"] != true {
				continue
			}
			// The embedded spec carries each operationId capitalized, as its Go name.
			got = append(got, strings.ToLower(op.OperationID[:1])+op.OperationID[1:])
			security := doc.Security
			if op.Security != nil {
				security = *op.Security
			}
			for _, req := range security {
				for _, scopes := range req {
					if slices.Contains(scopes, "admin") {
						t.Errorf("%s %s is replayable and admin-scoped", method, path)
					}
				}
			}
			if op.Extensions["x-upload"] == true {
				t.Errorf("%s %s is replayable and an upload", method, path)
			}
			if !slices.Contains(methods, method) {
				t.Errorf("%s %s is replayable by a method SyncAction does not allow", method, path)
			}
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("replayable:\n got %v\nwant %v", got, want)
	}
}

// The guard validates with these options before binding: an id that is not
// a canonical UUIDv7 fails in a path, a query and a body (400), and one that
// passes binds into uid.ID through UnmarshalText.
func TestIdsAreCanonicalUUIDv7(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	validate := func(method, target, body string) error {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, target, nil)
		} else {
			r = httptest.NewRequest(method, target, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		}
		route, params, err := router.FindRoute(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, target, err)
		}
		return openapi3filter.ValidateRequest(context.Background(), &openapi3filter.RequestValidationInput{
			Request: r, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, MultiError: true},
		})
	}
	good := uid.New().String()
	for _, id := range []string{good, uid.N(1).String()} {
		if err := validate("GET", "/api/v2/profiles/"+id, ""); err != nil {
			t.Errorf("path %s: %v", id, err)
		}
		if err := validate("GET", "/api/v2/items?libraryId="+id, ""); err != nil {
			t.Errorf("query %s: %v", id, err)
		}
		if err := validate("POST", "/api/v2/collections", `{"name":"x","id":"`+id+`"}`); err != nil {
			t.Errorf("body %s: %v", id, err)
		}
	}
	bad := []string{
		"1",
		strings.ToUpper(good),
		"6ba7b810-9dad-41d1-80b4-00c04fd430c8", // version 4
		"01936f0e-7c3a-7b2d-1e41-5a8c0d2f6b19", // variant 0xxx
		"01936f0e7c3a7b2d9e415a8c0d2f6b19",
		"{" + good + "}",
	}
	for _, id := range bad {
		if validate("GET", "/api/v2/profiles/"+id, "") == nil {
			t.Errorf("path %s passed", id)
		}
		if validate("GET", "/api/v2/items?libraryId="+id, "") == nil {
			t.Errorf("query %s passed", id)
		}
		if validate("POST", "/api/v2/collections", `{"name":"x","id":"`+id+`"}`) == nil {
			t.Errorf("body %s passed", id)
		}
	}
	if validate("POST", "/api/v2/collections", `{"name":"x","id":1}`) == nil {
		t.Error("integer id passed")
	}

	var path api.Id
	if err := runtime.BindStyledParameterWithOptions("simple", "profileId", good, &path, runtime.BindStyledParameterOptions{
		ParamLocation: runtime.ParamLocationPath, Required: true, Type: "string", Format: "uuid", ValueIsUnescaped: true,
	}); err != nil || path.String() != good {
		t.Errorf("path bound %v, %v", path, err)
	}
	var query *api.Id
	if err := runtime.BindQueryParameterWithOptions("form", true, false, "libraryId", url.Values{"libraryId": {good}}, &query,
		runtime.BindQueryParameterOptions{Type: "string", Format: "uuid"}); err != nil || query == nil || query.String() != good {
		t.Errorf("query bound %v, %v", query, err)
	}
}

// SyncScalar declares no type: a type list is OpenAPI 3.1's, and
// generators that read one type only could not decode the others. The
// validator holds a key to its length and refuses null, an untyped schema
// not being nullable; replaySyncActions refuses the other non-scalars.
func TestSyncScalarsAreUntyped(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	if typ := doc.Components.Schemas["SyncScalar"].Value.Type; typ != nil && len(*typ) > 0 {
		t.Errorf("SyncScalar is of type %v", *typ)
	}
	touched := doc.Components.Schemas["SyncTouched"].Value
	for _, key := range [][]any{{1.0}, {2.5, "tmdb"}, {true}, {map[string]any{}}, {[]any{1.0}}} {
		if err := touched.VisitJSON(map[string]any{"table": "item_genre", "key": key}); err != nil {
			t.Errorf("key %v: %v", key, err)
		}
	}
	for _, key := range [][]any{{}, {1.0, nil}, {1.0, 2.0, 3.0, 4.0}} {
		if err := touched.VisitJSON(map[string]any{"table": "item_genre", "key": key}); err == nil {
			t.Errorf("key %v passed", key)
		}
	}
}

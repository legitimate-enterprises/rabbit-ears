package api_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
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

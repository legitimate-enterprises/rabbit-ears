package httpapi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
)

// A movie's local frame is its art under Local only, served from disk with
// no download, and its tags are its metadata with no provider named;
// switching to Remote only shows the stored TMDB data and art at once.
func TestItemFollowsTheMetadataSettings(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, t.TempDir()
	lib := e.library("movies")
	items := e.svc.Items.Repo()
	m := &item.Item{LibraryID: lib, Kind: item.Movie, ParsedTitle: "lamplight"}
	must(t, items.Create(m))
	frame := filepath.Join(t.TempDir(), "frame.jpg")
	must(t, os.WriteFile(frame, []byte("frame"), 0o600))
	must(t, items.SetLocal(&item.Metadata{ItemID: m.ID, Title: "Lamplight", BackdropPath: frame}))
	pid := int64(603)
	must(t, items.SetMetadata(&item.Metadata{ItemID: m.ID, ProviderID: &pid, Status: item.Matched, Title: "Lamplight, as TMDB has it",
		Overview: "Told remotely.", BackdropPath: "/wide.jpg"}))

	must(t, e.svc.Server.SetSources(serverinfo.Default))
	var got api.Item
	e.expect(200, "GET", "/items/"+itoa(m.ID), e.plain.token, nil, &got)
	if md := got.Metadata; md == nil || *md.Title != "Lamplight" || md.Overview != nil || md.Provider != nil || md.ProviderId != nil ||
		md.Status != api.Unmatched {
		t.Fatalf("local only %+v", got.Metadata)
	}
	if *got.Metadata.BackdropPath != "frame.jpg" {
		t.Fatalf("a viewer sees no server folder: %q", *got.Metadata.BackdropPath)
	}
	if _, raw := e.do("GET", "/items/"+itoa(m.ID)+"/artwork/backdrop", e.plain.token, nil, nil); string(raw) != "frame" || images.calls != 0 {
		t.Fatalf("local backdrop %q after %d downloads", raw, images.calls)
	}

	must(t, e.svc.Server.SetSources(serverinfo.Sources{Source: serverinfo.Remote, Mode: serverinfo.Only}))
	e.expect(200, "GET", "/items/"+itoa(m.ID), e.plain.token, nil, &got)
	if md := got.Metadata; md == nil || *md.Title != "Lamplight, as TMDB has it" || *md.Overview != "Told remotely." ||
		*md.Provider != api.ItemMetadataProviderTmdb || *md.ProviderId != 603 || md.Status != api.Matched {
		t.Fatalf("remote only %+v", got.Metadata)
	}
	if *got.Metadata.BackdropPath != "/wide.jpg" {
		t.Fatalf("a TMDB path is served whole: %q", *got.Metadata.BackdropPath)
	}
	if _, raw := e.do("GET", "/items/"+itoa(m.ID)+"/artwork/backdrop", e.plain.token, nil, nil); string(raw) != "w1280/wide.jpg" {
		t.Fatalf("remote backdrop %q", raw)
	}
}

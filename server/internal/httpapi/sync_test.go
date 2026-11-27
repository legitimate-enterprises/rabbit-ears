package httpapi_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// synced is an env whose snapshots are kept for the test.
func synced(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.svc.Sync.Dir = t.TempDir()
	return e
}

// syncGet answers path as token's device, whatever its status.
func (e *env) syncGet(path, token string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest("GET", e.url+path, nil)
	must(e.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	return res
}

// snapshot downloads token's household's snapshot and returns its bytes and
// replica_meta's version and household.
func (e *env) snapshot(token string) ([]byte, int64, uid.ID) {
	e.t.Helper()
	res := e.syncGet("/sync/snapshot?schema=1", token)
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	must(e.t, err)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/vnd.sqlite3" || res.ContentLength != int64(len(body)) {
		e.t.Fatalf("snapshot %d %q %d bytes of %d", res.StatusCode, res.Header.Get("Content-Type"), len(body), res.ContentLength)
	}
	p := filepath.Join(e.t.TempDir(), "replica.sqlite")
	must(e.t, os.WriteFile(p, body, 0o600))
	r, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
	must(e.t, err)
	defer func() { _ = r.Close() }()
	var v int64
	var h uid.ID
	must(e.t, r.QueryRow(`SELECT version, household_id FROM replica_meta`).Scan(&v, &h))
	return body, v, h
}

func TestSyncAnswersOnlyASignedInDevice(t *testing.T) {
	e := synced(t)
	locked, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "Tablet", "ios")
	must(t, err)
	e.expect(204, "POST", "/me/lock", locked, nil, nil)
	for _, path := range []string{"/sync/changes?schema=1", "/sync/snapshot?schema=1", "/sync/version?after=0"} {
		raw := e.expect(401, "GET", path, "", nil, nil)
		if errorCode(t, raw) != "unauthorized" {
			t.Errorf("%s without a token: %s", path, raw)
		}
		if raw := e.expect(403, "GET", path, locked, nil, nil); errorCode(t, raw) != "device_locked" {
			t.Errorf("%s locked: %s", path, raw)
		}
	}
}

func TestSyncValidatesParameters(t *testing.T) {
	e := synced(t)
	tok := e.plain.token
	_, v, _ := e.snapshot(tok)
	for _, path := range []string{
		"/sync/changes", "/sync/changes?schema=0", "/sync/changes?schema=1&since=-1", "/sync/changes?schema=1&limit=0",
		"/sync/changes?schema=1&limit=5001", fmt.Sprintf("/sync/changes?schema=1&since=%d&cursor=nonsense", v),
		"/sync/snapshot", "/sync/snapshot?schema=2", "/sync/version", "/sync/version?after=-1",
	} {
		if raw := e.expect(400, "GET", path, tok, nil, nil); errorCode(t, raw) != "bad_request" {
			t.Errorf("%s: %s", path, raw)
		}
	}
	var c api.SyncChanges
	e.expect(200, "GET", "/sync/changes?schema=2&since=0", tok, nil, &c)
	want := api.SyncRanges{ApiMin: "0.21.0", ApiMax: "1.2.3", SchemaMin: 1, SchemaMax: 1}
	if c.Mode != api.Unsupported || c.Ranges != want || c.Version != nil || c.Tables != nil {
		t.Errorf("a schema the server lacks: %+v", c)
	}
}

// A client snapshots first, catches up from the snapshot's version with
// diffs of its own household's rows, and downloads the same file while
// nothing it holds changed.
func TestSyncSnapshotThenChanges(t *testing.T) {
	e := synced(t)
	tok := e.plain.token
	var c api.SyncChanges
	e.expect(200, "GET", "/sync/changes?schema=1", tok, nil, &c)
	if c.Mode != api.Snapshot || c.Ranges.SchemaMax != 1 {
		t.Fatalf("with nothing held: %+v", c)
	}
	first, v, h := e.snapshot(tok)
	if h != e.plain.household.ID {
		t.Errorf("snapshot of household %s", h)
	}
	since := fmt.Sprintf("/sync/changes?schema=1&since=%d", v)
	e.expect(200, "GET", since, tok, nil, &c)
	if c.Mode != api.Diff || c.Version == nil || *c.Version != v || c.Cursor != nil || len(*c.Tables)+len(*c.Tombstones) != 0 {
		t.Fatalf("caught up: %+v", c)
	}
	e.renameProfile(e.admin, "Elsewhere")
	if again, w, _ := e.snapshot(tok); w != v || !bytes.Equal(again, first) {
		t.Errorf("another household's change rebuilt the snapshot at %d", w)
	}
	e.renameProfile(e.plain, "Renamed")
	e.expect(200, "GET", since, tok, nil, &c)
	if c.Mode != api.Diff || len(*c.Tables) != 2 || (*c.Tables)[0].Name != "account" || (*c.Tables)[1].Name != "account_card" {
		t.Fatalf("after a rename: %+v", c)
	}
	acc := (*c.Tables)[0]
	if acc.Columns[0] != "id" || acc.Rows[0][0] != e.plain.account.ID.String() || acc.Rows[0][slices.Index(acc.Columns, "name")] != "Renamed" {
		t.Errorf("account row %v %v", acc.Columns, acc.Rows)
	}
	if _, w, _ := e.snapshot(tok); w <= v {
		t.Errorf("a change the household sees left the snapshot at %d", w)
	}
}

func (e *env) renameProfile(u user, name string) {
	e.t.Helper()
	u.account.Name = name
	must(e.t, e.svc.Accounts.Repo().Update(u.account, nil))
}

func TestSyncResponsesNameTheArtworkGeneration(t *testing.T) {
	e := synced(t)
	e.svc.EventHold = 10 * time.Millisecond
	for _, path := range []string{"/sync/changes?schema=1", "/sync/snapshot?schema=1", "/sync/version?after=0", "/sync/changes"} {
		res := e.syncGet(path, e.plain.token)
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.Header.Get("Artwork-Generation") == "" {
			t.Errorf("%s (%d) has no Artwork-Generation", path, res.StatusCode)
		}
	}
}

// wait asks the doorbell after v, timing the answer.
func (e *env) wait(token string, v int64) (bool, time.Duration) {
	e.t.Helper()
	var b api.SyncBell
	began := time.Now()
	e.expect(200, "GET", fmt.Sprintf("/sync/version?after=%d", v), token, nil, &b)
	if b.Ranges.SchemaMin != 1 {
		e.t.Errorf("ranges %+v", b.Ranges)
	}
	return b.Changed, time.Since(began)
}

func TestSyncDoorbell(t *testing.T) {
	e := synced(t)
	tok := e.plain.token
	_, v, _ := e.snapshot(tok)
	e.svc.EventHold = 300 * time.Millisecond
	if changed, took := e.wait(tok, v); changed || took < 300*time.Millisecond {
		t.Errorf("nothing new: changed %v after %v", changed, took)
	}
	if changed, took := e.wait(tok, 0); !changed || took > 200*time.Millisecond {
		t.Errorf("a snapshot to take: changed %v after %v", changed, took)
	}

	e.svc.EventHold = 5 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.renameProfile(e.admin, "Elsewhere")
		time.Sleep(900 * time.Millisecond)
		e.renameProfile(e.plain, "Renamed")
	}()
	if changed, took := e.wait(tok, v); !changed || took < time.Second || took > 3*time.Second {
		t.Errorf("a change the household sees after another's: changed %v after %v", changed, took)
	}
}

func TestSyncDoorbellRefusesASignedOutDevice(t *testing.T) {
	e := synced(t)
	tok := e.plain.token
	_, v, _ := e.snapshot(tok)
	e.svc.EventHold = 5 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		must(t, e.svc.Devices.Repo().Revoke(e.plain.deviceID, time.Now()))
	}()
	began := time.Now()
	e.expect(401, "GET", fmt.Sprintf("/sync/version?after=%d", v), tok, nil, nil)
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("answered after %v", took)
	}
}

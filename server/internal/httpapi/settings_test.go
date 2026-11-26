package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/sysinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
)

type fakeSampler struct{ path string }

func (f *fakeSampler) Sample(path string) (sysinfo.Facts, error) {
	f.path = path
	return sysinfo.Facts{CPUModel: "Apple M4", Cores: 10, CPUPercent: 12.5, MemoryUsed: 6 << 30, MemoryTotal: 16 << 30,
		DiskFree: 100 << 30, DiskTotal: 500 << 30, VideoToolbox: true}, nil
}

func TestSettingsReadAndEdit(t *testing.T) {
	e := newEnv(t)
	s := &fakeSampler{}
	e.svc.Sampler, e.svc.DataDir = s, "/data/rabbit-ears"
	e.svc.Port, e.svc.DatabasePath = 8477, "/data/rabbit-ears/rabbit-ears.db"
	e.svc.TMDB.Client = tmdb.New(tmdb.Config{Key: "k"})
	root := t.TempDir()
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: root})
	must(t, os.MkdirAll(filepath.Join(root, "9"), 0o750))
	must(t, os.WriteFile(filepath.Join(root, "9", "0.m4s"), make([]byte, 500), 0o600))
	must(t, e.svc.Server.Save("Den"))

	var got api.Settings
	e.expect(200, "GET", "/settings", e.admin.token, nil, &got)
	want := api.Settings{ServerName: "Den", DataDir: "/data/rabbit-ears", Port: 8477, DatabasePath: "/data/rabbit-ears/rabbit-ears.db", MaxTranscodes: 3, TranscodeCacheCapBytes: 20_000_000_000, ReadAheadSeconds: 300,
		Storage:  api.StorageFacts{TranscodeCacheBytes: 500, TranscodeCacheCapBytes: 20_000_000_000, DiskFreeBytes: 100 << 30, DiskTotalBytes: 500 << 30},
		Hardware: api.HardwareFacts{CpuModel: "Apple M4", Cores: 10, MemoryBytes: 16 << 30, VideoToolbox: true},
		Metadata: api.MetadataSources{TmdbConfigured: true}}
	if got.Gravatar == nil || *got.Gravatar || got.Metadata.Source == nil || got.Metadata.Mode == nil {
		t.Fatalf("gravatar starts off, and the metadata settings are always present: %+v", got)
	}
	got.Gravatar, got.Metadata.Source, got.Metadata.Mode = nil, nil, nil
	if got != want || s.path != "/data/rabbit-ears" {
		t.Fatalf("%+v sampled %q", got, s.path)
	}
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic", "tasksPaused": true}, &got)
	if got.ServerName != "Attic" || !got.TasksPaused {
		t.Fatalf("%+v", got)
	}
	var h api.Health
	e.expect(200, "GET", "/health", "", nil, &h)
	if h.ServerName == nil || *h.ServerName != "Attic" {
		t.Fatalf("%+v", h)
	}
	e.expect(400, "PUT", "/settings", e.admin.token, map[string]any{"serverName": ""}, nil)
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic"}, &got)
	if !got.TasksPaused {
		t.Fatal("absent keeps the stored value: still paused")
	}
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic", "tasksPaused": false}, &got)
	if got.TasksPaused {
		t.Fatal("false resumes")
	}
}

// fakeTMDB answers like TMDB: a key in valid passes, any other is a 401. It
// returns the key the last request carried.
func fakeTMDB(t *testing.T, valid ...string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("api_key")
		mu.Lock()
		last = key
		mu.Unlock()
		for _, v := range valid {
			if key == v {
				_, _ = w.Write([]byte(`{"success":true,"results":[]}`))
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status_code":7,"success":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() string { mu.Lock(); defer mu.Unlock(); return last }
}

func TestSettingsTMDBKey(t *testing.T) {
	e := newEnv(t)
	e.svc.Sampler = &fakeSampler{}
	srv, last := fakeTMDB(t, "good", "env")
	client := tmdb.New(tmdb.Config{BaseURL: srv.URL})
	e.svc.TMDB.Client, e.svc.TMDB.FallbackKey = client, "env"
	must(t, e.svc.Server.Save("Den"))
	must(t, e.svc.TMDB.Load())
	stored := func() string {
		i, err := e.svc.Server.Get()
		must(t, err)
		return i.TMDBKey
	}
	used := func() string {
		_, _ = e.svc.TMDB.Client.SearchMovie(context.Background(), "x", nil)
		return last()
	}
	if used() != "env" {
		t.Fatal("with no key saved, the environment's key is used")
	}

	var got api.Settings
	put := e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "tmdbKey": "good"}, &got)
	if !got.Metadata.TmdbConfigured || stored() != "good" || used() != "good" {
		t.Fatalf("a saved key is stored and used from the next request: %+v %q %q", got, stored(), last())
	}
	get := e.expect(200, "GET", "/settings", e.admin.token, nil, nil)
	for _, raw := range [][]byte{put, get} {
		if bytes.Contains(raw, []byte("good")) {
			t.Fatalf("the key is served: %s", raw)
		}
	}

	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic"}, &got)
	if stored() != "good" || !got.Metadata.TmdbConfigured {
		t.Fatal("an absent key leaves the key as it is")
	}

	raw := e.expect(400, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Loft", "tmdbKey": "wrong"}, nil)
	var bad api.Error
	must(t, json.Unmarshal(raw, &bad))
	if bad.Fields == nil || len(*bad.Fields) != 1 || (*bad.Fields)[0].Field != "tmdbKey" || bytes.Contains(raw, []byte("wrong")) {
		t.Fatalf("a rejected key is a field error on tmdbKey: %s", raw)
	}
	if i, _ := e.svc.Server.Get(); stored() != "good" || used() != "good" || i.Name != "Attic" {
		t.Fatalf("a rejected key changes nothing: %+v", i)
	}

	e.svc.TMDB.Client = tmdb.New(tmdb.Config{Key: "env", BaseURL: srv.URL})
	must(t, e.svc.TMDB.Load())
	if used() != "good" {
		t.Fatal("after a restart the saved key wins over the environment")
	}

	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic", "tmdbKey": ""}, &got)
	if stored() != "" || used() != "env" || !got.Metadata.TmdbConfigured {
		t.Fatalf("an empty key removes the saved one, back to the environment's: %q %q", stored(), last())
	}
	e.svc.TMDB.FallbackKey = ""
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic", "tmdbKey": ""}, &got)
	if got.Metadata.TmdbConfigured {
		t.Fatal("with neither, TMDB is not configured")
	}

	srv.Close()
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Attic", "tmdbKey": "unchecked"}, &got)
	if stored() != "unchecked" || !got.Metadata.TmdbConfigured {
		t.Fatal("while TMDB cannot be reached the key is stored unchecked")
	}
}

// The metadata source and mode read back as set, each kept when absent;
// an unknown value is refused; saving or removing the TMDB key leaves
// them as they are.
func TestSettingsMetadataSources(t *testing.T) {
	e := newEnv(t)
	e.svc.Sampler = &fakeSampler{}
	srv, _ := fakeTMDB(t, "good")
	e.svc.TMDB.Client = tmdb.New(tmdb.Config{BaseURL: srv.URL})
	must(t, e.svc.Server.SetSources(serverinfo.Default))
	var got api.Settings
	e.expect(200, "GET", "/settings", e.admin.token, nil, &got)
	if *got.Metadata.Source != api.Local || *got.Metadata.Mode != api.Only {
		t.Fatalf("starts Local only: %+v", got.Metadata)
	}
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "metadataSource": "remote"}, &got)
	if *got.Metadata.Source != api.Remote || *got.Metadata.Mode != api.Only {
		t.Fatalf("source alone: %+v", got.Metadata)
	}
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "metadataMode": "preferred"}, &got)
	if *got.Metadata.Source != api.Remote || *got.Metadata.Mode != api.Preferred {
		t.Fatalf("mode alone: %+v", got.Metadata)
	}
	e.expect(400, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "metadataSource": "web"}, nil)
	e.expect(400, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "metadataMode": "mostly"}, nil)
	for _, key := range []string{"good", ""} {
		e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "tmdbKey": key}, &got)
		if *got.Metadata.Source != api.Remote || *got.Metadata.Mode != api.Preferred {
			t.Fatalf("the key %q changed the settings: %+v", key, got.Metadata)
		}
	}
	e.expect(403, "PUT", "/settings", e.plain.token, map[string]any{"serverName": "Den", "metadataSource": "local"}, nil)
}

// The read-ahead reads back as set, is kept when absent, and is refused
// outside 30 seconds to 10 minutes.
func TestSettingsReadAhead(t *testing.T) {
	e := newEnv(t)
	e.svc.Sampler = &fakeSampler{}
	var got api.Settings
	if e.expect(200, "GET", "/settings", e.admin.token, nil, &got); got.ReadAheadSeconds != 300 {
		t.Fatalf("starts at five minutes: %d", got.ReadAheadSeconds)
	}
	if e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "readAheadSeconds": 120}, &got); got.ReadAheadSeconds != 120 {
		t.Fatalf("%d", got.ReadAheadSeconds)
	}
	if e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den"}, &got); got.ReadAheadSeconds != 120 {
		t.Fatalf("absent keeps the setting: %d", got.ReadAheadSeconds)
	}
	for _, bad := range []int{29, 601} {
		e.expect(400, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "readAheadSeconds": bad}, nil)
	}
	if e.expect(200, "GET", "/settings", e.admin.token, nil, &got); got.ReadAheadSeconds != 120 {
		t.Fatalf("a refused value changed the setting: %d", got.ReadAheadSeconds)
	}
	e.expect(403, "PUT", "/settings", e.plain.token, map[string]any{"serverName": "Den", "readAheadSeconds": 60}, nil)
}

// The transcode limit and cache cap read back as set, reach the engine, are
// kept when absent, and are refused outside 1 to 8 and 1 GB to 1 TB.
func TestSettingsTranscodeLimits(t *testing.T) {
	e := newEnv(t)
	e.svc.Sampler = &fakeSampler{}
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir()})
	var got api.Settings
	if e.expect(200, "GET", "/settings", e.admin.token, nil, &got); got.MaxTranscodes != 3 || got.TranscodeCacheCapBytes != 20_000_000_000 {
		t.Fatalf("starts at the engine's defaults: %+v", got)
	}
	e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den", "maxTranscodes": 5, "transcodeCacheCapBytes": 2_000_000_000}, &got)
	if got.MaxTranscodes != 5 || got.TranscodeCacheCapBytes != 2_000_000_000 || got.Storage.TranscodeCacheCapBytes != 2_000_000_000 {
		t.Fatalf("%+v", got)
	}
	if lim := e.svc.Playback.Engine.Limits(); lim != (transcode.Limits{CacheCap: 2_000_000_000, MaxVideo: 5}) {
		t.Fatalf("the engine runs by the saved limits: %+v", lim)
	}
	var st api.ServerStats
	if e.expect(200, "GET", "/stats", e.admin.token, nil, &st); st.MaxTranscodes != 5 {
		t.Fatalf("the dashboard shows the limit in force: %d", st.MaxTranscodes)
	}
	if e.expect(200, "PUT", "/settings", e.admin.token, map[string]any{"serverName": "Den"}, &got); got.MaxTranscodes != 5 || got.TranscodeCacheCapBytes != 2_000_000_000 {
		t.Fatalf("absent keeps the settings: %+v", got)
	}
	for _, bad := range []map[string]any{{"maxTranscodes": 0}, {"maxTranscodes": 9},
		{"transcodeCacheCapBytes": 999_999_999}, {"transcodeCacheCapBytes": 1_000_000_000_001}} {
		bad["serverName"] = "Den"
		e.expect(400, "PUT", "/settings", e.admin.token, bad, nil)
	}
	if e.expect(200, "GET", "/settings", e.admin.token, nil, &got); got.MaxTranscodes != 5 || got.TranscodeCacheCapBytes != 2_000_000_000 {
		t.Fatalf("a refused value changed the settings: %+v", got)
	}
	e.expect(403, "PUT", "/settings", e.plain.token, map[string]any{"serverName": "Den", "maxTranscodes": 1}, nil)
}

func TestStats(t *testing.T) {
	e := newEnv(t)
	e.svc.Sampler, e.svc.DataDir = &fakeSampler{}, t.TempDir()
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir(), MaxVideo: 3})
	must(t, e.svc.Playback.Engine.Acquire(41))
	var st api.ServerStats
	e.expect(200, "GET", "/stats", e.admin.token, nil, &st)
	want := api.ServerStats{CpuPercent: 12.5, MemoryUsedBytes: 6 << 30, MemoryTotalBytes: 16 << 30, StorageUsedBytes: 400 << 30,
		StorageTotalBytes: 500 << 30, ActiveTranscodes: 1, MaxTranscodes: 3}
	if st != want {
		t.Fatalf("%+v", st)
	}
}

func TestDangerZone(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: root})
	m, f := e.playable()
	live := &session.Session{DeviceID: e.plain.deviceID, AccountID: e.plain.account.ID, ItemID: m, MediaFileID: f}
	must(t, e.svc.Sessions.Repo().Create(live, []*session.PlanEntry{{Verdict: session.DirectPlay, VideoAction: session.ActionCopy,
		AudioAction: session.ActionCopy, SubtitleAction: session.SubtitleNone}}))
	for _, d := range []string{itoa(live.ID), "999"} {
		must(t, os.MkdirAll(filepath.Join(root, d), 0o750))
		must(t, os.WriteFile(filepath.Join(root, d, "0.m4s"), []byte("x"), 0o600))
	}
	e.expect(204, "DELETE", "/transcode-cache", e.admin.token, nil, nil)
	if _, err := os.Stat(filepath.Join(root, itoa(live.ID), "0.m4s")); err != nil {
		t.Fatal("an active session keeps its segments")
	}
	if _, err := os.Stat(filepath.Join(root, "999")); !os.IsNotExist(err) {
		t.Fatal("idle segments are cleared")
	}

	later := time.Now().Add(7 * 24 * time.Hour)
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: m, Status: item.Matched, RetryAt: &later}))
	day := 3600
	var mt api.Task
	e.expect(201, "POST", "/tasks", e.admin.token, map[string]any{"name": "Refresh metadata", "kind": "metadata", "intervalSeconds": day}, &mt)
	e.expect(202, "POST", "/metadata/refresh", e.admin.token, nil, nil)
	if due, _ := e.svc.Items.Repo().DueForRetry(time.Now(), 10); len(due) != 1 {
		t.Fatal("every item's metadata becomes due")
	}
	e.expect(200, "GET", "/tasks/"+itoa(mt.Id), e.admin.token, nil, &mt)
	if mt.RunRequestedAt == nil {
		t.Fatalf("the refresh task is queued: %+v", mt)
	}
}

func TestTaskRunNowAndRunDetail(t *testing.T) {
	e := newEnv(t)
	var tk api.Task
	e.expect(201, "POST", "/tasks", e.admin.token, map[string]any{"name": "Scan Films", "kind": "scan:1", "enabled": false}, &tk)
	e.expect(202, "POST", "/tasks/"+itoa(tk.Id)+"/runs", e.admin.token, nil, &tk)
	if tk.RunRequestedAt == nil {
		t.Fatalf("%+v", tk)
	}
	e.expect(404, "POST", "/tasks/999/runs", e.admin.token, nil, nil)
	run, err := e.svc.Tasks.Repo().StartRun(tk.Id, time.Now())
	must(t, err)
	var got api.TaskRun
	e.expect(200, "GET", "/tasks/"+itoa(tk.Id)+"/runs/"+itoa(run.ID), e.admin.token, nil, &got)
	if got.Id != run.ID || got.Status != api.TaskRunStatusRunning {
		t.Fatalf("%+v", got)
	}
	var other api.Task
	e.expect(201, "POST", "/tasks", e.admin.token, map[string]any{"name": "Other", "kind": "x"}, &other)
	e.expect(404, "GET", "/tasks/"+itoa(other.Id)+"/runs/"+itoa(run.ID), e.admin.token, nil, nil)
}

func TestLibraryFoldersListAndGet(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	var a, b api.Folder
	e.expect(201, "POST", "/libraries/"+itoa(lib)+"/folders", e.admin.token, map[string]any{"path": "/media/b"}, &b)
	e.expect(201, "POST", "/libraries/"+itoa(lib)+"/folders", e.admin.token, map[string]any{"path": "/media/a"}, &a)
	var page api.FolderPage
	e.expect(200, "GET", "/libraries/"+itoa(lib)+"/folders", e.admin.token, nil, &page)
	if len(page.Items) != 2 || page.Items[0].Path != "/media/a" {
		t.Fatalf("%+v", page)
	}
	var got api.Folder
	e.expect(200, "GET", "/libraries/"+itoa(lib)+"/folders/"+itoa(a.Id), e.admin.token, nil, &got)
	if got != a {
		t.Fatalf("%+v", got)
	}
	e.expect(404, "GET", "/libraries/999/folders", e.admin.token, nil, nil)
	other := e.library("tv")
	e.expect(404, "GET", "/libraries/"+itoa(other)+"/folders/"+itoa(a.Id), e.admin.token, nil, nil)
}

func TestNewAdminOperationsAreAdminOnly(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/settings"}, {"PUT", "/settings"}, {"GET", "/stats"}, {"DELETE", "/transcode-cache"},
		{"POST", "/metadata/refresh"}, {"POST", "/tasks/1/runs"}, {"GET", "/tasks/1/runs/1"},
	} {
		e.expect(403, c.method, c.path, e.plain.token, map[string]any{"serverName": "x"}, nil)
		e.expect(401, c.method, c.path, "", map[string]any{"serverName": "x"}, nil)
	}
}

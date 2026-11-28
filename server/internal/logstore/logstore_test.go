package logstore_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func entry(at time.Time, level, source, msg string) *logstore.Entry {
	return &logstore.Entry{At: at, Level: level, Source: source, Message: msg}
}

func TestRepoFiltersAndOrder(t *testing.T) {
	r := logstore.New(dbtest.Open(t))
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cmd := "ffmpeg -i a.mkv"
	for i, e := range []*logstore.Entry{
		entry(t0, "info", "scan", "Scan finished: 3 movies added"),
		entry(t0.Add(time.Minute), "warn", "tmdb", "TMDB returned 429"),
		entry(t0.Add(2*time.Minute), "error", "transcode", "encoder exited"),
		entry(t0.Add(3*time.Minute), "info", "playback", "Session 7 transcode"),
	} {
		if i == 3 {
			e.SessionID, e.FFmpegCommand, e.TranscodeReason = ptr(uid.N(7)), cmd, "HEVC not supported"
		}
		if err := r.Insert(e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := r.List(logstore.Filter{})
	if err != nil || len(all) != 4 || all[0].Source != "playback" || all[3].Source != "scan" {
		t.Fatalf("newest first: %+v %v", all, err)
	}
	if d := all[0]; d.SessionID == nil || *d.SessionID != uid.N(7) || d.FFmpegCommand != cmd || d.TranscodeReason != "HEVC not supported" || !d.At.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("detail columns: %+v", d)
	}
	for name, c := range map[string]struct {
		f    logstore.Filter
		want int
	}{
		"min warn":    {logstore.Filter{MinLevel: "warn"}, 2},
		"min error":   {logstore.Filter{MinLevel: "error"}, 1},
		"source":      {logstore.Filter{Source: "scan"}, 1},
		"since":       {logstore.Filter{Since: ptr(t0.Add(time.Minute))}, 3},
		"until":       {logstore.Filter{Until: ptr(t0.Add(time.Minute))}, 2},
		"q any case":  {logstore.Filter{Q: "TMDB"}, 1},
		"q literal %": {logstore.Filter{Q: "%"}, 0},
		"limit":       {logstore.Filter{Limit: 2}, 2},
		"offset":      {logstore.Filter{Limit: 10, Offset: 3}, 1},
	} {
		got, err := r.List(c.f)
		if err != nil || len(got) != c.want {
			t.Errorf("%s: got %d want %d (%v)", name, len(got), c.want, err)
		}
	}
}

func TestPrune(t *testing.T) {
	r := logstore.New(dbtest.Open(t))
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := r.Insert(entry(now.Add(-time.Duration(i)*24*time.Hour), "info", "scan", "m")); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Prune(now.Add(-3*24*time.Hour+time.Hour), 100, 100); err != nil { // keeps days 0..2
		t.Fatal(err)
	}
	if got, _ := r.List(logstore.Filter{}); len(got) != 3 {
		t.Fatalf("age: %d", len(got))
	}
	if err := r.Prune(time.Time{}, 2, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.List(logstore.Filter{}); len(got) != 2 || !got[0].At.Equal(now) {
		t.Fatalf("row cap keeps the newest: %+v", got)
	}
}

type memSink struct {
	mu sync.Mutex
	es []*logstore.Entry
}

func (m *memSink) Write(e *logstore.Entry) { m.mu.Lock(); m.es = append(m.es, e); m.mu.Unlock() }

func TestHandlerMapsAttrsAndRedacts(t *testing.T) {
	var stderr bytes.Buffer
	sink := &memSink{}
	l := slog.New(logstore.NewHandler(slog.NewTextHandler(&stderr, nil), sink, slog.LevelInfo)).With("source", "playback")
	l.Info("decided", "session_id", uid.N(9), "item_id", uid.N(4).String(), "ffmpeg_command", "ffmpeg -i x", "transcode_reason", "audio codec", "device", "Roku",
		"api_key", "SECRET1", "pin", "1234", "err", "GET https://x/3/movie?api_key=SECRET2&query=a: refused", "auth", "Bearer SECRET3",
		"callback", "/callback?code=SECRET4&state=s", "form", "client_secret=SECRET6&code_verifier=SECRET7")
	l.Debug("dropped")
	if len(sink.es) != 1 {
		t.Fatalf("entries %d", len(sink.es))
	}
	e := sink.es[0]
	if e.Level != "info" || e.Source != "playback" || *e.SessionID != uid.N(9) || *e.ItemID != uid.N(4) || e.FFmpegCommand != "ffmpeg -i x" || e.TranscodeReason != "audio codec" {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(e.Message, "decided") || !strings.Contains(e.Message, "device=Roku") {
		t.Errorf("message %q", e.Message)
	}
	for name, text := range map[string]string{"stored": e.Message, "stderr": stderr.String()} {
		for _, s := range []string{"SECRET1", "SECRET2", "SECRET3", "SECRET4", "SECRET6", "SECRET7", "1234"} {
			if strings.Contains(text, s) {
				t.Errorf("%s output leaks %s: %s", name, s, text)
			}
		}
	}
	if !strings.Contains(stderr.String(), "decided") {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestWriterPersistsAndCloseFlushes(t *testing.T) {
	r := logstore.New(dbtest.Open(t))
	w := logstore.NewWriter(r)
	l := slog.New(logstore.NewHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), w, slog.LevelInfo))
	l.Warn("one", "source", "scan")
	l.Error("two")
	w.Close()
	l.Info("after close is dropped, not a panic")
	got, err := r.List(logstore.Filter{})
	if err != nil || len(got) != 2 || got[1].Source != "scan" || got[0].Source != "server" || got[0].Level != "error" {
		t.Fatalf("%+v %v", got, err)
	}
}

func ptr[T any](v T) *T { return &v }

// Text a client chose, such as a device name, cannot start a line of its
// own in either log: control characters are escaped, never written.
func TestHandlerEscapesControlCharacters(t *testing.T) {
	var stderr bytes.Buffer
	sink := &memSink{}
	l := slog.New(logstore.NewHandler(slog.NewTextHandler(&stderr, nil), sink, slog.LevelInfo))
	l.Info("Approved device link for TV\nlevel=ERROR msg=forged", "reason", "x\r\nlevel=ERROR\x1b[2J")
	e := sink.es[0]
	for name, text := range map[string]string{"stored": e.Message, "stderr": strings.TrimSuffix(stderr.String(), "\n")} {
		if strings.ContainsAny(text, "\n\r\x1b") {
			t.Errorf("%s: %q", name, text)
		}
	}
	if !strings.Contains(e.Message, `TV\nlevel=ERROR`) {
		t.Errorf("stored %q", e.Message)
	}
}

func clientEntry(id uid.ID, at time.Time, platform, msg string) *logstore.Entry {
	return &logstore.Entry{ID: id, At: at, Level: "error", Source: logstore.ClientSource, Message: msg,
		Client: &logstore.Client{Platform: platform, AppVersion: "1.2.3", OSVersion: "iOS 27.1", Context: "home",
			Detail: "at a\nat b"}}
}

// A client's entry is stored once per id, however often it is sent, and
// lists with its own columns beside the server's entries.
func TestClientEntriesStoreOnceAndFilter(t *testing.T) {
	r := logstore.New(dbtest.Open(t))
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	must(t, r.Insert(entry(t0, "info", "scan", "Scan finished")))
	for range 2 {
		must(t, r.InsertClient(clientEntry(uid.N(1), t0.Add(time.Minute), "ios", "Home failed")))
	}
	must(t, r.InsertClient(clientEntry(uid.N(2), t0.Add(2*time.Minute), "web", "Details failed")))
	all, err := r.List(logstore.Filter{})
	if err != nil || len(all) != 3 || all[0].Client == nil || all[2].Client != nil {
		t.Fatalf("%+v %v", all, err)
	}
	c := all[1].Client
	if all[1].ID != uid.N(1) || c.Platform != "ios" || c.AppVersion != "1.2.3" || c.OSVersion != "iOS 27.1" || c.Context != "home" ||
		c.Detail != "at a\nat b" || c.DeviceID != nil || c.ProfileID != nil || c.DeviceName != "" {
		t.Fatalf("client columns: %+v", c)
	}
	for name, f := range map[string]struct {
		f    logstore.Filter
		want int
	}{
		"source client": {logstore.Filter{Source: logstore.ClientSource}, 2},
		"platform":      {logstore.Filter{Platform: "web"}, 1},
		"platform none": {logstore.Filter{Platform: "roku"}, 0},
		"server source": {logstore.Filter{Source: "scan", Platform: "web"}, 0},
	} {
		if got, err := r.List(f.f); err != nil || len(got) != f.want {
			t.Errorf("%s: got %d want %d (%v)", name, len(got), f.want, err)
		}
	}
}

// Clients' entries and the server's are capped apart, so a chatty client
// cannot push the server's own out of the window.
func TestPruneCapsClientEntriesApart(t *testing.T) {
	r := logstore.New(dbtest.Open(t))
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	must(t, r.Insert(entry(now.Add(-2*time.Hour), "error", "transcode", "old server error")))
	must(t, r.Insert(entry(now.Add(-time.Hour), "info", "scan", "server")))
	for i := range 5 {
		must(t, r.InsertClient(clientEntry(uid.N(i+1), now.Add(time.Duration(i)*time.Minute), "web", "client")))
	}
	must(t, r.InsertClient(clientEntry(uid.N(9), now.Add(-40*24*time.Hour), "web", "too old")))
	must(t, r.Prune(now.Add(-30*24*time.Hour), 2, 3))
	server, _ := r.List(logstore.Filter{Source: "transcode"})
	clients, _ := r.List(logstore.Filter{Source: logstore.ClientSource})
	if len(server) != 1 || len(clients) != 3 || clients[0].ID != uid.N(5) || clients[2].ID != uid.N(3) {
		t.Fatalf("server %d, clients %+v", len(server), clients)
	}
	must(t, r.Prune(time.Time{}, 1, 3))
	if all, _ := r.List(logstore.Filter{}); len(all) != 4 {
		t.Fatalf("server cap counts the server's only: %d", len(all))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

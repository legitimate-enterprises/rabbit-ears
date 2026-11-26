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
			e.SessionID, e.FFmpegCommand, e.TranscodeReason = ptr(int64(7)), cmd, "HEVC not supported"
		}
		if err := r.Insert(e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := r.List(logstore.Filter{})
	if err != nil || len(all) != 4 || all[0].Source != "playback" || all[3].Source != "scan" {
		t.Fatalf("newest first: %+v %v", all, err)
	}
	if d := all[0]; d.SessionID == nil || *d.SessionID != 7 || d.FFmpegCommand != cmd || d.TranscodeReason != "HEVC not supported" || !d.At.Equal(t0.Add(3*time.Minute)) {
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
	if err := r.Prune(now.Add(-3*24*time.Hour+time.Hour), 100); err != nil { // keeps days 0..2
		t.Fatal(err)
	}
	if got, _ := r.List(logstore.Filter{}); len(got) != 3 {
		t.Fatalf("age: %d", len(got))
	}
	if err := r.Prune(time.Time{}, 2); err != nil {
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
	l.Info("decided", "session_id", int64(9), "item_id", int64(4), "ffmpeg_command", "ffmpeg -i x", "transcode_reason", "audio codec", "device", "Roku",
		"api_key", "SECRET1", "pin", "1234", "err", "GET https://x/3/movie?api_key=SECRET2&query=a: refused", "auth", "Bearer SECRET3",
		"callback", "/callback?code=SECRET4&state=s", "form", "client_secret=SECRET6&code_verifier=SECRET7")
	l.Debug("dropped")
	if len(sink.es) != 1 {
		t.Fatalf("entries %d", len(sink.es))
	}
	e := sink.es[0]
	if e.Level != "info" || e.Source != "playback" || *e.SessionID != 9 || *e.ItemID != 4 || e.FFmpegCommand != "ffmpeg -i x" || e.TranscodeReason != "audio codec" {
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

package logstore

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Sink receives each entry the Handler accepts.
type Sink interface{ Write(*Entry) }

// Log returns the default logger tagged with a source. It resolves the
// default on each call so it follows slog.SetDefault.
func Log(source string) *slog.Logger { return slog.Default().With("source", source) }

// Handler writes every record to next and to a Sink, with secrets removed
// from both. The attributes source, session_id, item_id, ffmpeg_command and
// transcode_reason fill the entry's columns; the rest are appended to the
// message as key=value.
type Handler struct {
	next  slog.Handler
	sink  Sink
	min   slog.Level
	attrs []slog.Attr
}

// NewHandler returns a Handler that drops records below min.
func NewHandler(next slog.Handler, sink Sink, min slog.Level) *Handler {
	return &Handler{next: next, sink: sink, min: min}
}

// Enabled reports whether level is at or above the minimum.
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.min }

// WithAttrs returns a Handler whose records carry attrs.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	for i := range as {
		as[i] = clean(as[i])
	}
	c.next, c.attrs = h.next.WithAttrs(as), append(append([]slog.Attr(nil), h.attrs...), as...)
	return &c
}

// WithGroup ignores groups; entries are flat.
func (h *Handler) WithGroup(string) slog.Handler { return h }

var (
	secretKey  = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|authorization|cookie|(^|_)pin($|_))`)
	streamPath = regexp.MustCompile(`(/streams/)[^/\s"]+`) // the stream key in the path authorizes the request
	secretText = regexp.MustCompile(`(?i)((?:api_?key|access_token|token|password|pin|secret|code|verifier)=)[^&\s"]+|(bearer\s+)\S+`)
)

// scrub removes credentials from free text: query and form parameters, among
// them OAuth codes, secrets and PKCE verifiers, and bearer tokens.
func scrub(s string) string {
	return streamPath.ReplaceAllString(secretText.ReplaceAllString(s, "${1}${2}[redacted]"), "${1}[redacted]")
}

// Untrusted is text a client sent, with credentials removed and control
// characters escaped as oneLine does; lines keeps line feeds and tabs.
func Untrusted(s string, lines bool) string {
	s = scrub(s)
	if !lines {
		return oneLine(s)
	}
	return escape(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}

// oneLine escapes control characters, so text a client chose cannot pass
// for another entry or drive a terminal.
func oneLine(s string) string { return escape(s, unicode.IsControl) }

func escape(s string, control func(rune) bool) string {
	if !strings.ContainsFunc(s, control) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if control(r) {
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clean(a slog.Attr) slog.Attr {
	if secretKey.MatchString(a.Key) {
		return slog.String(a.Key, "[redacted]")
	}
	if v := a.Value.Resolve(); v.Kind() == slog.KindString || v.Kind() == slog.KindAny {
		return slog.String(a.Key, scrub(fmt.Sprint(v.Any())))
	}
	return a
}

// Handle writes r to the next handler and to the sink.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	clean2 := slog.NewRecord(r.Time, r.Level, scrub(r.Message), r.PC)
	e := &Entry{At: r.Time.UTC(), Level: levelName(r.Level), Source: "server"}
	var rest []string
	take := func(a slog.Attr) {
		a = clean(a)
		switch v := a.Value; a.Key {
		case "source":
			e.Source = v.String()
		case "session_id":
			e.SessionID = idAttr(v)
		case "item_id":
			e.ItemID = idAttr(v)
		case "ffmpeg_command":
			e.FFmpegCommand = v.String()
		case "transcode_reason":
			e.TranscodeReason = v.String()
		default:
			rest = append(rest, a.Key+"="+v.String())
		}
	}
	for _, a := range h.attrs {
		take(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		clean2.AddAttrs(clean(a))
		take(a)
		return true
	})
	e.Message = oneLine(strings.Join(append([]string{clean2.Message}, rest...), " "))
	h.sink.Write(e)
	return h.next.Handle(ctx, clean2)
}

// idAttr reads an id logged as its canonical string or as a uid.ID, which
// clean has already turned into that string.
func idAttr(v slog.Value) *uid.ID {
	id, err := uid.Parse(v.String())
	if err != nil {
		return nil
	}
	return &id
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

// Writer is a Sink that stores entries on its own goroutine, so logging
// never waits on the database (whose single connection a caller may hold).
// When the queue is full entries are dropped rather than blocking.
type Writer struct {
	repo       *Repo
	ch         chan *Entry
	done       chan struct{}
	mu         sync.Mutex
	closed     bool
	lastPruned time.Time
}

// NewWriter starts a Writer over repo; Close flushes it.
func NewWriter(repo *Repo) *Writer {
	w := &Writer{repo: repo, ch: make(chan *Entry, 1024), done: make(chan struct{})}
	go w.run()
	return w
}

// Write queues e; after Close it does nothing.
func (w *Writer) Write(e *Entry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	select {
	case w.ch <- e:
	default:
	}
}

// Close stops accepting entries and waits until the queued ones are stored.
func (w *Writer) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	<-w.done
}

func (w *Writer) run() {
	defer close(w.done)
	for e := range w.ch {
		// A failed insert must not log, or it would feed itself.
		_ = w.repo.Insert(e)
		if now := time.Now(); now.Sub(w.lastPruned) > time.Hour {
			w.lastPruned = now
			_ = w.repo.Prune(now.Add(-Retention), MaxRows, MaxClientRows)
		}
	}
}

package httpapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
)

func logFilter(level *api.LogLevel, source *string, since, until *time.Time, q *string) logstore.Filter {
	return logstore.Filter{MinLevel: string(or(level, "")), Source: or(source, ""), Since: since, Until: until, Q: or(q, "")}
}

func toLogEntry(e *logstore.Entry) api.LogEntry {
	return api.LogEntry{Id: e.ID, At: e.At, Level: api.LogLevel(e.Level), Source: e.Source, Message: e.Message,
		SessionId: e.SessionID, ItemId: e.ItemID, FfmpegCommand: optStr(e.FFmpegCommand), TranscodeReason: optStr(e.TranscodeReason)}
}

// ListLogs pages log entries, newest first.
func (s *Server) ListLogs(_ context.Context, r api.ListLogsRequestObject) (api.ListLogsResponseObject, error) {
	p := r.Params
	f := logFilter(p.Level, p.Source, p.Since, p.Until, p.Q)
	limit, offset := paging(p.Limit, p.Offset)
	f.Limit, f.Offset = limit+1, offset
	rows, err := s.svc.Logs.List(f)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListLogs200JSONResponse{Items: mapAll(rows, toLogEntry), NextOffset: next}, nil
}

// DownloadLogs returns every matching entry as text, oldest first, one line
// each; the ffmpeg command and transcode reason follow the message.
func (s *Server) DownloadLogs(_ context.Context, r api.DownloadLogsRequestObject) (api.DownloadLogsResponseObject, error) {
	p := r.Params
	f := logFilter(p.Level, p.Source, p.Since, p.Until, p.Q)
	f.Limit = logstore.MaxRows
	rows, err := s.svc.Logs.List(f)
	if err != nil {
		return nil, err
	}
	slices.Reverse(rows)
	var b strings.Builder
	for _, e := range rows {
		fmt.Fprintf(&b, "%s %s %s %s", e.At.Format(time.RFC3339), strings.ToUpper(e.Level), e.Source, oneLine(e.Message))
		if e.TranscodeReason != "" {
			fmt.Fprintf(&b, " | reason: %s", oneLine(e.TranscodeReason))
		}
		if e.FFmpegCommand != "" {
			fmt.Fprintf(&b, " | ffmpeg: %s", oneLine(e.FFmpegCommand))
		}
		b.WriteByte('\n')
	}
	name := `attachment; filename="rabbit-ears-logs.txt"`
	return api.DownloadLogs200TextResponse{Body: b.String(), Headers: api.DownloadLogs200ResponseHeaders{ContentDisposition: &name}}, nil
}

func oneLine(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ") }

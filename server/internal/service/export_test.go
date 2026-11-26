package service

import (
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
)

// Waited is how many Watch togethers have a channel for their next change.
func (s *WatchTogetherService) Waited() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.changed)
}

// Next is the conversion the worker makes next.
func (s *DownloadService) Next() (*download.Conversion, error) { return s.next() }

// Failed and Changed are what the playback service hands a session it
// found tuned in.
func (c *ChannelService) Failed(s *session.Session) error { return c.failed(s) }

// Listen adds a session to a source and reports that source's encoder
// session.
func (c *ChannelService) Listen(sessionID int64, key string, convert bool) (int64, error) {
	src, err := c.listen(sessionID, key, transcode.Stream{}, convert)
	if err != nil {
		return 0, err
	}
	return src.stream.SessionID, nil
}

func (c *ChannelService) Changed(s *session.Session, snap playback.Snapshot, sel playback.Selection) (bool, error) {
	return c.changed(s, snap, sel)
}

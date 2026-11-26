package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

// DownloadTTL is how long a download is kept after it was asked for and,
// for a conversion, after the conversion finished.
const DownloadTTL = 72 * time.Hour

// DownloadService says what a device would download of a title, claims
// files for it, and makes the conversions they need one at a time, never
// beside a live video transcode.
type DownloadService struct {
	repo     *download.Repo
	editions *edition.Repo
	files    *mediafile.Repo
	watch    *watchstate.Repo
	playback *PlaybackService
	// Dir keeps finished conversions, each named by its id and container,
	// and nothing else: cleaning up removes any other file in it. With none
	// set, nothing is converted or cleaned.
	Dir string
	TTL time.Duration

	wake chan struct{}
	mu   sync.Mutex
	// current is the conversion being made, cancel stops it and progress
	// is its share done.
	current  int64
	cancel   context.CancelFunc
	progress float64
	// settled is closed, and forgotten, when a conversion ends or goes, or
	// a download goes.
	settled chan struct{}
	// served is the turn in which each device last had a conversion made,
	// turns counting them. Only the worker touches them; a restart forgets
	// them.
	served map[int64]int64
	turns  int64
}

// NewDownloadService returns a DownloadService whose conversions wait on
// the playback engine's live transcodes.
func NewDownloadService(repo *download.Repo, editions *edition.Repo, files *mediafile.Repo, watch *watchstate.Repo,
	p *PlaybackService) *DownloadService {
	return &DownloadService{repo: repo, editions: editions, files: files, watch: watch, playback: p, TTL: DownloadTTL, wake: make(chan struct{}, 1),
		served: map[int64]int64{}}
}

// Repo exposes the repository.
func (s *DownloadService) Repo() *download.Repo { return s.repo }

// DownloadTitle is what a device would download of one title: Plan of
// File, nil when the title has no file on disk or none the device plays,
// and the profile's progress through it.
type DownloadTitle struct {
	playlist.Row
	File       *mediafile.File
	Plan       *playback.DownloadPlan
	Watched    bool
	PositionMs int64
}

// Titles is what the device described by snap would download of each row
// at quality q, each title once, in order, with accountID's progress.
func (s *DownloadService) Titles(rows []playlist.Row, accountID int64, snap playback.Snapshot, sel playback.Selection, q string) ([]*DownloadTitle, error) {
	seen := map[int64]bool{}
	var out []*DownloadTitle
	for _, r := range rows {
		if seen[r.ItemID] {
			continue
		}
		seen[r.ItemID] = true
		t, err := s.Title(r, accountID, snap, sel, q)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Title is what the device would download of the row's title.
func (s *DownloadService) Title(r playlist.Row, accountID int64, snap playback.Snapshot, sel playback.Selection, q string) (*DownloadTitle, error) {
	t := &DownloadTitle{Row: r}
	if r.Kind != item.Song {
		switch w, err := s.watch.Get(accountID, r.ItemID); {
		case err == nil:
			t.Watched = w.Watched
			if w.EditionID == nil || *w.EditionID == r.EditionID {
				t.PositionMs = w.PositionMs
			}
		case !errors.Is(err, db.ErrNotFound):
			return nil, err
		}
	}
	if r.EditionID == 0 {
		return t, nil
	}
	files, err := s.files.FilesForEdition(r.EditionID)
	if err != nil {
		return nil, err
	}
	if files, err = s.playback.OnDisk(files); err != nil || len(files) == 0 {
		return t, err
	}
	// A conversion is made whole, so a file of unknown length competes as
	// any other.
	f, err := s.playback.best(files, playback.Kept(snap), sel, playback.Decide)
	if errors.Is(err, ErrNothingPlayable) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	_, m, err := s.playback.Media(f.ID)
	if err != nil {
		return nil, err
	}
	if plan, ok := playback.DecideDownload(m, snap, sel, q); ok {
		t.File, t.Plan = f, &plan
	}
	return t, nil
}

// Row is the row of an item as a download target: the item itself playing
// editionID, or its default for 0.
func (s *DownloadService) Row(it *item.Item, editionID int64) (playlist.Row, error) {
	r := playlist.Row{ItemID: it.ID, Kind: it.Kind, LibraryID: it.LibraryID, EditionID: editionID}
	if editionID != 0 {
		ed, err := s.editions.Get(editionID)
		if err == nil && ed.ItemID != it.ID {
			err = db.ErrNotFound
		}
		return r, err
	}
	switch ed, err := s.editions.Default(it.ID); {
	case err == nil:
		r.EditionID = ed.ID
	case !errors.Is(err, db.ErrNotFound):
		return r, err
	}
	return r, nil
}

// ErrNotDownloadable is a title with no file on disk the device could
// keep.
var ErrNotDownloadable = errors.New("the title has no file on disk this device can keep")

// Create claims t's file for a profile of a device: the file itself, at
// once, or its conversion, queued. Asked again, it returns the same claim.
func (s *DownloadService) Create(deviceID, accountID int64, t *DownloadTitle) (*download.Download, error) {
	if t.Plan == nil {
		return nil, ErrNotDownloadable
	}
	d := &download.Download{DeviceID: deviceID, AccountID: accountID, ItemID: t.ItemID, MediaFileID: t.File.ID, Quality: t.Plan.Quality}
	if t.EditionID != 0 {
		d.EditionID = &t.EditionID
	}
	var c *download.Conversion
	if !t.Plan.Original {
		c = &download.Conversion{MediaFileID: t.File.ID, SourceModifiedAt: t.File.ModifiedAt, Entry: t.Plan.Entry, Container: t.Plan.Container,
			PredictedBytes: t.Plan.Bytes}
	}
	found, err := s.repo.Claim(d, c)
	// Woken once the claim is stored, the worker never finds the
	// conversion unclaimed.
	if err == nil && c != nil && c.Status == download.Queued {
		s.Wake()
	}
	return found, err
}

// Release drops a claim; a conversion nobody claims any more stops and
// goes.
func (s *DownloadService) Release(d *download.Download) error {
	defer s.settle()
	if err := s.repo.Delete(d.ID); err != nil {
		return err
	}
	if d.ConversionID == nil {
		return nil
	}
	return s.ReleaseConversions([]int64{*d.ConversionID})
}

// ReleaseConversions stops and removes those of ids no download claims any
// more.
func (s *DownloadService) ReleaseConversions(ids []int64) error {
	defer s.settle()
	for _, id := range ids {
		if _, err := s.dropUnclaimed(id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteConversion stops a conversion if it is being made and removes it,
// its file and the downloads that claim it.
func (s *DownloadService) DeleteConversion(id int64) error {
	defer s.settle()
	c, err := s.repo.Conversion(id)
	if err != nil {
		return err
	}
	s.stop(id)
	if err := s.repo.DeleteConversion(id); err != nil {
		return err
	}
	return removeFile(s.path(c))
}

// dropUnclaimed is DeleteConversion for a conversion no download claims,
// reporting whether it went; a claim made since the caller looked keeps it.
func (s *DownloadService) dropUnclaimed(id int64) (bool, error) {
	c, err := s.repo.Conversion(id)
	if err != nil {
		return false, err
	}
	if gone, err := s.repo.DeleteUnclaimed(id); err != nil || !gone {
		return false, err
	}
	s.stop(id)
	return true, removeFile(s.path(c))
}

// stop cancels conversion id if it is being made.
func (s *DownloadService) stop(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == id && s.cancel != nil {
		s.cancel()
	}
}

// Progress is the share of a conversion made so far: 0 until it starts and
// 1 once ready.
func (s *DownloadService) Progress(c *download.Conversion) float64 {
	switch c.Status {
	case download.Ready:
		return 1
	case download.Converting:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.current == c.ID {
			return s.progress
		}
	}
	return 0
}

// ExpiresAt is when an unreleased download goes: TTL after it was asked
// for or its conversion finished, whichever is later; nil while its
// conversion is still to finish.
func (s *DownloadService) ExpiresAt(d *download.Download, c *download.Conversion) *time.Time {
	at := d.CreatedAt
	if c != nil {
		if c.FinishedAt == nil {
			return nil
		}
		if c.FinishedAt.After(at) {
			at = *c.FinishedAt
		}
	}
	at = at.Add(s.TTL)
	return &at
}

// Path is where a ready conversion's file is.
func (s *DownloadService) Path(c *download.Conversion) string { return s.path(c) }

func (s *DownloadService) path(c *download.Conversion) string {
	return filepath.Join(s.Dir, fmt.Sprintf("%d.%s", c.ID, c.Container))
}

func removeFile(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Settled is closed when a conversion next ends, ready, failed or stopped,
// or goes, or a download goes; one taken before reading a conversion misses
// no change after.
func (s *DownloadService) Settled() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled == nil {
		s.settled = make(chan struct{})
	}
	return s.settled
}

func (s *DownloadService) settle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled != nil {
		close(s.settled)
		s.settled = nil
	}
}

// Wake has the worker look for a queued conversion now.
func (s *DownloadService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// downloadIdle is how long the worker waits for a wake before it looks
// again.
const downloadIdle = time.Minute

// Run makes queued conversions, by turns, until ctx ends, cleaning up
// once before the first. Conversions a stop left unfinished start again.
func (s *DownloadService) Run(ctx context.Context) {
	log := logstore.Log("transcode")
	if s.Dir == "" {
		log.Error("No download cache set; conversions wait")
		return
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		log.Error("No download cache", "err", err)
		return
	}
	if err := s.repo.Requeue(); err != nil {
		log.Error("Conversions not requeued", "err", err)
	}
	// Before any conversion starts, so a part a stop left is not taken
	// while its conversion writes it again.
	if err := s.Clean(time.Now()); err != nil {
		log.Error("Downloads not cleaned up", "err", err)
	}
	for ctx.Err() == nil {
		c, err := s.next()
		if err == nil {
			s.convert(ctx, c)
			continue
		}
		if !errors.Is(err, db.ErrNotFound) {
			log.Error("No next conversion", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-time.After(downloadIdle):
		}
	}
}

// next is the queued conversion the device whose turn it is claimed first:
// of the devices claiming one, the one served longest ago, a device never
// served before any other and the earliest claim breaking ties. It is a
// turn for every device claiming it.
func (s *DownloadService) next() (*download.Conversion, error) {
	for {
		turns, err := s.repo.Turns()
		if err != nil {
			return nil, err
		}
		if len(turns) == 0 {
			return nil, db.ErrNotFound
		}
		t := slices.MinFunc(turns, func(a, b download.Turn) int {
			return cmp.Or(cmp.Compare(s.served[a.DeviceID], s.served[b.DeviceID]), cmp.Compare(a.ClaimID, b.ClaimID))
		})
		c, err := s.repo.Conversion(t.ConversionID)
		if errors.Is(err, db.ErrNotFound) {
			continue // released since
		}
		if err != nil {
			return nil, err
		}
		devices, err := s.repo.Claimants(c.ID)
		if err != nil {
			return nil, err
		}
		s.turns++
		for _, d := range devices {
			s.served[d] = s.turns
		}
		return c, nil
	}
}

// convert makes one queued conversion, with the hardware encoder and then
// the software one. A conversion nobody claims goes instead.
func (s *DownloadService) convert(ctx context.Context, c *download.Conversion) {
	defer s.settle()
	log := logstore.Log("transcode").With("conversion_id", c.ID)
	if gone, err := s.dropUnclaimed(c.ID); err != nil || gone {
		if err != nil {
			log.Error("Unclaimed conversion not removed", "err", err)
		}
		return
	}
	job, itemID, durationMs, err := s.job(c)
	if itemID != 0 {
		log = log.With("item_id", itemID)
	}
	if err == nil {
		err = s.repo.Start(c.ID, time.Now())
	}
	if err != nil {
		s.failed(c, err, log)
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.current, s.cancel, s.progress = c.ID, cancel, 0
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.current, s.cancel = 0, nil
		s.mu.Unlock()
	}()
	busy := s.playback.Engine.Busy
	progress := func(f float64) {
		s.mu.Lock()
		s.progress = f
		s.mu.Unlock()
	}
	out := s.path(c)
	err = transcode.ConvertFile(cctx, job, out, durationMs, busy, progress, log)
	if errors.Is(err, transcode.ErrEncoder) && job.Entry.Encoder == session.EncoderHardware && cctx.Err() == nil {
		log.Warn("Hardware conversion failed; converting in software", "err", err)
		job.Entry.Encoder = session.EncoderSoftware
		err = transcode.ConvertFile(cctx, job, out, durationMs, busy, progress, log)
	}
	switch {
	case ctx.Err() != nil:
		// Stopping: the conversion stays converting and starts again.
	case cctx.Err() != nil:
		_ = removeFile(out)
	case err != nil:
		s.failed(c, err, log)
	default:
		st, err := os.Stat(out)
		if err == nil {
			err = s.repo.Finish(c.ID, st.Size(), time.Now())
		}
		if err != nil {
			_ = removeFile(out)
			if !errors.Is(err, db.ErrNotFound) {
				s.failed(c, err, log)
			}
			return
		}
		log.Info("Conversion ready", "size_bytes", st.Size())
	}
}

func (s *DownloadService) failed(c *download.Conversion, cause error, log *slog.Logger) {
	log.Warn(fmt.Sprintf("Conversion %d failed", c.ID), "err", cause)
	if err := s.repo.Fail(c.ID, cause.Error(), time.Now()); err != nil && !errors.Is(err, db.ErrNotFound) {
		log.Error("Conversion failure not recorded", "err", err)
	}
}

// job is the encoder's job for c, from its source as it is now, with the
// source's item; a source changed since c was asked for is an error.
func (s *DownloadService) job(c *download.Conversion) (transcode.Job, int64, int64, error) {
	f, m, err := s.playback.Media(c.MediaFileID)
	if err != nil {
		return transcode.Job{}, 0, 0, err
	}
	if !f.ModifiedAt.Equal(c.SourceModifiedAt) {
		return transcode.Job{}, f.ItemID, 0, errors.New("the source file changed")
	}
	if _, err := os.Stat(f.Path); err != nil {
		return transcode.Job{}, f.ItemID, 0, err
	}
	e := *c.Entry
	e.Encoder = session.EncoderSoftware
	if s.playback.Hardware {
		e.Encoder = session.EncoderHardware
	}
	v := playback.VideoTrack(m)
	j := transcode.Job{Input: f.Path, Entry: &e, Video: v, Audio: playback.AudioTrack(m, e.AudioTrackID), Rung: playback.Rungs(&e, v)[0]}
	if e.SubtitleAction == session.SubtitleBurn {
		for _, t := range m.Tracks {
			if e.SubtitleTrackID != nil && t.ID == *e.SubtitleTrackID {
				j.Subtitle = t
			}
		}
	}
	return j, f.ItemID, m.DurationMs, nil
}

// Clean removes the downloads expired at now, the conversions nobody
// claims, and any file in Dir that is no conversion's.
func (s *DownloadService) Clean(now time.Time) error {
	defer s.settle()
	if err := s.repo.Expire(now.Add(-s.TTL)); err != nil {
		return err
	}
	unneeded, err := s.repo.Unneeded()
	if err != nil {
		return err
	}
	for _, c := range unneeded {
		if _, err := s.dropUnclaimed(c.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
	}
	if s.Dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	for _, e := range entries {
		id, err := strconv.ParseInt(strings.SplitN(e.Name(), ".", 2)[0], 10, 64)
		if err == nil && id == current {
			continue
		}
		if err == nil {
			c, cerr := s.repo.Conversion(id)
			if cerr == nil && c.Status == download.Ready && filepath.Base(s.path(c)) == e.Name() {
				continue
			}
			if cerr != nil && !errors.Is(cerr, db.ErrNotFound) {
				return cerr
			}
		}
		if err := os.RemoveAll(filepath.Join(s.Dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

package scan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/task"
)

const (
	scanKindPrefix = "scan:"    // followed by the library ID
	MetadataKind   = "metadata" // kind of the Refresh metadata task
	metadataName   = "Refresh metadata"
	metadataEvery  = time.Hour
	retryBatch     = 200
	PreviewsKind   = "previews" // kind of the Seek previews task, made due by every scan
	previewsName   = "Seek previews"
	LocalKind      = "local" // kind of the Local metadata task, made due by every scan
	localName      = "Local metadata"
	StationsKind   = "stations" // kind of the Schedule stations task
	stationsName   = "Schedule stations"
	// stationsEvery keeps a schedule written a day ahead at least 23 hours
	// ahead; Schedule stations runs at the top of each hour.
	stationsEvery = time.Hour
	PurgeKind     = "sync_purge" // kind of the Purge sync history task
	purgeName     = "Purge sync history"
	purgeEvery    = 24 * time.Hour
)

// Runner owns the scan and metadata tasks: it keeps their rows in step with
// the libraries, runs those that are due and records each run.
type Runner struct {
	S        *Scanner
	Tasks    *task.Repo
	Watch    *Watcher      // optional; kept in step with the libraries on every tick
	Interval time.Duration // how often to look for due tasks
	// Paused, when it reports true, holds due runs (scheduled or from file
	// changes) until it reports false; requested runs still start.
	Paused func() bool
	// Stations, when set, is the hourly Schedule stations task: it writes
	// every station's schedule a day ahead and its rating, and reports what
	// it did.
	Stations func(context.Context) (string, error)
	// Purge, when set, is the daily Purge sync history task, and reports
	// what it purged.
	Purge func(context.Context) (string, error)
	// Events, when set, hears of each scan that changed its library.
	Events *live.Hub
	wake   chan struct{}
}

// NewRunner returns a Runner that checks for due tasks every 15 seconds.
func NewRunner(s *Scanner, tasks *task.Repo) *Runner {
	return &Runner{S: s, Tasks: tasks, Interval: 15 * time.Second, wake: make(chan struct{}, 1)}
}

// nextNightly is the next occurrence of hour:00 in now's zone after now.
func nextNightly(now time.Time, hour int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(now.Year(), now.Month(), now.Day()+1, hour, 0, 0, 0, now.Location())
	}
	return t
}

func ptr[T any](v T) *T { return &v }

// ScanKind is the kind of a library's scan task.
func ScanKind(libraryID int64) string { return scanKindPrefix + strconv.FormatInt(libraryID, 10) }

// Sync creates a nightly scan task for each library (due at once, so a new
// library scans promptly), the hourly metadata task, the on-demand Seek
// previews and Local metadata tasks, with Stations set the hourly Schedule
// stations task and with Purge set the daily Purge sync history task, and
// removes the scan tasks of deleted libraries.
func (r *Runner) Sync() error {
	libs, err := r.S.Libraries.List()
	if err != nil {
		return err
	}
	tasks, err := r.Tasks.List()
	if err != nil {
		return err
	}
	byKind := map[string]*task.Task{}
	for _, t := range tasks {
		byKind[t.Kind] = t
	}
	now := r.S.Now().UTC()
	want := map[string]bool{MetadataKind: true}
	if byKind[MetadataKind] == nil {
		if err := r.Tasks.Create(&task.Task{Name: metadataName, Kind: MetadataKind, IntervalSeconds: ptr(int(metadataEvery.Seconds())), Enabled: true, NextRunAt: &now}); err != nil {
			return err
		}
	}
	for _, k := range [][2]string{{PreviewsKind, previewsName}, {LocalKind, localName}} {
		if byKind[k[0]] == nil {
			if err := r.Tasks.Create(&task.Task{Name: k[1], Kind: k[0], Enabled: true, NextRunAt: &now}); err != nil {
				return err
			}
		}
	}
	hourly := int(stationsEvery.Seconds())
	switch t := byKind[StationsKind]; {
	case r.Stations == nil:
	case t == nil:
		if err := r.Tasks.Create(&task.Task{Name: stationsName, Kind: StationsKind, IntervalSeconds: &hourly, Enabled: true, NextRunAt: &now}); err != nil {
			return err
		}
	case t.IntervalSeconds == nil || *t.IntervalSeconds != hourly:
		t.IntervalSeconds = &hourly
		if err := r.Tasks.Update(t); err != nil {
			return err
		}
	}
	if r.Purge != nil && byKind[PurgeKind] == nil {
		if err := r.Tasks.Create(&task.Task{Name: purgeName, Kind: PurgeKind, IntervalSeconds: ptr(int(purgeEvery.Seconds())), Enabled: true, NextRunAt: &now}); err != nil {
			return err
		}
	}
	for _, l := range libs {
		kind := ScanKind(l.ID)
		want[kind] = true
		name := "Scan " + l.Name
		switch t := byKind[kind]; {
		case t == nil:
			if err := r.Tasks.Create(&task.Task{Name: name, Kind: kind, IntervalSeconds: ptr(86400), Enabled: true, NextRunAt: &now}); err != nil {
				return err
			}
		case t.Name != name:
			t.Name = name
			if err := r.Tasks.Update(t); err != nil {
				return err
			}
		}
	}
	for _, t := range tasks {
		if strings.HasPrefix(t.Kind, scanKindPrefix) && !want[t.Kind] {
			if err := r.Tasks.Delete(t.ID); err != nil {
				return err
			}
		}
	}
	if r.Watch != nil {
		r.Watch.Sync()
	}
	return nil
}

// Trigger makes a library's scan due now.
func (r *Runner) Trigger(libraryID int64) error {
	return r.makeDue(ScanKind(libraryID))
}

func (r *Runner) makeDue(kind string) error {
	ts, err := r.Tasks.List()
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Kind == kind {
			now := r.S.Now().UTC()
			if err := r.Tasks.Reschedule(t.ID, &now, t.RunRequestedAt); err != nil {
				return err
			}
			select {
			case r.wake <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

// Tick runs every requested task, then every due task unless paused, one at
// a time.
func (r *Runner) Tick(ctx context.Context) error {
	due, err := r.Tasks.Requested()
	if err != nil {
		return err
	}
	if r.Paused == nil || !r.Paused() {
		scheduled, err := r.Tasks.Due(r.S.Now().UTC())
		if err != nil {
			return err
		}
		for _, t := range scheduled {
			if !slices.ContainsFunc(due, func(d *task.Task) bool { return d.ID == t.ID }) {
				due = append(due, t)
			}
		}
	}
	for _, t := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		// An earlier task's run may have outlasted a pause or a disable.
		t, err := r.Tasks.Get(t.ID)
		if err != nil {
			return err
		}
		if t.RunRequestedAt == nil && (!t.Enabled || r.Paused != nil && r.Paused()) {
			continue
		}
		if err := r.execute(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) execute(ctx context.Context, t *task.Task) error {
	now := r.S.Now().UTC()
	// Rescheduling before the work lets a trigger that lands mid-run stand.
	if err := r.Tasks.Reschedule(t.ID, r.nextRun(t, now), nil); err != nil {
		return err
	}
	run, err := r.Tasks.StartRun(t.ID, now)
	if err != nil {
		return err
	}
	summary, runErr := r.work(ctx, t, func(p float64) { _ = r.Tasks.SetProgress(run.ID, p) })
	if summary != "" {
		_ = r.Tasks.SetSummary(run.ID, summary)
	}
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
		logstore.Log("task").Error("Task failed", "task", t.Name, "err", runErr)
	}
	return r.Tasks.FinishRun(run.ID, msg, r.S.Now().UTC())
}

func (r *Runner) nextRun(t *task.Task, now time.Time) *time.Time {
	if strings.HasPrefix(t.Kind, scanKindPrefix) {
		id, err := strconv.ParseInt(strings.TrimPrefix(t.Kind, scanKindPrefix), 10, 64)
		if err == nil {
			if lib, err := r.S.Libraries.Get(id); err == nil {
				n := nextNightly(now.Local(), lib.ScanHour).UTC()
				return &n
			}
		}
	}
	if t.Kind == StationsKind {
		n := now.Truncate(stationsEvery).Add(stationsEvery)
		return &n
	}
	if t.IntervalSeconds == nil {
		return nil
	}
	n := now.Add(time.Duration(*t.IntervalSeconds) * time.Second)
	return &n
}

func (r *Runner) work(ctx context.Context, t *task.Task, progress func(float64)) (string, error) {
	switch {
	case t.Kind == MetadataKind:
		more, err := r.S.RetryDue(ctx, retryBatch)
		if more {
			err = r.makeDue(MetadataKind)
		}
		return "", err
	case t.Kind == StationsKind && r.Stations != nil:
		return r.Stations(ctx)
	case t.Kind == PurgeKind && r.Purge != nil:
		return r.Purge(ctx)
	case t.Kind == PreviewsKind || t.Kind == LocalKind:
		makeAll := r.S.MakePreviews
		if t.Kind == LocalKind {
			makeAll = r.S.MakeLocal
		}
		sum, more, err := makeAll(ctx, progress, r.othersWaiting(t.Kind))
		if more || ctx.Err() != nil {
			// Due again now, so it runs after the tasks it gave way to,
			// which fell due earlier, or at the next start after a stop.
			err = errors.Join(err, r.makeDue(t.Kind))
		}
		return sum, err
	case strings.HasPrefix(t.Kind, scanKindPrefix):
		id, err := strconv.ParseInt(strings.TrimPrefix(t.Kind, scanKindPrefix), 10, 64)
		if err != nil {
			return "", fmt.Errorf("bad task kind %q", t.Kind)
		}
		lib, err := r.S.Libraries.Get(id)
		if err != nil {
			return "", err
		}
		log := logstore.Log("scan")
		log.Info("Scan started: " + lib.Name)
		sum, err := r.S.Scan(ctx, lib, progress)
		// A scan that fails partway may still have changed the library.
		if sum.changed() {
			r.Events.Publish(live.Event{Kind: live.Library, LibraryID: id})
		}
		if err != nil {
			log.Error("Scan of "+lib.Name+" failed: "+sum.String(), "err", err)
			return sum.String(), err
		}
		log.Info("Scan of " + lib.Name + " finished: " + sum.String())
		return sum.String(), errors.Join(r.S.Libraries.MarkScanned(id, r.S.Now().UTC()), r.makeDue(PreviewsKind), r.makeDue(LocalKind))
	}
	return "", fmt.Errorf("unknown task kind %q", t.Kind)
}

// othersWaiting reports whether a task of another kind than self is one
// Tick would run now, so a long previews run gives way to it.
func (r *Runner) othersWaiting(self string) func() bool {
	return func() bool {
		ts, err := r.Tasks.Requested()
		if err != nil {
			return false
		}
		if r.Paused == nil || !r.Paused() {
			due, err := r.Tasks.Due(r.S.Now().UTC())
			if err != nil {
				return false
			}
			ts = append(ts, due...)
		}
		return slices.ContainsFunc(ts, func(t *task.Task) bool { return t.Kind != self })
	}
}

// Run is the scheduler loop. It fails runs a crash left running, making an
// interrupted Seek previews or Local metadata run due again since nothing
// else would until the next scan, then syncs and ticks until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	ids, err := r.Tasks.FailInterrupted(r.S.Now().UTC())
	if err != nil {
		logstore.Log("task").Error("Could not fail interrupted runs", "err", err)
	}
	for _, id := range ids {
		if t, err := r.Tasks.Get(id); err == nil && (t.Kind == PreviewsKind || t.Kind == LocalKind) {
			if err := r.makeDue(t.Kind); err != nil {
				logstore.Log("task").Error("Could not resume "+t.Name, "err", err)
			}
		}
	}
	tick := time.NewTicker(r.Interval)
	defer tick.Stop()
	for ctx.Err() == nil {
		if err := errors.Join(r.Sync(), r.Tick(ctx)); err != nil && ctx.Err() == nil && !errors.Is(err, db.ErrNotFound) {
			logstore.Log("task").Error("Scheduler error", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		case <-r.wake:
		}
	}
}

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
)

func (s *Server) settings() (api.Settings, error) {
	info, err := s.svc.Server.Get()
	if err != nil {
		return api.Settings{}, err
	}
	f, err := s.svc.Sampler.Sample(s.svc.DataDir)
	if err != nil {
		return api.Settings{}, err
	}
	return api.Settings{ServerName: info.Name, TasksPaused: info.TasksPaused, Gravatar: &info.Gravatar, DataDir: s.svc.DataDir, Port: s.svc.Port, DatabasePath: s.svc.DatabasePath,
		MaxTranscodes: info.MaxTranscodes, TranscodeCacheCapBytes: info.TranscodeCacheCapBytes, ReadAheadSeconds: info.ReadAheadSeconds,
		Storage: api.StorageFacts{TranscodeCacheBytes: s.svc.Playback.Engine.Usage(), TranscodeCacheCapBytes: info.TranscodeCacheCapBytes,
			DiskFreeBytes: int64(f.DiskFree), DiskTotalBytes: int64(f.DiskTotal)}, //nolint:gosec // disk sizes fit int64
		Hardware: api.HardwareFacts{CpuModel: f.CPUModel, Cores: f.Cores, MemoryBytes: int64(f.MemoryTotal), VideoToolbox: f.VideoToolbox}, //nolint:gosec // memory fits int64
		Metadata: api.MetadataSources{TmdbConfigured: s.svc.TMDB.Configured(), Source: optEnum[api.MetadataSource](info.Metadata.Source),
			Mode: optEnum[api.MetadataMode](info.Metadata.Mode)}}, nil
}

// GetSettings returns the editable settings with the facts the Settings
// cards show.
func (s *Server) GetSettings(context.Context, api.GetSettingsRequestObject) (api.GetSettingsResponseObject, error) {
	v, err := s.settings()
	return api.GetSettings200JSONResponse(v), err
}

// ReplaceSettings saves the editable settings. The TMDB key goes first, so
// a rejected one changes nothing.
func (s *Server) ReplaceSettings(ctx context.Context, r api.ReplaceSettingsRequestObject) (api.ReplaceSettingsResponseObject, error) {
	info, err := s.svc.Server.Get()
	if err != nil {
		return nil, err
	}
	paused := or(r.Body.TasksPaused, info.TasksPaused)
	sources := serverinfo.Sources{Source: string(or(r.Body.MetadataSource, api.MetadataSource(info.Metadata.Source))),
		Mode: string(or(r.Body.MetadataMode, api.MetadataMode(info.Metadata.Mode)))}
	if !sources.Valid() {
		return nil, invalid("metadataSource", "metadata comes from local or remote, only or preferred")
	}
	if key := r.Body.TmdbKey; key != nil {
		err := s.svc.TMDB.Save(ctx, *key)
		if errors.Is(err, service.ErrKeyRejected) {
			return nil, invalid("tmdbKey", err.Error())
		}
		if err != nil {
			return nil, err
		}
		if *key == "" {
			audit(ctx, "Removed the TMDB key")
		} else {
			audit(ctx, "Saved a TMDB key")
		}
	}
	gravatar := or(r.Body.Gravatar, info.Gravatar)
	if err := s.svc.Server.Update(&serverinfo.Info{Name: r.Body.ServerName, TasksPaused: paused, Gravatar: gravatar}); err != nil {
		return nil, err
	}
	switch {
	case paused && !info.TasksPaused:
		audit(ctx, "Paused tasks")
	case !paused && info.TasksPaused:
		audit(ctx, "Resumed tasks")
	}
	if gravatar != info.Gravatar {
		audit(ctx, fmt.Sprintf("Turned Gravatar pictures %s", map[bool]string{true: "on", false: "off"}[gravatar]))
	}
	if sources != info.Metadata {
		if err := s.svc.Server.SetSources(sources); err != nil {
			return nil, err
		}
		audit(ctx, fmt.Sprintf("Set metadata to %s %s", sources.Source, sources.Mode))
	}
	if ahead := or(r.Body.ReadAheadSeconds, info.ReadAheadSeconds); ahead != info.ReadAheadSeconds {
		if err := s.svc.Server.SetReadAhead(ahead); err != nil {
			return nil, err
		}
		audit(ctx, fmt.Sprintf("Set the read-ahead to %d seconds", ahead))
	}
	maxVideo, cacheCap := or(r.Body.MaxTranscodes, info.MaxTranscodes), or(r.Body.TranscodeCacheCapBytes, info.TranscodeCacheCapBytes)
	if maxVideo != info.MaxTranscodes || cacheCap != info.TranscodeCacheCapBytes {
		if err := s.svc.Server.SetTranscodeLimits(maxVideo, cacheCap); err != nil {
			return nil, err
		}
		s.svc.Playback.Engine.SetLimits(transcode.Limits{CacheCap: cacheCap, MaxVideo: maxVideo})
		audit(ctx, fmt.Sprintf("Set the transcode limit to %d and the cache cap to %d bytes", maxVideo, cacheCap))
	}
	v, err := s.settings()
	return api.ReplaceSettings200JSONResponse(v), err
}

// GetStats samples the Dashboard gauges.
func (s *Server) GetStats(context.Context, api.GetStatsRequestObject) (api.GetStatsResponseObject, error) {
	f, err := s.svc.Sampler.Sample(s.svc.DataDir)
	if err != nil {
		return nil, err
	}
	eng := s.svc.Playback.Engine
	//nolint:gosec // byte counts fit int64
	return api.GetStats200JSONResponse{CpuPercent: f.CPUPercent, MemoryUsedBytes: int64(f.MemoryUsed), MemoryTotalBytes: int64(f.MemoryTotal),
		StorageUsedBytes: int64(f.DiskTotal - f.DiskFree), StorageTotalBytes: int64(f.DiskTotal),
		ActiveTranscodes: len(eng.Transcoding()), MaxTranscodes: eng.Limits().MaxVideo}, nil
}

// ClearTranscodeCache deletes cached segments of every session not playing.
func (s *Server) ClearTranscodeCache(ctx context.Context, _ api.ClearTranscodeCacheRequestObject) (api.ClearTranscodeCacheResponseObject, error) {
	active, err := s.svc.Sessions.Repo().Active()
	if err != nil {
		return nil, err
	}
	keep := map[int64]bool{}
	for _, v := range active {
		keep[v.ID] = true
	}
	if err := s.svc.Playback.Engine.Clear(keep); err != nil {
		return nil, err
	}
	audit(ctx, "Cleared the transcode cache")
	return api.ClearTranscodeCache204Response{}, nil
}

// RefreshAllMetadata makes every item due and queues the metadata task.
func (s *Server) RefreshAllMetadata(ctx context.Context, _ api.RefreshAllMetadataRequestObject) (api.RefreshAllMetadataResponseObject, error) {
	now := time.Now().UTC()
	if _, err := s.svc.Items.Repo().MarkAllDue(now); err != nil {
		return nil, err
	}
	if err := s.runMetadata(now); err != nil {
		return nil, err
	}
	audit(ctx, "Refreshed all metadata")
	return api.RefreshAllMetadata202Response{}, nil
}

// runMetadata asks the metadata task to run now.
func (s *Server) runMetadata(now time.Time) error {
	tasks, err := s.svc.Tasks.Repo().List()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Kind == scan.MetadataKind {
			if err := s.svc.Tasks.Repo().RequestRun(t.ID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// RequestTaskRun is Run now.
func (s *Server) RequestTaskRun(_ context.Context, r api.RequestTaskRunRequestObject) (api.RequestTaskRunResponseObject, error) {
	if err := s.svc.Tasks.Repo().RequestRun(r.TaskId, time.Now().UTC()); err != nil {
		return nil, err
	}
	t, err := s.svc.Tasks.Repo().Get(r.TaskId)
	if err != nil {
		return nil, err
	}
	v, err := s.task(t)
	return api.RequestTaskRun202JSONResponse(v), err
}

// GetTaskRun returns one run of the task.
func (s *Server) GetTaskRun(_ context.Context, r api.GetTaskRunRequestObject) (api.GetTaskRunResponseObject, error) {
	run, err := s.svc.Tasks.Repo().Run(r.RunId)
	if err == nil && run.TaskID != r.TaskId {
		err = db.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return api.GetTaskRun200JSONResponse(toRun(run)), nil
}

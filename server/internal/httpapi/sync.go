package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
)

// Client sync: a native client keeps a replica of what its household may
// see, filled from a snapshot and kept current from diffs.

// syncAPIMin is the oldest API version whose action batches are replayed.
const syncAPIMin = "0.21.0"

// syncPoll is how often a held doorbell looks at the version.
const syncPoll = 250 * time.Millisecond

func (s *Server) syncRanges() api.SyncRanges {
	return api.SyncRanges{ApiMin: syncAPIMin, ApiMax: s.version, SchemaMin: replica.V1.Version, SchemaMax: replica.V1.Version}
}

func (s *Server) GetSyncChanges(ctx context.Context, r api.GetSyncChangesRequestObject) (api.GetSyncChangesResponseObject, error) {
	out := api.GetSyncChanges200JSONResponse{Ranges: s.syncRanges(), Mode: api.Unsupported}
	if r.Params.Schema != replica.V1.Version {
		return out, nil
	}
	p, err := s.svc.Sync.Changes(ctx, caller(ctx).device.HouseholdID, r.Params.Since, or(r.Params.Cursor, ""), or(r.Params.Limit, 1000))
	if errors.Is(err, replica.ErrCursor) {
		return nil, fail(http.StatusBadRequest, api.ErrorCodeBadRequest, "cursor is not one of this pass's")
	}
	if err != nil {
		return nil, err
	}
	out.Mode = api.SyncChangesMode(p.Mode)
	if p.Mode != replica.Diff {
		return out, nil
	}
	tables, tombs := make([]api.SyncTable, len(p.Tables)), make([]api.SyncTombstones, len(p.Tombstones))
	for i, t := range p.Tables {
		tables[i] = api.SyncTable{Name: t.Name, Columns: t.Columns, Rows: t.Rows}
	}
	for i, k := range p.Tombstones {
		tombs[i] = api.SyncTombstones{Table: k.Table, Keys: k.Keys}
	}
	out.Version, out.Tables, out.Tombstones = &p.Version, &tables, &tombs
	if p.Cursor != "" {
		out.Cursor = &p.Cursor
	}
	return out, nil
}

func (s *Server) GetSyncSnapshot(ctx context.Context, r api.GetSyncSnapshotRequestObject) (api.GetSyncSnapshotResponseObject, error) {
	if r.Params.Schema != replica.V1.Version {
		return nil, fail(http.StatusBadRequest, api.ErrorCodeBadRequest, "the server serves no such sync schema")
	}
	f, err := s.svc.Sync.Snapshot(ctx, caller(ctx).device.HouseholdID)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return api.GetSyncSnapshot200ApplicationvndSqlite3Response{Body: f.File, ContentLength: info.Size()}, nil
}

// WaitSyncVersion holds until the household has something to read since
// after, looking again whenever the version moves.
func (s *Server) WaitSyncVersion(ctx context.Context, r api.WaitSyncVersionRequestObject) (api.WaitSyncVersionResponseObject, error) {
	out := api.WaitSyncVersion200JSONResponse{Ranges: s.syncRanges()}
	household := caller(ctx).device.HouseholdID
	timeout := time.NewTimer(s.svc.EventHold)
	defer timeout.Stop()
	tick := time.NewTicker(syncPoll)
	defer tick.Stop()
	seen := int64(-1)
	for {
		v, err := s.svc.Sync.Version(ctx)
		if err != nil {
			return nil, err
		}
		if v != seen {
			seen = v
			if out.Changed, err = s.svc.Sync.Changed(ctx, household, r.Params.After); err != nil || out.Changed {
				return out, err
			}
		}
		select {
		case <-tick.C:
			// A sign-out moves no version; the device learns of it at once.
			if err := s.stillSignedIn(ctx); err != nil {
				return nil, err
			}
		case <-timeout.C:
			return out, nil
		case <-ctx.Done():
			// The client has gone; nobody reads this.
			return out, nil
		}
	}
}

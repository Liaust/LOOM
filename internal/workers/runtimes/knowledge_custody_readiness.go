package runtimes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"loom.local/loom/internal/workers"
)

type custodyCompletionReader interface {
	CompletionRevision(context.Context) (string, error)
}

type custodyScheduleCheckpoint struct {
	Revision    string    `json:"revision"`
	Pending     bool      `json:"pending"`
	WrapPending bool      `json:"wrap_pending,omitempty"`
	RetryAt     time.Time `json:"retry_at"`
}

func (s custodyScheduleCheckpoint) due(now time.Time) bool {
	return s.Pending || !now.Before(s.RetryAt)
}

func (r KnowledgeIndexerRuntime) nextCustodySchedule(ctx context.Context, checkpoints map[string]workers.WorkerCheckpoint) (custodyScheduleCheckpoint, error) {
	var saved custodyScheduleCheckpoint
	if checkpoint, ok := checkpoints["custody_schedule"]; ok {
		if err := json.Unmarshal(checkpoint.CheckpointJSON, &saved); err != nil {
			return saved, err
		}
	}
	if reader, ok := r.Custody.(custodyCompletionReader); ok {
		revision, err := reader.CompletionRevision(ctx)
		if err != nil {
			return saved, err
		}
		if revision != saved.Revision {
			saved.Revision, saved.Pending = revision, true
			saved.WrapPending = true
		}
	}
	return saved, nil
}

func (s custodyScheduleCheckpoint) afterBatch(startCursor string, wrapped bool, now time.Time) custodyScheduleCheckpoint {
	if startCursor == "" {
		s.WrapPending = false
	}
	s.Pending = !wrapped || s.WrapPending
	s.RetryAt = now.Add(6 * time.Hour)
	return s
}

func (r KnowledgeIndexerRuntime) custodyReady(ctx context.Context, instance workers.WorkerInstance, now time.Time) (bool, *time.Time, error) {
	if r.Custody == nil {
		return false, nil, nil
	}
	var raw []byte
	err := r.Knowledge.Store().DB().QueryRowContext(ctx, `SELECT checkpoint_json FROM workers.worker_checkpoints
	 WHERE worker_instance_id=$1 AND checkpoint_key='custody_schedule'`, instance.WorkerInstanceID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	checkpoints := map[string]workers.WorkerCheckpoint{}
	if len(raw) > 0 {
		checkpoints["custody_schedule"] = workers.WorkerCheckpoint{CheckpointJSON: raw}
	}
	saved, err := r.nextCustodySchedule(ctx, checkpoints)
	return saved.due(now), &saved.RetryAt, err
}

package runtimes

import (
	"context"
	"fmt"
	"sync"
	"time"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/workers"
)

const cloudArchiveCompletionReserve = 2 * time.Minute

func cloudArchiveWorkContext(ctx context.Context, deadline time.Time, maximum time.Duration) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	workUntil := time.Now().Add(maximum)
	if outer, ok := ctx.Deadline(); ok && (deadline.IsZero() || outer.Before(deadline)) {
		deadline = outer
	}
	if !deadline.IsZero() && deadline.Add(-cloudArchiveCompletionReserve).Before(workUntil) {
		workUntil = deadline.Add(-cloudArchiveCompletionReserve)
	}
	if maximum <= 0 || !workUntil.After(time.Now()) {
		return nil, nil, fmt.Errorf("cloud deadline has no safe archive/shutdown budget")
	}
	child, cancel := context.WithDeadline(ctx, workUntil)
	return child, cancel, nil
}

func cloudArchiveProgressReporter(ctx context.Context, run workers.RunContext) func(cloudstorage.DirectArchiveProgress) {
	var mutex sync.Mutex
	var phase string
	var observed time.Time
	var latest *cloudstorage.BorgProgress
	return func(progress cloudstorage.DirectArchiveProgress) {
		mutex.Lock()
		defer mutex.Unlock()
		if progress.Borg != nil {
			copy := *progress.Borg
			latest = &copy
		}
		now := time.Now().UTC()
		if phase == progress.Phase && now.Sub(observed) < 30*time.Second && (progress.Borg == nil || !progress.Borg.Finished) {
			return
		}
		phase, observed = progress.Phase, now
		summary := mustWorkerJSON(map[string]any{"schema_version": "cloud_snapshot_upload.progress.v1", "status": "running", "committed": false, "worker_run_id": run.Run.WorkerRunID, "phase": phase, "observed_at": now, "borg": latest})
		if run.Logger != nil {
			run.Logger.Info("cloud archive progress", "worker_run_id", run.Run.WorkerRunID, "phase", phase, "borg", latest)
		}
		if run.Service.DB != nil {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := run.Service.RecordRunProgress(writeCtx, run, summary); err != nil && run.Logger != nil {
				run.Logger.Warn("cloud progress observation could not be saved", "worker_run_id", run.Run.WorkerRunID, "error", err)
			}
		}
	}
}

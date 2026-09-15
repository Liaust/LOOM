package minidashboard

import (
	"context"
	"strings"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/workers"
)

func RuntimeStatus(ctx context.Context, maintenanceService maintenance.Service, workerService workers.Service, jobService jobs.Service, now time.Time) (RuntimeState, error) {
	db, err := maintenanceService.DBStatus(ctx)
	if err != nil {
		return RuntimeState{}, err
	}
	items, err := workerService.ListWorkers(ctx, workers.WorkerFilter{Limit: 200})
	if err != nil {
		return RuntimeState{}, err
	}
	queue, err := jobService.JobQueueSummary(ctx)
	if err != nil {
		return RuntimeState{}, err
	}
	result := RuntimeState{Available: true, State: SourceLive, DatabaseState: db.Status, MigrationsCurrent: migrationsCurrent(db), Freshness: Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive}}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		result.EnabledWorkers++
		switch item.HealthStatus {
		case workers.HealthHealthy, workers.HealthRunning:
			result.HealthyWorkers++
		case workers.HealthFailed:
			result.CriticalWorkers++
		default:
			result.DegradedWorkers++
		}
	}
	result.Queued, result.Running, result.Failed = queue.QueuedCount, queue.RunningCount, queue.FailedCount+queue.TimedOutCount
	result.ManualAction, result.OfflineRunners = queue.ManualActionCount, queue.OfflineRunnerCount+queue.FailedRunnerCount
	result.OldestQueuedAgeSec = queue.OldestQueuedAgeSeconds
	return result, nil
}

func migrationsCurrent(status maintenance.DBStatus) bool {
	switch strings.ToLower(strings.TrimSpace(status.MigrationStatus)) {
	case "ok", "current":
		return status.Pending == 0 && (status.LatestVersion == 0 || status.CurrentVersion == status.LatestVersion)
	default:
		return false
	}
}

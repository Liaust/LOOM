package minidashboard

import (
	"context"
	"encoding/json"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/maintenance"
)

func ProtectionStatus(ctx context.Context, service maintenance.Service, now time.Time) (ProtectionSnapshot, error) {
	local, err := service.BackupStatus(ctx)
	if err != nil {
		return ProtectionSnapshot{}, err
	}
	cloud, err := service.CloudProtectionStatus(ctx)
	if err != nil {
		return ProtectionSnapshot{}, err
	}
	result := ProtectionSnapshot{
		Local:    ProtectionState{Available: true, State: local.Status, SourceUpdatedAt: now, StaleAfterSeconds: int64((48 * time.Hour) / time.Second), SourceState: SourceLive},
		Cloud:    ProtectionState{Available: cloud.Available, State: cloud.Verification, WorkerState: cloud.WorkerState, LastSuccessAt: cloud.LastSuccessAt, LastFailureAt: cloud.LastFailureAt, SourceUpdatedAt: cloud.UpdatedAt, StaleAfterSeconds: int64((48 * time.Hour) / time.Second), SourceState: SourceCached},
		Coverage: unavailableCoverage(now),
		Findings: FindingSummary{Available: true, Open: local.OpenFindings.Open + cloud.OpenFindingCount, Warning: local.OpenFindings.Warning + local.OpenFindings.Error + cloud.WarningFindings, Critical: local.OpenFindings.Critical + cloud.CriticalFindings},
	}
	if local.Worker != nil {
		result.Local.WorkerState, result.Local.LastSuccessAt, result.Local.LastFailureAt = local.Worker.HealthStatus, local.Worker.LastSuccessAt, local.Worker.LastFailureAt
	}
	if local.LatestSuccessful != nil {
		result.Local.LastSuccessAt = local.LatestSuccessful.Operation.FinishedAt
		result.Local.SourceUpdatedAt = local.LatestSuccessful.Operation.UpdatedAt
		var payload struct {
			Coverage backupcoverage.BoundedSummary `json:"coverage"`
		}
		if json.Unmarshal(local.LatestSuccessful.Operation.ResultJSON, &payload) == nil && !payload.Coverage.CapturedAt.IsZero() {
			result.Coverage = CoverageState{Available: true, State: payload.Coverage.Status, CapturedAt: payload.Coverage.CapturedAt, StaleAfterSeconds: int64((48 * time.Hour) / time.Second), SourceState: SourceCached}
		}
	}
	if result.Cloud.SourceUpdatedAt.IsZero() {
		result.Cloud.SourceUpdatedAt = now
		if !cloud.Available {
			result.Cloud.SourceState = SourceUnavailable
		}
	}
	return result, nil
}

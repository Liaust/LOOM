package supportbundle

import (
	"context"

	"loom.local/loom/internal/maintenance"
)

type BackupSummary struct {
	Status           string                     `json:"status"`
	Worker           *maintenance.WorkerStatus  `json:"worker,omitempty"`
	LatestSuccessful *BackupOperationSummary    `json:"latest_successful,omitempty"`
	LatestFailed     *BackupOperationSummary    `json:"latest_failed,omitempty"`
	OpenFindings     maintenance.FindingSummary `json:"open_findings"`
}

type BackupOperationSummary struct {
	OperationID string `json:"operation_id,omitempty"`
	Status      string `json:"status,omitempty"`
	Kind        string `json:"kind,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	ArtifactNum int    `json:"artifact_count"`
}

func collectBackup(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := backupClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	status, err := client.MaintenanceBackupStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := BackupSummary{
		Status:       status.Data.Status,
		Worker:       status.Data.Worker,
		OpenFindings: status.Data.OpenFindings,
	}
	summary.LatestSuccessful = backupOperationSummary(status.Data.LatestSuccessful)
	summary.LatestFailed = backupOperationSummary(status.Data.LatestFailed)
	return jsonSummary("summaries/backup_coverage.json", summary, PrivacyDiagnosticSummary)
}

func backupOperationSummary(operation *maintenance.BackupOperation) *BackupOperationSummary {
	if operation == nil {
		return nil
	}
	summary := &BackupOperationSummary{
		OperationID: operation.Operation.MaintenanceOperationID,
		Status:      operation.Operation.Status,
		Kind:        operation.Operation.OperationKind,
		ArtifactNum: len(operation.Artifacts),
	}
	if !operation.Operation.StartedAt.IsZero() {
		summary.StartedAt = operation.Operation.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if operation.Operation.FinishedAt != nil {
		summary.FinishedAt = operation.Operation.FinishedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return summary
}

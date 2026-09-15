package supportbundle

import (
	"context"
	"fmt"

	"loom.local/loom/internal/cloudstorage"
)

type CloudSummary struct {
	SchemaVersion string                                    `json:"schema_version,omitempty"`
	Status        string                                    `json:"status"`
	Mode          string                                    `json:"mode"`
	CheckedAt     string                                    `json:"checked_at,omitempty"`
	Config        cloudstorage.ConfigSummary                `json:"config"`
	Remote        *cloudstorage.RemoteStatus                `json:"remote,omitempty"`
	Roots         []cloudstorage.RootStatus                 `json:"roots,omitempty"`
	RemoteState   *cloudstorage.RemoteState                 `json:"remote_state,omitempty"`
	LockPath      string                                    `json:"lock_path,omitempty"`
	SnapshotStore *cloudstorage.SnapshotBackendStatusReport `json:"snapshot_store,omitempty"`
}

func collectCloud(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	if collection.Options.CloudStatusProvider == nil {
		return CollectorOutput{}, fmt.Errorf("cloud cached status provider unavailable")
	}
	report, err := collection.Options.CloudStatusProvider(ctx, collection.Options)
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := CloudSummary{
		SchemaVersion: report.SchemaVersion,
		Status:        report.Status,
		Mode:          report.Mode,
		Config:        report.Config,
		Remote:        report.Remote,
		Roots:         limitItems(report.Roots, itemLimit(collection.Options)),
		RemoteState:   report.RemoteState,
		LockPath:      report.LockPath,
		SnapshotStore: report.SnapshotStore,
	}
	if !report.CheckedAt.IsZero() {
		summary.CheckedAt = report.CheckedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return jsonSummary("summaries/cloud.json", summary, PrivacyDiagnosticSummary)
}

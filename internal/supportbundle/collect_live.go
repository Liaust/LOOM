package supportbundle

import (
	"context"

	"loom.local/loom/internal/cloudstorage"
)

type LiveDiagnosticsProvider func(context.Context, Options) (LiveDiagnosticsSummary, error)

type LiveDiagnosticsSummary struct {
	Status        string                                  `json:"status"`
	Cloud         *cloudstorage.StatusReport              `json:"cloud,omitempty"`
	CloudSnapshot *cloudstorage.CloudSnapshotStatusReport `json:"cloud_snapshot,omitempty"`
	Warnings      []string                                `json:"warnings,omitempty"`
}

func collectLive(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	if collection.Options.LiveDiagnosticsProvider == nil {
		return CollectorOutput{SkipReason: "live_provider_unavailable"}, nil
	}
	summary, err := collection.Options.LiveDiagnosticsProvider(ctx, collection.Options)
	if err != nil {
		return CollectorOutput{}, err
	}
	if summary.Status == "" {
		summary.Status = "collected"
	}
	return jsonSummary("summaries/live.json", summary, PrivacyDiagnosticSummary)
}

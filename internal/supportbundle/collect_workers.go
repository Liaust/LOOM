package supportbundle

import (
	"context"

	"loom.local/loom/internal/workers"
)

type WorkersSummary struct {
	Total             int                        `json:"total"`
	ByLifecycleStatus map[string]int             `json:"by_lifecycle_status"`
	ByHealthStatus    map[string]int             `json:"by_health_status"`
	AttentionRequired int                        `json:"attention_required"`
	Degraded          []WorkerOperationalSummary `json:"degraded,omitempty"`
}

type WorkerOperationalSummary struct {
	WorkerInstanceID  string `json:"worker_instance_id,omitempty"`
	WorkerKey         string `json:"worker_key,omitempty"`
	WorkerKind        string `json:"worker_kind,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	LifecycleStatus   string `json:"lifecycle_status,omitempty"`
	HealthStatus      string `json:"health_status,omitempty"`
	Severity          string `json:"severity,omitempty"`
	AttentionRequired bool   `json:"attention_required,omitempty"`
}

func collectWorkers(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := workersClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	limit := itemLimit(collection.Options)
	envelope, err := client.ListWorkers(ctx, correlationID(collection), workers.WorkerFilter{Limit: limit})
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := WorkersSummary{
		Total:             len(envelope.Data),
		ByLifecycleStatus: map[string]int{},
		ByHealthStatus:    map[string]int{},
	}
	for _, worker := range envelope.Data {
		summary.ByLifecycleStatus[worker.LifecycleStatus]++
		summary.ByHealthStatus[worker.HealthStatus]++
		if worker.AttentionRequired {
			summary.AttentionRequired++
		}
		if worker.AttentionRequired || worker.HealthStatus == workers.HealthDegraded || worker.HealthStatus == workers.HealthFailed {
			summary.Degraded = append(summary.Degraded, workerOperationalSummary(worker))
		}
	}
	summary.Degraded = limitItems(summary.Degraded, limit)
	return jsonSummary("summaries/workers.json", summary, PrivacyDiagnosticSummary)
}

func workerOperationalSummary(worker workers.WorkerListItem) WorkerOperationalSummary {
	return WorkerOperationalSummary{
		WorkerInstanceID:  worker.WorkerInstanceID,
		WorkerKey:         worker.WorkerKey,
		WorkerKind:        worker.WorkerKind,
		DisplayName:       worker.DisplayName,
		LifecycleStatus:   worker.LifecycleStatus,
		HealthStatus:      worker.HealthStatus,
		Severity:          worker.Severity,
		AttentionRequired: worker.AttentionRequired,
	}
}

package minidashboard

import (
	"context"
	"time"

	"loom.local/loom/internal/activitysummary"
	"loom.local/loom/internal/workers"
)

func ActivityStatus(ctx context.Context, service workers.Service, now time.Time) (ActivityState, error) {
	items, err := service.ListWorkers(ctx, workers.WorkerFilter{Limit: activitysummary.MaxCandidates})
	if err != nil {
		return ActivityState{}, err
	}
	candidates := make([]activitysummary.Candidate, 0, len(items)*2)
	for _, item := range items {
		if item.CurrentRunID != nil {
			candidates = append(candidates, activitysummary.Candidate{Kind: item.WorkerKind, State: "running"})
		}
		if item.LastSuccessAt != nil {
			candidates = append(candidates, activitysummary.Candidate{Kind: item.WorkerKind, State: "completed", CompletedAt: item.LastSuccessAt})
		}
		if item.LastFailureAt != nil {
			candidates = append(candidates, activitysummary.Candidate{Kind: item.WorkerKind, State: "failed", CompletedAt: item.LastFailureAt})
		}
	}
	summary := activitysummary.Build(candidates)
	result := ActivityState{Freshness: Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive}}
	if summary.Running != nil {
		result.Running = &Activity{Kind: summary.Running.Kind, State: summary.Running.State, StartedAt: summary.Running.StartedAt, CompletedAt: summary.Running.CompletedAt}
	}
	if summary.Recent != nil {
		result.Recent = &Activity{Kind: summary.Recent.Kind, State: summary.Recent.State, StartedAt: summary.Recent.StartedAt, CompletedAt: summary.Recent.CompletedAt}
	}
	return result, nil
}

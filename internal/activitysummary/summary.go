package activitysummary

import (
	"sort"
	"strings"
	"time"
)

const MaxCandidates = 20

type Candidate struct {
	Kind        string
	State       string
	StartedAt   *time.Time
	CompletedAt *time.Time
}

type Activity struct {
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Summary struct {
	Running *Activity `json:"running,omitempty"`
	Recent  *Activity `json:"recent,omitempty"`
}

var labels = map[string]string{
	"main_backup": "backup", "cloud_snapshot_upload": "cloud snapshot", "job": "job",
	"worker_run": "worker", "storage_export_refresh": "storage refresh", "lane_transfer": "lane transfer",
}

func NormalizeState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running", "starting", "active":
		return "running"
	case "succeeded", "completed", "complete", "ok", "healthy":
		return "completed"
	case "failed", "timed_out", "critical", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "unknown"
	}
}

func Label(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if value := labels[kind]; value != "" {
		return value
	}
	return "activity"
}

func Build(candidates []Candidate) Summary {
	if len(candidates) > MaxCandidates {
		candidates = candidates[:MaxCandidates]
	}
	items := make([]Activity, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, Activity{Kind: Label(candidate.Kind), State: NormalizeState(candidate.State), StartedAt: candidate.StartedAt, CompletedAt: candidate.CompletedAt})
	}
	sort.SliceStable(items, func(i, j int) bool { return activityTime(items[i]).After(activityTime(items[j])) })
	var summary Summary
	for i := range items {
		item := items[i]
		if item.State == "running" && summary.Running == nil {
			summary.Running = &item
			continue
		}
		if item.State != "running" && summary.Recent == nil {
			summary.Recent = &item
		}
	}
	return summary
}

func activityTime(value Activity) time.Time {
	if value.CompletedAt != nil {
		return value.CompletedAt.UTC()
	}
	if value.StartedAt != nil {
		return value.StartedAt.UTC()
	}
	return time.Time{}
}

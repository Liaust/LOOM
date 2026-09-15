package minidashboard

import (
	"context"
	"time"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/nodes"
)

func NetworkStatus(ctx context.Context, nodeService nodes.Service, communicationService communication.Service, cloudConfigPath string, now time.Time) (NetworkState, error) {
	items, err := nodeService.ListNodes(ctx, 50)
	if err != nil {
		return NetworkState{}, err
	}
	health, err := communicationService.Health(ctx)
	if err != nil {
		return NetworkState{}, err
	}
	report, cloudErr := cloudstorage.Status(ctx, cloudstorage.StatusInput{ConfigPath: cloudConfigPath, Mode: cloudstorage.StatusModeCached, Now: func() time.Time { return now }})
	result := NetworkState{Available: true, Freshness: Freshness{SourceUpdatedAt: now, StaleAfterSeconds: 30, SourceState: SourceLive}, Communication: CommunicationSummary{Available: true, Pending: health.PendingMessages, Failed: health.FailedMessages, DeadLetter: health.DeadLetterMessages}}
	for _, item := range items {
		if item.Status != "active" {
			continue
		}
		result.Nodes = append(result.Nodes, NodePresence{Key: SafeToken(item.NodeKey, "node", MaxNodeKeyLength), Label: SafeLabel(item.DisplayName, "NODE", MaxNodeLabelLength), Online: item.PresenceState == "online", LastSeenAt: item.LastSeenAt})
	}
	if cloudErr != nil {
		result.Cloud = CloudReachability{State: "unknown", SourceState: SourceUnavailable}
		return result, nil
	}
	result.Cloud.Enabled = report.Config.Enabled
	result.Cloud.State = report.Status
	result.Cloud.Cached = true
	result.Cloud.CheckedAt = report.CheckedAt
	result.Cloud.SourceState = SourceCached
	if report.RemoteState != nil {
		result.Cloud.State = report.RemoteState.State
		result.Cloud.CacheUpdatedAt = report.RemoteState.UpdatedAt
		result.Cloud.Available = !report.RemoteState.UpdatedAt.IsZero()
	}
	if !report.Config.Enabled {
		result.Cloud.SourceState = SourceDisabled
		result.Cloud.State = "disabled"
	}
	return result, nil
}

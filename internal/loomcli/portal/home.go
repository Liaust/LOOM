package portal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/lane"
)

const (
	homeAttentionLimit                    = 3
	homeTimelinePreviewLimit              = 5
	homeFreshRunningWindow                = 30 * time.Minute
	schedulePendingInvocationWarningAfter = 30 * time.Second
)

func BuildHomeData(snapshot Snapshot) HomeData {
	timeline := BuildTimelineData(TimelineBuildInput{Snapshot: snapshot})
	recent := homeActivitiesFromTimeline(homeTimelinePreview(timeline, snapshot.CapturedAt, homeTimelinePreviewLimit), homeTimelinePreviewLimit)
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		recent = localOfflineHomeActivity(recent)
	}
	return HomeData{
		Snapshot:   snapshot,
		Connection: buildHomeConnection(snapshot),
		System:     buildHomeSystem(snapshot),
		Network:    buildHomeNetwork(snapshot),
		Storage:    buildHomeStorage(snapshot),
		Runtime:    buildHomeRuntime(snapshot),
		Attention:  buildHomeAttention(snapshot),
		Recent:     recent,
	}
}

func buildHomeConnection(snapshot Snapshot) []HomeSummaryItem {
	availability := snapshot.MainAvailability
	state := string(availability.State)
	if state == "" {
		state = "unknown"
	}
	checkedAt := "-"
	if !availability.CheckedAt.IsZero() {
		checkedAt = availability.CheckedAt.Local().Format("15:04:05")
	}
	return []HomeSummaryItem{
		{Label: "Main", Status: state, Detail: firstNonEmpty(availability.Summary, "Main availability has not been checked.")},
		{Label: "Target", Status: "info", Detail: firstNonEmpty(availability.Target, "not configured for display")},
		{Label: "Last checked", Status: "info", Detail: checkedAt},
	}
}

func buildHomeSystem(snapshot Snapshot) []HomeSummaryItem {
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		captured := "-"
		if !snapshot.CapturedAt.IsZero() {
			captured = snapshot.CapturedAt.Format("15:04:05")
		}
		return []HomeSummaryItem{
			{Label: "loom", Status: "unavailable", Detail: "main runtime unavailable"},
			{Label: "node", Status: "unavailable", Detail: "main node identity unavailable"},
			{Label: "captured", Status: "ok", Detail: captured},
		}
	}
	node := fmt.Sprintf("%s (%s)", firstNonEmpty(snapshot.Health.Node.ID, "-"), firstNonEmpty(snapshot.Health.Node.Role, "-"))
	captured := "-"
	if !snapshot.CapturedAt.IsZero() {
		captured = snapshot.CapturedAt.Format("15:04:05")
	}
	return []HomeSummaryItem{
		{Label: "loom", Status: firstNonEmpty(snapshot.Status.Status, snapshot.Health.Status, "unknown"), Detail: firstNonEmpty(snapshot.Health.Service, "daemon")},
		{Label: "node", Status: "ok", Detail: node},
		{Label: "captured", Status: "ok", Detail: captured},
	}
}

func buildHomeNetwork(snapshot Snapshot) []HomeSummaryItem {
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		return []HomeSummaryItem{
			{Label: "nodes", Status: "unavailable", Detail: "requires main"},
			{Label: "events", Status: "unavailable", Detail: "requires main"},
		}
	}
	items := []HomeSummaryItem{}
	if len(snapshot.Nodes) == 0 {
		items = append(items, HomeSummaryItem{Label: "nodes", Status: "unknown", Detail: "no node list"})
	} else {
		attention := 0
		online := 0
		for _, node := range snapshot.Nodes {
			status := firstNonEmpty(node.PresenceState, node.Status)
			if homeStatusNeedsAttention(status) {
				attention++
				continue
			}
			online++
		}
		status := "ok"
		if attention > 0 {
			status = "warning"
		}
		items = append(items, HomeSummaryItem{Label: "nodes", Status: status, Detail: fmt.Sprintf("%d online, %d attention", online, attention)})
	}

	eventStatus := "ok"
	if snapshot.DirectEventStatus.FailedCount > 0 || snapshot.DirectEventStatus.MappingPendingCount > 0 {
		eventStatus = "warning"
	}
	items = append(items, HomeSummaryItem{
		Label:  "events",
		Status: eventStatus,
		Detail: fmt.Sprintf("%d active endpoints", snapshot.DirectEventStatus.ActiveEndpointCount),
	})
	return items
}

func rankAttentionItems(items []AttentionItem) []AttentionItem {
	if len(items) <= 1 {
		return items
	}
	ranked := append([]AttentionItem(nil), items...)
	sort.SliceStable(ranked, func(i, j int) bool {
		left := attentionSeverityRank(ranked[i].Severity)
		right := attentionSeverityRank(ranked[j].Severity)
		if left != right {
			return left > right
		}
		leftTime := ranked[i].CreatedAt
		rightTime := ranked[j].CreatedAt
		if !leftTime.IsZero() || !rightTime.IsZero() {
			if leftTime.IsZero() {
				return false
			}
			if rightTime.IsZero() {
				return true
			}
			if !leftTime.Equal(rightTime) {
				return leftTime.After(rightTime)
			}
		}
		return ranked[i].ID < ranked[j].ID
	})
	return ranked
}

func attentionSeverityRank(severity string) int {
	switch strings.TrimSpace(strings.ToLower(severity)) {
	case "fatal", "critical", "panic":
		return 100
	case "failed", "failure", "error", "unhealthy":
		return 90
	case "degraded", "blocked", "missing", "timed_out", "timeout", "cancelled", "canceled":
		return 80
	case "warning", "warn", "requires_manual_action", "missed":
		return 70
	case "unknown":
		return 40
	case "info", "notice":
		return 20
	default:
		return 50
	}
}

func buildHomeStorage(snapshot Snapshot) []HomeSummaryItem {
	items := []HomeSummaryItem{}
	boxStatus := firstNonEmpty(snapshot.BoxStatus.State, "unknown")
	boxDetail := "not initialized"
	if snapshot.BoxStatus.RootPath != "" {
		boxDetail = firstNonEmpty(snapshot.BoxStatus.Profile, "-") + " at " + snapshot.BoxStatus.RootPath
	}
	items = append(items, HomeSummaryItem{Label: "box", Status: boxStatus, Detail: boxDetail})

	if portalHasWorkspaceLane(snapshot.BoxStatus) {
		laneStatus := firstNonEmpty(snapshot.BoxStatus.LaneState, "unknown")
		laneDetail := "no workspace Lane status"
		laneStatus = firstNonEmpty(snapshot.BoxStatus.Lane.State, laneStatus)
		laneDetail = fmt.Sprintf("%d pending, %s", snapshot.BoxStatus.Lane.PendingItems, storageFormatBytes(snapshot.BoxStatus.Lane.PendingBytes))
		if snapshot.BoxStatus.Lane.LastTransfer != nil && snapshot.BoxStatus.Lane.PendingItems == 0 {
			laneDetail = fmt.Sprintf("last %s, %d files", snapshot.BoxStatus.Lane.LastTransfer.Status, snapshot.BoxStatus.Lane.LastTransfer.FileCount)
		}
		items = append(items, HomeSummaryItem{Label: "workspace lane", Status: laneStatus, Detail: laneDetail})
	} else if snapshot.BoxStatus.Profile == "main" {
		items = append(items, HomeSummaryItem{Label: "imports", Status: "active", Detail: "destination for workspace LOOM Lane transfers"})
	}

	if snapshot.BoxWatchStatusAvailable {
		status := "ok"
		if len(snapshot.WatchedRootFindings) > 0 {
			status = "warning"
		}
		items = append(items, HomeSummaryItem{Label: "watching", Status: status, Detail: fmt.Sprintf("%d roots", len(snapshot.BoxWatchStatus.Registrations))})
	} else if snapshot.MainAvailability.State == MainAvailabilityOffline {
		items = append(items, HomeSummaryItem{Label: "watching", Status: "unavailable", Detail: "backend status requires main"})
	}
	return items
}

func buildHomeRuntime(snapshot Snapshot) []HomeSummaryItem {
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		return []HomeSummaryItem{
			{Label: "loomd", Status: "unavailable", Detail: "requires main"},
			{Label: "database", Status: "unavailable", Detail: "requires main"},
			{Label: "workers", Status: "unavailable", Detail: "requires main"},
			{Label: "jobs", Status: "unavailable", Detail: "requires main"},
		}
	}
	workerStatus := "ok"
	degradedWorkers := snapshot.DegradedWorkerCount()
	if degradedWorkers > 0 {
		workerStatus = "warning"
	}

	jobFailures := activeJobFailureCount(snapshot.JobStatus)
	jobStatus := "ok"
	if jobFailures > 0 {
		jobStatus = "failed"
	} else if snapshot.JobStatus.ManualActionCount > 0 {
		jobStatus = "warning"
	} else if snapshot.JobStatus.RunningCount > 0 {
		jobStatus = "running"
	} else if snapshot.JobStatus.QueuedCount > 0 {
		jobStatus = "queued"
	}

	dbStatus := firstNonEmpty(snapshot.Health.Checks.Database.Status, snapshot.Health.Checks.Migrations.Status, "unknown")
	dbDetail := firstNonEmpty(snapshot.Health.Checks.Database.Database, "database")
	if snapshot.Health.Checks.Migrations.Pending > 0 {
		dbStatus = "warning"
		dbDetail = fmt.Sprintf("%d migrations pending", snapshot.Health.Checks.Migrations.Pending)
	}

	return []HomeSummaryItem{
		{Label: "loomd", Status: firstNonEmpty(snapshot.Health.Status, snapshot.Status.Status, "unknown"), Detail: firstNonEmpty(snapshot.Health.Version, "version unknown")},
		{Label: "database", Status: dbStatus, Detail: dbDetail},
		{Label: "workers", Status: workerStatus, Detail: fmt.Sprintf("%d active, %d attention", len(snapshot.Workers), degradedWorkers)},
		{Label: "jobs", Status: jobStatus, Detail: fmt.Sprintf("%d running, %d queued, %d failed", snapshot.JobStatus.RunningCount, snapshot.JobStatus.QueuedCount, jobFailures)},
	}
}

func buildHomeAttention(snapshot Snapshot) []AttentionItem {
	items := []AttentionItem{}
	seen := map[string]bool{}
	add := func(item AttentionItem) {
		item.ID = firstNonEmpty(item.ID, item.Domain+":"+item.Title+":"+item.TargetRef)
		if item.ID == "" || seen[item.ID] {
			return
		}
		seen[item.ID] = true
		item.Severity = firstNonEmpty(item.Severity, "warning")
		item = applyAttentionRepairSuggestion(item)
		items = append(items, item)
	}
	addStatusAttention := func(id, domain, title, status, reason, suggestion, targetKind, targetRef string) {
		if !homeStatusNeedsAttention(status) {
			return
		}
		add(AttentionItem{
			ID:         id,
			Domain:     domain,
			Severity:   homeSeverityForStatus(status),
			Title:      title,
			Reason:     firstNonEmpty(reason, "status is "+status),
			Suggestion: suggestion,
			TargetKind: targetKind,
			TargetRef:  targetRef,
		})
	}

	addStatusAttention("system.health", "System", "System health needs attention", firstNonEmpty(snapshot.Status.Status, snapshot.Health.Status), snapshot.Health.Checks.Config.Error, "open Status or run loom health", "screen", ScreenHome)
	addStatusAttention("database.health", "Runtime", "Database check is not healthy", snapshot.Health.Checks.Database.Status, snapshot.Health.Checks.Database.Error, "open Jobs or run loom health", "screen", ScreenJobs)
	addStatusAttention("storage.health", "Storage", "Storage check is not healthy", snapshot.Health.Checks.Storage.Status, snapshot.Health.Checks.Storage.Error, "open Storage", "screen", ScreenStorage)
	addStatusAttention("migrations.health", "Runtime", "Migrations need attention", snapshot.Health.Checks.Migrations.Status, snapshot.Health.Checks.Migrations.Error, "run loom migrations status", "screen", ScreenJobs)
	addStatusAttention("bootstrap.health", "System", "Bootstrap records need attention", snapshot.Health.Checks.Bootstrap.Status, snapshot.Health.Checks.Bootstrap.Error, "run loom setup doctor", "screen", ScreenHome)

	for _, partial := range snapshot.PartialErrors {
		add(AttentionItem{
			ID:         "partial." + partial.Source,
			Domain:     "System",
			Severity:   "warning",
			Title:      "Partial portal data",
			Reason:     partial.Source + " unavailable: " + partial.Message,
			Suggestion: "refresh or inspect that surface",
			TargetKind: "source",
			TargetRef:  partial.Source,
		})
	}

	for _, worker := range snapshot.Workers {
		status := firstNonEmpty(worker.HealthStatus, worker.LifecycleStatus)
		if !workerNeedsOperationalAttention(worker) {
			continue
		}
		add(AttentionItem{
			ID:         "worker." + firstNonEmpty(worker.WorkerInstanceID, worker.WorkerKey),
			Domain:     "Workers",
			Severity:   firstNonEmpty(worker.Severity, homeSeverityForStatus(status)),
			Title:      firstNonEmpty(worker.DisplayName, worker.WorkerKey),
			Reason:     "worker status is " + firstNonEmpty(status, "attention required"),
			Suggestion: "open Background Operations",
			TargetKind: "worker",
			TargetRef:  firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID),
		})
	}

	for _, finding := range snapshot.MaintenanceFindings {
		add(AttentionItem{
			ID:         "maintenance." + firstNonEmpty(finding.MaintenanceFindingID, finding.FindingKey),
			Domain:     "Maintenance",
			Severity:   firstNonEmpty(finding.Severity, "warning"),
			Title:      firstNonEmpty(finding.FindingKind, "Maintenance finding"),
			Reason:     firstNonEmpty(finding.Summary, finding.SubjectKind+" "+finding.SubjectID),
			Suggestion: "open Background Operations",
			TargetKind: finding.SubjectKind,
			TargetRef:  firstNonEmpty(finding.SubjectID, finding.WorkerKey),
			CreatedAt:  finding.FirstSeenAt,
		})
	}
	if snapshot.Maintenance.Findings.Open > len(snapshot.MaintenanceFindings) {
		add(AttentionItem{
			ID:         "maintenance.open",
			Domain:     "Maintenance",
			Severity:   "warning",
			Title:      "Open maintenance findings",
			Reason:     fmt.Sprintf("%d open findings", snapshot.Maintenance.Findings.Open),
			Suggestion: "open Background Operations",
			TargetKind: "screen",
			TargetRef:  ScreenBackground,
		})
	}

	jobFailures := activeJobFailureCount(snapshot.JobStatus)
	if jobFailures > 0 {
		add(AttentionItem{
			ID:         "jobs.failed",
			Domain:     "Jobs",
			Severity:   "error",
			Title:      "Failed jobs",
			Reason:     fmt.Sprintf("%d jobs failed or timed out", jobFailures),
			Suggestion: "open Jobs",
			TargetKind: "screen",
			TargetRef:  ScreenJobs,
		})
	}
	if snapshot.JobStatus.ManualActionCount > 0 {
		add(AttentionItem{
			ID:         "jobs.manual",
			Domain:     "Jobs",
			Severity:   "warning",
			Title:      "Jobs require manual action",
			Reason:     fmt.Sprintf("%d jobs require attention", snapshot.JobStatus.ManualActionCount),
			Suggestion: "open Jobs",
			TargetKind: "screen",
			TargetRef:  ScreenJobs,
		})
	}

	if len(snapshot.IndexFailures) > 0 {
		reason := fmt.Sprintf("%d failed indexes", len(snapshot.IndexFailures))
		if snapshot.IndexFailures[0].LastErrorMessage != "" {
			reason = snapshot.IndexFailures[0].LastErrorMessage
		}
		add(AttentionItem{
			ID:         "indexes.failed",
			Domain:     "Search",
			Severity:   "error",
			Title:      "Indexing failures",
			Reason:     reason,
			Suggestion: "open Jobs",
			TargetKind: "screen",
			TargetRef:  ScreenJobs,
		})
	}

	if snapshot.ScheduleStatus.FailedInvocationCount > 0 || snapshot.ScheduleStatus.MissedFireCount > 0 {
		add(AttentionItem{
			ID:         "schedules.failed",
			Domain:     "Automations",
			Severity:   "warning",
			Title:      "Schedule failures",
			Reason:     fmt.Sprintf("%d failed invocations, %d missed fires", snapshot.ScheduleStatus.FailedInvocationCount, snapshot.ScheduleStatus.MissedFireCount),
			Suggestion: "open Automation Center",
			TargetKind: "screen",
			TargetRef:  ScreenAutomations,
		})
	}
	if lag, ok := schedulePendingInvocationLag(snapshot); ok {
		add(AttentionItem{
			ID:         "schedules.pending_stale",
			Domain:     "Automations",
			Severity:   "warning",
			Title:      "Schedule dispatch delayed",
			Reason:     fmt.Sprintf("%d pending invocation(s), oldest waiting %s", snapshot.ScheduleStatus.PendingInvocationCount, lag.Round(time.Second)),
			Suggestion: "open Timeline or Automation Center",
			TargetKind: "screen",
			TargetRef:  ScreenTimeline,
			CreatedAt:  *snapshot.ScheduleStatus.OldestPendingInvocationAt,
		})
	}
	if snapshot.DirectEventStatus.FailedCount > 0 || snapshot.DirectEventStatus.MappingPendingCount > 0 {
		add(AttentionItem{
			ID:         "direct_events.failed",
			Domain:     "Automations",
			Severity:   "warning",
			Title:      "Direct events need attention",
			Reason:     fmt.Sprintf("%d failed, %d mapping pending", snapshot.DirectEventStatus.FailedCount, snapshot.DirectEventStatus.MappingPendingCount),
			Suggestion: "open Automation Center",
			TargetKind: "screen",
			TargetRef:  ScreenAutomations,
		})
	}
	if len(snapshot.InvocationFailures) > 0 {
		add(AttentionItem{
			ID:         "invocations.failed",
			Domain:     "Automations",
			Severity:   "error",
			Title:      "Capability invocations failed",
			Reason:     fmt.Sprintf("%d failed invocations", len(snapshot.InvocationFailures)),
			Suggestion: "open Automation Center",
			TargetKind: "screen",
			TargetRef:  ScreenAutomations,
		})
	}
	if len(snapshot.DirectEventFailures) > 0 {
		add(AttentionItem{
			ID:         "direct_events.records_failed",
			Domain:     "Automations",
			Severity:   "error",
			Title:      "Direct event records failed",
			Reason:     fmt.Sprintf("%d failed direct events", len(snapshot.DirectEventFailures)),
			Suggestion: "open Automation Center",
			TargetKind: "screen",
			TargetRef:  ScreenAutomations,
		})
	}

	for _, finding := range snapshot.WatchedRootFindings {
		add(AttentionItem{
			ID:         "watched_root." + firstNonEmpty(finding.WatchedRootFindingID, finding.FindingKey),
			Domain:     "Watched roots",
			Severity:   firstNonEmpty(finding.Severity, "warning"),
			Title:      firstNonEmpty(finding.Kind, "Watched root finding"),
			Reason:     firstNonEmpty(finding.Summary, finding.RelativePath),
			Suggestion: "open Nodes And Watched Roots",
			TargetKind: "watched_root",
			TargetRef:  firstNonEmpty(finding.RootKey, finding.WatchedRootID),
			CreatedAt:  finding.FirstSeenAt,
		})
	}

	for _, diagnostic := range snapshot.BoxStatus.Diagnostics {
		if !homeDiagnosticNeedsAttention(diagnostic.Severity) {
			continue
		}
		add(AttentionItem{
			ID:         "box." + diagnostic.Code,
			Domain:     "Storage",
			Severity:   firstNonEmpty(diagnostic.Severity, "warning"),
			Title:      firstNonEmpty(diagnostic.Code, "Box diagnostic"),
			Reason:     diagnostic.Message,
			Suggestion: firstNonEmpty(diagnostic.Suggestion, "open LOOM Box"),
			TargetKind: "path",
			TargetRef:  diagnostic.Path,
		})
	}
	if portalHasWorkspaceLane(snapshot.BoxStatus) {
		for _, diagnostic := range snapshot.BoxStatus.Lane.Diagnostics {
			if !homeDiagnosticNeedsAttention(diagnostic.Severity) {
				continue
			}
			add(AttentionItem{
				ID:         "lane." + diagnostic.Code,
				Domain:     "Storage",
				Severity:   firstNonEmpty(diagnostic.Severity, "warning"),
				Title:      firstNonEmpty(diagnostic.Code, "Lane diagnostic"),
				Reason:     diagnostic.Message,
				Suggestion: firstNonEmpty(diagnostic.Suggestion, "open LOOM Box"),
				TargetKind: "path",
				TargetRef:  diagnostic.Path,
			})
		}
	}
	for _, node := range snapshot.Nodes {
		status := firstNonEmpty(node.PresenceState, node.Status)
		if !homeStatusNeedsAttention(status) {
			continue
		}
		add(AttentionItem{
			ID:         "node." + firstNonEmpty(node.NodeID, node.NodeKey),
			Domain:     "Network",
			Severity:   homeSeverityForStatus(status),
			Title:      firstNonEmpty(node.DisplayName, node.NodeKey, node.NodeID),
			Reason:     "node status is " + status,
			Suggestion: "open Nodes And Watched Roots",
			TargetKind: "node",
			TargetRef:  firstNonEmpty(node.NodeKey, node.NodeID),
		})
	}
	ranked := rankAttentionItems(items)
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		return offlineHomeAttention(snapshot, ranked)
	}
	return ranked
}

func offlineHomeAttention(snapshot Snapshot, items []AttentionItem) []AttentionItem {
	availability := snapshot.MainAvailability
	result := []AttentionItem{{
		ID:         "main.offline",
		Domain:     "Network",
		Severity:   "warning",
		Title:      "Main node is unreachable",
		Reason:     firstNonEmpty(availability.Summary, "Main is offline."),
		Suggestion: "Restore network or main availability and press r.",
		TargetKind: "screen",
		TargetRef:  ScreenDoctor,
		CreatedAt:  availability.CheckedAt,
	}}
	for _, item := range items {
		if strings.HasPrefix(item.ID, "box.") || strings.HasPrefix(item.ID, "lane.") || item.ID == "partial.box" || item.ID == "partial.box_watch_plan" {
			result = append(result, item)
		}
	}
	return result
}

func localOfflineHomeActivity(items []HomeActivityItem) []HomeActivityItem {
	local := make([]HomeActivityItem, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.ID, "lane.") {
			local = append(local, item)
		}
	}
	return local
}

func activeJobFailureCount(summary jobs.QueueSummary) int {
	return summary.FailedCount + summary.TimedOutCount
}

func schedulePendingInvocationLag(snapshot Snapshot) (time.Duration, bool) {
	if snapshot.ScheduleStatus.PendingInvocationCount <= 0 || snapshot.ScheduleStatus.OldestPendingInvocationAt == nil {
		return 0, false
	}
	now := snapshot.CapturedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	oldest := snapshot.ScheduleStatus.OldestPendingInvocationAt.UTC()
	lag := now.Sub(oldest)
	if lag < 0 {
		lag = 0
	}
	return lag, lag >= schedulePendingInvocationWarningAfter
}

func buildHomeRunning(snapshot Snapshot) []HomeActivityItem {
	items := []HomeActivityItem{}
	if snapshot.JobStatus.RunningCount > 0 {
		items = append(items, HomeActivityItem{
			ID:         "jobs.running",
			Domain:     "Jobs",
			Title:      "Jobs running",
			Status:     "running",
			Detail:     fmt.Sprintf("%d active jobs", snapshot.JobStatus.RunningCount),
			TargetKind: "screen",
			TargetRef:  ScreenJobs,
		})
	}
	for _, worker := range snapshot.Workers {
		if worker.CurrentRunID == nil {
			continue
		}
		items = append(items, HomeActivityItem{
			ID:         "worker." + *worker.CurrentRunID,
			Domain:     "Workers",
			Title:      firstNonEmpty(worker.DisplayName, worker.WorkerKey),
			Status:     "running",
			Detail:     "current run " + *worker.CurrentRunID,
			TargetKind: "worker",
			TargetRef:  firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID),
		})
	}
	if portalHasWorkspaceLane(snapshot.BoxStatus) && snapshot.BoxStatus.Lane.LastTransfer != nil && !homeTerminalActivityStatus(snapshot.BoxStatus.Lane.LastTransfer.Status) {
		transfer := snapshot.BoxStatus.Lane.LastTransfer
		var progress *float64
		if transfer.Progress.Observed {
			progress = &transfer.Progress.Percent
		}
		items = append(items, HomeActivityItem{
			ID:        "lane." + transfer.BatchID,
			Domain:    "Storage",
			Title:     "Lane transfer",
			Status:    firstNonEmpty(transfer.Status, "running"),
			Detail:    fmt.Sprintf("%d files, %s", transfer.FileCount, storageFormatBytes(transfer.TotalBytes)),
			Progress:  progress,
			StartedAt: transfer.StartedAt,
		})
	}
	return items
}

func buildHomeRecent(snapshot Snapshot) []HomeActivityItem {
	items := []HomeActivityItem{}
	if portalHasWorkspaceLane(snapshot.BoxStatus) && snapshot.BoxStatus.Lane.LastTransfer != nil && homeTerminalActivityStatus(snapshot.BoxStatus.Lane.LastTransfer.Status) {
		transfer := snapshot.BoxStatus.Lane.LastTransfer
		items = append(items, HomeActivityItem{
			ID:          "lane.recent." + transfer.BatchID,
			Domain:      "Storage",
			Title:       "Lane transfer",
			Status:      firstNonEmpty(transfer.Status, "completed"),
			Detail:      fmt.Sprintf("%d files, %s", transfer.FileCount, storageFormatBytes(transfer.TotalBytes)),
			StartedAt:   transfer.StartedAt,
			CompletedAt: transfer.CompletedAt,
		})
	}
	if snapshot.Status.Jobs.CompletedRecent > 0 {
		items = append(items, HomeActivityItem{
			ID:         "jobs.completed_recent",
			Domain:     "Jobs",
			Title:      "Jobs completed",
			Status:     "completed",
			Detail:     fmt.Sprintf("%d completed in the last 24h", snapshot.Status.Jobs.CompletedRecent),
			TargetKind: "screen",
			TargetRef:  ScreenJobs,
		})
	}
	if snapshot.Status.Events.Recent > 0 {
		items = append(items, HomeActivityItem{
			ID:         "events.recent",
			Domain:     "Events",
			Title:      "Direct events received",
			Status:     "recent",
			Detail:     fmt.Sprintf("%d received in the last 24h", snapshot.Status.Events.Recent),
			TargetKind: "screen",
			TargetRef:  ScreenAutomations,
		})
	}
	if len(items) > 5 {
		return items[:5]
	}
	return items
}

func homeActivitiesFromTimeline(events []TimelineEvent, limit int) []HomeActivityItem {
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	items := make([]HomeActivityItem, 0, len(events))
	for _, event := range events {
		var progress *float64
		if event.Progress != nil && event.Progress.Total > 0 {
			value := float64(event.Progress.Current) / float64(event.Progress.Total) * 100
			progress = &value
		}
		items = append(items, HomeActivityItem{
			ID:          event.ID,
			Domain:      event.Domain,
			Title:       event.Title,
			Status:      event.Status,
			Detail:      firstNonEmpty(event.Summary, event.TargetRef),
			TargetKind:  event.TargetKind,
			TargetRef:   event.TargetRef,
			Progress:    progress,
			StartedAt:   homeTimePtr(event.StartedAt),
			CompletedAt: event.FinishedAt,
		})
	}
	return items
}

func homeTimelinePreview(data TimelineData, capturedAt time.Time, limit int) []TimelineEvent {
	if limit <= 0 {
		return nil
	}
	now := capturedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	events := make([]TimelineEvent, 0, len(data.Recent)+len(data.Failed)+len(data.Running))
	seen := map[string]bool{}
	add := func(event TimelineEvent) {
		if strings.TrimSpace(event.ID) == "" || seen[event.ID] {
			return
		}
		seen[event.ID] = true
		events = append(events, event)
	}
	for _, event := range data.Failed {
		add(event)
	}
	for _, event := range data.Recent {
		add(event)
	}
	for _, event := range data.Running {
		when := timelineEventSortTime(event)
		if when.IsZero() || now.Sub(when) <= homeFreshRunningWindow {
			add(event)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		left := timelineEventSortTime(events[i])
		right := timelineEventSortTime(events[j])
		if left.Equal(right) {
			return events[i].ID < events[j].ID
		}
		return left.After(right)
	})
	if len(events) > limit {
		events = events[:limit]
	}
	return events
}

func homeTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func renderHomeDashboard(builder *strings.Builder, state ScreenState, data HomeData) {
	renderHomeConnection(builder, data.Connection)
	if len(data.Attention) > 0 {
		renderHomeAttention(builder, data.Attention)
	}
	renderHomeOverview(builder, data)
	if len(data.Recent) > 0 {
		renderHomeActivitySection(builder, "Recent Activity", data.Recent)
	}
	renderHomeNavigation(builder, state)
}

func renderHomeConnection(builder *strings.Builder, items []HomeSummaryItem) {
	renderPrimarySection(builder, "Main Connection")
	for _, item := range items {
		fmt.Fprintf(builder, "  %-12s %s  %s\n",
			item.Label,
			renderStatus(firstNonEmpty(item.Status, "unknown")),
			portalRenderContext().Styles.Muted.Render(firstNonEmpty(item.Detail, "-")),
		)
	}
}

func renderHomeOverview(builder *strings.Builder, data HomeData) {
	renderSummarySection(builder, "Overview")
	if len(data.Attention) == 0 {
		renderEmpty(builder, "No failures.")
	}
	renderHealthStrip(builder, homeHealthItemsForDomain(data.System, portalDomainDiagnostics)...)
	renderHealthStrip(builder, homeHealthItemsForDomain(data.Network, portalDomainNetwork)...)
	renderHealthStrip(builder, homeHealthItemsForDomain(data.Storage, portalDomainStorage)...)
	renderHealthStrip(builder, homeHealthItemsForDomain(data.Runtime, portalDomainJobs)...)
}

func homeHealthItemsForDomain(items []HomeSummaryItem, domain portalDomain) []portalHealthItem {
	result := make([]portalHealthItem, 0, len(items))
	for _, item := range items {
		result = append(result, portalHealthItem{
			Label:  item.Label,
			Status: item.Status,
			Detail: item.Detail,
			Domain: domain,
		})
	}
	return result
}

func renderHomeSummarySection(builder *strings.Builder, title string, items []HomeSummaryItem) {
	renderSection(builder, title)
	if len(items) == 0 {
		renderEmpty(builder, "No data.")
		return
	}
	width := usableWidth(portalRenderContext().Width, 24)
	for _, item := range items {
		detail := trimForWidth(item.Detail, width)
		fmt.Fprintf(builder, "  %-10s %s  %s\n", item.Label, renderStatus(firstNonEmpty(item.Status, "unknown")), portalRenderContext().Styles.Muted.Render(detail))
	}
}

func renderHomeAttention(builder *strings.Builder, items []AttentionItem) {
	renderAttentionSection(builder, "Attention")
	if len(items) == 0 {
		renderEmpty(builder, "No failures.")
		return
	}
	limit := len(items)
	if limit > homeAttentionLimit {
		limit = homeAttentionLimit
	}
	width := usableWidth(portalRenderContext().Width, 8)
	renderEmpty(builder, fmt.Sprintf("Open Doctor for full triage and repair routing across %d issue(s).", len(items)))
	for _, item := range items[:limit] {
		title := trimForWidth(firstNonEmpty(item.Title, item.TargetRef, item.ID), 32)
		reason := trimForWidth(firstNonEmpty(item.Reason, item.TargetRef), width-34)
		fmt.Fprintf(builder, "  %s  %-28s %s\n", renderStatus(item.Severity), title, portalRenderContext().Styles.Muted.Render(reason))
	}
	if len(items) > limit {
		renderEmpty(builder, fmt.Sprintf("%d more attention item(s). Open Doctor for full detail.", len(items)-limit))
	}
}

func renderHomeActivitySection(builder *strings.Builder, title string, items []HomeActivityItem) {
	renderSection(builder, title)
	width := usableWidth(portalRenderContext().Width, 8)
	for _, item := range items {
		detail := item.Detail
		if item.Progress != nil {
			detail = fmt.Sprintf("%.0f%%  %s", *item.Progress, detail)
		}
		renderWrappedPartsLine(builder, "  ", "    ", "  ",
			renderStatus(firstNonEmpty(item.Status, "running")),
			trimForWidth(item.Title, 24),
			portalRenderContext().Styles.Muted.Render(trimForWidth(detail, width-30)),
		)
	}
}

func renderHomeNavigation(builder *strings.Builder, state ScreenState) {
	renderSection(builder, "Navigation")
	row := 0
	for _, group := range ScreenGroups() {
		fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Subsection.Render(group.Title))
		for _, screen := range group.Screens {
			fmt.Fprintf(builder, "%s   %-24s %s\n",
				renderSelectedMarker(row, state.SelectedIndex),
				screen.Title,
				portalRenderContext().Styles.Muted.Render(homeScreenHint(screen.ID)),
			)
			row++
		}
	}
}

func homeScreenHint(screen string) string {
	switch NormalizeScreen(screen) {
	case ScreenHome:
		return "dashboard"
	case ScreenDoctor:
		return "issues, repair"
	case ScreenTimeline:
		return "operations"
	case ScreenBox:
		return "box, workspace lane, watches"
	case ScreenStorage:
		return "main storage"
	case ScreenProjects:
		return "projects"
	case ScreenNotes:
		return "notes, files"
	case ScreenDatabase:
		return "objects"
	case ScreenBackground:
		return "workers"
	case ScreenAutomations:
		return "schedules, events"
	case ScreenJobs:
		return "jobs, indexes"
	case ScreenNodes:
		return "nodes, roots"
	case ScreenCapabilities:
		return "providers"
	default:
		return ""
	}
}

func homeStatusNeedsAttention(status string) bool {
	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case "", "-", "ok", "healthy", "running", "ready", "active", "enabled", "online", "reachable", "complete", "completed", "succeeded", "success", "accepted", lane.BatchStatusCataloged, lane.BatchStatusPublishedStorageView, lane.BatchStatusLocalCleanupDone, "empty", lane.StatePending:
		return false
	default:
		return true
	}
}

func homeSeverityForStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "critical", "fatal", "unhealthy", "failed", "error", lane.BatchStatusPromotionFailed, lane.BatchStatusCatalogFailed, lane.BatchStatusSourceCleanupFailed, lane.BatchStatusObsoleteAcceptedMigrationRequired:
		return "error"
	case "warning", "warn", "degraded", "stale", "missed", "partial", "manual_action":
		return "warning"
	default:
		return "warning"
	}
}

func homeDiagnosticNeedsAttention(severity string) bool {
	switch strings.TrimSpace(strings.ToLower(severity)) {
	case "warning", "warn", "error", "critical", "fatal":
		return true
	default:
		return false
	}
}

func homeTerminalActivityStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "accepted", lane.BatchStatusCataloged, lane.BatchStatusPublishedStorageView,
		lane.BatchStatusPromotionFailed, lane.BatchStatusAcceptedOnMain, lane.BatchStatusCatalogFailed,
		lane.BatchStatusSourceCleanupFailed, lane.BatchStatusObsoleteAcceptedMigrationRequired,
		lane.BatchStatusLocalCleanupWithheld, lane.BatchStatusLocalCleanupDone,
		"completed", "complete", "succeeded", "success", "failed", "cancelled", "canceled", "error", lane.BatchStatusSuperseded:
		return true
	default:
		return false
	}
}

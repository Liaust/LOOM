package portal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/activitysummary"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/search"
)

const (
	timelineRunningLimit         = 8
	timelineAttentionLimit       = 8
	timelineEventLimit           = 40
	timelineActiveEventWindow    = 30 * time.Minute
	timelineActiveProgressWindow = 10 * time.Minute
)

type TimelineBuildInput struct {
	Snapshot    Snapshot
	Background  BackgroundData
	Jobs        JobsData
	Automations AutomationsData
	Storage     StorageData
}

func BuildTimelineData(input TimelineBuildInput) TimelineData {
	events := []TimelineEvent{}
	seen := map[string]bool{}
	add := func(event TimelineEvent) {
		event.ID = strings.TrimSpace(event.ID)
		if event.ID == "" || seen[event.ID] {
			return
		}
		seen[event.ID] = true
		event.Domain = firstNonEmpty(event.Domain, "system")
		event.Kind = firstNonEmpty(event.Kind, "operation")
		event.Title = firstNonEmpty(event.Title, titleFromToken(event.Kind))
		event.Status = firstNonEmpty(event.Status, "unknown")
		if event.Payload == nil {
			event.Payload = timelineEventPayload(event)
		}
		events = append(events, event)
	}

	addJobsToTimeline(add, input)
	addWorkersToTimeline(add, input)
	addStorageToTimeline(add, input)
	addIndexesToTimeline(add, input)
	addAutomationsToTimeline(add, input)
	addSnapshotSummariesToTimeline(add, input.Snapshot)

	sort.SliceStable(events, func(i, j int) bool {
		left := timelineEventSortTime(events[i])
		right := timelineEventSortTime(events[j])
		if left.Equal(right) {
			return events[i].ID < events[j].ID
		}
		return left.After(right)
	})

	data := TimelineData{Snapshot: input.Snapshot, Events: events}
	now := input.Snapshot.CapturedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for _, event := range events {
		if timelineEventRunningNow(event, now) {
			data.Running = append(data.Running, event)
			continue
		}
		data.Recent = append(data.Recent, event)
	}
	data.Running = limitTimelineEvents(data.Running, timelineRunningLimit)
	data.Recent = limitTimelineEvents(data.Recent, timelineEventLimit)
	data.Failed = limitTimelineEvents(timelineAttentionEvents(data.Recent), timelineAttentionLimit)
	return data
}

func addJobsToTimeline(add func(TimelineEvent), input TimelineBuildInput) {
	seen := map[string]bool{}
	addJob := func(kind string, job jobs.Job) {
		ref := strings.TrimSpace(job.JobID)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		started := firstTimelineTime(job.StartedAt, job.QueuedAt, &job.CreatedAt)
		finished := firstTimelineTimePtr(job.CompletedAt, job.FailedAt, job.CancelledAt)
		targetKind := ""
		if job.TargetKind != nil {
			targetKind = *job.TargetKind
		}
		targetRef := ""
		if job.TargetID != nil {
			targetRef = *job.TargetID
		}
		errorCode := ""
		if job.FailureCode != nil {
			errorCode = *job.FailureCode
		}
		errorMessage := ""
		if job.FailureMessage != nil {
			errorMessage = *job.FailureMessage
		}
		add(TimelineEvent{
			ID:            "job." + ref,
			Domain:        "Jobs",
			Kind:          kind,
			Title:         firstNonEmpty(titleFromToken(job.JobType), "Job"),
			Status:        job.Status,
			StartedAt:     started,
			FinishedAt:    finished,
			Progress:      timelineProgressFromJSON(job.ProgressJSON),
			TargetKind:    firstNonEmpty(targetKind, "job"),
			TargetRef:     firstNonEmpty(targetRef, ref),
			Summary:       fmt.Sprintf("attempt %d/%d", job.AttemptCount, job.MaxAttempts),
			ErrorCode:     errorCode,
			ErrorMessage:  errorMessage,
			RelatedAction: "job.inspect",
			Payload:       jobPayload(job),
		})
	}
	for _, job := range input.Jobs.Jobs {
		addJob("job", job)
	}
	for _, job := range input.Jobs.QueuedJobs {
		addJob("queued_job", job)
	}
	for _, job := range input.Jobs.FailedJobs {
		addJob("failed_job", job)
	}
}

func addWorkersToTimeline(add func(TimelineEvent), input TimelineBuildInput) {
	for workerKey, runs := range input.Background.WorkerRunsByWorker {
		for _, run := range runs {
			ref := firstNonEmpty(run.WorkerRunID, run.CorrelationID)
			if ref == "" {
				continue
			}
			add(TimelineEvent{
				ID:            "worker_run." + ref,
				Domain:        "Workers",
				Kind:          "worker_run",
				Title:         firstNonEmpty(titleFromToken(run.WorkerKind), workerKey),
				Status:        run.RunStatus,
				StartedAt:     run.StartedAt,
				FinishedAt:    run.FinishedAt,
				TargetKind:    "worker",
				TargetRef:     workerKey,
				Summary:       firstNonEmpty(run.TriggerKind, "worker run"),
				ErrorMessage:  timelineCompactJSON(run.ErrorJSON),
				RelatedAction: "worker.runs.inspect",
				CorrelationID: run.CorrelationID,
			})
		}
	}
	workers := input.Background.Workers
	if len(workers) == 0 {
		workers = input.Snapshot.Workers
	}
	for _, worker := range workers {
		if worker.CurrentRunID == nil || strings.TrimSpace(*worker.CurrentRunID) == "" {
			continue
		}
		add(TimelineEvent{
			ID:            "worker_run." + *worker.CurrentRunID,
			Domain:        "Workers",
			Kind:          "worker_run",
			Title:         firstNonEmpty(worker.DisplayName, worker.WorkerKey),
			Status:        "running",
			TargetKind:    "worker",
			TargetRef:     firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID),
			Summary:       "current worker run",
			RelatedAction: "worker.runs.inspect",
		})
	}
}

func addStorageToTimeline(add func(TimelineEvent), input TimelineBuildInput) {
	exportStatus := input.Storage.ExportStatus
	if input.Storage.ExportStatusAvailable || exportStatus.ExportRoot != "" || exportStatus.ViewKey != "" || exportStatus.LastRebuildAt != nil {
		if exportStatus.Refresh != nil {
			if exportStatus.Refresh.Running {
				add(TimelineEvent{
					ID:         "storage_export.refresh.running",
					Domain:     "Storage",
					Kind:       "storage_export_refresh",
					Title:      "Storage export refresh",
					Status:     "running",
					StartedAt:  timelineTimeValue(exportStatus.Refresh.LastStartedAt),
					TargetKind: "storage_export",
					TargetRef:  firstNonEmpty(exportStatus.ViewKey, "main"),
					Summary:    fmt.Sprintf("%d hints", len(exportStatus.Refresh.ChangedHints)+len(exportStatus.Refresh.ChangedEntryIDs)+len(exportStatus.Refresh.ChangedPrefixes)),
				})
			}
			if exportStatus.Refresh.LastFinishedAt != nil {
				status := firstNonEmpty(exportStatus.Refresh.State, "completed")
				if exportStatus.Refresh.LastError != "" {
					status = "failed"
				}
				add(TimelineEvent{
					ID:           "storage_export.refresh.last",
					Domain:       "Storage",
					Kind:         "storage_export_refresh",
					Title:        "Storage export refresh",
					Status:       status,
					StartedAt:    timelineTimeValue(exportStatus.Refresh.LastStartedAt),
					FinishedAt:   exportStatus.Refresh.LastFinishedAt,
					TargetKind:   "storage_export",
					TargetRef:    firstNonEmpty(exportStatus.ViewKey, "main"),
					Summary:      "last export refresh",
					ErrorMessage: exportStatus.Refresh.LastError,
				})
			}
		}
		if exportStatus.LastRebuildAt != nil {
			add(TimelineEvent{
				ID:         "storage_export.rebuild.last",
				Domain:     "Storage",
				Kind:       "storage_export_rebuild",
				Title:      "Storage export rebuilt",
				Status:     "completed",
				StartedAt:  *exportStatus.LastRebuildAt,
				FinishedAt: exportStatus.LastRebuildAt,
				TargetKind: "storage_export",
				TargetRef:  firstNonEmpty(exportStatus.ViewKey, "main"),
				Summary:    fmt.Sprintf("%d entries, %d files", exportStatus.Counts.Entries, exportStatus.Counts.Files),
			})
		}
	}

	laneStatus := input.Storage.LaneStatus
	if laneStatus == nil {
		laneStatus = input.Snapshot.BoxStatus.Lane
	}
	if laneStatus != nil {
		if laneStatus.LastTransfer != nil {
			transfer := laneStatus.LastTransfer
			status := firstNonEmpty(transfer.Status, laneStatus.State)
			add(TimelineEvent{
				ID:         "lane.transfer." + firstNonEmpty(transfer.BatchID, status),
				Domain:     "Storage",
				Kind:       "lane_transfer",
				Title:      "LOOM Lane push",
				Status:     status,
				StartedAt:  timelineTimeValue(transfer.StartedAt),
				FinishedAt: transfer.CompletedAt,
				Progress:   timelineLaneProgress(transfer),
				TargetKind: "lane",
				TargetRef:  firstNonEmpty(transfer.VisibleStoragePath, laneStatus.LaneRelativePath, "loom-lane"),
				Summary:    fmt.Sprintf("%d files, %s", transfer.FileCount, storageFormatBytes(transfer.TotalBytes)),
			})
		}
		if laneStatus.PendingItems > 0 {
			add(TimelineEvent{
				ID:         "lane.pending",
				Domain:     "Storage",
				Kind:       "lane_pending",
				Title:      "LOOM Lane pending",
				Status:     lane.StatePending,
				StartedAt:  laneStatus.InspectedAt,
				Progress:   &OperationProgress{Current: laneStatus.PendingBytes, Total: laneStatus.PendingBytes, Unit: "bytes", Label: storageFormatBytes(laneStatus.PendingBytes)},
				TargetKind: "lane",
				TargetRef:  firstNonEmpty(laneStatus.LaneRelativePath, "loom-lane"),
				Summary:    fmt.Sprintf("%d pending items", laneStatus.PendingItems),
			})
		}
	}

	cloudStatus := input.Storage.CloudStatus
	if cloudStatus.Status == "" {
		cloudStatus = input.Background.CloudStatus
	}
	if cloudStatus.Status != "" {
		add(TimelineEvent{
			ID:         "cloud.status",
			Domain:     "Cloud",
			Kind:       "cloud_status",
			Title:      "Cloud storage status",
			Status:     cloudStatus.Status,
			StartedAt:  cloudStatus.CheckedAt,
			FinishedAt: &cloudStatus.CheckedAt,
			TargetKind: "cloud",
			TargetRef:  firstNonEmpty(cloudStatus.Config.RemoteName, cloudStatus.Config.Provider),
			Summary:    firstNonEmpty(cloudStatus.Mode, cloudStatus.Config.SnapshotBackend),
		})
		if cloudStatus.SnapshotStore != nil {
			add(TimelineEvent{
				ID:         "cloud.snapshot_store",
				Domain:     "Cloud",
				Kind:       "cloud_snapshot",
				Title:      "Cloud snapshot store",
				Status:     firstNonEmpty(cloudStatus.SnapshotStore.Status, "unknown"),
				StartedAt:  cloudStatus.CheckedAt,
				FinishedAt: &cloudStatus.CheckedAt,
				TargetKind: "cloud_snapshot_store",
				TargetRef:  firstNonEmpty(cloudStatus.SnapshotStore.Repository, cloudStatus.Config.RemoteName),
				Summary:    fmt.Sprintf("%d archives", cloudStatus.SnapshotStore.ArchiveCount),
			})
		}
	}
}

func addIndexesToTimeline(add func(TimelineEvent), input TimelineBuildInput) {
	seen := map[string]bool{}
	addIndex := func(kind string, status string, itemStatus string, index searchIndexLike) {
		ref := firstNonEmpty(index.id, index.objectID)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		finished := firstTimelineTimePtr(index.completedAt, index.failedAt)
		add(TimelineEvent{
			ID:            "index." + ref,
			Domain:        "Search",
			Kind:          kind,
			Title:         "Index " + firstNonEmpty(index.indexType, "work"),
			Status:        firstNonEmpty(status, itemStatus, "unknown"),
			StartedAt:     firstTimelineTime(index.startedAt, index.queuedAt, &index.createdAt),
			FinishedAt:    finished,
			TargetKind:    "index_status",
			TargetRef:     ref,
			Summary:       firstNonEmpty(index.objectID, index.sourceKind),
			ErrorCode:     index.errorCode,
			ErrorMessage:  index.errorMessage,
			RelatedAction: "index.inspect",
		})
	}
	for _, item := range input.Jobs.IndexStatuses {
		addIndex("index_status", "", item.Status, searchIndexLikeFromStatus(item))
	}
	for _, item := range input.Jobs.IndexQueue {
		addIndex("index_queue", "queued", item.Status, searchIndexLikeFromStatus(item))
	}
	for _, item := range input.Jobs.IndexFailures {
		addIndex("index_failure", "failed", item.Status, searchIndexLikeFromStatus(item))
	}
	for _, item := range input.Snapshot.IndexFailures {
		addIndex("index_failure", "failed", item.Status, searchIndexLikeFromStatus(item))
	}
}

type searchIndexLike struct {
	id           string
	objectID     string
	sourceKind   string
	indexType    string
	errorCode    string
	errorMessage string
	queuedAt     *time.Time
	startedAt    *time.Time
	completedAt  *time.Time
	failedAt     *time.Time
	createdAt    time.Time
}

func searchIndexLikeFromStatus(status search.IndexStatus) searchIndexLike {
	return searchIndexLike{
		id:           status.IndexStatusID,
		objectID:     status.ObjectID,
		sourceKind:   status.SourceKind,
		indexType:    status.IndexType,
		errorCode:    status.LastErrorCode,
		errorMessage: status.LastErrorMessage,
		queuedAt:     status.QueuedAt,
		startedAt:    status.StartedAt,
		completedAt:  status.CompletedAt,
		failedAt:     status.FailedAt,
		createdAt:    status.CreatedAt,
	}
}

func addAutomationsToTimeline(add func(TimelineEvent), input TimelineBuildInput) {
	for _, fire := range input.Automations.ScheduleFires {
		ref := fire.ScheduleFireID
		if ref == "" {
			continue
		}
		finished := firstTimelineTimePtr(fire.CompletedAt, fire.FailedAt)
		add(TimelineEvent{
			ID:            "schedule_fire." + ref,
			Domain:        "Automations",
			Kind:          "schedule_fire",
			Title:         "Schedule fire",
			Status:        fire.Status,
			StartedAt:     firstTimelineTime(&fire.ScheduledFor, &fire.CreatedAt),
			FinishedAt:    finished,
			TargetKind:    "schedule_fire",
			TargetRef:     ref,
			Summary:       firstNonEmpty(timelineStringPtr(fire.InvocationID), timelineStringPtr(fire.JobID)),
			ErrorCode:     timelineStringPtr(fire.FailureCode),
			ErrorMessage:  timelineStringPtr(fire.FailureMessage),
			RelatedAction: "schedule_fire.inspect",
		})
	}
	addDirectEventsToTimeline(add, input.Automations.DirectEvents)
	addDirectEventsToTimeline(add, input.Automations.DirectEventFailures)
	addInvocationsToTimeline(add, input.Automations.Invocations)
	addInvocationsToTimeline(add, input.Automations.InvocationFailures)
	if len(input.Automations.DirectEventFailures) == 0 {
		addDirectEventsToTimeline(add, input.Snapshot.DirectEventFailures)
	}
	if len(input.Automations.InvocationFailures) == 0 {
		addInvocationsToTimeline(add, input.Snapshot.InvocationFailures)
	}
}

func addDirectEventsToTimeline(add func(TimelineEvent), events []automation.DirectEvent) {
	for _, event := range events {
		ref := event.DirectEventID
		if ref == "" {
			continue
		}
		finished := event.CompletedAt
		add(TimelineEvent{
			ID:            "direct_event." + ref,
			Domain:        "Automations",
			Kind:          "direct_event",
			Title:         "Direct event",
			Status:        event.Status,
			StartedAt:     event.ReceivedAt,
			FinishedAt:    finished,
			TargetKind:    "direct_event",
			TargetRef:     ref,
			Summary:       firstNonEmpty(event.RequestPath, event.ExternalEventID),
			ErrorCode:     timelineStringPtr(event.FailureCode),
			ErrorMessage:  timelineStringPtr(event.FailureMessage),
			RelatedAction: "direct_event.inspect",
		})
	}
}

func addInvocationsToTimeline(add func(TimelineEvent), invocations []automation.Invocation) {
	for _, invocation := range invocations {
		ref := invocation.InvocationID
		if ref == "" {
			continue
		}
		finished := firstTimelineTimePtr(invocation.CompletedAt, invocation.FailedAt)
		add(TimelineEvent{
			ID:            "invocation." + ref,
			Domain:        "Automations",
			Kind:          "invocation",
			Title:         "Capability invocation",
			Status:        invocation.Status,
			StartedAt:     firstTimelineTime(invocation.StartedAt, &invocation.CreatedAt),
			FinishedAt:    finished,
			TargetKind:    "capability",
			TargetRef:     firstNonEmpty(invocation.TargetCapability, ref),
			Summary:       fmt.Sprintf("attempt %d/%d", invocation.AttemptCount, invocation.MaxAttempts),
			ErrorCode:     timelineStringPtr(invocation.FailureCode),
			ErrorMessage:  timelineStringPtr(invocation.FailureMessage),
			RelatedAction: "invocation.inspect",
		})
	}
}

func addSnapshotSummariesToTimeline(add func(TimelineEvent), snapshot Snapshot) {
	now := snapshot.CapturedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if snapshot.JobStatus.RunningCount > 0 {
		add(TimelineEvent{ID: "jobs.summary.running", Domain: "Jobs", Kind: "job_summary", Title: "Jobs running", Status: "running", StartedAt: now, TargetKind: "screen", TargetRef: ScreenJobs, Summary: fmt.Sprintf("%d running jobs", snapshot.JobStatus.RunningCount)})
	}
	if jobFailures := activeJobFailureCount(snapshot.JobStatus); jobFailures > 0 {
		add(TimelineEvent{ID: "jobs.summary.failed", Domain: "Jobs", Kind: "job_summary", Title: "Failed jobs", Status: "failed", StartedAt: now, TargetKind: "screen", TargetRef: ScreenJobs, Summary: fmt.Sprintf("%d failed jobs", jobFailures)})
	}
	if snapshot.ScheduleStatus.FailedInvocationCount > 0 || snapshot.ScheduleStatus.MissedFireCount > 0 {
		add(TimelineEvent{ID: "schedules.summary.failed", Domain: "Automations", Kind: "schedule_summary", Title: "Schedule failures", Status: "failed", StartedAt: now, TargetKind: "screen", TargetRef: ScreenAutomations, Summary: fmt.Sprintf("%d failed invocations, %d missed fires", snapshot.ScheduleStatus.FailedInvocationCount, snapshot.ScheduleStatus.MissedFireCount)})
	}
	if lag, ok := schedulePendingInvocationLag(snapshot); ok {
		started := now
		if snapshot.ScheduleStatus.OldestPendingInvocationAt != nil {
			started = *snapshot.ScheduleStatus.OldestPendingInvocationAt
		}
		add(TimelineEvent{ID: "schedules.summary.pending_stale", Domain: "Automations", Kind: "schedule_summary", Title: "Schedule dispatch delayed", Status: "warning", StartedAt: started, TargetKind: "screen", TargetRef: ScreenAutomations, Summary: fmt.Sprintf("%d pending invocation(s), oldest waiting %s", snapshot.ScheduleStatus.PendingInvocationCount, lag.Round(time.Second))})
	}
	if snapshot.DirectEventStatus.FailedCount > 0 {
		add(TimelineEvent{ID: "direct_events.summary.failed", Domain: "Automations", Kind: "direct_event_summary", Title: "Direct event failures", Status: "failed", StartedAt: now, TargetKind: "screen", TargetRef: ScreenAutomations, Summary: fmt.Sprintf("%d failed direct events", snapshot.DirectEventStatus.FailedCount)})
	}
	for _, partial := range snapshot.PartialErrors {
		add(TimelineEvent{ID: "partial." + partial.Source, Domain: "System", Kind: "partial_data", Title: "Partial portal data", Status: "warning", StartedAt: now, TargetKind: "source", TargetRef: partial.Source, Summary: partial.Message})
	}
}

func renderTimeline(builder *strings.Builder, state ScreenState) {
	data := state.Data.Timeline
	if len(data.Events) == 0 && !data.Snapshot.CapturedAt.IsZero() {
		data = BuildTimelineData(TimelineBuildInput{Snapshot: data.Snapshot})
	}
	if len(data.Events) == 0 {
		renderSection(builder, "Timeline")
		renderEmpty(builder, "No timeline events returned by the backend.")
		return
	}
	row := 0
	if len(data.Running) > 0 {
		renderTimelineFlatSection(builder, "Active Now", data.Running, state.SelectedIndex, &row)
	}
	if len(data.Failed) > 0 {
		renderTimelineAttentionSection(builder, data.Failed)
	}
	renderTimelineGroupedSection(builder, "Full History", data.Recent, state.SelectedIndex, &row, data.Snapshot.CapturedAt)
}

func timelineAttentionEvents(events []TimelineEvent) []TimelineEvent {
	result := []TimelineEvent{}
	for _, event := range events {
		if timelineEventNeedsAttention(event) {
			result = append(result, event)
		}
	}
	return result
}

func timelineEventNeedsAttention(event TimelineEvent) bool {
	return homeStatusNeedsAttention(event.Status) || strings.TrimSpace(event.ErrorCode) != "" || strings.TrimSpace(event.ErrorMessage) != ""
}

func renderTimelineAttentionSection(builder *strings.Builder, events []TimelineEvent) {
	renderAttentionSection(builder, "Recent Failures Or Attention")
	if len(events) == 0 {
		renderEmpty(builder, "No recent failures.")
		return
	}
	width := usableWidth(portalRenderContext().Width, 8)
	for _, event := range events {
		detail := firstNonEmpty(event.ErrorMessage, event.Summary, event.TargetRef)
		fmt.Fprintf(builder, "  %s %s  %-24s %s\n",
			renderDomainBadge(timelineEventPortalDomain(event)),
			renderStatus(firstNonEmpty(event.Status, "warning")),
			trimForWidth(event.Title, 24),
			portalRenderContext().Styles.Muted.Render(trimForWidth(detail, width-40)),
		)
	}
}

func renderTimelineFlatSection(builder *strings.Builder, title string, events []TimelineEvent, selected int, row *int) {
	renderSection(builder, title)
	if len(events) == 0 {
		renderEmpty(builder, "None.")
		return
	}
	for _, event := range events {
		renderTimelineEventRow(builder, event, selected, row)
	}
}

func renderTimelineGroupedSection(builder *strings.Builder, title string, events []TimelineEvent, selected int, row *int, capturedAt time.Time) {
	renderSection(builder, title)
	if len(events) == 0 {
		renderEmpty(builder, "No recent operations.")
		return
	}
	now := capturedAt
	if now.IsZero() {
		now = time.Now()
	}
	previousDay := ""
	for _, event := range events {
		when := timelineEventSortTime(event).Local()
		dayKey := ""
		if !when.IsZero() {
			dayKey = when.Format("2006-01-02")
		}
		if dayKey != previousDay {
			previousDay = dayKey
			heading := "Unknown date"
			if !when.IsZero() {
				heading = timelineDateHeading(when, now.Local())
			}
			fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Section.Render(heading))
		}
		renderTimelineEventRow(builder, event, selected, row)
	}
}

func renderTimelineEventRow(builder *strings.Builder, event TimelineEvent, selected int, row *int) {
	marker := renderSelectedMarker(*row, selected)
	timeLabel := timelineDisplayTime(event)
	status := renderStatus(event.Status)
	domain := renderDomainBadge(timelineEventPortalDomain(event))
	progress := timelineProgressLabel(event.Progress)
	if progress == "" {
		progress = event.Status
	}
	titleValue := trimForWidth(event.Title, usableWidth(portalRenderContext().Width, 58))
	fmt.Fprintf(builder, "%s %-5s %-12s %-14s %-24s %s\n", marker, timeLabel, status, domain, titleValue, portalRenderContext().Styles.Muted.Render(trimForWidth(progress, 18)))
	if *row == selected {
		detail := firstNonEmpty(event.ErrorMessage, event.Summary, event.TargetRef)
		if detail != "" {
			fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Muted.Render(trimForWidth(detail, usableWidth(portalRenderContext().Width, 4))))
		}
	}
	*row = *row + 1
}

func timelineEventPortalDomain(event TimelineEvent) portalDomain {
	domain := strings.TrimSpace(strings.ToLower(firstNonEmpty(event.Domain, event.Kind)))
	switch domain {
	case "notes", "note", "search", "index", "indexes":
		return portalDomainNotes
	case "storage", "box", "lane":
		return portalDomainStorage
	case "project", "projects":
		return portalDomainProjects
	case "automation", "automations", "events", "direct_events":
		return portalDomainAutomation
	case "job", "jobs", "worker", "workers":
		return portalDomainJobs
	case "network", "node", "nodes":
		return portalDomainNetwork
	case "backup", "cloud":
		return portalDomainBackup
	default:
		return portalDomainDiagnostics
	}
}

func NewTimelineEventInspectAction(event TimelineEvent) PortalAction {
	payload := timelineEventPayload(event)
	action := NewPortalRecordInspectAction("timeline", ScreenTimeline, "timeline_event", event.ID, event.Title, payload)
	action.ID = fmt.Sprintf("timeline.%s.inspect", safeActionID(event.ID))
	action.Label = "Inspect Timeline Event"
	action.Description = "Inspect this timeline event status, progress, target, and related action."
	action.TargetKind = "timeline_event"
	action.TargetRef = event.ID
	action.TargetLabel = firstNonEmpty(event.Title, event.ID)
	action.Executor = PortalActionExecutor{Kind: PortalExecutorRecordInspect, Target: event.ID, Payload: payload}
	action.RawDetails = payload
	action.RefreshScreen = ScreenTimeline
	return action
}

func timelineEventPayload(event TimelineEvent) map[string]string {
	payload := map[string]string{
		"event_id":       event.ID,
		"domain":         event.Domain,
		"kind":           event.Kind,
		"title":          event.Title,
		"status":         event.Status,
		"started_at":     timeOrDash(event.StartedAt),
		"finished_at":    timePtrOrDash(event.FinishedAt),
		"target_kind":    event.TargetKind,
		"target_ref":     event.TargetRef,
		"summary":        event.Summary,
		"error_code":     event.ErrorCode,
		"error_message":  event.ErrorMessage,
		"related_action": event.RelatedAction,
		"correlation_id": event.CorrelationID,
	}
	if event.Progress != nil {
		payload["progress"] = timelineProgressLabel(event.Progress)
		payload["progress_current"] = fmt.Sprintf("%d", event.Progress.Current)
		payload["progress_total"] = fmt.Sprintf("%d", event.Progress.Total)
		payload["progress_unit"] = event.Progress.Unit
		payload["progress_updated_at"] = timePtrOrDash(event.Progress.UpdatedAt)
	}
	return payload
}

func timelineEventSortTime(event TimelineEvent) time.Time {
	if event.FinishedAt != nil && !event.FinishedAt.IsZero() {
		return *event.FinishedAt
	}
	if !event.StartedAt.IsZero() {
		return event.StartedAt
	}
	return time.Time{}
}

func timelineEventRunning(event TimelineEvent) bool {
	status := strings.TrimSpace(strings.ToLower(event.Status))
	if activitysummary.NormalizeState(status) == "running" {
		return true
	}
	switch status {
	case "leased", "calling", "waiting_approval", "pending_invocation", "invocation_created", "mapping_pending", "transferring":
		return true
	default:
		return false
	}
}

func timelineEventRunningNow(event TimelineEvent, now time.Time) bool {
	if !timelineEventRunning(event) {
		return false
	}
	if event.FinishedAt != nil && !event.FinishedAt.IsZero() {
		return false
	}
	activityAt := timelineEventActivityTime(event)
	if activityAt.IsZero() {
		return true
	}
	window := timelineActiveEventWindow
	if event.Progress != nil && event.Progress.UpdatedAt != nil && !event.Progress.UpdatedAt.IsZero() {
		window = timelineActiveProgressWindow
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !now.Before(activityAt) && now.Sub(activityAt) <= window
}

func timelineEventActivityTime(event TimelineEvent) time.Time {
	if event.Progress != nil && event.Progress.UpdatedAt != nil && !event.Progress.UpdatedAt.IsZero() {
		return *event.Progress.UpdatedAt
	}
	if !event.StartedAt.IsZero() {
		return event.StartedAt
	}
	return time.Time{}
}

func timelineEventFailed(event TimelineEvent) bool {
	status := strings.TrimSpace(strings.ToLower(event.Status))
	switch status {
	case "failed", "error", "unhealthy", "timed_out", "timeout", "cancelled", "canceled", "warning", automation.FireStatusMissed, automation.FireStatusRequiresManualAction:
		return true
	default:
		return strings.TrimSpace(event.ErrorMessage) != "" || strings.TrimSpace(event.ErrorCode) != ""
	}
}

func timelineDisplayTime(event TimelineEvent) string {
	when := timelineEventSortTime(event)
	if when.IsZero() {
		return "--:--"
	}
	return when.Local().Format("15:04")
}

func timelineDateHeading(day time.Time, now time.Time) string {
	day = day.Local()
	now = now.Local()
	dayDate := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	nowDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case dayDate.Equal(nowDate):
		return "Today, " + day.Format("Jan 2, 2006")
	case dayDate.Equal(nowDate.AddDate(0, 0, -1)):
		return "Yesterday, " + day.Format("Jan 2, 2006")
	default:
		return day.Format("Monday, Jan 2, 2006")
	}
}

func timelineProgressLabel(progress *OperationProgress) string {
	if progress == nil {
		return ""
	}
	if progress.Label != "" {
		return progress.Label
	}
	if progress.Total > 0 {
		percent := float64(progress.Current) / float64(progress.Total) * 100
		return fmt.Sprintf("%.0f%%", percent)
	}
	if progress.Current > 0 {
		return fmt.Sprintf("%d %s", progress.Current, firstNonEmpty(progress.Unit, "done"))
	}
	return ""
}

func timelineLaneProgress(transfer *lane.TransferSummary) *OperationProgress {
	if transfer == nil {
		return nil
	}
	if transfer.Progress.Observed {
		label := storageFormatBytes(transfer.Progress.BytesTransferred)
		if transfer.Progress.TotalBytes > 0 {
			label = fmt.Sprintf("%.0f%%", transfer.Progress.Percent)
		}
		return &OperationProgress{
			Current:   transfer.Progress.BytesTransferred,
			Total:     firstNonZeroInt64(transfer.Progress.TotalBytes, transfer.TotalBytes),
			Unit:      "bytes",
			Label:     label,
			UpdatedAt: transfer.Progress.UpdatedAt,
		}
	}
	if transfer.TotalBytes > 0 {
		return &OperationProgress{Current: transfer.TotalBytes, Total: transfer.TotalBytes, Unit: "bytes", Label: storageFormatBytes(transfer.TotalBytes)}
	}
	return nil
}

func timelineProgressFromJSON(raw json.RawMessage) *OperationProgress {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	current := timelineJSONNumber(value, "current", "completed", "done", "bytes_transferred")
	total := timelineJSONNumber(value, "total", "count", "bytes_total", "total_bytes")
	label := timelineJSONString(value, "label", "status")
	unit := timelineJSONString(value, "unit")
	if current == 0 && total == 0 && label == "" {
		percent := timelineJSONNumber(value, "percent")
		if percent > 0 {
			return &OperationProgress{Current: percent, Total: 100, Unit: "percent", Label: fmt.Sprintf("%d%%", percent)}
		}
		return nil
	}
	return &OperationProgress{Current: current, Total: total, Unit: unit, Label: label}
}

func timelineJSONNumber(value map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch typed := value[key].(type) {
		case float64:
			return int64(typed)
		case int64:
			return typed
		case int:
			return int64(typed)
		case string:
			var parsed int64
			if _, err := fmt.Sscanf(typed, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func timelineJSONString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if typed, ok := value[key].(string); ok {
			return typed
		}
	}
	return ""
}

func timelineCompactJSON(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" || value == "{}" {
		return ""
	}
	if len(value) > 180 {
		return value[:177] + "..."
	}
	return value
}

func timelineStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func firstTimelineTime(values ...*time.Time) time.Time {
	for _, value := range values {
		if value != nil && !value.IsZero() {
			return *value
		}
	}
	return time.Time{}
}

func firstTimelineTimePtr(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil && !value.IsZero() {
			return value
		}
	}
	return nil
}

func timelineTimeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func limitTimelineEvents(events []TimelineEvent, limit int) []TimelineEvent {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	return events[:limit]
}

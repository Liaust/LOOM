package portal

import (
	"context"
	"fmt"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/search"
	loomstatus "loom.local/loom/internal/status"
	"loom.local/loom/internal/update"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type Snapshot struct {
	MainAvailability        MainAvailability
	Health                  health.Report
	Status                  loomstatus.Report
	Workers                 []workers.WorkerListItem
	Maintenance             maintenance.Status
	MaintenanceFindings     []maintenance.Finding
	Schedules               []automation.Schedule
	ScheduleStatus          automation.ScheduleStatus
	DirectEventEndpoints    []automation.DirectEventEndpoint
	DirectEventStatus       automation.DirectEventStatus
	InvocationFailures      []automation.Invocation
	DirectEventFailures     []automation.DirectEvent
	JobStatus               jobs.QueueSummary
	IndexFailures           []search.IndexStatus
	Nodes                   []nodes.Node
	WatchedRoots            []mainwatchedroots.RootStatus
	WatchedRootFindings     []mainwatchedroots.Finding
	BoxStatus               box.Status
	BoxWatchPlan            box.WatchPlan
	BoxWatchStatus          box.WatchStatusResult
	BoxWatchStatusAvailable bool
	UpdateStatus            update.UpdateStatus
	CapturedAt              time.Time
	PartialErrors           []SnapshotError
}

type SnapshotOptions struct {
	BoxResolved    *box.Resolved
	UpdateStateDir string
	MainTransport  MainTransportKind
	MainTarget     string
}

type SnapshotError struct {
	Source  string
	Message string
}

func CollectSnapshot(ctx context.Context, client Client, correlationID string, options ...SnapshotOptions) (Snapshot, error) {
	snapshot := Snapshot{CapturedAt: time.Now().UTC()}
	snapshotOptions := normalizeSnapshotOptions(options...)
	if client == nil {
		return snapshot, ErrMissingClientFor("collect portal snapshot")
	}

	resolvedBox, watchPlanAvailable := collectLocalSnapshotData(&snapshot, snapshotOptions)

	probeCtx, cancelProbe := context.WithTimeout(ctx, mainHealthProbeTimeout)
	healthEnvelope, err := client.Health(probeCtx, correlationID)
	cancelProbe()
	checkedAt := time.Now().UTC()
	if err != nil {
		snapshot.MainAvailability = newMainAvailability(MainAvailabilityOffline, snapshotOptions, checkedAt, "health", err)
		snapshot.PartialErrors = append(snapshot.PartialErrors, SnapshotError{
			Source:  "main_availability",
			Message: snapshot.MainAvailability.Summary,
		})
		return snapshot, nil
	}
	snapshot.Health = healthEnvelope.Data

	statusEnvelope, err := client.Status(ctx, correlationID)
	if err != nil {
		snapshot.MainAvailability = newMainAvailability(MainAvailabilityDegraded, snapshotOptions, time.Now().UTC(), "status", err)
		snapshot.addPartial("status", fmt.Errorf("status check failed: %w", err))
	} else {
		snapshot.Status = statusEnvelope.Data
		snapshot.MainAvailability = newMainAvailability(MainAvailabilityOnline, snapshotOptions, time.Now().UTC(), "status", nil)
	}

	if envelope, err := client.ListWorkers(ctx, correlationID, workers.WorkerFilter{Limit: 100}); err != nil {
		snapshot.addMainPartial("workers", err, snapshotOptions)
	} else {
		snapshot.Workers = envelope.Data
	}
	if envelope, err := client.MaintenanceStatus(ctx, correlationID); err != nil {
		snapshot.addMainPartial("maintenance", err, snapshotOptions)
	} else {
		snapshot.Maintenance = envelope.Data
	}
	if envelope, err := client.ListMaintenanceFindings(ctx, correlationID, maintenance.FindingFilter{Status: "open", Limit: 20}); err != nil {
		snapshot.addMainPartial("maintenance_findings", err, snapshotOptions)
	} else {
		snapshot.MaintenanceFindings = envelope.Data
	}
	if envelope, err := client.ListSchedules(ctx, correlationID, automation.ScheduleFilter{Limit: 20}); err != nil {
		snapshot.addMainPartial("schedule_list", err, snapshotOptions)
	} else {
		snapshot.Schedules = envelope.Data
	}
	if envelope, err := client.ScheduleStatus(ctx, correlationID); err != nil {
		snapshot.addMainPartial("schedules", err, snapshotOptions)
	} else {
		snapshot.ScheduleStatus = envelope.Data
	}
	if envelope, err := client.ListDirectEventEndpoints(ctx, correlationID, automation.DirectEventEndpointFilter{Limit: 20}); err != nil {
		snapshot.addMainPartial("direct_event_endpoints", err, snapshotOptions)
	} else {
		snapshot.DirectEventEndpoints = envelope.Data
	}
	if envelope, err := client.DirectEventStatus(ctx, correlationID); err != nil {
		snapshot.addMainPartial("direct_events", err, snapshotOptions)
	} else {
		snapshot.DirectEventStatus = envelope.Data
	}
	if envelope, err := client.ListInvocationFailures(ctx, correlationID, automation.InvocationFilter{Limit: 20}); err != nil {
		snapshot.addMainPartial("invocation_failures", err, snapshotOptions)
	} else {
		snapshot.InvocationFailures = envelope.Data
	}
	if envelope, err := client.ListDirectEventFailures(ctx, correlationID, automation.DirectEventFilter{Limit: 20}); err != nil {
		snapshot.addMainPartial("direct_event_failures", err, snapshotOptions)
	} else {
		snapshot.DirectEventFailures = envelope.Data
	}
	if envelope, err := client.JobStatus(ctx, correlationID); err != nil {
		snapshot.addMainPartial("jobs", err, snapshotOptions)
	} else {
		snapshot.JobStatus = envelope.Data
	}
	if envelope, err := client.ListIndexFailures(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		snapshot.addMainPartial("index_failures", err, snapshotOptions)
	} else {
		snapshot.IndexFailures = envelope.Data
	}
	if envelope, err := client.ListNodes(ctx, correlationID, 50); err != nil {
		snapshot.addMainPartial("nodes", err, snapshotOptions)
	} else {
		snapshot.Nodes = envelope.Data
	}
	if envelope, err := client.ListWatchedRootStatus(ctx, correlationID, mainwatchedroots.StatusFilter{}); err != nil {
		snapshot.addMainPartial("watched_roots", err, snapshotOptions)
	} else {
		snapshot.WatchedRoots = envelope.Data
	}
	if envelope, err := client.ListWatchedRootFindings(ctx, correlationID, mainwatchedroots.FindingFilter{Status: "open", Limit: 20}); err != nil {
		snapshot.addMainPartial("watched_root_findings", err, snapshotOptions)
	} else {
		snapshot.WatchedRootFindings = envelope.Data
	}
	if resolvedBox != nil && watchPlanAvailable {
		envelope, err := client.GetBoxWatchStatus(ctx, correlationID, box.WatchStatusInput{Resolved: *resolvedBox, Plan: &snapshot.BoxWatchPlan})
		if err != nil {
			snapshot.addMainPartial("box_watch_status", err, snapshotOptions)
		} else {
			snapshot.BoxWatchStatus = envelope.Data
			snapshot.BoxWatchStatusAvailable = true
		}
	}

	return snapshot, nil
}

func collectLocalSnapshotData(snapshot *Snapshot, options SnapshotOptions) (*box.Resolved, bool) {
	snapshot.UpdateStatus = update.Status(update.StatusInput{StateDir: options.UpdateStateDir, Limit: 20})
	resolved, err := resolvePortalBox(options)
	if err != nil {
		snapshot.addPartial("box", err)
		return nil, false
	}
	snapshot.BoxStatus = portalBoxStatus(box.Inspect(resolved))
	if snapshot.BoxStatus.State != "ok" {
		return &resolved, false
	}
	plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	snapshot.BoxWatchPlan = plan
	if err != nil {
		snapshot.addPartial("box_watch_plan", err)
		return &resolved, false
	}
	return &resolved, true
}

func normalizeSnapshotOptions(options ...SnapshotOptions) SnapshotOptions {
	if len(options) == 0 {
		return SnapshotOptions{}
	}
	return options[0]
}

func (s *Snapshot) addPartial(source string, err error) {
	s.PartialErrors = append(s.PartialErrors, SnapshotError{Source: source, Message: err.Error()})
}

func (s *Snapshot) addMainPartial(source string, err error, options SnapshotOptions) {
	s.addPartial(source, err)
	if s.MainAvailability.State == MainAvailabilityOnline {
		s.MainAvailability = newMainAvailability(MainAvailabilityDegraded, options, time.Now().UTC(), source, err)
	}
}

func (s Snapshot) AttentionCount() int {
	return s.DegradedWorkerCount() +
		s.Maintenance.Findings.Open +
		len(s.MaintenanceFindings) +
		activeJobFailureCount(s.JobStatus) +
		s.JobStatus.ManualActionCount +
		len(s.IndexFailures) +
		int(s.ScheduleStatus.FailedInvocationCount) +
		int(s.DirectEventStatus.FailedCount) +
		len(s.InvocationFailures) +
		len(s.DirectEventFailures) +
		len(s.WatchedRootFindings)
}

func (s Snapshot) DegradedWorkerCount() int {
	count := 0
	for _, worker := range s.Workers {
		if workerNeedsOperationalAttention(worker) {
			count++
		}
	}
	return count
}

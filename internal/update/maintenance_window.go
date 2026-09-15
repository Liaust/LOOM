package update

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/requestctx"
)

type NoopMaintenanceCoordinator struct {
	Reason string
}

func (c NoopMaintenanceCoordinator) Open(_ context.Context, input MaintenanceOpenInput) (MaintenanceWindow, error) {
	now := currentTime(input.Now)
	finished := now
	reason := strings.TrimSpace(c.Reason)
	if reason == "" {
		reason = "No maintenance coordinator was configured."
	}
	return MaintenanceWindow{
		SchemaVersion: MaintenanceWindowSchemaVersion,
		WindowID:      maintenanceWindowID(input.UpdateID, now),
		UpdateID:      strings.TrimSpace(input.UpdateID),
		Status:        MaintenanceWindowStatusSkipped,
		StartedAt:     now,
		FinishedAt:    &finished,
		PausePolicy:   normalizeMaintenancePausePolicy(input.Policy),
		Diagnostics: []UpdateDiagnostic{{
			Severity: DiagnosticInfo,
			Code:     "update.maintenance_window_skipped",
			Message:  reason,
		}},
	}, nil
}

func (c NoopMaintenanceCoordinator) Resume(_ context.Context, input MaintenanceResumeInput) (MaintenanceWindow, error) {
	window := input.Window
	if window.SchemaVersion == "" {
		window.SchemaVersion = MaintenanceWindowSchemaVersion
	}
	if window.WindowID == "" {
		window.WindowID = maintenanceWindowID(input.UpdateID, currentTime(input.Now))
	}
	if window.UpdateID == "" {
		window.UpdateID = strings.TrimSpace(input.UpdateID)
	}
	if window.Status == "" {
		window.Status = MaintenanceWindowStatusSkipped
	}
	if maintenanceWindowNeedsResume(&window) {
		reason := strings.TrimSpace(c.Reason)
		if reason == "" {
			reason = "No maintenance coordinator was configured."
		}
		markMaintenanceResumeRequired(&window)
		window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(
			DiagnosticError,
			"update.maintenance_resume_unavailable",
			reason+" Recorded resources still require a real maintenance coordinator to resume.",
		))
		return window, fmt.Errorf("maintenance coordinator is required to resume recorded resources")
	}
	return window, nil
}

type AutomationMaintenanceCoordinator struct {
	Service automation.Service
	Request requestctx.Context
}

func (c AutomationMaintenanceCoordinator) Open(ctx context.Context, input MaintenanceOpenInput) (MaintenanceWindow, error) {
	now := currentTime(input.Now)
	policy := normalizeMaintenancePausePolicy(input.Policy)
	window := MaintenanceWindow{
		SchemaVersion: MaintenanceWindowSchemaVersion,
		WindowID:      maintenanceWindowID(input.UpdateID, now),
		UpdateID:      strings.TrimSpace(input.UpdateID),
		Status:        MaintenanceWindowStatusPausing,
		StartedAt:     now,
		PausePolicy:   policy,
	}
	var failures []string
	if policy.PauseSchedules {
		schedules, err := c.Service.ListSchedulesForMaintenance(ctx, automation.ScheduleStatusActive)
		if err != nil {
			window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticError, "update.maintenance_schedule_list_failed", err.Error()))
			failures = append(failures, err.Error())
		} else {
			for _, schedule := range schedules {
				item := MaintenancePausedItem{
					Kind:           MaintenanceItemKindSchedule,
					Ref:            schedule.ScheduleID,
					DisplayName:    firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID),
					PreviousStatus: schedule.Status,
				}
				detail, err := c.Service.PauseSchedule(ctx, c.Request, schedule.ScheduleID, automation.UpdateScheduleStatusInput{
					Reason:   maintenanceReason(input.UpdateID),
					Metadata: maintenanceMetadata(input.UpdateID, window.WindowID),
				})
				pausedAt := currentTime(input.Now)
				item.PausedAt = &pausedAt
				if err != nil {
					item.PauseStatus = MaintenanceItemStatusFailed
					item.Error = err.Error()
					window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticError, "update.maintenance_schedule_pause_failed", err.Error()))
					failures = append(failures, err.Error())
				} else {
					item.PauseStatus = MaintenanceItemStatusPaused
					item.PausedStatus = detail.Schedule.Status
				}
				window.Schedules = append(window.Schedules, item)
			}
		}
	}
	if policy.PauseDirectEventEndpoints {
		endpoints, err := c.Service.ListDirectEventEndpointsForMaintenance(ctx, automation.DirectEventEndpointStatusActive)
		if err != nil {
			window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticError, "update.maintenance_direct_event_list_failed", err.Error()))
			failures = append(failures, err.Error())
		} else {
			for _, endpoint := range endpoints {
				item := MaintenancePausedItem{
					Kind:           MaintenanceItemKindDirectEventEndpoint,
					Ref:            endpoint.EndpointID,
					DisplayName:    firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID),
					PreviousStatus: endpoint.Status,
				}
				detail, err := c.Service.PauseDirectEventEndpoint(ctx, c.Request, endpoint.EndpointID, automation.UpdateDirectEventEndpointStatusInput{
					Reason:   maintenanceReason(input.UpdateID),
					Metadata: maintenanceMetadata(input.UpdateID, window.WindowID),
				})
				pausedAt := currentTime(input.Now)
				item.PausedAt = &pausedAt
				if err != nil {
					item.PauseStatus = MaintenanceItemStatusFailed
					item.Error = err.Error()
					window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticError, "update.maintenance_direct_event_pause_failed", err.Error()))
					failures = append(failures, err.Error())
				} else {
					item.PauseStatus = MaintenanceItemStatusPaused
					item.PausedStatus = detail.Endpoint.Status
				}
				window.DirectEvents = append(window.DirectEvents, item)
			}
		}
	}
	if policy.PauseOptionalWorkers {
		window.Workers = append(window.Workers, MaintenancePausedItem{
			Kind:           MaintenanceItemKindWorker,
			Ref:            "optional_workers",
			DisplayName:    "Optional workers",
			PreviousStatus: "unknown",
			PauseStatus:    MaintenanceItemStatusUnsupported,
			Error:          "Worker pause/resume service methods are not implemented in v0.5.1 slice 04.5; schedules and direct-event endpoints are quieted.",
		})
		window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticWarning, "update.maintenance_worker_pause_unsupported", "Optional worker pause is recorded as unsupported for this slice."))
	}
	finished := currentTime(input.Now)
	window.FinishedAt = &finished
	if len(failures) > 0 {
		window.Status = MaintenanceWindowStatusFailed
		return window, fmt.Errorf("maintenance window pause failed: %s", strings.Join(failures, "; "))
	}
	if maintenanceWindowPausedCount(window) == 0 {
		window.Status = MaintenanceWindowStatusSkipped
	} else {
		window.Status = MaintenanceWindowStatusPaused
	}
	return window, nil
}

func (c AutomationMaintenanceCoordinator) Resume(ctx context.Context, input MaintenanceResumeInput) (MaintenanceWindow, error) {
	window := input.Window
	if window.SchemaVersion == "" {
		window.SchemaVersion = MaintenanceWindowSchemaVersion
	}
	if window.WindowID == "" {
		window.WindowID = maintenanceWindowID(input.UpdateID, currentTime(input.Now))
	}
	if window.UpdateID == "" {
		window.UpdateID = strings.TrimSpace(input.UpdateID)
	}
	window.Status = MaintenanceWindowStatusResuming
	var failures []string
	for i, item := range window.Schedules {
		if item.PauseStatus != MaintenanceItemStatusPaused || item.ResumeStatus == MaintenanceItemStatusResumed {
			continue
		}
		detail, err := c.Service.ResumeSchedule(ctx, c.Request, item.Ref, automation.UpdateScheduleStatusInput{
			Reason:   maintenanceReason(input.UpdateID),
			Metadata: maintenanceMetadata(input.UpdateID, window.WindowID),
		})
		resumedAt := currentTime(input.Now)
		window.Schedules[i].ResumedAt = &resumedAt
		if err != nil {
			window.Schedules[i].ResumeStatus = MaintenanceItemStatusResumeRequired
			window.Schedules[i].Error = err.Error()
			window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticError, "update.maintenance_schedule_resume_failed", err.Error()))
			failures = append(failures, err.Error())
		} else {
			window.Schedules[i].ResumeStatus = MaintenanceItemStatusResumed
			window.Schedules[i].PausedStatus = detail.Schedule.Status
		}
	}
	for i, item := range window.DirectEvents {
		if item.PauseStatus != MaintenanceItemStatusPaused || item.ResumeStatus == MaintenanceItemStatusResumed {
			continue
		}
		detail, err := c.Service.ResumeDirectEventEndpoint(ctx, c.Request, item.Ref, automation.UpdateDirectEventEndpointStatusInput{
			Reason:   maintenanceReason(input.UpdateID),
			Metadata: maintenanceMetadata(input.UpdateID, window.WindowID),
		})
		resumedAt := currentTime(input.Now)
		window.DirectEvents[i].ResumedAt = &resumedAt
		if err != nil {
			window.DirectEvents[i].ResumeStatus = MaintenanceItemStatusResumeRequired
			window.DirectEvents[i].Error = err.Error()
			window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(DiagnosticError, "update.maintenance_direct_event_resume_failed", err.Error()))
			failures = append(failures, err.Error())
		} else {
			window.DirectEvents[i].ResumeStatus = MaintenanceItemStatusResumed
			window.DirectEvents[i].PausedStatus = detail.Endpoint.Status
		}
	}
	finished := currentTime(input.Now)
	window.FinishedAt = &finished
	if len(failures) > 0 {
		window.Status = MaintenanceWindowStatusResumeRequired
		return window, fmt.Errorf("maintenance window resume failed: %s", strings.Join(failures, "; "))
	}
	if maintenanceWindowPausedCount(window) == 0 {
		window.Status = MaintenanceWindowStatusSkipped
	} else {
		window.Status = MaintenanceWindowStatusResumed
	}
	return window, nil
}

func normalizeMaintenancePausePolicy(policy MaintenancePausePolicy) MaintenancePausePolicy {
	return MaintenancePausePolicy{
		PauseSchedules:            policy.PauseSchedules,
		PauseDirectEventEndpoints: policy.PauseDirectEventEndpoints,
		PauseOptionalWorkers:      policy.PauseOptionalWorkers,
	}
}

func DefaultMaintenancePausePolicy() MaintenancePausePolicy {
	return MaintenancePausePolicy{
		PauseSchedules:            true,
		PauseDirectEventEndpoints: true,
		PauseOptionalWorkers:      true,
	}
}

func maintenanceWindowID(updateID string, now time.Time) string {
	updateID = strings.TrimSpace(updateID)
	if updateID == "" {
		updateID = "update_unknown"
	}
	return updateID + "_maintenance_" + now.UTC().Format("20060102150405")
}

func maintenanceReason(updateID string) string {
	return "loom update " + strings.TrimSpace(updateID) + " maintenance window"
}

func maintenanceMetadata(updateID, windowID string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"source":    "loom_update_maintenance_window",
		"update_id": strings.TrimSpace(updateID),
		"window_id": strings.TrimSpace(windowID),
	})
	return raw
}

func maintenanceDiagnostic(severity, code, message string) UpdateDiagnostic {
	return UpdateDiagnostic{
		Severity: severity,
		Code:     code,
		Message:  message,
	}
}

func maintenanceWindowPausedCount(window MaintenanceWindow) int {
	count := 0
	for _, item := range window.Schedules {
		if item.PauseStatus == MaintenanceItemStatusPaused {
			count++
		}
	}
	for _, item := range window.DirectEvents {
		if item.PauseStatus == MaintenanceItemStatusPaused {
			count++
		}
	}
	for _, item := range window.Workers {
		if item.PauseStatus == MaintenanceItemStatusPaused {
			count++
		}
	}
	return count
}

func maintenanceWindowNeedsResume(window *MaintenanceWindow) bool {
	if window == nil {
		return false
	}
	for _, item := range window.Schedules {
		if item.PauseStatus == MaintenanceItemStatusPaused && item.ResumeStatus != MaintenanceItemStatusResumed {
			return true
		}
	}
	for _, item := range window.DirectEvents {
		if item.PauseStatus == MaintenanceItemStatusPaused && item.ResumeStatus != MaintenanceItemStatusResumed {
			return true
		}
	}
	for _, item := range window.Workers {
		if item.PauseStatus == MaintenanceItemStatusPaused && item.ResumeStatus != MaintenanceItemStatusResumed {
			return true
		}
	}
	return false
}

func markMaintenanceResumeRequired(window *MaintenanceWindow) {
	if window == nil || !maintenanceWindowNeedsResume(window) {
		return
	}
	window.Status = MaintenanceWindowStatusResumeRequired
	window.Diagnostics = append(window.Diagnostics, maintenanceDiagnostic(
		DiagnosticWarning,
		"update.maintenance_resume_required",
		"Some resources were paused by this update and still need to be resumed. Run: loom update maintenance resume --yes",
	))
	for i, item := range window.Schedules {
		if item.PauseStatus == MaintenanceItemStatusPaused && item.ResumeStatus != MaintenanceItemStatusResumed {
			window.Schedules[i].ResumeStatus = MaintenanceItemStatusResumeRequired
		}
	}
	for i, item := range window.DirectEvents {
		if item.PauseStatus == MaintenanceItemStatusPaused && item.ResumeStatus != MaintenanceItemStatusResumed {
			window.DirectEvents[i].ResumeStatus = MaintenanceItemStatusResumeRequired
		}
	}
	for i, item := range window.Workers {
		if item.PauseStatus == MaintenanceItemStatusPaused && item.ResumeStatus != MaintenanceItemStatusResumed {
			window.Workers[i].ResumeStatus = MaintenanceItemStatusResumeRequired
		}
	}
}

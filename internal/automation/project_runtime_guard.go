package automation

import (
	"context"
	"database/sql"

	"loom.local/loom/internal/projects"
)

func ensureScheduleRuntimeActive(ctx context.Context, q projects.RuntimeStatusQuerier, schedule Schedule) error {
	return projects.EnsureRuntimeActive(ctx, q, projects.RuntimeRef{
		ProjectID:    stringPointerValue(schedule.ProjectID),
		ScopeID:      stringPointerValue(schedule.ScopeID),
		ResourceKind: "schedule",
		ResourceRef:  schedule.ScheduleKey,
	})
}

func ensureAutomationRuntimeActive(ctx context.Context, q projects.RuntimeStatusQuerier, automation Automation, resourceKind, resourceRef string) error {
	return projects.EnsureRuntimeActive(ctx, q, projects.RuntimeRef{
		ProjectID:    stringPointerValue(automation.ProjectID),
		ScopeID:      stringPointerValue(automation.ScopeID),
		ResourceKind: resourceKind,
		ResourceRef:  resourceRef,
	})
}

func ensureInvocationRuntimeActive(ctx context.Context, q projects.RuntimeStatusQuerier, invocation Invocation) error {
	return projects.EnsureRuntimeActive(ctx, q, projects.RuntimeRef{
		ProjectID:    stringPointerValue(invocation.ProjectID),
		ScopeID:      stringPointerValue(invocation.ScopeID),
		ResourceKind: "automation_invocation",
		ResourceRef:  invocation.InvocationID,
	})
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ projects.RuntimeStatusQuerier = (*sql.DB)(nil)
var _ projects.RuntimeStatusQuerier = (*sql.Tx)(nil)

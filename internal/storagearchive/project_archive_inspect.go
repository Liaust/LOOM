package storagearchive

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"loom.local/loom/internal/projects"
)

// This is current inspection evidence, not a replacement for the preserved
// reviewed plan. No inventories, registration contents or host paths escape.
type ProjectPhysicalArchiveInspection struct {
	SchemaVersion      string                          `json:"schema_version"`
	ProjectID          string                          `json:"project_id"`
	ArchiveOperationID string                          `json:"archive_operation_id"`
	RestoreOperationID string                          `json:"restore_operation_id,omitempty"`
	OperationID        string                          `json:"operation_id"`
	PlanDigest         string                          `json:"plan_digest"`
	Phase              string                          `json:"phase"`
	Status             string                          `json:"status"`
	MutationBlocked    bool                            `json:"mutation_blocked"`
	ActivationState    WorkspaceActivationState        `json:"activation_state,omitempty"`
	EvidenceStatus     string                          `json:"evidence_status"`
	NextAction         string                          `json:"next_action"`
	Workspace          *WorkspaceMoveInspectionSummary `json:"workspace,omitempty"`
}

type projectWorkspaceInspector interface {
	InspectOperation(context.Context, string) (WorkspaceArchiveInspection, error)
}

func physicalProjectArchiveStatePresent(raw json.RawMessage) bool {
	var header struct {
		SchemaVersion string `json:"schema_version"`
	}
	return json.Unmarshal(raw, &header) == nil && strings.HasPrefix(header.SchemaVersion, "project.physical_")
}

func (s ProjectRuntimeService) inspectProjectPhysicalArchive(ctx context.Context, detail projects.ProjectRegistrationDetail) *ProjectPhysicalArchiveInspection {
	project := detail.Project.Project
	state, valid := projects.ParseProjectPhysicalArchiveState(project.ArchiveState)
	if !valid && !physicalProjectArchiveStatePresent(project.ArchiveState) {
		return nil
	}
	result := &ProjectPhysicalArchiveInspection{
		SchemaVersion: "storage.project_physical_archive_inspection.v1",
		ProjectID:     project.ProjectID, MutationBlocked: true,
		EvidenceStatus: "conflict", NextAction: "inspect_evidence",
	}
	before, beforeErr := canonicalProjectRegistrationDetail(detail)
	if beforeErr != nil || !valid || state.ProjectID != project.ProjectID || state.ProjectSlug != project.Slug {
		return result
	}
	result.ArchiveOperationID = state.OperationID
	result.OperationID, result.PlanDigest = state.OperationID, state.PlanDigest
	result.Phase, result.Status = string(state.Phase), state.Status
	result.NextAction = "recover_archive"
	wantOperation, wantPlan := state.OperationID, state.WorkspacePlanDigest
	wantKind, wantPhase, wantCustody := WorkspaceOperationArchive, PhaseArchiveComplete, CustodyArchived
	wantManifest, completedAt := state.ArchiveManifestDigest, state.WorkspaceCompletedAt
	wantProject, wantRegistration := "active", projects.ProjectRegistrationStatusRegistered
	terminal := state.Phase == projects.ProjectArchivePhaseComplete
	if terminal {
		wantProject, wantRegistration = "archived", projects.ProjectRegistrationStatusArchived
		result.NextAction = "review_restore_plan"
	}
	if state.RuntimeDeactivatedAt != nil {
		result.ActivationState = WorkspaceActivationInactive
	}
	if restore := state.Restore; restore != nil {
		result.RestoreOperationID = restore.OperationID
		result.OperationID, result.PlanDigest = restore.OperationID, restore.PlanDigest
		result.Phase, result.Status = string(restore.Phase), restore.Status
		result.ActivationState = WorkspaceActivationInactive
		result.NextAction = "recover_restore"
		wantOperation, wantPlan = restore.OperationID, restore.WorkspacePlanDigest
		wantKind, wantPhase, wantCustody = WorkspaceOperationRestore, PhaseRestoreComplete, CustodyActive
		wantManifest, completedAt = restore.ActiveManifestDigest, restore.WorkspaceCompletedAt
		terminal = restore.Phase == projects.ProjectRestorePhaseComplete
		if terminal {
			wantProject, wantRegistration = "active", projects.ProjectRegistrationStatusRegistered
			result.NextAction = "activation_not_available"
		}
	}
	if project.Status != wantProject || detail.Registration == nil ||
		detail.Registration.RegistrationStatus != wantRegistration ||
		(result.ActivationState == WorkspaceActivationInactive && detail.Registration.ActivationStatus != projects.ProjectActivationStatusInactive) {
		result.NextAction = "inspect_evidence"
		return result
	}
	inspector, ok := s.WorkspaceMove.(projectWorkspaceInspector)
	if terminal {
		result.NextAction = "inspect_evidence"
	}
	if !ok || inspector == nil || (reflect.ValueOf(inspector).Kind() == reflect.Pointer && reflect.ValueOf(inspector).IsNil()) {
		result.EvidenceStatus = "unavailable"
		return result
	}
	inspection, err := inspector.InspectOperation(ctx, wantOperation)
	if err != nil {
		// Absence before generic intent and unavailable/corrupt evidence are not
		// interchangeable. Do not turn arbitrary inspection errors into success.
		result.EvidenceStatus = "unavailable"
		return result
	}
	after, readErr := s.Projects.GetProjectRegistrationStatus(ctx, state.ProjectID)
	if readErr != nil {
		result.EvidenceStatus = "unavailable"
		return result
	}
	after, readErr = canonicalProjectRegistrationDetail(after)
	if readErr != nil || !reflect.DeepEqual(before, after) {
		result.NextAction = "inspect_evidence"
		return result
	}
	op := inspection.Operation
	planDigest, planErr := WorkspaceArchivePlanDigest(inspection.Plan)
	wantActor, wantSource, wantDestination := state.ActorID, state.ActivePath, state.ArchivePath
	if state.Restore != nil {
		wantActor, wantSource, wantDestination = state.Restore.Request.ActorID, state.ArchivePath, state.ActivePath
	}
	if ValidateWorkspaceArchiveOperation(op) != nil || op.OperationID != wantOperation || op.OperationKind != wantKind ||
		op.PlanDigest != wantPlan || op.ObjectID != state.ProjectID || op.Kind != WorkspaceKindProject || op.Slug != state.ProjectSlug ||
		op.ActorID != wantActor || op.Source.Path.AbsolutePath != wantSource || op.Destination.Path.AbsolutePath != wantDestination ||
		planErr != nil || planDigest != wantPlan || inspection.Plan.OperationKind != wantKind ||
		inspection.Plan.OperationID != op.OperationID || inspection.Plan.PlanDigest != wantPlan ||
		inspection.Plan.ObjectID != op.ObjectID || inspection.Plan.Kind != op.Kind || inspection.Plan.Slug != op.Slug ||
		!reflect.DeepEqual(op.Source, inspection.Plan.Source) || !reflect.DeepEqual(op.Destination, inspection.Plan.Destination) {
		result.NextAction = "inspect_evidence"
		return result
	}
	summary := NewWorkspaceMoveInspectionSummary(inspection)
	result.Workspace = &summary
	result.EvidenceStatus = "pending"
	if completedAt == nil {
		return result
	}
	digest, digestErr := projectWorkspaceManifestDigest(inspection)
	if digestErr != nil || digest != wantManifest || op.Phase != wantPhase || op.Status != OperationStatusComplete ||
		op.CompletedAt == nil || !completedAt.Equal(*op.CompletedAt) || inspection.Custody != wantCustody ||
		(wantKind == WorkspaceOperationRestore && inspection.ActivationState != WorkspaceActivationInactive) || inspection.Manifest == nil ||
		inspection.Manifest.ArchiveOperationID != state.OperationID || inspection.Manifest.ObjectID != state.ProjectID || inspection.Manifest.Slug != state.ProjectSlug ||
		(wantKind == WorkspaceOperationArchive && inspection.Manifest.LifecycleState != WorkspaceLifecycleArchived) ||
		(wantKind == WorkspaceOperationRestore && (inspection.Manifest.RestoreOperationID != wantOperation || inspection.Plan.ArchiveOperationID != state.OperationID || inspection.Manifest.LifecycleState != WorkspaceLifecycleActive)) {
		result.EvidenceStatus, result.NextAction = "conflict", "inspect_evidence"
		return result
	}
	if terminal {
		result.EvidenceStatus = "verified"
		result.NextAction = "review_restore_plan"
		if wantKind == WorkspaceOperationRestore {
			result.NextAction = "activation_not_available"
		}
	}
	return result
}

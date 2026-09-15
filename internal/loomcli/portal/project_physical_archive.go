package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

// Optional extension keeps older/read-only Portal clients source-compatible.
type projectPhysicalClient interface {
	ReviewProjectPhysicalArchive(context.Context, string, string, string) (response.Envelope[storagearchive.ProjectPhysicalPlanReview], error)
	ReviewProjectPhysicalRestore(context.Context, string, string, string) (response.Envelope[storagearchive.ProjectPhysicalPlanReview], error)
	ApplyProjectPhysicalArchive(context.Context, string, string, storagearchive.ProjectPhysicalApplyRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error)
	ApplyProjectPhysicalRestore(context.Context, string, string, storagearchive.ProjectPhysicalApplyRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error)
	RecoverProjectPhysicalArchive(context.Context, string, string, storagearchive.ProjectPhysicalRecoverRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error)
	RecoverProjectPhysicalRestore(context.Context, string, string, storagearchive.ProjectPhysicalRecoverRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error)
}

func newProjectPhysicalMoveAction(detail projects.ProjectRegistrationDetail, screen string, restore bool) PortalAction {
	a := newLegacyProjectArchiveAction(detail, screen)
	a.Label = "Review Archive"
	a.Description = "Review the exact physical move and runtime deactivations before confirmation."
	a.Executor.Payload["physical_archive"] = "true"
	a.InputFields = []PortalActionField{{Name: "reason", Label: "Reason", Kind: ActionFieldText, Required: true}}
	a.InputValues = map[string]string{}
	a.RawCommand = []string{"loom", "project", "archive", "plan", a.TargetRef}
	if restore {
		a.ID += ".restore"
		a.Label = "Review Restore (Inactive)"
		a.Description = "Review the exact return to Box; services and runtime fences remain inactive."
		a.Executor.Kind = PortalExecutorProjectArchiveRestore
		a.RawCommand = []string{"loom", "project", "archive", "restore", "plan", a.TargetRef}
		// Archived custody is the prerequisite for this action, not a rejection.
		if a.TargetRef != "" && detail.Registration != nil {
			a.State, a.DisabledReason = ActionAvailable, ""
		}
	}
	return a
}

func (e ActionExecutor) executeProjectPhysicalMove(ctx context.Context, action PortalAction, restore bool) PortalActionResult {
	client, ok := e.Client.(projectPhysicalClient)
	if !ok {
		return failedActionResult(action, "project_archive.not_ready", "Reviewed project movement is unavailable.")
	}
	ref := action.TargetRef
	reason := strings.TrimSpace(action.InputValues["reason"])
	if ref == "" || reason == "" {
		return failedActionResult(action, "project_archive.invalid_request", "Project and reason are required.")
	}
	kind := storagearchive.WorkspaceOperationArchive
	if restore {
		kind = storagearchive.WorkspaceOperationRestore
	}
	if action.InputValues["physical_review"] == "" {
		var out response.Envelope[storagearchive.ProjectPhysicalPlanReview]
		var err error
		if restore {
			out, err = client.ReviewProjectPhysicalRestore(ctx, e.CorrelationID, ref, reason)
		} else {
			out, err = client.ReviewProjectPhysicalArchive(ctx, e.CorrelationID, ref, reason)
		}
		if err != nil {
			return projectPhysicalPortalResult(action, storagearchive.ProjectPhysicalMutationSummary{}, err)
		}
		review := out.Data
		raw, err := json.Marshal(review)
		if err != nil || len(raw) > storagearchive.MaximumWorkspaceSurfaceRequestBytes || storagearchive.ValidateProjectPhysicalPlanReview(review) != nil || review.Workspace.OperationKind != kind || review.Workspace.Reason != reason {
			return failedActionResult(action, "project_archive.invalid_review", "Invalid bounded review returned.")
		}
		w := review.Workspace
		return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleReview, Summary: "No move has occurred. Review this plan before confirmation; resulting runtime is inactive.", CorrelationID: out.Meta.CorrelationID,
			Fields:    []ActionResultField{{Label: "Project", Value: review.ProjectID}, {Label: "Operation", Value: w.OperationID}, {Label: "Kind", Value: string(w.OperationKind)}, {Label: "Plan digest", Value: review.PlanDigest}, {Label: "Source", Value: string(w.Source.Root) + ":" + w.Source.RelativePath}, {Label: "Destination", Value: string(w.Destination.Root) + ":" + w.Destination.RelativePath}, {Label: "Entries / bytes", Value: fmt.Sprintf("%d / %d", w.InventoryEntries, w.InventoryBytes)}, {Label: "Deactivations", Value: fmt.Sprintf("%d facets / %d actions", review.DeactivationFacets, review.DeactivationActions)}, {Label: "Runtime", Value: "inactive"}},
			NextInput: map[string]string{"physical_review": string(raw), "plan_digest": review.PlanDigest}}
	}
	var review storagearchive.ProjectPhysicalPlanReview
	raw := action.InputValues["physical_review"]
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if len(raw) > storagearchive.MaximumWorkspaceSurfaceRequestBytes || !json.Valid([]byte(raw)) || decoder.Decode(&review) != nil || storagearchive.ValidateProjectPhysicalPlanReview(review) != nil || review.Workspace.OperationKind != kind || review.Workspace.Reason != reason || review.PlanDigest != action.InputValues["plan_digest"] {
		return failedActionResult(action, "project_archive.invalid_review", "Review changed; generate a fresh plan.")
	}
	input := storagearchive.ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true}
	var out response.Envelope[storagearchive.ProjectPhysicalMutationSummary]
	var err error
	if restore {
		out, err = client.ApplyProjectPhysicalRestore(ctx, e.CorrelationID, ref, input)
	} else {
		out, err = client.ApplyProjectPhysicalArchive(ctx, e.CorrelationID, ref, input)
	}
	return projectPhysicalPortalResult(action, out.Data, err)
}

func (e ActionExecutor) executeProjectPhysicalRecovery(ctx context.Context, action PortalAction) PortalActionResult {
	client, ok := e.Client.(projectPhysicalClient)
	if !ok {
		return failedActionResult(action, "project_archive.not_ready", "Reviewed project recovery is unavailable.")
	}
	input := storagearchive.ProjectPhysicalRecoverRequest{OperationID: action.InputValues["operation_id"], PlanDigest: action.InputValues["plan_digest"], Confirm: true}
	var out response.Envelope[storagearchive.ProjectPhysicalMutationSummary]
	var err error
	switch action.InputValues["physical_recovery"] {
	case "recover_archive":
		out, err = client.RecoverProjectPhysicalArchive(ctx, e.CorrelationID, action.TargetRef, input)
	case "recover_restore":
		out, err = client.RecoverProjectPhysicalRestore(ctx, e.CorrelationID, action.TargetRef, input)
	default:
		return failedActionResult(action, "project_archive.invalid_request", "Unknown recovery kind.")
	}
	return projectPhysicalPortalResult(action, out.Data, err)
}

func projectPhysicalPortalResult(action PortalAction, data storagearchive.ProjectPhysicalMutationSummary, err error) PortalActionResult {
	result := PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleSucceeded, Summary: "Reviewed project operation completed; no runtime activation.", RefreshScreen: ScreenProjects}
	if err != nil {
		result.Status, result.Summary, result.ErrorCode = ActionLifecycleFailed, "Request did not complete. Inspect the durable operation before recovery.", "project_archive.outcome_unavailable"
		var requestErr *localclient.RequestError
		if errors.As(err, &requestErr) {
			result.ErrorCode, result.CorrelationID = requestErr.Envelope.Error.Code, requestErr.Envelope.Meta.CorrelationID
		}
	}
	if data.OperationID != "" {
		result.Fields = []ActionResultField{{Label: "Operation", Value: data.OperationID}, {Label: "Plan digest", Value: data.PlanDigest}, {Label: "Phase", Value: data.Phase}, {Label: "Runtime", Value: firstNonEmpty(string(data.ActivationState), "not yet verified inactive")}, {Label: "Mutation blocked", Value: fmt.Sprint(data.MutationBlocked)}, {Label: "Recoverable", Value: fmt.Sprint(data.Recoverable)}, {Label: "Replay", Value: fmt.Sprint(data.Replay)}}
	}
	return result
}

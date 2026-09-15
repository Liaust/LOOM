package localclient

import (
	"context"
	"net/http"
	"net/url"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func (c Client) PlanWorkspaceArchive(ctx context.Context, correlationID string, input storagearchive.WorkspaceArchivePlanRequest) (response.Envelope[storagearchive.WorkspaceMovePlanReview], error) {
	return doJSON[storagearchive.WorkspaceMovePlanReview](c, ctx, http.MethodPost, "/v1/storage/workspace-archive/plan", correlationID, input)
}

func (c Client) ApplyWorkspaceArchive(ctx context.Context, correlationID string, input storagearchive.WorkspaceMoveApplyRequest) (response.Envelope[storagearchive.WorkspaceMoveInspectionSummary], error) {
	return doJSON[storagearchive.WorkspaceMoveInspectionSummary](c, ctx, http.MethodPost, "/v1/storage/workspace-archive/apply", correlationID, input)
}

func (c Client) InspectWorkspaceArchive(ctx context.Context, correlationID, operationID string) (response.Envelope[storagearchive.WorkspaceMoveInspectionSummary], error) {
	query := url.Values{"operation_id": []string{operationID}}
	return doJSON[storagearchive.WorkspaceMoveInspectionSummary](c, ctx, http.MethodGet, "/v1/storage/workspace-archive/inspect?"+query.Encode(), correlationID, nil)
}

func (c Client) RecoverWorkspaceArchive(ctx context.Context, correlationID string, input storagearchive.WorkspaceMoveRecoverRequest) (response.Envelope[storagearchive.WorkspaceMoveInspectionSummary], error) {
	return doJSON[storagearchive.WorkspaceMoveInspectionSummary](c, ctx, http.MethodPost, "/v1/storage/workspace-archive/recover", correlationID, input)
}

func (c Client) PlanWorkspaceRestore(ctx context.Context, correlationID string, input storagearchive.WorkspaceRestorePlanRequest) (response.Envelope[storagearchive.WorkspaceMovePlanReview], error) {
	return doJSON[storagearchive.WorkspaceMovePlanReview](c, ctx, http.MethodPost, "/v1/storage/workspace-archive/restore-plan", correlationID, input)
}

func (c Client) ApplyWorkspaceRestore(ctx context.Context, correlationID string, input storagearchive.WorkspaceMoveApplyRequest) (response.Envelope[storagearchive.WorkspaceMoveInspectionSummary], error) {
	return doJSON[storagearchive.WorkspaceMoveInspectionSummary](c, ctx, http.MethodPost, "/v1/storage/workspace-archive/restore-apply", correlationID, input)
}

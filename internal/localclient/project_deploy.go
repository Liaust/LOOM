package localclient

import (
	"context"
	"net/http"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func (c Client) DeployProject(ctx context.Context, cid string, request pc.ProjectDeploymentRequest) (response.Envelope[pc.ProjectDeploymentResult], error) {
	return declarationJSON[pc.ProjectDeploymentResult](c, ctx, http.MethodPost, "/v1/project-deploy", cid, request)
}

func (c Client) ImportProjectCredential(ctx context.Context, cid string, request pc.ProjectCredentialImportRequest) (response.Envelope[pc.ProjectCredentialImportResult], error) {
	return declarationJSON[pc.ProjectCredentialImportResult](c, ctx, http.MethodPost, "/v1/project-credential-import", cid, request)
}

func (c Client) RunProjectAction(ctx context.Context, cid string, request pc.ProjectActionRequest) (response.Envelope[pc.ProjectActionResult], error) {
	return declarationJSON[pc.ProjectActionResult](c, ctx, http.MethodPost, "/v1/project-run-action", cid, request)
}

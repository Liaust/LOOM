package httpapi

import (
	"context"
	"net/http"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

type projectDeploymentService interface {
	Deploy(context.Context, projectapply.Principal, pc.ProjectDeploymentRequest) (pc.ProjectDeploymentResult, error)
}

func (s Server) handleProjectDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.declarationAdmission(w, r, http.MethodPost, false) {
		return
	}
	var input pc.ProjectDeploymentRequest
	err := decodeDeclaration(r, &input, "project_ref", "node_ref", "resource", "check")
	if err == nil {
		err = validateDeclarationPlan(pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: input.ProjectRef, NodeRef: input.NodeRef})
	}
	if input.Resource != "" {
		if _, valid := pc.ApplicationDeployResource([]pc.DeclarationEffect{pc.ApplicationDeployEffect(input.Resource)}); !valid {
			err = declarationFailure(pc.DeclarationInvalid, "application_resource_invalid")
		}
	}
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	p, err := s.declarationPrincipal(r)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	service, ok := s.services.ProjectDeclaration.(projectDeploymentService)
	if !ok {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationUnsupported, "application_deployment_not_configured"), nil, false)
		return
	}
	cid, ctx := requestMeta(r)
	result, err := service.Deploy(ctx, p, input)
	if err != nil {
		s.writeDeclarationError(w, r, err, result.Operation, false)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}

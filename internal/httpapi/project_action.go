package httpapi

import (
	"context"
	"net/http"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

type projectActionService interface {
	RunApplicationAction(context.Context, projectapply.Principal, pc.ProjectActionRequest) (pc.ProjectActionResult, error)
}

func (s Server) handleProjectAction(w http.ResponseWriter, r *http.Request) {
	if !s.declarationAdmission(w, r, http.MethodPost, false) {
		return
	}
	var input pc.ProjectActionRequest
	err := decodeDeclaration(r, &input, "project_ref", "node_ref", "resource", "action", "run", "check", "release_failed")
	if err == nil {
		err = validateDeclarationPlan(pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: input.ProjectRef, NodeRef: input.NodeRef})
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
	service, ok := s.services.ProjectDeclaration.(projectActionService)
	if !ok {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationUnsupported, "application_action_not_configured"), nil, false)
		return
	}
	cid, ctx := requestMeta(r)
	result, err := service.RunApplicationAction(ctx, p, input)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}

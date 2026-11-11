package httpapi

import (
	"context"
	"net/http"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

type projectCredentialImporter interface {
	ImportApplicationCredential(context.Context, projectapply.Principal, pc.ProjectCredentialImportRequest) (pc.ProjectCredentialImportResult, error)
}

func (s Server) handleProjectCredentialImport(w http.ResponseWriter, r *http.Request) {
	if !s.declarationAdmission(w, r, http.MethodPost, false) {
		return
	}
	var input pc.ProjectCredentialImportRequest
	err := decodeDeclaration(r, &input, "project_ref", "node_ref", "resource", "source_path", "account", "scopes")
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
	service, ok := s.services.ProjectDeclaration.(projectCredentialImporter)
	if !ok {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationUnsupported, "application_credential_import_not_configured"), nil, false)
		return
	}
	cid, ctx := requestMeta(r)
	result, err := service.ImportApplicationCredential(ctx, p, input)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}

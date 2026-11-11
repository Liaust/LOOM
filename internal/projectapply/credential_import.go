package projectapply

import (
	"context"

	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

type applicationCredentialImporter interface {
	ImportCredential(context.Context, sr.ApplicationCredentialImport) (sr.ApplicationCredentialImportReceipt, error)
}

func (s *Service) ImportApplicationCredential(ctx context.Context, p Principal, input pc.ProjectCredentialImportRequest) (pc.ProjectCredentialImportResult, error) {
	var out pc.ProjectCredentialImportResult
	if err := s.config(true); err != nil {
		return out, err
	}
	if err := validPrincipal(p); err != nil {
		return out, err
	}
	r, ok := s.resolver.(*LocalResolver)
	if !ok {
		return out, fail(pc.DeclarationUnsupported, "application_credential_import_not_configured")
	}
	x, err := r.load(ctx, p, input.ProjectRef, input.NodeRef)
	if err != nil {
		return out, err
	}
	resource, exists := x.Analysis.Loaded.Declaration.Resources[input.Resource]
	if !exists || resource.Application == nil || resource.Application.Source == nil {
		return out, fail(pc.DeclarationInvalid, "application_resource_required")
	}
	if !x.Authority.CanWrite || x.Authority.Level < 5 {
		return out, fail(pc.DeclarationUnauthorized, "application_execution_authority_required")
	}
	helper, ok := r.Applications.(applicationCredentialImporter)
	if !ok {
		return out, fail(pc.DeclarationUnsupported, "application_credential_import_not_configured")
	}
	result, err := helper.ImportCredential(ctx, sr.ApplicationCredentialImport{Owner: sr.ApplicationOwner{ProjectID: x.Target.ProjectID, NodeID: x.Target.OwnerNodeID, Resource: string(input.Resource)}, SourcePath: input.SourcePath, ExpectedAccount: input.Account, ExpectedScopes: input.Scopes})
	if err != nil {
		return out, applicationFailure(err)
	}
	return pc.ProjectCredentialImportResult{ProjectID: x.Target.ProjectID, Resource: input.Resource, Reference: result.Reference, Revision: result.Revision}, nil
}

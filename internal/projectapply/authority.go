package projectapply

import (
	"context"
	"database/sql"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type CurrentAuthority struct{ Projects projects.Service }

func NewCurrentAuthority(db *sql.DB) CurrentAuthority {
	return CurrentAuthority{Projects: projects.NewService(db)}
}
func declarationRequest(p Principal) requestctx.Context {
	return requestctx.Context{ActorID: p.ActorID, OriginNodeID: p.OriginNodeID, Source: "project.declaration"}
}
func (a CurrentAuthority) Read(ctx context.Context, p Principal, t pc.DeclarationTarget) error {
	if err := validPrincipal(p); err != nil {
		return err
	}
	authority, err := a.Projects.GetDeclarationAuthority(ctx, declarationRequest(p), t.ProjectID, t.OwnerNodeID)
	if err != nil {
		return fail(pc.DeclarationUnauthorized, "current_project_read_access_required")
	}
	if !authority.CanRead {
		return authorityFailure(authority)
	}
	return nil
}
func (a CurrentAuthority) Execute(ctx context.Context, p Principal, b pc.DeclarationPlanBasis, action pc.DeclarationAction, approvals []string) error {
	if err := validPrincipal(p); err != nil {
		return err
	}
	repositoryIDs := []string{}
	for _, binding := range b.Bindings {
		if binding.RepositoryID != "" {
			repositoryIDs = append(repositoryIDs, binding.RepositoryID)
		}
	}
	current, err := a.Projects.GetDeclarationAuthorityForRepositories(ctx, declarationRequest(p), b.Target.ProjectID, b.Target.OwnerNodeID, repositoryIDs)
	if err != nil {
		return fail(pc.DeclarationUnauthorized, "current_project_write_access_required")
	}
	if !current.CanWrite {
		return authorityFailure(current)
	}
	expected := b.Revisions["authorization:"+p.ActorID+":"+b.Target.OwnerNodeID]
	if current.Revision != expected || action.Authorization.PolicyRevision != expected {
		return fail(pc.DeclarationPlanStale, "authorization_changed")
	}
	level := 3
	if action.Owner == pc.DeclarationOwnerApplication {
		level = 5
	}
	if action.Authorization.ActorID != p.ActorID || action.Authorization.NodeID != b.Target.OwnerNodeID || action.Authorization.Level != level || current.Level < level {
		return fail(pc.DeclarationUnauthorized, "action_authority_mismatch")
	}
	if action.Authorization.ApprovalRequired || len(approvals) != 0 {
		return fail(pc.DeclarationApprovalRequired, "typed_declaration_approval_owner_required")
	}
	return nil
}

func authorityFailure(current projects.DeclarationAuthority) error {
	if current.Prerequisite == "typed_declaration_policy_evaluator_required" || current.Prerequisite == "typed_declaration_action_category_evaluator_required" {
		return fail(pc.DeclarationUnsupported, current.Prerequisite)
	}
	if causePattern.MatchString(current.Prerequisite) {
		return fail(pc.DeclarationUnauthorized, current.Prerequisite)
	}
	return fail(pc.DeclarationUnauthorized, "current_project_access_required")
}

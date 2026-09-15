package projectapply

import (
	"context"
	"path/filepath"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

// ConnectCreatedProject reuses the declaration journal and its registration
// owner. Construction authorizes identity/source registration, never resources.
func (s *Service) ConnectCreatedProject(ctx context.Context, p Principal, projectID, root, owner string) (pc.DeclarationResult, error) {
	if err := s.config(true); err != nil {
		return pc.DeclarationResult{}, err
	}
	if err := validPrincipal(p); err != nil {
		return pc.DeclarationResult{}, err
	}
	if ids.Validate(ids.ProjectPrefix, projectID) != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || owner == "" {
		return pc.DeclarationResult{}, fail(pc.DeclarationInvalid, "invalid_created_project")
	}
	guard := createdProjectResolver{Resolver: s.resolver, projectID: projectID, root: root}
	if err := guard.source(); err != nil {
		return pc.DeclarationResult{}, err
	}
	restricted := *s
	restricted.resolver = guard
	key := "project-create:" + projectID
	opID, err := findOperation(ctx, s.db, p, projectID, key)
	if err != nil {
		return pc.DeclarationResult{}, err
	}
	if opID != "" {
		op, err := loadOperation(ctx, s.db, opID)
		if err != nil {
			return pc.DeclarationResult{}, err
		}
		if err := s.read(ctx, p, op.Resolution.Plan.Basis.Target); err != nil {
			return pc.DeclarationResult{}, err
		}
		if op.Principal != p || op.Request.NodeRef != owner || op.Request.IdempotencyKey != key {
			return pc.DeclarationResult{}, fail(pc.DeclarationIdentityConflict, "created_project_operation_changed")
		}
		if err := guard.resolution(op.Resolution); err != nil {
			return pc.DeclarationResult{}, err
		}
		request := op.Request
		request.OperationID = opID
		return restricted.Apply(ctx, p, request)
	}
	request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root, NodeRef: owner, Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}}
	plan, err := restricted.Plan(ctx, p, request)
	if err != nil {
		return pc.DeclarationResult{}, err
	}
	return restricted.Apply(ctx, p, pc.DeclarationApplyRequest{SchemaVersion: request.SchemaVersion, ProjectRef: root, NodeRef: owner, Effects: request.Effects, PlanID: plan.PlanID, IdempotencyKey: key})
}

type createdProjectResolver struct {
	Resolver
	projectID, root string
}

func (r createdProjectResolver) source() error {
	loaded, err := pc.LoadProject(r.root)
	if err != nil || loaded.Declaration == nil || loaded.Declaration.Project.ID != r.projectID || len(loaded.Declaration.Resources) != 0 || (loaded.Declaration.Project.Status != "" && loaded.Declaration.Project.Status != pc.ProjectStatusDraft) {
		return fail(pc.DeclarationIdentityConflict, "created_project_source_changed")
	}
	return nil
}

func (r createdProjectResolver) resolution(x Resolution) error {
	b := x.Plan.Basis
	if b.Target.ProjectID != r.projectID || b.Target.ProjectRoot != r.root || len(b.Bindings) != 0 || len(b.Actions) != 1 || len(x.Payloads) != 1 {
		return fail(pc.DeclarationIdentityConflict, "create_requires_registration_only")
	}
	a := b.Actions[0]
	if a.ID != "register_project:project" || a.Kind != pc.DeclarationRegisterProject || a.Owner != pc.DeclarationOwnerProjects || a.Resource != "" || a.TargetRef != r.projectID || len(a.DependsOn) != 0 {
		return fail(pc.DeclarationUnsupported, "create_requires_registration_only")
	}
	var payload projects.DeclarationRegistrationPayload
	if strictOwnerPayload(x.Payloads[a.ID], &payload) != nil || payload.Project.ID != r.projectID || payload.ProjectRoot != r.root || len(payload.Repositories) != 0 || payload.Predecessor != nil {
		return fail(pc.DeclarationIdentityConflict, "create_registration_payload_changed")
	}
	return nil
}

func (r createdProjectResolver) Resolve(ctx context.Context, p Principal, request pc.DeclarationPlanRequest) (Resolution, error) {
	if err := r.source(); err != nil {
		return Resolution{}, err
	}
	x, err := r.Resolver.Resolve(ctx, p, request)
	if err == nil {
		err = r.resolution(x)
	}
	return x, err
}

func (r createdProjectResolver) Current(ctx context.Context, p Principal, original Resolution) (Prerequisites, error) {
	if err := r.resolution(original); err != nil {
		return Prerequisites{}, err
	}
	if err := r.source(); err != nil {
		return Prerequisites{}, err
	}
	return r.Resolver.Current(ctx, p, original)
}

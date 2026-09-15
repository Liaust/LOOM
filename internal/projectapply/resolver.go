package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/nodes"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
)

// LocalResolver reads the current host configuration and existing registration
// owners. It does not maintain another project/root inventory. Before initial
// registration, the caller must select an explicit path under the configured Box.
type LocalResolver struct {
	DB           *sql.DB
	Config       func() (config.Config, error)
	Applications ApplicationPrerequisiteReader
}

func NewLocalResolver(db *sql.DB, currentConfig func() (config.Config, error)) *LocalResolver {
	return &LocalResolver{DB: db, Config: currentConfig}
}

// RemoteDeclarationSource describes the required future owner boundary. No
// adapter here substitutes local filesystem contents for an unavailable node.
type RemoteDeclarationSource interface {
	Snapshot(context.Context, Principal, pc.DeclarationTarget) (pc.Analysis, error)
}

type localDeclaration struct {
	Analysis       pc.Analysis
	Input          projects.RegisterProjectContractInput
	Target         pc.DeclarationTarget
	State          projects.DeclarationProjectState
	Authority      projects.DeclarationAuthority
	WatchStates    []projects.DeclarationWatchResourceState
	LegacyContract json.RawMessage
	PhysicalRoot   string
}

func (r *LocalResolver) load(ctx context.Context, p Principal, projectRef, nodeRef string) (localDeclaration, error) {
	var out localDeclaration
	if r == nil || r.DB == nil || r.Config == nil {
		return out, fail(pc.DeclarationTargetUnavailable, "local_source_not_configured")
	}
	cfg, err := r.Config()
	if err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "local_config_unavailable")
	}
	local, err := nodes.NewService(r.DB).GetNode(ctx, cfg.NodeID)
	if err != nil || local.Status != "active" {
		return out, fail(pc.DeclarationTargetUnavailable, "configured_node_unavailable")
	}
	if nodeRef != "" && nodeRef != local.NodeID && nodeRef != local.NodeKey {
		return out, fail(pc.DeclarationTargetUnavailable, "remote_source_owner_required")
	}
	root := projectRef
	expectedID := ""
	if !filepath.IsAbs(projectRef) {
		project, err := projects.NewService(r.DB).ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return out, projectReferenceLookupError(err)
		}
		if project.HomeNodeID == nil || *project.HomeNodeID != local.NodeID {
			return out, fail(pc.DeclarationTargetUnavailable, "remote_source_owner_required")
		}
		expectedID = project.ProjectID
		if err = r.DB.QueryRowContext(ctx, `SELECT project_root FROM projects.project_contract_registrations WHERE project_id=$1`, project.ProjectID).Scan(&root); err != nil {
			return out, fail(pc.DeclarationTargetUnavailable, "registered_project_location_required")
		}
	}
	if filepath.Clean(root) != root || !filepath.IsAbs(root) || cfg.BoxPath == "" {
		return out, fail(pc.DeclarationTargetUnavailable, "configured_project_root_required")
	}
	trusted, err := filepath.EvalSymlinks(cfg.BoxPath)
	if err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "configured_box_unavailable")
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "project_source_unavailable")
	}
	rel, err := filepath.Rel(trusted, physical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return out, fail(pc.DeclarationUnauthorized, "source_outside_configured_box")
	}
	out.Analysis = pc.Analyze(root)
	if out.Analysis.Loaded != nil && out.Analysis.Loaded.Declaration != nil && out.Analysis.Loaded.Declaration.LegacyContracts != nil {
		intent, mapErr := projectregistration.BuildDeclarationIntent(out.Analysis, "project.declaration")
		err = mapErr
		out.Input, out.LegacyContract = intent.Input, intent.LegacyContract
	} else {
		out.Input, err = projectregistration.BuildInput(out.Analysis, "project.declaration")
	}
	if err != nil || out.Analysis.Loaded == nil || out.Analysis.Loaded.Declaration == nil {
		return out, fail(pc.DeclarationInvalid, "valid_declaration_source_required")
	}
	if out.Input.Project.Status == "archived" {
		return out, fail(pc.DeclarationTargetUnavailable, "project_archive_owner_required")
	}
	projectID := out.Input.Project.ID
	if expectedID != "" && expectedID != projectID {
		return out, fail(pc.DeclarationIdentityConflict, "source_project_identity_changed")
	}
	if out.Input.Project.OwnerNode != local.NodeKey {
		return out, fail(pc.DeclarationTargetUnavailable, "remote_source_owner_required")
	}
	var registeredRoot sql.NullString
	var registeredNode sql.NullString
	err = r.DB.QueryRowContext(ctx, `SELECT r.project_root,p.home_node_id FROM projects.projects p LEFT JOIN projects.project_contract_registrations r USING(project_id) WHERE p.project_id=$1`, projectID).Scan(&registeredRoot, &registeredNode)
	if err != nil && err != sql.ErrNoRows {
		return out, fail(pc.DeclarationTargetUnavailable, "registered_location_unavailable")
	}
	if err == nil && (registeredRoot.Valid && registeredRoot.String != root || !registeredNode.Valid || registeredNode.String != local.NodeID) {
		return out, fail(pc.DeclarationIdentityConflict, "registered_location_mismatch")
	}
	if err == nil {
		currentProject, readErr := projects.NewService(r.DB).ResolveProjectRef(ctx, projectID)
		if readErr != nil {
			return out, fail(pc.DeclarationTargetUnavailable, "project_lifecycle_unavailable")
		}
		if projects.EnsureProjectMutable(currentProject, "project_declaration", projectID) != nil {
			return out, fail(pc.DeclarationTargetUnavailable, "project_archive_recovery_required")
		}
	}
	location, _ := canonicalValue(map[string]string{"node_id": local.NodeID, "box": trusted, "project_root": root, "physical_root": physical})
	out.Target = pc.DeclarationTarget{ProjectID: projectID, OwnerNodeID: local.NodeID, ProjectRoot: root, LocationRevision: hashBytes(location)}
	out.PhysicalRoot = physical
	repositoryIDs := []string{}
	for _, m := range out.Analysis.Plan.Declaration.Repositories {
		if m.ID != "" {
			repositoryIDs = append(repositoryIDs, m.ID)
		}
	}
	out.Authority, err = projects.NewService(r.DB).GetDeclarationAuthorityForRepositories(ctx, declarationRequest(p), projectID, local.NodeID, repositoryIDs)
	if err != nil {
		return out, fail(pc.DeclarationUnauthorized, "current_project_read_access_required")
	}
	if !out.Authority.CanRead {
		return out, authorityFailure(out.Authority)
	}
	out.State, err = projects.NewService(r.DB).GetDeclarationProjectState(ctx, projectID)
	if err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "project_owner_state_unavailable")
	}
	out.WatchStates, err = projects.NewService(r.DB).ReadDeclarationWatchState(ctx, projectID, local.NodeID)
	if err != nil {
		return out, fail(pc.DeclarationTargetUnavailable, "watch_owner_state_unavailable")
	}
	return out, nil
}
func localPrerequisites(p Principal, x localDeclaration) Prerequisites {
	sources := []pc.DeclarationSource{}
	for _, s := range x.Analysis.Plan.Declaration.Sources {
		sources = append(sources, s.DeclarationSource)
	}
	bindings := map[pc.ResourceKey]pc.DeclarationBinding{}
	for k, res := range x.Analysis.Loaded.Declaration.Resources {
		binding := pc.DeclarationBinding{Kind: res.Kind}
		if res.Repository != nil {
			binding.RepositoryID = x.State.Repositories[string(k)]
			if binding.RepositoryID == "" {
				binding.RepositoryID = res.Repository.ID
			}
		}
		bindings[k] = binding
	}
	contributors, _ := pc.DeclarationContributors(*x.Analysis.Loaded.Declaration)
	for _, c := range contributors {
		if _, exists := bindings[c.Key]; !exists {
			bindings[c.Key] = pc.DeclarationBinding{Kind: declarationWatchKind(string(c.Owner))}
		}
	}
	prereq := Prerequisites{Target: x.Target, Sources: sources, Bindings: bindings, Revisions: map[string]string{"projects:registry": x.State.RegistryRevision, "projects:lifecycle": x.State.LifecycleRevision, "authorization:" + p.ActorID + ":" + x.Target.OwnerNodeID: x.Authority.Revision}}
	for key, binding := range bindings {
		if owner := declarationWatchOwner(binding.Kind); owner != "" {
			prereq.Revisions[owner+":"+string(key)] = "absent"
		}
	}
	for _, state := range x.WatchStates {
		key := pc.ResourceKey(state.Resource)
		if _, exists := bindings[key]; !exists && (!state.Retire || !state.Applied) {
			bindings[key] = pc.DeclarationBinding{Kind: declarationWatchKind(state.Owner)}
		}
		if binding, exists := bindings[key]; exists && declarationWatchOwner(binding.Kind) == state.Owner {
			prereq.Revisions[state.Owner+":"+state.Resource] = state.Revision
		}
	}
	return prereq
}
func declarationWatchOwner(kind pc.DeclarationResourceKind) string {
	switch kind {
	case pc.DeclarationKnowledge:
		return string(pc.DeclarationOwnerKnowledge)
	case pc.DeclarationProtection:
		return string(pc.DeclarationOwnerProtection)
	}
	return ""
}
func declarationWatchKind(owner string) pc.DeclarationResourceKind {
	if owner == string(pc.DeclarationOwnerKnowledge) {
		return pc.DeclarationKnowledge
	}
	return pc.DeclarationProtection
}

// Preserve the original retired resource closure when observing an old operation.
// Values still come from current durable owner evidence, never the frozen basis.
func localPrerequisitesFor(p Principal, x localDeclaration, original map[pc.ResourceKey]pc.DeclarationBinding) Prerequisites {
	out := localPrerequisites(p, x)
	for _, state := range x.WatchStates {
		key := pc.ResourceKey(state.Resource)
		if binding, exists := original[key]; exists && declarationWatchOwner(binding.Kind) == state.Owner {
			if _, active := out.Bindings[key]; !active {
				out.Bindings[key] = pc.DeclarationBinding{Kind: binding.Kind}
				out.Revisions[state.Owner+":"+state.Resource] = state.Revision
			}
		}
	}
	return out
}
func declarationWatchContributors(x localDeclaration) ([]projects.DeclarationWatchContributor, error) {
	out := []projects.DeclarationWatchContributor{}
	current := map[pc.ResourceKey]string{}
	contributors, err := pc.DeclarationContributors(*x.Analysis.Loaded.Declaration)
	if err != nil {
		return nil, err
	}
	for _, c := range contributors {
		current[c.Key] = string(c.Owner)
		out = append(out, projects.DeclarationWatchContributor{ActionID: string(c.Kind) + ":" + string(c.Key), Owner: string(c.Owner), Resource: string(c.Key)})
	}
	for _, state := range x.WatchStates {
		if state.Retire && state.Applied {
			continue
		}
		if owner, exists := current[pc.ResourceKey(state.Resource)]; exists {
			if owner != state.Owner {
				return nil, fail(pc.DeclarationIdentityConflict, "watch_resource_owner_changed")
			}
			continue
		}
		out = append(out, projects.DeclarationWatchContributor{ActionID: "retire_resource:" + state.Resource + ":" + state.Owner, Owner: state.Owner, Resource: state.Resource, Retire: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActionID < out[j].ActionID })
	return out, nil
}
func (r *LocalResolver) Current(ctx context.Context, p Principal, original Resolution) (Prerequisites, error) {
	x, err := r.load(ctx, p, original.Plan.Basis.Target.ProjectRoot, original.Plan.Basis.Target.OwnerNodeID)
	if err != nil {
		return Prerequisites{}, err
	}
	return localPrerequisitesFor(p, x, original.Plan.Basis.Bindings), nil
}
func (r *LocalResolver) Resolve(ctx context.Context, p Principal, request pc.DeclarationPlanRequest) (Resolution, error) {
	x, err := r.load(ctx, p, request.ProjectRef, request.NodeRef)
	if err != nil {
		return Resolution{}, err
	}
	prereq := localPrerequisites(p, x)
	effects := request.Effects
	if effects == nil {
		effects = []pc.DeclarationEffect{pc.DeclarationReconcile}
	}
	for _, effect := range effects {
		if effect != pc.DeclarationReconcile {
			return Resolution{}, fail(pc.DeclarationUnsupported, "projection_owner_required")
		}
	}
	var predecessor *projects.DeclarationLegacyPredecessor
	var desired projects.DeclarationRegistrationPayload
	if x.LegacyContract != nil {
		predecessor, err = projects.NewService(r.DB).ReadDeclarationLegacyPredecessor(ctx, declarationRequest(p), x.Input, x.LegacyContract, x.Target.OwnerNodeID, x.PhysicalRoot)
		if err != nil {
			return Resolution{}, fail(pc.DeclarationUnsupported, pc.LegacyOwnerTransitionRequired)
		}
		desired, err = projects.BuildDeclarationLegacyRegistrationIntent(x.Input, predecessor)
	} else {
		desired, err = projects.BuildDeclarationRegistrationPayload(x.Input)
	}
	if err != nil {
		return Resolution{}, fail(pc.DeclarationInvalid, "registration_input_invalid")
	}
	payload, err := canonicalValue(desired)
	if err != nil {
		return Resolution{}, err
	}
	action := pc.DeclarationAction{ID: "register_project:project", Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, TargetRef: x.Target.ProjectID, InputHash: hashBytes(payload), DependsOn: []string{}, Authorization: pc.DeclarationAuthorization{ActorID: p.ActorID, NodeID: x.Target.OwnerNodeID, Level: 3, PolicyRevision: x.Authority.Revision}}
	basis := pc.DeclarationPlanBasis{SchemaVersion: pc.DeclarationPlanSchemaV05, Target: prereq.Target, Sources: prereq.Sources, Bindings: prereq.Bindings, Revisions: prereq.Revisions, Effects: effects, Actions: []pc.DeclarationAction{action}}
	payloads := map[string]json.RawMessage{action.ID: payload}
	contributors, err := declarationWatchContributors(x)
	if err != nil {
		return Resolution{}, err
	}
	if len(contributors) > 0 {
		group, err := projectwatch.BuildDeclarationWatchGroup(x.Analysis, x.Target.OwnerNodeID, x.Input.Project.OwnerNode, contributors)
		if err != nil {
			return Resolution{}, fail(pc.DeclarationInvalid, "watch_group_invalid")
		}
		group.Predecessor = predecessor
		if err = projectwatch.ValidateDeclarationWatchGroup(group); err != nil {
			return Resolution{}, fail(pc.DeclarationInvalid, "watch_group_invalid")
		}
		groupHash, err := projectwatch.DeclarationGroupHash(group)
		if err != nil {
			return Resolution{}, err
		}
		previous := action.ID
		for _, c := range contributors {
			desired := projects.DeclarationWatchIntentPayload{Group: group, GroupHash: groupHash, Owner: c.Owner, Resource: c.Resource, Retire: c.Retire}
			raw, err := canonicalValue(desired)
			if err != nil {
				return Resolution{}, err
			}
			kind := pc.DeclarationEnrollKnowledge
			if c.Owner == string(pc.DeclarationOwnerProtection) {
				kind = pc.DeclarationReconcileProtection
			}
			if c.Retire {
				kind = pc.DeclarationRetireResource
			}
			next := pc.DeclarationAction{ID: c.ActionID, Kind: kind, Owner: pc.DeclarationOwner(c.Owner), Resource: pc.ResourceKey(c.Resource), TargetRef: x.Target.ProjectID + "/" + c.Resource, InputHash: hashBytes(raw), DependsOn: []string{previous}, Authorization: action.Authorization}
			basis.Actions = append(basis.Actions, next)
			payloads[next.ID] = raw
			previous = next.ID
		}
	}
	if err := r.appendApplications(ctx, x, &basis, payloads); err != nil {
		return Resolution{}, err
	}
	return Resolution{Plan: pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, Basis: basis, Errors: []pc.DeclarationError{}}, Payloads: payloads}, nil
}

// Only the authoritative project lookup can establish absence. A registration
// without a usable location (including its own ErrNoRows) stays unavailable.
func projectReferenceLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fail(pc.DeclarationReferenceMissing, "project_missing")
	}
	return fail(pc.DeclarationTargetUnavailable, "registered_project_location_required")
}

package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const ProjectRetirementPlanSchema = "project.registration_retirement_plan.v1"

type ProjectRetirementInput struct {
	RetainedRoot string `json:"retained_root"`
	Reason       string `json:"reason"`
}

type ProjectRetirementPlan struct {
	SchemaVersion      string             `json:"schema_version"`
	ProjectID          string             `json:"project_id"`
	ProjectSlug        string             `json:"project_slug"`
	Request            requestctx.Context `json:"request"`
	OriginalRoot       string             `json:"original_root"`
	RetainedRoot       string             `json:"retained_root"`
	RegistrationDigest string             `json:"registration_digest"`
	RepositoryDigest   string             `json:"repository_digest"`
	RuntimeDigest      string             `json:"runtime_digest"`
	InventoryDigest    string             `json:"inventory_digest"`
	InventoryEntries   int                `json:"inventory_entries"`
	InventoryBytes     int64              `json:"inventory_bytes"`
	Reason             string             `json:"reason"`
	PlannedAt          time.Time          `json:"planned_at"`
	PlanDigest         string             `json:"plan_digest"`
}

type ProjectRetirementApply struct {
	Plan       ProjectRetirementPlan `json:"plan"`
	PlanDigest string                `json:"plan_digest"`
	Confirm    bool                  `json:"confirm"`
}

type projectRegistrationRetirer interface {
	AcquireProjectArchiveLocks(context.Context, []string) (func() error, error)
	RetireProjectRegistration(context.Context, requestctx.Context, projects.ProjectRegistrationRetirementInput) (projects.Project, error)
}

func (s ProjectRuntimeService) ReviewProjectRegistrationRetirement(ctx context.Context, req requestctx.Context, ref string, input ProjectRetirementInput) (ProjectRetirementPlan, error) {
	return s.planRegistrationRetirement(ctx, req, ref, input, s.now())
}

func (s ProjectRuntimeService) planRegistrationRetirement(ctx context.Context, req requestctx.Context, ref string, input ProjectRetirementInput, at time.Time) (ProjectRetirementPlan, error) {
	fail := func(code string) (ProjectRetirementPlan, error) {
		return ProjectRetirementPlan{}, projectSurfaceError(code)
	}
	if projects.ValidateProjectPlanRequest(req) != nil || s.Projects == nil || s.RepositoryState == nil || s.Activation == nil {
		return fail("not_ready")
	}
	d, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil || d.Registration == nil || d.Registration.RegistrationStatus != projects.ProjectRegistrationStatusRegistered || d.Project.Project.Status != "active" || d.Project.Project.ProjectScopeID != req.ScopeID || (d.Registration.ActivationStatus != projects.ProjectActivationStatusInactive && d.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive) {
		return fail("retirement_requires_inactive_registration")
	}
	root := input.RetainedRoot
	// Only legacy acceptance custody, not an arbitrary path or a normal project.
	legacy := filepath.Join(s.WorkspaceRoots.BoxRoot, "Projects", ".loom-acceptance")
	quarantine := filepath.Join(filepath.Dir(s.WorkspaceRoots.BoxRoot), ".retired-fixtures")
	if !withinRetained(d.Registration.ProjectRoot, legacy) || root != filepath.Clean(root) || !filepath.IsAbs(root) || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2048 || (!withinRetained(root, quarantine) && root != d.Registration.ProjectRoot) {
		return fail("retirement_unsupported_custody")
	}
	if root != d.Registration.ProjectRoot {
		if _, err := os.Lstat(d.Registration.ProjectRoot); !os.IsNotExist(err) {
			return fail("retirement_original_root_not_absent")
		}
	}
	// Walk every retained ancestor without following links before authenticating
	// the original contract's exact bytes and inventorying the retained payload.
	base := filepath.Dir(s.WorkspaceRoots.BoxRoot)
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fail("retirement_unsupported_custody")
	}
	chain, err := openHeldDirectoryChain(base, filepath.ToSlash(rel), nil)
	if err != nil {
		return fail("retained_source_unavailable")
	}
	defer chain.Close()
	if err := projects.AuthenticateRetainedProjectContract(d, root); err != nil {
		return fail("retained_contract_mismatch")
	}
	f, info, err := openMappedDirectoryNoFollow(TrustedWorkspaceRoots{BoxRoot: base, StorageRoot: s.WorkspaceRoots.StorageRoot}, WorkspacePathRef{Root: WorkspaceRootBox, RelativePath: filepath.ToSlash(rel), AbsolutePath: root})
	if err != nil {
		return fail("retained_source_unavailable")
	}
	defer f.Close()
	inv, err := inventoryOpenRootMode(ctx, f, info, true)
	if err != nil {
		return fail("retained_inventory_unavailable")
	}
	activation, ok := s.Activation.(ProjectPhysicalArchiveActivationService)
	if !ok {
		return fail("not_ready")
	}
	var actions []projects.ProjectDeactivationAction
	for _, facet := range physicalArchiveDeactivationFacets(d, true) {
		result, err := activation.DeactivateForPhysicalArchive(ctx, req, d.Project.Project.ProjectID, projects.DeactivateProjectInput{Facet: facet, ProjectRoot: d.Registration.ProjectRoot, Reason: input.Reason, DryRun: true})
		if err != nil || !result.DryRun || result.Changed {
			return fail("retirement_runtime_not_inactive")
		}
		actions = append(actions, result.Actions...)
	}
	sort.Slice(actions, func(i, j int) bool {
		return projectDeactivationActionKey(actions[i]) < projectDeactivationActionKey(actions[j])
	})
	runtimeRaw, _ := json.Marshal(actions)
	repo, err := s.RepositoryState.ReadProjectRepositoryState(ctx, d.Project.Project.ProjectID)
	if err != nil {
		return fail("project_unavailable")
	}
	d, err = canonicalProjectArchiveRegistration(d, true)
	if err != nil {
		return fail("invalid_plan")
	}
	repo, err = canonicalProjectRepositoryState(repo)
	if err != nil {
		return fail("invalid_plan")
	}
	regRaw, _ := json.Marshal(d)
	repoRaw, _ := json.Marshal(repo)
	p := ProjectRetirementPlan{SchemaVersion: ProjectRetirementPlanSchema, ProjectID: d.Project.Project.ProjectID, ProjectSlug: d.Project.Project.Slug, Request: req, OriginalRoot: d.Registration.ProjectRoot, RetainedRoot: root, RegistrationDigest: sha256Hex(regRaw), RepositoryDigest: sha256Hex(repoRaw), InventoryDigest: inv.Digest, InventoryEntries: inv.EntryCount, InventoryBytes: inv.TotalBytes, Reason: input.Reason, PlannedAt: at.UTC()}
	p.RuntimeDigest = sha256Hex(runtimeRaw)
	p.PlanDigest = retirementPlanDigest(p)
	return p, nil
}

func retirementPlanDigest(p ProjectRetirementPlan) string {
	p.PlanDigest = ""
	b, _ := json.Marshal(p)
	return sha256Hex(b)
}

func (s ProjectRuntimeService) ApplyProjectRegistrationRetirement(ctx context.Context, live requestctx.Context, ref string, input ProjectRetirementApply) (projects.Project, error) {
	p := input.Plan
	if !input.Confirm || p.SchemaVersion != ProjectRetirementPlanSchema || p.PlanDigest != input.PlanDigest || p.PlanDigest != retirementPlanDigest(p) {
		return projects.Project{}, projectSurfaceError("review_confirmation_required")
	}
	req, err := bindProjectReviewRequest(live, p.Request)
	if err != nil {
		return projects.Project{}, err
	}
	if err = s.requireProjectReviewRef(ctx, ref, p.ProjectID); err != nil {
		return projects.Project{}, err
	}
	owner, ok := s.Projects.(projectRegistrationRetirer)
	if !ok {
		return projects.Project{}, projectSurfaceError("not_ready")
	}
	release, err := owner.AcquireProjectArchiveLocks(ctx, projects.ProjectArchiveLockKeys(p.ProjectID, p.ProjectSlug, "", nil, []string{"scripts", "schedules", "direct_events", "connectors", "modules", "watched_roots", "workflows", "services"}))
	if err != nil {
		return projects.Project{}, projectSurfaceError("project_busy")
	}
	defer release()
	fresh, err := s.planRegistrationRetirement(ctx, req, p.ProjectID, ProjectRetirementInput{RetainedRoot: p.RetainedRoot, Reason: p.Reason}, p.PlannedAt)
	if err != nil {
		return projects.Project{}, err
	}
	if !reflect.DeepEqual(fresh, p) {
		return projects.Project{}, projectSurfaceError("review_stale")
	}
	d, err := s.Projects.GetProjectRegistrationStatus(ctx, p.ProjectID)
	if err != nil {
		return projects.Project{}, projectSurfaceError("project_unavailable")
	}
	repo, err := s.RepositoryState.ReadProjectRepositoryState(ctx, p.ProjectID)
	if err != nil {
		return projects.Project{}, projectSurfaceError("project_unavailable")
	}
	canonical, err := canonicalProjectArchiveRegistration(d, true)
	if err != nil {
		return projects.Project{}, projectSurfaceError("review_stale")
	}
	repo, err = canonicalProjectRepositoryState(repo)
	if err != nil {
		return projects.Project{}, projectSurfaceError("review_stale")
	}
	regRaw, _ := json.Marshal(canonical)
	repoRaw, _ := json.Marshal(repo)
	if sha256Hex(regRaw) != p.RegistrationDigest || sha256Hex(repoRaw) != p.RepositoryDigest {
		return projects.Project{}, projectSurfaceError("review_stale")
	}
	if s.PauseNativeSchedules != nil {
		if err := s.PauseNativeSchedules(ctx, p.ProjectID); err != nil {
			return projects.Project{}, ProjectPhysicalSurfaceError{Code: "retirement_runtime_not_inactive", cause: err}
		}
	}
	state, _ := json.Marshal(map[string]any{"schema_version": "project.registration_retirement.v1", "evidence_kind": "retained_registration_disposition", "project_id": p.ProjectID, "project_slug": p.ProjectSlug, "status": "retired", "activation_state": "inactive", "original_root": p.OriginalRoot, "retained_root": p.RetainedRoot, "inventory_digest": p.InventoryDigest, "inventory_entries": p.InventoryEntries, "inventory_bytes": p.InventoryBytes, "plan_digest": p.PlanDigest, "reason": p.Reason, "request": req, "retired_at": s.now(), "previous_archive_state": json.RawMessage(d.Project.Project.ArchiveState), "safe_to_delete": false})
	commit := projects.ProjectRegistrationRetirementInput{ProjectID: p.ProjectID, ScopeID: req.ScopeID, RegistrationID: d.Registration.ProjectContractRegistrationID, RegistrationRevision: d.Registration.RegistrationRevision, ContractHash: d.Registration.ContractHash, ActivationStatus: d.Registration.ActivationStatus, ExpectedArchiveState: d.Project.Project.ArchiveState, State: state}
	if repo.Source != nil {
		commit.RepositoryRevision = repo.Source.SourceRevision
	}
	for _, m := range repo.Members {
		commit.Members = append(commit.Members, projects.ProjectArchiveRepositoryMemberBinding{RepositoryID: m.RepositoryID, RepositoryOwnerProjectID: m.RepositoryOwnerProjectID, MemberKey: m.Key, Role: m.Role})
	}
	out, err := owner.RetireProjectRegistration(ctx, req, commit)
	if err != nil {
		return projects.Project{}, ProjectPhysicalSurfaceError{Code: "retirement_failed", cause: err}
	}
	return out, nil
}

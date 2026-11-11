package projectapply

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	hs "loom.local/loom/internal/hermesschedules"
	pc "loom.local/loom/internal/projectcontracts"
)

type hermesOwnerPayload struct {
	Source    hs.Source       `json:"source"`
	ProjectID string          `json:"project_id"`
	Resource  pc.ResourceKey  `json:"resource"`
	Spec      *hs.ProjectSpec `json:"spec,omitempty"`
	Agent     *hs.AgentSpec   `json:"agent,omitempty"`
}

func (r *LocalResolver) readHermesSchedules(ctx context.Context, x *localDeclaration) error {
	x.HermesStates = map[pc.ResourceKey]hs.ProjectJob{}
	needed := false
	for _, res := range x.Analysis.Loaded.Declaration.Resources {
		needed = needed || res.HermesSchedule != nil || res.Agent != nil
	}
	if !needed && r.DB != nil {
		// The existing operation journal preserves withdrawn ownership too. Do
		// not make ordinary application/file work depend on an unrelated daemon,
		// but never skip a previously managed Hermes resource's retirement/replay.
		if err := r.DB.QueryRowContext(ctx, `SELECT EXISTS (
		 SELECT 1 FROM projects.declaration_operations o
		 JOIN projects.declaration_action_receipts a USING(operation_id)
		 WHERE o.project_id=$1 AND a.owner='hermesschedules')`, x.Target.ProjectID).Scan(&needed); err != nil {
			return fail(pc.DeclarationTargetUnavailable, "hermes_owner_history_unavailable")
		}
	}
	if !needed {
		return nil
	}
	if r.Hermes == nil {
		return fail(pc.DeclarationTargetUnavailable, "hermes_owner_not_configured")
	}
	out, err := r.Hermes.Call(ctx, hs.ProjectRequest{Operation: "list", Source: r.HermesSource, ProjectID: x.Target.ProjectID})
	if err != nil {
		return fail(pc.DeclarationTargetUnavailable, "hermes_owner_unavailable")
	}
	for _, state := range out.Jobs {
		key := pc.ResourceKey(state.Resource)
		kind := hermesResourceKind(state)
		if res, ok := x.Analysis.Loaded.Declaration.Resources[key]; ok && res.Kind != kind {
			return fail(pc.DeclarationIdentityConflict, "hermes_resource_kind_changed")
		}
		if _, exists := x.HermesStates[key]; exists {
			return fail(pc.DeclarationIdentityConflict, "hermes_resource_duplicate")
		}
		x.HermesStates[key] = state
	}
	return nil
}
func addHermesPrerequisites(out *Prerequisites, x localDeclaration, original map[pc.ResourceKey]pc.DeclarationBinding) {
	for key, b := range out.Bindings {
		if b.Kind == pc.DeclarationHermesSchedule || b.Kind == pc.DeclarationAgent {
			out.Revisions["hermesschedules:"+string(key)] = "absent"
		}
	}
	for key, state := range x.HermesStates {
		_, declared := out.Bindings[key]
		_, retained := original[key]
		if !declared && state.Retired && !retained {
			continue
		}
		out.Bindings[key] = pc.DeclarationBinding{Kind: hermesResourceKind(state), OwnerRef: state.ID}
		out.Revisions["hermesschedules:"+string(key)] = state.Revision
	}
}
func (r *LocalResolver) appendHermesSchedules(x localDeclaration, basis *pc.DeclarationPlanBasis, payloads map[string]json.RawMessage) error {
	keys := []pc.ResourceKey{}
	for key, b := range basis.Bindings {
		if b.Kind == pc.DeclarationHermesSchedule || b.Kind == pc.DeclarationAgent {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := basis.Bindings[keys[i]].Kind == pc.DeclarationAgent, basis.Bindings[keys[j]].Kind == pc.DeclarationAgent
		if ai != aj {
			return ai
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		payload := hermesOwnerPayload{Source: r.HermesSource, ProjectID: x.Target.ProjectID, Resource: key}
		kind := pc.DeclarationRetireResource
		if agent := x.Analysis.Loaded.Declaration.Resources[key].Agent; agent != nil {
			if x.Input.Project.OwnerNode != r.HermesSource.Host {
				return fail(pc.DeclarationIdentityConflict, "hermes_profile_host_mismatch")
			}
			spec, err := pc.AgentInput(*agent, x.Target.ProjectRoot, x.Analysis.Plan.Declaration.Sources)
			if err != nil {
				return fail(pc.DeclarationInvalid, "hermes_agent_invalid")
			}
			payload.Agent = &spec
			kind = pc.DeclarationReconcileAgent
		}
		if s := x.Analysis.Loaded.Declaration.Resources[key].HermesSchedule; s != nil {
			if x.Input.Project.OwnerNode != r.HermesSource.Host {
				return fail(pc.DeclarationIdentityConflict, "hermes_profile_host_mismatch")
			}
			spec, err := pc.HermesScheduleInput(*s, x.Target.ProjectRoot)
			if err != nil {
				return fail(pc.DeclarationInvalid, "hermes_spec_invalid")
			}
			payload.Spec = &spec
			if s.Profile != "mina" {
				payload.Spec.Profile = pc.AgentProfileID(x.Target.ProjectID, pc.ResourceKey(s.Profile))
			}
			kind = pc.DeclarationReconcileHermesSchedule
		}
		if kind != pc.DeclarationRetireResource && x.HermesStates[key].Drift {
			return fail(pc.DeclarationPlanStale, "hermes_native_configuration_drift")
		}
		raw, err := canonicalValue(payload)
		if err != nil {
			return err
		}
		a := pc.DeclarationAction{ID: string(kind) + ":" + string(key), Kind: kind, Resource: key, Owner: pc.DeclarationOwnerHermes, TargetRef: x.Target.ProjectID + "/" + string(key), InputHash: hashBytes(raw), DependsOn: []string{"register_project:project"}, Authorization: basis.Actions[0].Authorization}
		if payload.Spec != nil && payload.Spec.Profile != "" {
			a.DependsOn = append(a.DependsOn, "reconcile_agent:"+x.Analysis.Loaded.Declaration.Resources[key].HermesSchedule.Profile)
		}
		if payload.Spec == nil && payload.Agent == nil {
			a.ID += ":hermesschedules"
		}
		basis.Actions = append(basis.Actions, a)
		payloads[a.ID] = raw
	}
	return nil
}

func hermesResourceKind(j hs.ProjectJob) pc.DeclarationResourceKind {
	if j.Kind == "agent" {
		return pc.DeclarationAgent
	}
	return pc.DeclarationHermesSchedule
}

// HermesScheduleOwner uses the native token receipt as the external commit
// witness. PostgreSQL's declaration journal retains each completed operation.
type HermesScheduleOwner struct{ Resolver *LocalResolver }

func (o HermesScheduleOwner) Validate(_ context.Context, a pc.DeclarationAction, raw json.RawMessage) error {
	var p hermesOwnerPayload
	if strictOwnerPayload(raw, &p) != nil || o.Resolver == nil || o.Resolver.Hermes == nil || p.Source != o.Resolver.HermesSource || !hs.SupportedRevision(p.Source.Revision) || p.Source.Profile != "mina" || p.ProjectID+"/"+string(p.Resource) != a.TargetRef || p.Resource != a.Resource || a.Owner != pc.DeclarationOwnerHermes {
		return fail(pc.DeclarationInvalid, "hermes_owner_input_invalid")
	}
	if p.Agent != nil {
		if p.Source.Revision != hs.PlatformRevision || p.Spec != nil || a.Kind != pc.DeclarationReconcileAgent || p.Agent.Instructions == "" || p.Agent.Model == "" || p.Agent.Provider == "" {
			return fail(pc.DeclarationInvalid, "hermes_agent_invalid")
		}
	} else if p.Spec == nil {
		if a.Kind != pc.DeclarationRetireResource {
			return fail(pc.DeclarationInvalid, "hermes_retire_invalid")
		}
	} else if a.Kind != pc.DeclarationReconcileHermesSchedule || p.Spec.Prompt == "" || p.Spec.Schedule == "" || (p.Spec.Status != "active" && p.Spec.Status != "paused") {
		return fail(pc.DeclarationInvalid, "hermes_spec_invalid")
	}
	if p.Spec != nil && p.Spec.Codex != nil && (p.Source.Revision != hs.PlatformRevision || !hs.ValidCodexProjectSpec(*p.Spec.Codex) || len(p.Spec.Skills) != 0) {
		return fail(pc.DeclarationInvalid, "hermes_codex_invalid")
	}
	return nil
}
func hermesReceipt(call ActionCall, j hs.ProjectJob) (Observation, error) {
	if j.Token != call.Token {
		return Observation{State: Absent}, nil
	}
	expectedRevision := call.Expected.Revisions["hermesschedules:"+string(call.Action.Resource)]
	expectedBinding := call.Expected.Bindings[call.Action.Resource]
	before := expectedRevision == j.BeforeRevision && expectedBinding.OwnerRef == j.BeforeID
	after := expectedRevision == j.CommittedRevision && expectedBinding.OwnerRef == j.ID
	// The coordinator includes an already-recorded receipt in Expected on replay.
	// Return the same native before/after receipt in either state.
	if j.InputHash != call.Action.InputHash || (!before && !after) {
		return Observation{}, fail(pc.DeclarationIdentityConflict, "hermes_receipt_mismatch")
	}
	receipt := &Receipt{Token: call.Token, ActionID: call.Action.ID, Owner: pc.DeclarationOwnerHermes, InputHash: call.Action.InputHash, EffectRef: j.ID, Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{}}
	if j.BeforeRevision != j.CommittedRevision {
		receipt.Revisions["hermesschedules:"+string(call.Action.Resource)] = RevisionChange{Before: j.BeforeRevision, After: j.CommittedRevision}
	}
	if j.BeforeID == "" {
		kind := hermesResourceKind(j)
		receipt.Bindings[call.Action.Resource] = BindingChange{Before: pc.DeclarationBinding{Kind: kind}, After: pc.DeclarationBinding{Kind: kind, OwnerRef: j.ID}}
	}
	return Observation{State: Committed, Receipt: receipt}, nil
}
func (o HermesScheduleOwner) Observe(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	out, err := o.Resolver.Hermes.Call(ctx, hs.ProjectRequest{Operation: "list", Source: o.Resolver.HermesSource, ProjectID: call.Target.ProjectID, Resource: string(call.Action.Resource)})
	if err != nil {
		return Observation{}, fail(pc.DeclarationTargetUnavailable, "hermes_observation_unavailable")
	}
	if len(out.Jobs) == 0 {
		return Observation{State: Absent}, nil
	}
	if len(out.Jobs) != 1 {
		return Observation{}, fail(pc.DeclarationIdentityConflict, "hermes_resource_duplicate")
	}
	return hermesReceipt(call, out.Jobs[0])
}
func (o HermesScheduleOwner) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	if prior, err := o.Observe(ctx, call); err != nil || prior.State == Committed {
		return prior, err
	}
	var payload hermesOwnerPayload
	if err := strictOwnerPayload(call.Payload, &payload); err != nil {
		return Observation{}, err
	}
	if payload.Spec != nil && payload.Spec.Workdir != call.Target.ProjectRoot {
		return Observation{}, fail(pc.DeclarationIdentityConflict, "hermes_workdir_mismatch")
	}
	if payload.Agent != nil {
		// Context files were captured in the source fence; workdir remains inside
		// the same project and is never selected from arbitrary native metadata.
		rel, err := filepath.Rel(call.Target.ProjectRoot, payload.Agent.Workdir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return Observation{}, fail(pc.DeclarationIdentityConflict, "hermes_workdir_mismatch")
		}
	}
	// Shared project lock serializes with the existing archive writer. Its native
	// pause hook runs after committing the mutation-blocked phase under its
	// exclusive lifecycle lock (integration).
	tx, err := o.Resolver.DB.BeginTx(ctx, nil)
	if err != nil {
		return Observation{}, err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM projects.projects WHERE project_id=$1 FOR SHARE`, call.Target.ProjectID).Scan(&status); err != nil {
		return Observation{}, err
	}
	if status != "active" && status != "draft" && status != "paused" {
		return Observation{}, fail(pc.DeclarationTargetUnavailable, "project_archived")
	}
	if err = (declarationSourceFence{Call: call, Resolver: o.Resolver}).Check(ctx); err != nil {
		return Observation{}, err
	}
	operation := "reconcile"
	if payload.Spec == nil {
		operation = "retire"
	}
	if payload.Agent != nil {
		operation = "reconcile_agent"
	}
	if operation == "retire" && call.Expected.Bindings[payload.Resource].Kind == pc.DeclarationAgent {
		operation = "retire_agent"
	}
	out, err := o.Resolver.Hermes.Call(ctx, hs.ProjectRequest{Operation: operation, Source: payload.Source, ProjectID: call.Target.ProjectID, Resource: string(payload.Resource), ExpectedRevision: call.Expected.Revisions["hermesschedules:"+string(payload.Resource)], ExpectedID: call.Expected.Bindings[payload.Resource].OwnerRef, Token: call.Token, InputHash: call.Action.InputHash, Spec: payload.Spec, Agent: payload.Agent})
	if errors.Is(err, hs.ErrProjectConflict) {
		return Observation{}, fail(pc.DeclarationPlanStale, "hermes_native_changed")
	}
	if errors.Is(err, hs.ErrTerminalRequiresResume) {
		return Observation{}, fail(pc.DeclarationOwnerFailed, "hermes_terminal_requires_native_resume")
	}
	if err != nil || len(out.Jobs) != 1 {
		return Observation{}, fail(pc.DeclarationOwnerFailed, "hermes_commit_uncertain")
	}
	return hermesReceipt(call, out.Jobs[0])
}

// PauseHermesProject is the archive owner's hook after committing its project
// mutation-blocked lifecycle fence. It never resumes, removes, adopts or dispatches native jobs.
func (r *LocalResolver) PauseHermesProject(ctx context.Context, projectID string) error {
	if r.Hermes == nil {
		return nil
	}
	_, err := r.Hermes.Call(ctx, hs.ProjectRequest{Operation: "pause_project", Source: r.HermesSource, ProjectID: projectID})
	if err != nil {
		return fail(pc.DeclarationTargetUnavailable, "hermes_archive_pause_unavailable")
	}
	return nil
}

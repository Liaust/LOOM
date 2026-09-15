package projectapply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

type ProjectsOwner struct {
	Resolver *LocalResolver
	Projects projects.Service
}

func (o ProjectsOwner) Validate(ctx context.Context, a pc.DeclarationAction, raw json.RawMessage) error {
	if a.Kind != pc.DeclarationRegisterProject || a.Owner != pc.DeclarationOwnerProjects || a.ID != "register_project:project" || a.Resource != "" {
		return fail(pc.DeclarationUnsupported, "atomic_registration_required")
	}
	var input projects.DeclarationRegistrationPayload
	if err := strictOwnerPayload(raw, &input); err != nil {
		return err
	}
	if input.Project.ID != a.TargetRef || input.Repositories == nil || input.ContractPath == "" || !digestPattern.MatchString(input.ContractHash) {
		return fail(pc.DeclarationInvalid, "invalid_registration_payload")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, present := fields["predecessor"]; present && input.Predecessor == nil {
		return fail(pc.DeclarationInvalid, "invalid_registration_predecessor")
	}
	if input.Predecessor != nil {
		if projects.ValidateDeclarationLegacyPredecessor(input.Predecessor) != nil || !digestPattern.MatchString(input.IntentHash) || len(input.Declaration) == 0 || string(input.Declaration) == "null" || input.Predecessor.ProjectID != input.Project.ID || input.Predecessor.ProjectRoot != input.ProjectRoot || len(input.Repositories) != 0 {
			return fail(pc.DeclarationInvalid, "invalid_registration_predecessor")
		}
	} else if input.IntentHash != "" || len(input.Declaration) > 0 {
		return fail(pc.DeclarationInvalid, "unbound_registration_intent")
	}
	seen := map[string]bool{}
	for _, m := range input.Repositories {
		if !keyPattern.MatchString(m.Key) || seen[m.Key] || !pc.ValidDeclarationRepositoryStateRoot(m.StateRoot) || (m.Role != projects.ProjectRepositoryRolePrimary && m.Role != projects.ProjectRepositoryRoleComponent && m.Role != projects.ProjectRepositoryRoleReference) {
			return fail(pc.DeclarationInvalid, "invalid_registration_repository")
		}
		seen[m.Key] = true
	}
	return nil
}
func strictOwnerPayload(raw []byte, target any) error {
	if _, err := CanonicalJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fail(pc.DeclarationInvalid, "unknown_owner_payload_field")
	}
	return nil
}
func projectCall(call ActionCall) projects.DeclarationOwnerRequest {
	repos := map[string]string{}
	for key, b := range call.Expected.Bindings {
		if b.Kind == pc.DeclarationRepository && b.RepositoryID != "" {
			repos[string(key)] = b.RepositoryID
		}
	}
	return projects.DeclarationOwnerRequest{OperationID: call.OperationID, ActionID: call.Action.ID, Token: call.Token, InputHash: call.Action.InputHash, TargetNodeID: call.Target.OwnerNodeID, AuthorityRevision: call.Expected.Revisions["authorization:"+call.Principal.ActorID+":"+call.Target.OwnerNodeID], Expected: projects.DeclarationProjectState{RegistryRevision: call.Expected.Revisions["projects:registry"], LifecycleRevision: call.Expected.Revisions["projects:lifecycle"], Repositories: repos}}
}
func projectReceipt(call ActionCall, result *projects.DeclarationOwnerResult) (*Receipt, error) {
	var desired projects.DeclarationRegistrationPayload
	if err := strictOwnerPayload(call.Payload, &desired); err != nil {
		return nil, err
	}
	receipt := &Receipt{Token: call.Token, ActionID: call.Action.ID, Owner: call.Action.Owner, InputHash: call.Action.InputHash, EffectRef: result.EffectRef, Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{}}
	for key, change := range map[string]RevisionChange{"projects:registry": {Before: result.Before.RegistryRevision, After: result.After.RegistryRevision}, "projects:lifecycle": {Before: result.Before.LifecycleRevision, After: result.After.LifecycleRevision}} {
		if change.Before != change.After {
			receipt.Revisions[key] = change
		}
	}
	for _, m := range desired.Repositories {
		key := pc.ResourceKey(m.Key)
		expected, ok := call.Expected.Bindings[key]
		if !ok || expected.Kind != pc.DeclarationRepository {
			return nil, fail(pc.DeclarationInvalid, "registration_binding_missing")
		}
		before := pc.DeclarationBinding{Kind: pc.DeclarationRepository, RepositoryID: result.Before.Repositories[m.Key]}
		if before.RepositoryID == "" {
			before.RepositoryID = m.RepositoryID
		}
		after := before
		after.RepositoryID = result.After.Repositories[m.Key]
		if after.RepositoryID == "" {
			return nil, fail(pc.DeclarationOwnerFailed, "registered_repository_missing")
		}
		if before != after {
			receipt.Bindings[key] = BindingChange{Before: before, After: after}
		}
	}
	return receipt, nil
}
func (o ProjectsOwner) Observe(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	found, err := o.Projects.ObserveDeclarationRegistration(ctx, declarationRequest(call.Principal), call.Target.ProjectID, projectCall(call))
	if err != nil {
		return Observation{}, err
	}
	if found == nil {
		return Observation{State: Absent}, nil
	}
	receipt, err := projectReceipt(call, found)
	return Observation{State: Committed, Receipt: receipt}, err
}
func (o ProjectsOwner) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	x, err := o.Resolver.load(ctx, call.Principal, call.Target.ProjectRoot, call.Target.OwnerNodeID)
	if err != nil {
		return Observation{}, err
	}
	if !same(localPrerequisitesFor(call.Principal, x, call.Expected.Bindings), call.Expected) {
		return Observation{}, fail(pc.DeclarationPlanStale, "registration_prerequisite_changed")
	}
	var frozen projects.DeclarationRegistrationPayload
	if err = strictOwnerPayload(call.Payload, &frozen); err != nil {
		return Observation{}, err
	}
	var desired projects.DeclarationRegistrationPayload
	if x.LegacyContract != nil {
		if frozen.Predecessor == nil || !same(x.LegacyContract, frozen.Predecessor.Contract) || x.PhysicalRoot != frozen.Predecessor.PhysicalRoot {
			return Observation{}, fail(pc.DeclarationPlanStale, "legacy_predecessor_source_changed")
		}
		desired, err = projects.BuildDeclarationLegacyRegistrationIntent(x.Input, frozen.Predecessor)
	} else {
		desired, err = projects.BuildDeclarationRegistrationPayload(x.Input)
	}
	if err != nil {
		return Observation{}, err
	}
	raw, err := canonicalValue(desired)
	if err != nil || !bytes.Equal(raw, call.Payload) {
		return Observation{}, fail(pc.DeclarationPlanStale, "registration_source_changed")
	}
	// Pass the actual full current domain membership vector, including removed
	// keys. It is already covered by the exact registry CAS above.
	domainCall := projectCall(call)
	domainCall.Expected = x.State
	domainCall.Predecessor = frozen.Predecessor
	result, err := o.Projects.RegisterDeclaration(ctx, declarationRequest(call.Principal), x.Input, domainCall, declarationSourceFence{Call: call, Resolver: o.Resolver})
	if err != nil {
		return Observation{}, err
	}
	receipt, err := projectReceipt(call, result)
	return Observation{State: Committed, Receipt: receipt}, err
}

type declarationSourceFence struct {
	Call     ActionCall
	Resolver *LocalResolver
}

func (f declarationSourceFence) Check(ctx context.Context) error {
	if f.Call.Fence == nil {
		return fmt.Errorf("missing operation fence")
	}
	if err := f.Call.Fence.Check(ctx); err != nil {
		return err
	}
	// Current() cannot see uncommitted owner rows. Compare only sources/target
	// here; the domain transaction separately owns registry and authority CAS.
	x, err := f.Resolver.load(ctx, f.Call.Principal, f.Call.Target.ProjectRoot, f.Call.Target.OwnerNodeID)
	if err != nil {
		return err
	}
	current := localPrerequisites(f.Call.Principal, x)
	if !same(current.Sources, f.Call.Expected.Sources) || current.Target != f.Call.Target {
		return fail(pc.DeclarationPlanStale, "source_changed_before_registration_commit")
	}
	return nil
}

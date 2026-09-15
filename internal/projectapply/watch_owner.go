package projectapply

import (
	"context"
	"encoding/json"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
)

// WatchOwner records a single exact owner intent. Shared roots are queued once,
// by the final contributor, whose receipt awaits real node configuration ACK.
type WatchOwner struct {
	Resolver   *LocalResolver
	Reconciler projectwatch.DeclarationReconciler
}

func (o WatchOwner) Validate(ctx context.Context, a pc.DeclarationAction, raw json.RawMessage) error {
	payload, err := projectwatch.ValidateDeclarationWatchIntent(raw)
	if err != nil {
		return fail(pc.DeclarationInvalid, "invalid_watch_intent")
	}
	kind := pc.DeclarationEnrollKnowledge
	if payload.Owner == string(pc.DeclarationOwnerProtection) {
		kind = pc.DeclarationReconcileProtection
	}
	if payload.Retire {
		kind = pc.DeclarationRetireResource
	}
	if a.Kind != kind || string(a.Owner) != payload.Owner || string(a.Resource) != payload.Resource || a.TargetRef != payload.Group.ProjectID+"/"+payload.Resource {
		return fail(pc.DeclarationInvalid, "watch_action_identity_mismatch")
	}
	for _, c := range payload.Group.Contributors {
		if c.ActionID == a.ID && c.Owner == payload.Owner && c.Resource == payload.Resource && c.Retire == payload.Retire {
			return nil
		}
	}
	return fail(pc.DeclarationInvalid, "watch_action_contributor_mismatch")
}
func watchCall(call ActionCall) (projects.DeclarationWatchIntentRequest, error) {
	payload, err := projectwatch.ValidateDeclarationWatchIntent(call.Payload)
	return projects.DeclarationWatchIntentRequest{DeclarationOwnerRequest: projectCall(call), Payload: payload, ExpectedRevision: call.Expected.Revisions[string(call.Action.Owner)+":"+string(call.Action.Resource)]}, err
}
func watchObservation(call ActionCall, result *projects.DeclarationWatchIntentObservation) (Observation, error) {
	if result == nil {
		return Observation{State: Absent}, nil
	}
	if result.Final && result.Stage == "pending" {
		return Observation{State: Pending, CauseCode: "awaiting_node_configuration"}, nil
	}
	if result.Final && (result.Stage != "applied" || !result.Applied) {
		return Observation{State: Uncertain, CauseCode: "node_configuration_evidence_unavailable"}, nil
	}
	if !result.Final && result.Stage != "registered" {
		return Observation{}, fail(pc.DeclarationOwnerFailed, "invalid_watch_intent_stage")
	}
	receipt := &Receipt{Token: call.Token, ActionID: call.Action.ID, Owner: call.Action.Owner, InputHash: call.Action.InputHash, EffectRef: result.EffectRef, Revisions: map[string]RevisionChange{string(call.Action.Owner) + ":" + string(call.Action.Resource): {Before: result.BeforeRevision, After: result.AfterRevision}}, Bindings: map[pc.ResourceKey]BindingChange{}}
	return Observation{State: Committed, Receipt: receipt}, nil
}
func (o WatchOwner) Observe(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	domain, err := watchCall(call)
	if err != nil {
		return Observation{}, err
	}
	result, err := o.Reconciler.Observe(ctx, declarationRequest(call.Principal), domain)
	if err != nil {
		return Observation{}, err
	}
	return watchObservation(call, result)
}
func (o WatchOwner) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	domain, err := watchCall(call)
	if err != nil {
		return Observation{}, err
	}
	x, err := o.Resolver.load(ctx, call.Principal, call.Target.ProjectRoot, call.Target.OwnerNodeID)
	if err != nil {
		return Observation{}, err
	}
	if !same(localPrerequisitesFor(call.Principal, x, call.Expected.Bindings), call.Expected) {
		return Observation{}, fail(pc.DeclarationPlanStale, "watch_prerequisite_changed")
	}
	group, err := projectwatch.BuildDeclarationWatchGroup(x.Analysis, x.Target.OwnerNodeID, x.Input.Project.OwnerNode, domain.Payload.Group.Contributors)
	desired := domain.Payload.Group
	desired.Predecessor = nil
	if err != nil || !same(group, desired) {
		return Observation{}, fail(pc.DeclarationPlanStale, "watch_source_changed")
	}
	if p := domain.Payload.Group.Predecessor; p != nil {
		if !same(x.LegacyContract, p.Contract) || x.PhysicalRoot != p.PhysicalRoot {
			return Observation{}, fail(pc.DeclarationPlanStale, "watch_predecessor_source_changed")
		}
	} else if x.LegacyContract != nil {
		return Observation{}, fail(pc.DeclarationPlanStale, "watch_predecessor_required")
	}
	domain.Expected = x.State
	result, err := o.Reconciler.Commit(ctx, declarationRequest(call.Principal), domain, declarationSourceFence{Call: call, Resolver: o.Resolver})
	if err != nil {
		return Observation{}, err
	}
	return watchObservation(call, result)
}

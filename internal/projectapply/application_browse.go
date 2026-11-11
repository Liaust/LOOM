package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/routing"
	sr "loom.local/loom/internal/serviceregistry"
)

func validateApplicationBrowse(a pc.DeclarationAction, p applicationPayload) error {
	q := p.Browse
	address, e := capabilities.ParseAddress(p.Endpoint)
	if q == nil || p.Preparation != nil || p.Request.SchemaVersion != "" || a.Kind != pc.DeclarationApplyApplication || a.ID != "apply_application_browse:"+string(a.Resource) || a.Owner != pc.DeclarationOwnerApplication || q.Operation != "apply" || q.OperationToken != "" || q.Owner.ProjectID+"/"+q.Owner.Resource != a.TargetRef || q.Owner.Resource != string(a.Resource) || e != nil || p.Endpoint != capabilities.NodeSystemProviderAddress(strings.TrimPrefix(address.ScopePath, "workspace/"))+".project.application.browse" || a.PublicURL != "" || len(q.Views) > 32 {
		return fail(pc.DeclarationInvalid, "application_browse_payload_invalid")
	}
	for _, v := range q.Views {
		if !v.View.Valid() || v.BindingRef == "" {
			return fail(pc.DeclarationInvalid, "application_browse_payload_invalid")
		}
	}
	return nil
}
func browseCallInput(call ActionCall) (applicationPayload, []byte, error) {
	var p applicationPayload
	if strictOwnerPayload(call.Payload, &p) != nil || validateApplicationBrowse(call.Action, p) != nil {
		return p, nil, fail(pc.DeclarationInvalid, "application_browse_payload_invalid")
	}
	if p.Browse.Owner.ProjectID != call.Target.ProjectID || p.Browse.Owner.NodeID != call.Target.OwnerNodeID {
		return p, nil, fail(pc.DeclarationIdentityConflict, "application_target_mismatch")
	}
	p.Browse.OperationToken = call.Token
	raw, e := json.Marshal(p.Browse)
	return p, raw, e
}
func (o ApplicationOwner) observeBrowse(ctx context.Context, call ActionCall) (Observation, error) {
	p, raw, e := browseCallInput(call)
	if e != nil {
		return Observation{}, e
	}
	if nilInterface(o.Calls) {
		return Observation{}, fail(pc.DeclarationTargetUnavailable, "application_routing_not_configured")
	}
	found, e := o.Calls.FindCapabilityCallByToken(ctx, call.Principal.ActorID, call.Token)
	if errors.Is(e, sql.ErrNoRows) {
		return Observation{State: Absent}, nil
	}
	if e != nil {
		return Observation{}, e
	}
	if found.ActorID != call.Principal.ActorID || found.OriginNodeID != call.Principal.OriginNodeID || found.TargetNodeID != call.Target.OwnerNodeID || found.Operation != "capability:"+p.Endpoint || !sameJSON(found.InputJSON, raw) {
		return Observation{}, fail(pc.DeclarationOperationConflict, "application_call_identity_conflict")
	}
	if found.Status == routing.CapabilityCallStatusFailed {
		return Observation{State: Uncertain, CauseCode: "application_browse_failed"}, nil
	}
	if found.Status != routing.CapabilityCallStatusCompleted {
		return Observation{State: Pending, CauseCode: "application_node_execution_pending"}, nil
	}
	var result sr.ApplicationBrowseReceipt
	if json.Unmarshal(found.ResultJSON, &result) != nil || result.State != "succeeded" || result.Owner != p.Browse.Owner || result.OperationToken != call.Token || result.InputDigest != p.Browse.Digest() || result.Revision == "" {
		return Observation{}, fail(pc.DeclarationOwnerFailed, "application_receipt_invalid")
	}
	return Observation{State: Committed, Receipt: &Receipt{Token: call.Token, ActionID: call.Action.ID, Owner: call.Action.Owner, InputHash: call.Action.InputHash, EffectRef: "application-browse:" + result.Revision, Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{}}}, nil
}
func (o ApplicationOwner) applyBrowse(ctx context.Context, call ActionCall) (Observation, error) {
	observed, e := o.observeBrowse(ctx, call)
	if e != nil || observed.State != Absent {
		return observed, e
	}
	p, raw, e := browseCallInput(call)
	if e != nil {
		return Observation{}, e
	}
	if call.Fence == nil {
		return Observation{}, fail(pc.DeclarationUnauthorized, "application_fence_required")
	}
	if e = call.Fence.Check(ctx); e != nil {
		return Observation{}, e
	}
	req := declarationRequest(call.Principal)
	req.CorrelationID = call.OperationID
	if _, e = o.Calls.Call(ctx, req, routing.CapabilityCallInput{Target: p.Endpoint, Input: raw}, call.Token); e != nil {
		return Observation{}, applicationFailure(e)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		observed, e = o.observeBrowse(ctx, call)
		if e != nil || observed.State != Pending {
			return observed, e
		}
		select {
		case <-ctx.Done():
			return observed, ctx.Err()
		case <-deadline.C:
			return observed, nil
		case <-tick.C:
		}
	}
}
func (o ApplicationOwner) browseStatus(ctx context.Context, a pc.DeclarationAction, raw json.RawMessage, p applicationPayload) (pc.DeclarationReadiness, error) {
	out := readiness(a.InputHash)
	if e := o.Validate(ctx, a, raw); e != nil {
		return out, e
	}
	reader, ok := o.Inspector.(interface {
		Browse(context.Context, sr.ApplicationBrowseRequest) (sr.ApplicationBrowseReceipt, error)
	})
	if !ok {
		return out, fail(pc.DeclarationTargetUnavailable, "application_browse_unavailable")
	}
	q := *p.Browse
	q.Operation = "inspect"
	q.OperationToken = "project-browse-status"
	result, e := reader.Browse(ctx, q)
	if e != nil {
		return out, applicationFailure(e)
	}
	if result.Matches {
		out.Applied = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: result.Revision, EvidenceRef: "application-browse:" + result.Revision}
	} else {
		out.Applied.State = pc.DeclarationPending
	}
	return out, nil
}

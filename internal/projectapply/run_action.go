package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"loom.local/loom/internal/capabilities"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/routing"
	sr "loom.local/loom/internal/serviceregistry"
)

type applicationActionReader interface {
	InspectAction(context.Context, sr.ApplicationActionRequest) (sr.ApplicationActionReceipt, error)
}

func (s *Service) RunApplicationAction(ctx context.Context, principal Principal, input pc.ProjectActionRequest) (pc.ProjectActionResult, error) {
	var out pc.ProjectActionResult
	if err := s.config(true); err != nil {
		return out, err
	}
	if err := validPrincipal(principal); err != nil {
		return out, err
	}
	if input.Check && input.ReleaseFailed {
		return out, fail(pc.DeclarationInvalid, "application_action_release_is_not_read_only")
	}
	if !keyPattern.MatchString(input.Action) || (input.Run != "" && (!causePattern.MatchString(input.Run) || len(input.Run) > 64)) {
		return out, fail(pc.DeclarationInvalid, "application_action_invalid")
	}
	r, ok := s.resolver.(*LocalResolver)
	if !ok {
		return out, fail(pc.DeclarationUnsupported, "application_action_not_configured")
	}
	x, err := r.load(ctx, principal, input.ProjectRef, input.NodeRef)
	if err != nil {
		return out, err
	}
	resource, exists := x.Analysis.Loaded.Declaration.Resources[input.Resource]
	if !exists || resource.Application == nil {
		return out, fail(pc.DeclarationInvalid, "application_resource_required")
	}
	if !input.Check && (!x.Authority.CanWrite || x.Authority.Level < 5) {
		return out, fail(pc.DeclarationUnauthorized, "application_execution_authority_required")
	}
	if _, declared := resource.Application.Actions[input.Action]; !declared {
		return out, fail(pc.DeclarationInvalid, "application_action_not_declared")
	}
	helper, ok := r.Applications.(applicationActionReader)
	if !ok {
		return out, fail(pc.DeclarationUnsupported, "application_action_not_configured")
	}
	owner := sr.ApplicationOwner{ProjectID: x.Target.ProjectID, NodeID: x.Target.OwnerNodeID, Resource: string(input.Resource)}
	facts, err := r.Applications.QueryPrerequisites(ctx, sr.ApplicationPrerequisiteQuery{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: owner})
	if err != nil {
		return out, applicationFailure(err)
	}
	if !facts.Installation.Present || facts.Installation.Applied == nil || !*facts.Installation.Applied {
		return out, fail(pc.DeclarationTargetUnavailable, "application_installation_required")
	}
	run := input.Run
	if run == "" {
		run = "default"
	}
	identity, _ := canonicalValue(struct {
		Owner                 sr.ApplicationOwner
		Revision, Action, Run string
	}{owner, facts.Installation.Revision, input.Action, run})
	q := sr.ApplicationActionRequest{Owner: owner, Name: input.Action, Token: "action:" + hashBytes(identity)[7:], ExpectedInstallationRevision: facts.Installation.Revision}
	q.ReleaseFailed = input.ReleaseFailed
	out = pc.ProjectActionResult{ProjectID: owner.ProjectID, Resource: input.Resource, Action: input.Action, Run: run, State: "pending"}
	projectActionResult := func(receipt sr.ApplicationActionReceipt) pc.ProjectActionResult {
		out.State, out.ErrorCode, out.ServiceResumed = string(receipt.Phase), receipt.ErrorCode, receipt.ServiceResumed
		out.Finalized = receipt.Finalized
		if receipt.Phase == sr.ApplicationActionSucceeded && !receipt.Finalized {
			out.State = "awaiting_finalization"
		}
		if input.ReleaseFailed && receipt.Phase == sr.ApplicationActionFailed && receipt.Finalized {
			out.State = "released"
		}
		return out
	}
	if input.Check {
		receipt, err := helper.InspectAction(ctx, q)
		if err != nil {
			return out, applicationFailure(err)
		}
		return projectActionResult(receipt), nil
	}
	endpoint := capabilities.NodeSystemProviderAddress(x.Input.Project.OwnerNode) + ".project.application.action"
	raw, _ := json.Marshal(q)
	calls := routing.NewService(s.db)
	token := q.Token
	if input.ReleaseFailed {
		token = hashBytes([]byte("application-action-release-failed:" + q.Token))
	}
	for attempt := 0; attempt < 16; attempt++ {
		found, err := calls.FindCapabilityCallByToken(ctx, principal.ActorID, token)
		if errors.Is(err, sql.ErrNoRows) {
			request := declarationRequest(principal)
			request.CorrelationID = q.Token
			result, err := calls.Call(ctx, request, routing.CapabilityCallInput{Target: endpoint, Input: raw}, token)
			out.CallRef = result.CapabilityCall.CapabilityCallID
			if err != nil {
				return out, applicationFailure(err)
			}
			return out, nil
		}
		if err != nil {
			return out, applicationFailure(err)
		}
		out.CallRef = found.CapabilityCallID
		if found.ActorID != principal.ActorID || found.OriginNodeID != principal.OriginNodeID || found.TargetNodeID != owner.NodeID || found.Operation != "capability:"+endpoint || !sameJSON(found.InputJSON, raw) {
			return out, fail(pc.DeclarationIdentityConflict, "application_action_call_conflict")
		}
		if found.Status != routing.CapabilityCallStatusCompleted && found.Status != routing.CapabilityCallStatusFailed {
			return out, nil
		}
		var receipt sr.ApplicationActionReceipt
		bound := json.Unmarshal(found.ResultJSON, &receipt) == nil && receipt.Owner == owner && receipt.Token == q.Token && receipt.InstallationRevision == q.ExpectedInstallationRevision
		if found.Status == routing.CapabilityCallStatusCompleted && !bound {
			return out, fail(pc.DeclarationIdentityConflict, "application_action_receipt_invalid")
		}
		if bound && (receipt.Phase == sr.ApplicationActionSucceeded && receipt.Finalized || receipt.Phase == sr.ApplicationActionFailed) {
			return projectActionResult(receipt), nil
		}
		observed, err := helper.InspectAction(ctx, q)
		if err != nil {
			out.State, out.ErrorCode = "uncertain", "application.action.observation_uncertain"
			return out, nil
		}
		if observed.Phase == sr.ApplicationActionPending {
			return projectActionResult(observed), nil
		}
		if observed.Phase != sr.ApplicationActionSucceeded && observed.Phase != sr.ApplicationActionFailed {
			return projectActionResult(observed), nil
		}
		// Persist the same terminal result, never execute its package again.
		// Failure stays held; only success may resume the former consumer.
		token = hashBytes([]byte("application-action-finalize:" + q.Token + ":" + found.CapabilityCallID))
	}
	return out, fail(pc.DeclarationOperationConflict, "application_action_continuation_limit")
}

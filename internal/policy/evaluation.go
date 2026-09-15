package policy

import (
	"context"
	"encoding/json"

	"loom.local/loom/internal/requestctx"
)

// Shared read-only evaluation. Lifecycle mutations belong to the caller after
// this returns; queryer deliberately exposes no Exec operation.
type policyEvaluation struct {
	actor                   *actorRecord
	originNode              *nodeRecord
	scope                   *scopeRecord
	operation               policyOperation
	target                  *capabilityTarget
	outcome                 decisionOutcome
	actorAuthorizationLevel *int
	coveringGrant           *Grant
	contextSummary          json.RawMessage
	contextHash             string
}

func evaluatePolicy(ctx context.Context, q queryer, req requestctx.Context, input DecisionInput) (policyEvaluation, error) {
	actorRef := defaultString(input.ActorRef, req.ActorID)
	actor, err := resolveActorRecord(ctx, q, actorRef)
	if err != nil {
		return policyEvaluation{}, err
	}

	originNodeRef := defaultString(input.OriginNodeRef, req.OriginNodeID)
	originNode, err := resolveNodeRecord(ctx, q, originNodeRef)
	if err != nil {
		return policyEvaluation{}, err
	}

	scopeRef := defaultString(input.ScopeRef, req.ScopeID)
	scope, err := resolveScopeRecord(ctx, q, scopeRef)
	if err != nil {
		return policyEvaluation{}, err
	}

	operation := parsePolicyOperation(input.Operation)
	var target *capabilityTarget
	if operation.Supported && operation.ResourceKind == resourceKindCapability {
		target, err = resolveCapabilityTarget(ctx, q, operation.ResourceRef)
		if err != nil {
			return policyEvaluation{}, err
		}
	}

	outcome := decisionOutcome{
		Decision:        DecisionDeny,
		ReasonCode:      "policy.unsupported_operation",
		SafeExplanation: "Operation is not a supported policy target. Slice 8 only evaluates capability:<compact-address> operations.",
	}
	var actorAuthorizationLevel *int
	var coveringGrant *Grant

	switch {
	case !operation.Supported:
		// Keep default unsupported operation denial.
	case actor == nil || actor.Status != "active":
		outcome = decisionOutcome{
			Decision:        DecisionDeny,
			ReasonCode:      "actor_inactive",
			SafeExplanation: "Actor is missing, disabled, or revoked.",
		}
	case originNode == nil || originNode.Status != "active":
		outcome = decisionOutcome{
			Decision:        DecisionDeny,
			ReasonCode:      "node_inactive",
			SafeExplanation: "Origin node is missing, disabled, retired, or quarantined.",
		}
	case target == nil || target.EndpointStatus != "active":
		outcome = decisionOutcome{
			Decision:        DecisionDeny,
			ReasonCode:      "capability_inactive",
			SafeExplanation: "Capability endpoint is missing or not active.",
		}
	case target.ProviderStatus != "active":
		outcome = decisionOutcome{
			Decision:        DecisionDeny,
			ReasonCode:      "provider_inactive",
			SafeExplanation: "Capability provider is missing or not active.",
		}
	case target.TargetNodeStatus != "active":
		outcome = decisionOutcome{
			Decision:        DecisionDeny,
			ReasonCode:      "node_inactive",
			SafeExplanation: "Target node is missing, disabled, retired, or quarantined.",
		}
	default:
		authLevel, err := activeActorAuthorizationLevel(ctx, q, actor.ID, target.TargetNodeID)
		if err != nil {
			return policyEvaluation{}, err
		}
		actorAuthorizationLevel = authLevel
		if authLevel == nil {
			outcome = decisionOutcome{
				Decision:        DecisionDeny,
				ReasonCode:      "actor_not_authorized_on_target_node",
				SafeExplanation: "Actor has no active authorization on the capability target node.",
			}
			break
		}

		grant, err := findCoveringGrant(ctx, q, grantCoverageTarget{
			ActorID:                     actor.ID,
			TargetNodeID:                target.TargetNodeID,
			ScopeID:                     optionalRecordID(scope),
			CapabilityEndpointID:        target.EndpointID,
			RiskLevel:                   target.RiskLevel,
			ExecutionAuthorizationLevel: target.ExecutionAuthorizationLevel,
		})
		if err != nil {
			return policyEvaluation{}, err
		}
		if grant != nil {
			coveringGrant = grant
			outcome = decisionOutcome{
				Decision:        DecisionAllow,
				ReasonCode:      "actor_allowed_by_grant",
				SafeExplanation: "Actor has an active grant that covers this capability request.",
			}
			break
		}

		if *authLevel >= target.ExecutionAuthorizationLevel {
			outcome = decisionOutcome{
				Decision:        DecisionAllow,
				ReasonCode:      "actor_allowed_by_level",
				SafeExplanation: "Actor authorization level is sufficient for this capability.",
			}
			break
		}

		outcome = decisionOutcome{
			Decision:        DecisionApprovalRequired,
			ReasonCode:      "actor_level_below_capability_level",
			SafeExplanation: "Actor authorization level is below the capability requirement; approval is required.",
		}
	}

	contextSummary, contextHash, err := buildContextSummary(req, input, operation, actor, originNode, scope, target, actorAuthorizationLevel, coveringGrant)
	if err != nil {
		return policyEvaluation{}, err
	}

	return policyEvaluation{actor, originNode, scope, operation, target, outcome, actorAuthorizationLevel, coveringGrant, contextSummary, contextHash}, nil
}

package policy

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"loom.local/loom/internal/requestctx"
)

// PreviewInput deliberately has no caller-supplied actor or origin identity.
type PreviewInput struct {
	Operation string `json:"operation"`
	ScopeRef  string `json:"scope_ref,omitempty"`
}

// PolicyPreview is an observation, never a durable decision, approval or permit.
// Project membership and lifecycle checks remain with the projects owner.
type PolicyPreview struct {
	Decision       string     `json:"decision"`
	ReasonCode     string     `json:"reason_code"`
	ActorID        *string    `json:"actor_id,omitempty"`
	OriginNodeID   *string    `json:"origin_node_id,omitempty"`
	TargetNodeID   *string    `json:"target_node_id,omitempty"`
	ScopeID        *string    `json:"scope_id,omitempty"`
	EndpointID     *string    `json:"endpoint_id,omitempty"`
	RequiredLevel  *int       `json:"required_level,omitempty"`
	ActorLevel     *int       `json:"actor_level,omitempty"`
	GrantRef       *string    `json:"grant_ref,omitempty"`
	GrantExpiresAt *time.Time `json:"grant_expires_at,omitempty"`
	ContextHash    string     `json:"context_hash"`
}

// Preview uses a stable database snapshot without lifecycle writes. In particular
// it must not use FindCoveringGrant, whose public API expires stale records.
func (s Service) Preview(ctx context.Context, req requestctx.Context, input PreviewInput) (PolicyPreview, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return PolicyPreview{}, err
	}
	defer tx.Rollback()
	evaluation, err := evaluatePolicy(ctx, tx, req, DecisionInput{Operation: strings.TrimSpace(input.Operation), ScopeRef: input.ScopeRef})
	if err != nil {
		return PolicyPreview{}, err
	}
	result := previewEvaluation(evaluation)
	if err = tx.Commit(); err != nil {
		return PolicyPreview{}, err
	}
	return result, nil
}
func previewEvaluation(e policyEvaluation) PolicyPreview {
	out := PolicyPreview{Decision: e.outcome.Decision, ReasonCode: e.outcome.ReasonCode, ActorLevel: e.actorAuthorizationLevel, ContextHash: e.contextHash}
	if e.actor != nil {
		out.ActorID = &e.actor.ID
	}
	if e.originNode != nil {
		out.OriginNodeID = &e.originNode.ID
	}
	if e.scope != nil {
		out.ScopeID = &e.scope.ID
	}
	if e.target != nil {
		out.TargetNodeID = &e.target.TargetNodeID
		out.EndpointID = &e.target.EndpointID
		out.RequiredLevel = &e.target.ExecutionAuthorizationLevel
	}
	if e.coveringGrant != nil {
		out.GrantRef = &e.coveringGrant.GrantID
		out.GrantExpiresAt = &e.coveringGrant.ExpiresAt
	}
	return out
}

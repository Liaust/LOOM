package policy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	defaultDecisionLimit = 50
	maxDecisionLimit     = 200
	defaultApprovalTTL   = 15 * time.Minute
	defaultGrantTTL      = 15 * time.Minute

	resourceKindCapability = "capability"
	resourceKindOperation  = "operation"

	operationCapabilityPrefix = "capability:"
)

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) Explain(ctx context.Context, req requestctx.Context, input DecisionInput) (PolicyExplanation, error) {
	input.Operation = strings.TrimSpace(input.Operation)
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return PolicyExplanation{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PolicyExplanation{}, err
	}
	defer tx.Rollback()

	evaluation, err := evaluatePolicy(ctx, tx, req, input)
	if err != nil {
		return PolicyExplanation{}, err
	}
	actor, originNode, scope := evaluation.actor, evaluation.originNode, evaluation.scope
	operation, target, outcome := evaluation.operation, evaluation.target, evaluation.outcome
	actorAuthorizationLevel, coveringGrant := evaluation.actorAuthorizationLevel, evaluation.coveringGrant
	contextSummary, contextHash := evaluation.contextSummary, evaluation.contextHash

	decision, err := insertDecision(ctx, tx, decisionInsert{
		Actor:                   actor,
		OriginNode:              originNode,
		Target:                  target,
		Scope:                   scope,
		Operation:               operation,
		ActorAuthorizationLevel: actorAuthorizationLevel,
		Grant:                   coveringGrant,
		Outcome:                 outcome,
		ContextSummary:          contextSummary,
		ContextHash:             contextHash,
		Metadata:                objectOrDefault(input.Metadata),
	})
	if err != nil {
		return PolicyExplanation{}, err
	}

	var approval *Approval
	var approvalCreated bool
	if decision.Decision == DecisionApprovalRequired && input.CreateApprovalRequest {
		approval, approvalCreated, err = createOrReuseApproval(ctx, tx, req, input, decision, actor, originNode, scope, target)
		if err != nil {
			return PolicyExplanation{}, err
		}
		if approval != nil {
			decision, err = updateDecisionApproval(ctx, tx, decision.PolicyDecisionID, approval.ApprovalID)
			if err != nil {
				return PolicyExplanation{}, err
			}
		}
	}

	eventReq := effectiveEventRequest(req, actor, originNode, scope)
	if err := appendDecisionEvent(ctx, tx, eventReq, decision, approval, coveringGrant, target); err != nil {
		return PolicyExplanation{}, err
	}
	if approvalCreated && approval != nil {
		if err := appendApprovalRequestedEvent(ctx, tx, eventReq, *approval, decision); err != nil {
			return PolicyExplanation{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return PolicyExplanation{}, err
	}

	explanation := PolicyExplanation{
		Decision: decision,
		Approval: approval,
		Grant:    coveringGrant,
		Target:   targetModel(operation, target),
		Context:  contextModel(req, operation, actor, originNode, scope, target, actorAuthorizationLevel, coveringGrant),
	}
	return explanation, nil
}

func (s Service) ListDecisions(ctx context.Context, filter DecisionFilter) ([]Decision, error) {
	if filter.Limit <= 0 || filter.Limit > maxDecisionLimit {
		filter.Limit = defaultDecisionLimit
	}
	if strings.TrimSpace(filter.Decision) != "" && !ValidDecision(strings.TrimSpace(filter.Decision)) {
		return nil, fmt.Errorf("unsupported decision filter: %s", filter.Decision)
	}

	query := decisionSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if actorRef := strings.TrimSpace(filter.ActorRef); actorRef != "" {
		actor, err := resolveActorRecord(ctx, s.DB, actorRef)
		if err != nil {
			return nil, err
		}
		if actor == nil {
			return nil, sql.ErrNoRows
		}
		add("actor_id =", actor.ID)
	}
	if nodeRef := strings.TrimSpace(filter.OriginNodeRef); nodeRef != "" {
		node, err := resolveNodeRecord(ctx, s.DB, nodeRef)
		if err != nil {
			return nil, err
		}
		if node == nil {
			return nil, sql.ErrNoRows
		}
		add("origin_node_id =", node.ID)
	}
	if nodeRef := strings.TrimSpace(filter.TargetNodeRef); nodeRef != "" {
		node, err := resolveNodeRecord(ctx, s.DB, nodeRef)
		if err != nil {
			return nil, err
		}
		if node == nil {
			return nil, sql.ErrNoRows
		}
		add("target_node_id =", node.ID)
	}
	if operation := strings.TrimSpace(filter.Operation); operation != "" {
		add("operation =", operation)
	}
	if decision := strings.TrimSpace(filter.Decision); decision != "" {
		add("decision =", decision)
	}
	if capabilityRef := strings.TrimSpace(filter.CapabilityEndpointRef); capabilityRef != "" {
		endpointID, err := resolveEndpointID(ctx, s.DB, capabilityRef)
		if err != nil {
			return nil, err
		}
		add("capability_endpoint_id =", endpointID)
	}
	if approvalRef := strings.TrimSpace(filter.ApprovalRef); approvalRef != "" {
		approvalID, err := resolveApprovalID(ctx, s.DB, approvalRef)
		if err != nil {
			return nil, err
		}
		add("approval_id =", approvalID)
	}
	if grantRef := strings.TrimSpace(filter.GrantRef); grantRef != "" {
		grantID, err := resolveGrantID(ctx, s.DB, grantRef)
		if err != nil {
			return nil, err
		}
		add("grant_id =", grantID)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	decisions := []Decision{}
	for rows.Next() {
		decision, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, rows.Err()
}

func (s Service) GetDecision(ctx context.Context, ref string) (Decision, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Decision{}, fmt.Errorf("decision ref is required")
	}
	row := s.DB.QueryRowContext(ctx, decisionSelectSQL()+`
		WHERE policy_decision_id = $1 OR decision_key = $1
	`, ref)
	return scanDecision(row)
}

func (s Service) ListApprovals(ctx context.Context, filter ApprovalFilter) ([]Approval, error) {
	if _, err := s.ExpireStale(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	if filter.Limit <= 0 || filter.Limit > maxDecisionLimit {
		filter.Limit = defaultDecisionLimit
	}
	if strings.TrimSpace(filter.Status) != "" && !ValidApprovalStatus(strings.TrimSpace(filter.Status)) {
		return nil, fmt.Errorf("unsupported approval status filter: %s", filter.Status)
	}

	query := approvalSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if actorRef := strings.TrimSpace(filter.ActorRef); actorRef != "" {
		actor, err := resolveActorRecord(ctx, s.DB, actorRef)
		if err != nil {
			return nil, err
		}
		if actor == nil {
			return nil, sql.ErrNoRows
		}
		add("requested_by_actor_id =", actor.ID)
	}
	if actorRef := strings.TrimSpace(filter.ApprovingActorRef); actorRef != "" {
		actor, err := resolveActorRecord(ctx, s.DB, actorRef)
		if err != nil {
			return nil, err
		}
		if actor == nil {
			return nil, sql.ErrNoRows
		}
		add("approving_actor_id =", actor.ID)
	}
	if nodeRef := strings.TrimSpace(filter.TargetNodeRef); nodeRef != "" {
		node, err := resolveNodeRecord(ctx, s.DB, nodeRef)
		if err != nil {
			return nil, err
		}
		if node == nil {
			return nil, sql.ErrNoRows
		}
		add("target_node_id =", node.ID)
	}
	if capabilityRef := strings.TrimSpace(filter.CapabilityEndpointRef); capabilityRef != "" {
		endpointID, err := resolveEndpointID(ctx, s.DB, capabilityRef)
		if err != nil {
			return nil, err
		}
		add("capability_endpoint_id =", endpointID)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	approvals := []Approval{}
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

func (s Service) GetApproval(ctx context.Context, ref string) (Approval, error) {
	if _, err := s.ExpireStale(ctx, time.Now().UTC()); err != nil {
		return Approval{}, err
	}
	approvalID, err := resolveApprovalID(ctx, s.DB, ref)
	if err != nil {
		return Approval{}, err
	}
	row := s.DB.QueryRowContext(ctx, approvalSelectSQL()+` WHERE approval_id = $1`, approvalID)
	return scanApproval(row)
}

func (s Service) DecideApproval(ctx context.Context, req requestctx.Context, input ApprovalDecisionInput) (ApprovalDecisionResult, error) {
	input.ApprovalRef = strings.TrimSpace(input.ApprovalRef)
	input.Decision = strings.TrimSpace(input.Decision)
	input.DecidingActorRef = strings.TrimSpace(input.DecidingActorRef)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	if input.ApprovalRef == "" {
		return ApprovalDecisionResult{}, fmt.Errorf("approval_ref is required")
	}
	if !ValidApprovalDecision(input.Decision) {
		return ApprovalDecisionResult{}, fmt.Errorf("decision must be approve or deny")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	defer tx.Rollback()

	approvalID, err := resolveApprovalID(ctx, tx, input.ApprovalRef)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	approval, err := scanApproval(tx.QueryRowContext(ctx, approvalSelectSQL()+` WHERE approval_id = $1 FOR UPDATE`, approvalID))
	if err != nil {
		return ApprovalDecisionResult{}, err
	}

	deciderRef := defaultString(input.DecidingActorRef, req.ActorID)
	decider, err := resolveActorRecord(ctx, tx, deciderRef)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if decider == nil || decider.Status != "active" {
		return ApprovalDecisionResult{}, fmt.Errorf("approving actor is missing, disabled, or revoked")
	}
	eventReq := effectiveEventRequest(req, decider, nil, nil)

	now := time.Now().UTC()
	if approval.Status != ApprovalPending {
		return ApprovalDecisionResult{}, fmt.Errorf("approval is not pending: %s", approval.Status)
	}
	if !approval.ExpiresAt.After(now) {
		expired, err := updateApprovalStatus(ctx, tx, approval.ApprovalID, ApprovalExpired, decider.ID, "approval expired before decision")
		if err != nil {
			return ApprovalDecisionResult{}, err
		}
		if err := appendApprovalLifecycleEvent(ctx, tx, eventReq, events.TypeApprovalExpired, expired, "approval_expired"); err != nil {
			return ApprovalDecisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ApprovalDecisionResult{}, err
		}
		return ApprovalDecisionResult{}, fmt.Errorf("approval is expired")
	}
	if approval.TargetNodeID == nil || *approval.TargetNodeID == "" {
		return ApprovalDecisionResult{}, fmt.Errorf("approval has no target node")
	}
	authLevel, err := activeActorAuthorizationLevel(ctx, tx, decider.ID, *approval.TargetNodeID)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if authLevel == nil || *authLevel < approval.ExecutionAuthorizationLevel {
		return ApprovalDecisionResult{}, fmt.Errorf("approving actor authorization is below required level %d", approval.ExecutionAuthorizationLevel)
	}

	if input.Decision == ApprovalDecisionDeny {
		denied, err := updateApprovalStatus(ctx, tx, approval.ApprovalID, ApprovalDenied, decider.ID, input.DecisionReason)
		if err != nil {
			return ApprovalDecisionResult{}, err
		}
		if err := appendApprovalLifecycleEvent(ctx, tx, eventReq, events.TypeApprovalDenied, denied, "approval_denied"); err != nil {
			return ApprovalDecisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ApprovalDecisionResult{}, err
		}
		return ApprovalDecisionResult{Approval: denied}, nil
	}

	ttl := input.GrantTTL
	if ttl <= 0 && input.GrantTTLSeconds > 0 {
		ttl = time.Duration(input.GrantTTLSeconds) * time.Second
	}
	if ttl <= 0 {
		ttl = defaultGrantTTL
	}
	grant, err := insertApprovalGrant(ctx, tx, approval, decider.ID, now.Add(ttl))
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	approved, err := updateApprovalApproved(ctx, tx, approval.ApprovalID, decider.ID, input.DecisionReason, grant.GrantID)
	if err != nil {
		return ApprovalDecisionResult{}, err
	}
	if err := appendApprovalLifecycleEvent(ctx, tx, eventReq, events.TypeApprovalApproved, approved, "approval_approved"); err != nil {
		return ApprovalDecisionResult{}, err
	}
	if err := appendGrantLifecycleEvent(ctx, tx, eventReq, events.TypeGrantIssued, grant, "grant_issued"); err != nil {
		return ApprovalDecisionResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return ApprovalDecisionResult{}, err
	}
	return ApprovalDecisionResult{Approval: approved, Grant: &grant}, nil
}

func (s Service) ListGrants(ctx context.Context, filter GrantFilter) ([]Grant, error) {
	if _, err := s.ExpireStale(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	if filter.Limit <= 0 || filter.Limit > maxDecisionLimit {
		filter.Limit = defaultDecisionLimit
	}
	if strings.TrimSpace(filter.Status) != "" && !ValidGrantStatus(strings.TrimSpace(filter.Status)) {
		return nil, fmt.Errorf("unsupported grant status filter: %s", filter.Status)
	}
	if strings.TrimSpace(filter.GrantType) != "" && !ValidGrantType(strings.TrimSpace(filter.GrantType)) {
		return nil, fmt.Errorf("unsupported grant type filter: %s", filter.GrantType)
	}

	targetNodeID := ""
	if nodeRef := strings.TrimSpace(filter.TargetNodeRef); nodeRef != "" {
		node, err := resolveNodeRecord(ctx, s.DB, nodeRef)
		if err != nil {
			return nil, err
		}
		if node == nil {
			return nil, sql.ErrNoRows
		}
		targetNodeID = node.ID
	}
	capabilityEndpointID := ""
	if capabilityRef := strings.TrimSpace(filter.CapabilityEndpointRef); capabilityRef != "" {
		endpointID, err := resolveEndpointID(ctx, s.DB, capabilityRef)
		if err != nil {
			return nil, err
		}
		capabilityEndpointID = endpointID
	}

	query := grantSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if grantType := strings.TrimSpace(filter.GrantType); grantType != "" {
		add("grant_type =", grantType)
	}
	if actorRef := strings.TrimSpace(filter.GrantedToActorRef); actorRef != "" {
		actor, err := resolveActorRecord(ctx, s.DB, actorRef)
		if err != nil {
			return nil, err
		}
		if actor == nil {
			return nil, sql.ErrNoRows
		}
		add("granted_to_actor_id =", actor.ID)
	}
	if actorRef := strings.TrimSpace(filter.GrantedByActorRef); actorRef != "" {
		actor, err := resolveActorRecord(ctx, s.DB, actorRef)
		if err != nil {
			return nil, err
		}
		if actor == nil {
			return nil, sql.ErrNoRows
		}
		add("granted_by_actor_id =", actor.ID)
	}
	if approvalRef := strings.TrimSpace(filter.ApprovalRef); approvalRef != "" {
		approvalID, err := resolveApprovalID(ctx, s.DB, approvalRef)
		if err != nil {
			return nil, err
		}
		add("approval_id =", approvalID)
	}

	queryLimit := filter.Limit
	if targetNodeID != "" || capabilityEndpointID != "" {
		queryLimit = maxDecisionLimit
	}
	args = append(args, queryLimit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	grants := []Grant{}
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		if targetNodeID != "" && !constraintAllows(grant.NodeConstraints, targetNodeID, "target_node_id", "target_node_ids", "node_id", "node_ids") {
			continue
		}
		if capabilityEndpointID != "" && !constraintAllows(grant.CapabilityConstraints, capabilityEndpointID, "capability_endpoint_id", "capability_endpoint_ids") {
			continue
		}
		grants = append(grants, grant)
		if len(grants) >= filter.Limit {
			break
		}
	}
	return grants, rows.Err()
}

func (s Service) GetGrant(ctx context.Context, ref string) (Grant, error) {
	if _, err := s.ExpireStale(ctx, time.Now().UTC()); err != nil {
		return Grant{}, err
	}
	grantID, err := resolveGrantID(ctx, s.DB, ref)
	if err != nil {
		return Grant{}, err
	}
	row := s.DB.QueryRowContext(ctx, grantSelectSQL()+` WHERE grant_id = $1`, grantID)
	return scanGrant(row)
}

func (s Service) FindCoveringGrant(ctx context.Context, input GrantCoverageInput) (*Grant, error) {
	if _, err := s.ExpireStale(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	actor, err := resolveActorRecord(ctx, s.DB, input.ActorRef)
	if err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, sql.ErrNoRows
	}
	node, err := resolveNodeRecord(ctx, s.DB, input.TargetNodeRef)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, sql.ErrNoRows
	}
	target, err := resolveCapabilityTarget(ctx, s.DB, input.CapabilityEndpointRef)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, sql.ErrNoRows
	}
	risk := defaultString(input.RiskLevel, target.RiskLevel)
	level := input.ExecutionAuthorizationLevel
	if level == 0 {
		level = target.ExecutionAuthorizationLevel
	}
	return findCoveringGrant(ctx, s.DB, grantCoverageTarget{
		ActorID:                     actor.ID,
		TargetNodeID:                node.ID,
		CapabilityEndpointID:        target.EndpointID,
		RiskLevel:                   risk,
		ExecutionAuthorizationLevel: level,
	})
}

func (s Service) RevokeGrant(ctx context.Context, req requestctx.Context, input GrantRevokeInput) (Grant, error) {
	input.GrantRef = strings.TrimSpace(input.GrantRef)
	input.RevokedByActorRef = strings.TrimSpace(input.RevokedByActorRef)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.GrantRef == "" {
		return Grant{}, fmt.Errorf("grant_ref is required")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback()

	grantID, err := resolveGrantID(ctx, tx, input.GrantRef)
	if err != nil {
		return Grant{}, err
	}
	grant, err := scanGrant(tx.QueryRowContext(ctx, grantSelectSQL()+` WHERE grant_id = $1 FOR UPDATE`, grantID))
	if err != nil {
		return Grant{}, err
	}
	if grant.Status != GrantActive {
		return Grant{}, fmt.Errorf("grant is not active: %s", grant.Status)
	}

	revokerRef := defaultString(input.RevokedByActorRef, req.ActorID)
	revoker, err := resolveActorRecord(ctx, tx, revokerRef)
	if err != nil {
		return Grant{}, err
	}
	if revoker == nil || revoker.Status != "active" {
		return Grant{}, fmt.Errorf("revoking actor is missing, disabled, or revoked")
	}
	if targetNodeID := constraintStringValue(grant.NodeConstraints, "target_node_id", "node_id"); targetNodeID != "" {
		authLevel, err := activeActorAuthorizationLevel(ctx, tx, revoker.ID, targetNodeID)
		if err != nil {
			return Grant{}, err
		}
		if authLevel == nil || *authLevel < 5 {
			return Grant{}, fmt.Errorf("revoking actor authorization is below required level 5")
		}
	}

	revoked, err := updateGrantRevoked(ctx, tx, grant.GrantID, revoker.ID, input.Reason)
	if err != nil {
		return Grant{}, err
	}
	eventReq := effectiveEventRequest(req, revoker, nil, nil)
	if err := appendGrantLifecycleEvent(ctx, tx, eventReq, events.TypeGrantRevoked, revoked, "grant_revoked"); err != nil {
		return Grant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, err
	}
	return revoked, nil
}

func (s Service) ConsumeGrant(ctx context.Context, req requestctx.Context, input GrantConsumeInput) (Grant, error) {
	input.GrantRef = strings.TrimSpace(input.GrantRef)
	input.Reason = strings.TrimSpace(input.Reason)
	input.RouteID = strings.TrimSpace(input.RouteID)
	input.CapabilityCallID = strings.TrimSpace(input.CapabilityCallID)
	if input.GrantRef == "" {
		return Grant{}, fmt.Errorf("grant_ref is required")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback()

	grantID, err := resolveGrantID(ctx, tx, input.GrantRef)
	if err != nil {
		return Grant{}, err
	}
	grant, err := scanGrant(tx.QueryRowContext(ctx, grantSelectSQL()+` WHERE grant_id = $1 FOR UPDATE`, grantID))
	if err != nil {
		return Grant{}, err
	}
	if grant.Status != GrantActive {
		return Grant{}, fmt.Errorf("grant is not active: %s", grant.Status)
	}
	now := time.Now().UTC()
	if !grant.ExpiresAt.After(now) {
		expired, err := updateGrantExpired(ctx, tx, grant.GrantID)
		if err != nil {
			return Grant{}, err
		}
		if err := appendGrantLifecycleEvent(ctx, tx, req, events.TypeGrantExpired, expired, "grant_expired"); err != nil {
			return Grant{}, err
		}
		if err := tx.Commit(); err != nil {
			return Grant{}, err
		}
		return Grant{}, fmt.Errorf("grant is expired")
	}
	if grant.MaxUses != nil && grant.UsesCount >= *grant.MaxUses {
		consumed, err := updateGrantConsumed(ctx, tx, grant.GrantID)
		if err != nil {
			return Grant{}, err
		}
		if err := appendGrantUsedEvent(ctx, tx, req, consumed, GrantConsumeInput{
			GrantRef:         input.GrantRef,
			Reason:           "grant_already_consumed",
			RouteID:          input.RouteID,
			CapabilityCallID: input.CapabilityCallID,
		}); err != nil {
			return Grant{}, err
		}
		if err := tx.Commit(); err != nil {
			return Grant{}, err
		}
		return Grant{}, fmt.Errorf("grant has no remaining uses")
	}

	consumed, err := incrementGrantUse(ctx, tx, grant)
	if err != nil {
		return Grant{}, err
	}
	if err := appendGrantUsedEvent(ctx, tx, req, consumed, input); err != nil {
		return Grant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, err
	}
	return consumed, nil
}

func (s Service) ExpireStale(ctx context.Context, now time.Time) (ExpirationResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ExpirationResult{}, err
	}
	defer tx.Rollback()

	req, err := resolveServiceRequestContext(ctx, tx, "policy_expiration")
	if err != nil {
		return ExpirationResult{}, err
	}
	result, err := expireStaleTx(ctx, tx, req, now)
	if err != nil {
		return ExpirationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExpirationResult{}, err
	}
	return result, nil
}

type policyOperation struct {
	Raw          string
	Supported    bool
	ResourceKind string
	ResourceRef  string
}

func parsePolicyOperation(raw string) policyOperation {
	raw = strings.TrimSpace(raw)
	operation := policyOperation{
		Raw:          raw,
		ResourceKind: resourceKindOperation,
		ResourceRef:  raw,
	}
	if !strings.HasPrefix(raw, operationCapabilityPrefix) {
		return operation
	}
	ref := strings.TrimSpace(strings.TrimPrefix(raw, operationCapabilityPrefix))
	if ref == "" {
		return operation
	}
	operation.Supported = true
	operation.ResourceKind = resourceKindCapability
	operation.ResourceRef = ref
	return operation
}

type decisionOutcome struct {
	Decision        string
	ReasonCode      string
	SafeExplanation string
}

type decisionInsert struct {
	Actor                   *actorRecord
	OriginNode              *nodeRecord
	Target                  *capabilityTarget
	Scope                   *scopeRecord
	Operation               policyOperation
	ActorAuthorizationLevel *int
	Grant                   *Grant
	Outcome                 decisionOutcome
	ContextSummary          json.RawMessage
	ContextHash             string
	Metadata                json.RawMessage
}

func insertDecision(ctx context.Context, q queryExecer, input decisionInsert) (Decision, error) {
	decisionID := ids.NewPolicyDecisionID()
	resourceID := input.Operation.ResourceRef
	var capabilityEndpointID string
	var providerID string
	var targetNodeID string
	var riskLevel string
	var executionAuthorizationLevel *int
	if input.Target != nil {
		resourceID = input.Target.EndpointID
		capabilityEndpointID = input.Target.EndpointID
		providerID = input.Target.ProviderID
		targetNodeID = input.Target.TargetNodeID
		riskLevel = input.Target.RiskLevel
		executionAuthorizationLevel = &input.Target.ExecutionAuthorizationLevel
	}
	var grantID string
	if input.Grant != nil {
		grantID = input.Grant.GrantID
	}

	row := q.QueryRowContext(ctx, decisionSelectSQL(`
		INSERT INTO policy.decisions (
			policy_decision_id, actor_id, origin_node_id, target_node_id, scope_id,
			resource_kind, resource_id, operation, capability_endpoint_id, provider_id,
			risk_level, execution_authorization_level, actor_authorization_level,
			decision, reason_code, safe_explanation, grant_id, context_hash,
			context_summary, metadata
		)
		VALUES (
			$1, nullif($2, ''), nullif($3, ''), nullif($4, ''), nullif($5, ''),
			$6, nullif($7, ''), $8, nullif($9, ''), nullif($10, ''),
			nullif($11, ''), $12, $13, $14, $15, $16, nullif($17, ''), $18,
			$19, $20
		)
	`),
		decisionID,
		recordID(input.Actor),
		nodeRecordID(input.OriginNode),
		targetNodeID,
		optionalRecordID(input.Scope),
		input.Operation.ResourceKind,
		resourceID,
		input.Operation.Raw,
		capabilityEndpointID,
		providerID,
		riskLevel,
		executionAuthorizationLevel,
		input.ActorAuthorizationLevel,
		input.Outcome.Decision,
		input.Outcome.ReasonCode,
		input.Outcome.SafeExplanation,
		grantID,
		input.ContextHash,
		input.ContextSummary,
		input.Metadata,
	)
	return scanDecision(row)
}

func updateDecisionApproval(ctx context.Context, q queryExecer, decisionID, approvalID string) (Decision, error) {
	row := q.QueryRowContext(ctx, decisionSelectSQL(`
		UPDATE policy.decisions
		SET approval_id = $2
		WHERE policy_decision_id = $1
	`), decisionID, approvalID)
	return scanDecision(row)
}

func createOrReuseApproval(
	ctx context.Context,
	q queryExecer,
	req requestctx.Context,
	input DecisionInput,
	decision Decision,
	actor *actorRecord,
	originNode *nodeRecord,
	scope *scopeRecord,
	target *capabilityTarget,
) (*Approval, bool, error) {
	if actor == nil || target == nil {
		return nil, false, nil
	}

	existingRow := q.QueryRowContext(ctx, approvalSelectSQL()+`
		WHERE requested_by_actor_id = $1
		  AND operation = $2
		  AND capability_endpoint_id = $3
		  AND status = 'pending'
		  AND expires_at > now()
		  AND metadata->>'context_hash' = $4
		ORDER BY created_at DESC
		LIMIT 1
	`, actor.ID, decision.Operation, target.EndpointID, decision.ContextHash)
	existing, err := scanApproval(existingRow)
	if err == nil {
		return &existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	ttl := input.ApprovalTTL
	if ttl <= 0 && input.ApprovalTTLSeconds > 0 {
		ttl = time.Duration(input.ApprovalTTLSeconds) * time.Second
	}
	if ttl <= 0 {
		ttl = defaultApprovalTTL
	}
	expiresAt := time.Now().UTC().Add(ttl)
	approvalID := ids.NewApprovalID()
	metadata, err := json.Marshal(approvalMetadata(decision.ContextHash, req.CorrelationID, input.Metadata))
	if err != nil {
		return nil, false, err
	}
	actionSummary := fmt.Sprintf("Approve %s for actor %s on node %s.", decision.Operation, actor.Key, target.TargetNodeKey)
	row := q.QueryRowContext(ctx, approvalSelectSQL(`
		INSERT INTO policy.approvals (
			approval_id, approval_key, requested_by_actor_id, origin_node_id, target_node_id,
			scope_id, resource_kind, resource_id, operation, capability_endpoint_id, provider_id,
			policy_decision_id, risk_level, execution_authorization_level, request_reason,
			action_summary, status, expires_at, metadata
		)
		VALUES (
			$1, $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''),
			$7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'pending', $17, $18
		)
	`),
		approvalID,
		approvalID,
		actor.ID,
		nodeRecordID(originNode),
		target.TargetNodeID,
		optionalRecordID(scope),
		decision.ResourceKind,
		decision.ResourceID,
		decision.Operation,
		target.EndpointID,
		target.ProviderID,
		decision.PolicyDecisionID,
		target.RiskLevel,
		target.ExecutionAuthorizationLevel,
		strings.TrimSpace(input.ApprovalReason),
		actionSummary,
		expiresAt,
		metadata,
	)
	approval, err := scanApproval(row)
	if err != nil {
		return nil, false, err
	}
	return &approval, true, nil
}

func updateApprovalStatus(ctx context.Context, q queryExecer, approvalID, status, actorID, reason string) (Approval, error) {
	row := q.QueryRowContext(ctx, approvalSelectSQL(`
		UPDATE policy.approvals
		SET status = $2,
		    approving_actor_id = nullif($3, ''),
		    decided_at = now(),
		    decision_reason = nullif($4, '')
		WHERE approval_id = $1
	`), approvalID, status, actorID, reason)
	return scanApproval(row)
}

func updateApprovalApproved(ctx context.Context, q queryExecer, approvalID, actorID, reason, grantID string) (Approval, error) {
	row := q.QueryRowContext(ctx, approvalSelectSQL(`
		UPDATE policy.approvals
		SET status = 'approved',
		    approving_actor_id = $2,
		    decided_at = now(),
		    decision_reason = nullif($3, ''),
		    resulting_grant_id = $4
		WHERE approval_id = $1
	`), approvalID, actorID, reason, grantID)
	return scanApproval(row)
}

func insertApprovalGrant(ctx context.Context, q queryExecer, approval Approval, grantedByActorID string, expiresAt time.Time) (Grant, error) {
	if approval.TargetNodeID == nil || approval.CapabilityEndpointID == nil {
		return Grant{}, fmt.Errorf("approval cannot issue grant without target node and capability endpoint")
	}
	grantID := ids.NewGrantID()
	maxUses := 1
	nodeConstraints := mustJSON(map[string]string{"target_node_id": *approval.TargetNodeID})
	capabilityConstraints := mustJSON(map[string]string{"capability_endpoint_id": *approval.CapabilityEndpointID})
	scopeConstraints := json.RawMessage(`{}`)
	if approval.ScopeID != nil && *approval.ScopeID != "" {
		scopeConstraints = mustJSON(map[string]string{"scope_id": *approval.ScopeID})
	}
	metadata := mustJSON(map[string]string{
		"source":      "approval.decision",
		"approval_id": approval.ApprovalID,
	})

	row := q.QueryRowContext(ctx, grantSelectSQL(`
		INSERT INTO policy.grants (
			grant_id, grant_key, grant_type, granted_by_actor_id, granted_to_actor_id,
			approval_id, status, bypass_confirmation, max_risk_level, max_authorization_level,
			scope_constraints, node_constraints, capability_constraints, max_uses,
			audit_level, expires_at, metadata
		)
		VALUES (
			$1, $2, 'one_shot', $3, $4, $5, 'active', true, $6, $7,
			$8, $9, $10, $11, 'audit', $12, $13
		)
	`),
		grantID,
		grantID,
		grantedByActorID,
		approval.RequestedByActorID,
		approval.ApprovalID,
		approval.RiskLevel,
		approval.ExecutionAuthorizationLevel,
		scopeConstraints,
		nodeConstraints,
		capabilityConstraints,
		maxUses,
		expiresAt,
		metadata,
	)
	return scanGrant(row)
}

func updateGrantRevoked(ctx context.Context, q queryExecer, grantID, revokedByActorID, reason string) (Grant, error) {
	row := q.QueryRowContext(ctx, grantSelectSQL(`
		UPDATE policy.grants
		SET status = 'revoked',
		    revoked_at = now(),
		    revoked_by_actor_id = $2,
		    revoked_reason = nullif($3, '')
		WHERE grant_id = $1
	`), grantID, revokedByActorID, reason)
	return scanGrant(row)
}

func updateGrantExpired(ctx context.Context, q queryExecer, grantID string) (Grant, error) {
	row := q.QueryRowContext(ctx, grantSelectSQL(`
		UPDATE policy.grants
		SET status = 'expired'
		WHERE grant_id = $1
	`), grantID)
	return scanGrant(row)
}

func updateGrantConsumed(ctx context.Context, q queryExecer, grantID string) (Grant, error) {
	row := q.QueryRowContext(ctx, grantSelectSQL(`
		UPDATE policy.grants
		SET status = 'consumed'
		WHERE grant_id = $1
	`), grantID)
	return scanGrant(row)
}

func incrementGrantUse(ctx context.Context, q queryExecer, grant Grant) (Grant, error) {
	nextUses := grant.UsesCount + 1
	nextStatus := GrantActive
	if grant.MaxUses != nil && nextUses >= *grant.MaxUses {
		nextStatus = GrantConsumed
	}
	row := q.QueryRowContext(ctx, grantSelectSQL(`
		UPDATE policy.grants
		SET uses_count = $2,
		    status = $3
		WHERE grant_id = $1
	`), grant.GrantID, nextUses, nextStatus)
	return scanGrant(row)
}

func appendDecisionEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, decision Decision, approval *Approval, grant *Grant, target *capabilityTarget) error {
	payload := map[string]any{
		"policy_decision_id":               decision.PolicyDecisionID,
		"decision":                         decision.Decision,
		"reason_code":                      decision.ReasonCode,
		"operation":                        decision.Operation,
		"context_hash":                     decision.ContextHash,
		"capability_endpoint_id":           pointerValue(decision.CapabilityEndpointID),
		"provider_id":                      pointerValue(decision.ProviderID),
		"target_node_id":                   pointerValue(decision.TargetNodeID),
		"risk_level":                       pointerValue(decision.RiskLevel),
		"execution_authorization_level":    intPointerValue(decision.ExecutionAuthorizationLevel),
		"actor_authorization_level":        intPointerValue(decision.ActorAuthorizationLevel),
		"approval_id":                      pointerValue(decision.ApprovalID),
		"grant_id":                         pointerValue(decision.GrantID),
		"capability_address":               "",
		"provider_address":                 "",
		"approval_request_created_or_used": approval != nil,
		"grant_used":                       grant != nil,
	}
	if target != nil {
		payload["capability_address"] = target.CapabilityAddress
		payload["provider_address"] = target.ProviderAddress
	}
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypePolicyDecisionCreated,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      "policy_decision",
		TargetID:        decision.PolicyDecisionID,
		Status:          decision.Decision,
		Result:          decision.ReasonCode,
		Payload:         payload,
		VisibilityClass: "security",
	})
	if err != nil {
		return fmt.Errorf("append policy decision event: %w", err)
	}
	return nil
}

func appendApprovalRequestedEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, approval Approval, decision Decision) error {
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeApprovalRequested,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "approval",
		TargetID:   approval.ApprovalID,
		Status:     approval.Status,
		Result:     "approval_requested",
		Payload: map[string]any{
			"approval_id":              approval.ApprovalID,
			"policy_decision_id":       decision.PolicyDecisionID,
			"operation":                approval.Operation,
			"requested_by_actor_id":    approval.RequestedByActorID,
			"target_node_id":           pointerValue(approval.TargetNodeID),
			"capability_endpoint_id":   pointerValue(approval.CapabilityEndpointID),
			"provider_id":              pointerValue(approval.ProviderID),
			"risk_level":               approval.RiskLevel,
			"authorization_level":      approval.ExecutionAuthorizationLevel,
			"expires_at":               approval.ExpiresAt.UTC().Format(time.RFC3339),
			"approval_creation_source": "policy_explain",
		},
		VisibilityClass: "security",
	})
	if err != nil {
		return fmt.Errorf("append approval requested event: %w", err)
	}
	return nil
}

func appendApprovalLifecycleEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, eventType string, approval Approval, result string) error {
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         pointerValue(approval.ScopeID),
		TargetKind:      "approval",
		TargetID:        approval.ApprovalID,
		Status:          approval.Status,
		Result:          result,
		Payload:         approvalEventPayload(approval),
		VisibilityClass: "security",
	})
	if err != nil {
		return fmt.Errorf("append approval lifecycle event: %w", err)
	}
	return nil
}

func appendGrantLifecycleEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, eventType string, grant Grant, result string) error {
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      "grant",
		TargetID:        grant.GrantID,
		Status:          grant.Status,
		Result:          result,
		Payload:         grantEventPayload(grant),
		VisibilityClass: "security",
	})
	if err != nil {
		return fmt.Errorf("append grant lifecycle event: %w", err)
	}
	return nil
}

func appendGrantUsedEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, grant Grant, input GrantConsumeInput) error {
	payload := grantEventPayload(grant)
	payload["consume_reason"] = input.Reason
	payload["route_id"] = input.RouteID
	payload["capability_call_id"] = input.CapabilityCallID
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeGrantUsed,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      "grant",
		TargetID:        grant.GrantID,
		RouteID:         input.RouteID,
		Status:          grant.Status,
		Result:          "grant_used",
		Payload:         payload,
		VisibilityClass: "security",
	})
	if err != nil {
		return fmt.Errorf("append grant used event: %w", err)
	}
	return nil
}

func approvalEventPayload(approval Approval) map[string]any {
	return map[string]any{
		"approval_id":                   approval.ApprovalID,
		"status":                        approval.Status,
		"operation":                     approval.Operation,
		"requested_by_actor_id":         approval.RequestedByActorID,
		"approving_actor_id":            pointerValue(approval.ApprovingActorID),
		"target_node_id":                pointerValue(approval.TargetNodeID),
		"capability_endpoint_id":        pointerValue(approval.CapabilityEndpointID),
		"provider_id":                   pointerValue(approval.ProviderID),
		"policy_decision_id":            pointerValue(approval.PolicyDecisionID),
		"resulting_grant_id":            pointerValue(approval.ResultingGrantID),
		"risk_level":                    approval.RiskLevel,
		"execution_authorization_level": approval.ExecutionAuthorizationLevel,
		"expires_at":                    approval.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func grantEventPayload(grant Grant) map[string]any {
	return map[string]any{
		"grant_id":                grant.GrantID,
		"grant_type":              grant.GrantType,
		"status":                  grant.Status,
		"granted_by_actor_id":     grant.GrantedByActorID,
		"granted_to_actor_id":     grant.GrantedToActorID,
		"approval_id":             pointerValue(grant.ApprovalID),
		"max_risk_level":          pointerValue(grant.MaxRiskLevel),
		"max_authorization_level": intPointerValue(grant.MaxAuthorizationLevel),
		"max_uses":                intPointerValue(grant.MaxUses),
		"uses_count":              grant.UsesCount,
		"expires_at":              grant.ExpiresAt.UTC().Format(time.RFC3339),
		"revoked_by_actor_id":     pointerValue(grant.RevokedByActorID),
	}
}

func resolveServiceRequestContext(ctx context.Context, q queryer, correlationID string) (requestctx.Context, error) {
	var req requestctx.Context
	err := q.QueryRowContext(ctx, `
		SELECT
			service.actor_id,
			service.actor_key,
			main.node_id,
			main.node_key,
			system_scope.scope_id,
			system_scope.scope_key
		FROM identity.actors service
		CROSS JOIN nodes.nodes main
		CROSS JOIN scopes.scopes system_scope
		WHERE service.actor_key = 'service:loomd'
		  AND main.node_key = 'main'
		  AND system_scope.scope_key = 'system'
	`).Scan(
		&req.ActorID,
		&req.ActorKey,
		&req.OriginNodeID,
		&req.OriginNodeKey,
		&req.ScopeID,
		&req.ScopeKey,
	)
	if err != nil {
		return requestctx.Context{}, err
	}
	req.CorrelationID = correlationID
	req.FreshnessMode = "live_required"
	req.Source = "policy-expiration"
	return req, nil
}

func expireStaleTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, now time.Time) (ExpirationResult, error) {
	result := ExpirationResult{}

	approvalRows, err := tx.QueryContext(ctx, approvalSelectSQL(`
		UPDATE policy.approvals
		SET status = 'expired',
		    decided_at = now(),
		    decision_reason = 'expired before approval decision'
		WHERE status = 'pending'
		  AND expires_at <= $1
	`), now)
	if err != nil {
		return result, err
	}
	expiredApprovals := []Approval{}
	for approvalRows.Next() {
		approval, err := scanApproval(approvalRows)
		if err != nil {
			approvalRows.Close()
			return result, err
		}
		expiredApprovals = append(expiredApprovals, approval)
	}
	if err := approvalRows.Close(); err != nil {
		return result, err
	}
	if err := approvalRows.Err(); err != nil {
		return result, err
	}
	for _, approval := range expiredApprovals {
		result.ApprovalsExpired++
		if err := appendApprovalLifecycleEvent(ctx, tx, req, events.TypeApprovalExpired, approval, "approval_expired"); err != nil {
			return result, err
		}
	}

	grantRows, err := tx.QueryContext(ctx, grantSelectSQL(`
		UPDATE policy.grants
		SET status = 'expired'
		WHERE status = 'active'
		  AND expires_at <= $1
	`), now)
	if err != nil {
		return result, err
	}
	expiredGrants := []Grant{}
	for grantRows.Next() {
		grant, err := scanGrant(grantRows)
		if err != nil {
			grantRows.Close()
			return result, err
		}
		expiredGrants = append(expiredGrants, grant)
	}
	if err := grantRows.Close(); err != nil {
		return result, err
	}
	if err := grantRows.Err(); err != nil {
		return result, err
	}
	for _, grant := range expiredGrants {
		result.GrantsExpired++
		if err := appendGrantLifecycleEvent(ctx, tx, req, events.TypeGrantExpired, grant, "grant_expired"); err != nil {
			return result, err
		}
	}

	return result, nil
}

func buildContextSummary(
	req requestctx.Context,
	input DecisionInput,
	operation policyOperation,
	actor *actorRecord,
	originNode *nodeRecord,
	scope *scopeRecord,
	target *capabilityTarget,
	actorAuthorizationLevel *int,
	grant *Grant,
) (json.RawMessage, string, error) {
	summary := map[string]any{
		"operation":                     operation.Raw,
		"operation_supported":           operation.Supported,
		"resource_kind":                 operation.ResourceKind,
		"resource_ref":                  operation.ResourceRef,
		"actor_ref":                     defaultString(input.ActorRef, req.ActorKey),
		"origin_node_ref":               defaultString(input.OriginNodeRef, req.OriginNodeKey),
		"scope_ref":                     defaultString(input.ScopeRef, req.ScopeKey),
		"actor_authorization_level":     actorAuthorizationLevel,
		"grant_id":                      "",
		"capability_endpoint_id":        "",
		"capability_address":            "",
		"provider_id":                   "",
		"provider_address":              "",
		"target_node_id":                "",
		"target_node_key":               "",
		"risk_level":                    "",
		"execution_authorization_level": nil,
	}
	if actor != nil {
		summary["actor_id"] = actor.ID
		summary["actor_key"] = actor.Key
		summary["actor_status"] = actor.Status
	}
	if originNode != nil {
		summary["origin_node_id"] = originNode.ID
		summary["origin_node_key"] = originNode.Key
		summary["origin_node_status"] = originNode.Status
	}
	if scope != nil {
		summary["scope_id"] = scope.ID
		summary["scope_key"] = scope.Key
		summary["scope_status"] = scope.Status
	}
	if target != nil {
		summary["capability_endpoint_id"] = target.EndpointID
		summary["capability_address"] = target.CapabilityAddress
		summary["capability_status"] = target.EndpointStatus
		summary["provider_id"] = target.ProviderID
		summary["provider_address"] = target.ProviderAddress
		summary["provider_status"] = target.ProviderStatus
		summary["target_node_id"] = target.TargetNodeID
		summary["target_node_key"] = target.TargetNodeKey
		summary["target_node_status"] = target.TargetNodeStatus
		summary["risk_level"] = target.RiskLevel
		summary["execution_authorization_level"] = target.ExecutionAuthorizationLevel
	}
	if grant != nil {
		summary["grant_id"] = grant.GrantID
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(raw)
	return json.RawMessage(raw), fmt.Sprintf("sha256:%x", hash[:]), nil
}

func contextModel(req requestctx.Context, operation policyOperation, actor *actorRecord, originNode *nodeRecord, scope *scopeRecord, target *capabilityTarget, actorAuthorizationLevel *int, grant *Grant) PolicyContext {
	out := PolicyContext{
		ResourceKind:  operation.ResourceKind,
		ResourceID:    operation.ResourceRef,
		Operation:     operation.Raw,
		CorrelationID: req.CorrelationID,
	}
	if actor != nil {
		out.ActorID = actor.ID
		out.ActorKey = actor.Key
	}
	if originNode != nil {
		out.OriginNodeID = originNode.ID
		out.OriginNodeKey = originNode.Key
	}
	if scope != nil {
		out.ScopeID = scope.ID
		out.ScopeKey = scope.Key
	}
	if target != nil {
		out.ResourceID = target.EndpointID
		out.TargetNodeID = target.TargetNodeID
		out.TargetNodeKey = target.TargetNodeKey
		out.CapabilityEndpointID = target.EndpointID
		out.CapabilityAddress = target.CapabilityAddress
		out.ProviderID = target.ProviderID
		out.ProviderAddress = target.ProviderAddress
		out.RiskLevel = target.RiskLevel
		out.ExecutionAuthorizationLevel = target.ExecutionAuthorizationLevel
	}
	if actorAuthorizationLevel != nil {
		out.ActorAuthorizationLevel = *actorAuthorizationLevel
	}
	if grant != nil {
		out.GrantID = grant.GrantID
	}
	return out
}

func targetModel(operation policyOperation, target *capabilityTarget) PolicyTarget {
	out := PolicyTarget{
		Operation:    operation.Raw,
		ResourceKind: operation.ResourceKind,
	}
	if target != nil {
		out.CapabilityAddress = target.CapabilityAddress
		out.CapabilityEndpointID = target.EndpointID
		out.ProviderAddress = target.ProviderAddress
		out.ProviderID = target.ProviderID
		out.TargetNodeID = target.TargetNodeID
		out.TargetNodeKey = target.TargetNodeKey
	}
	return out
}

type actorRecord struct {
	ID     string
	Key    string
	Status string
}

type nodeRecord struct {
	ID     string
	Key    string
	Status string
}

type scopeRecord struct {
	ID     string
	Key    string
	Status string
}

type capabilityTarget struct {
	EndpointID                  string
	CapabilityAddress           string
	EndpointStatus              string
	ProviderID                  string
	ProviderAddress             string
	ProviderStatus              string
	TargetNodeID                string
	TargetNodeKey               string
	TargetNodeStatus            string
	RiskLevel                   string
	ExecutionAuthorizationLevel int
}

func resolveActorRecord(ctx context.Context, q queryer, ref string) (*actorRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var actor actorRecord
	err := q.QueryRowContext(ctx, `
		SELECT actor_id, actor_key, status
		FROM identity.actors
		WHERE actor_id = $1 OR actor_key = $1
	`, ref).Scan(&actor.ID, &actor.Key, &actor.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &actor, nil
}

func resolveNodeRecord(ctx context.Context, q queryer, ref string) (*nodeRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var node nodeRecord
	err := q.QueryRowContext(ctx, `
		SELECT node_id, node_key, status
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, ref).Scan(&node.ID, &node.Key, &node.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &node, nil
}

func resolveScopeRecord(ctx context.Context, q queryer, ref string) (*scopeRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var scope scopeRecord
	err := q.QueryRowContext(ctx, `
		SELECT scope_id, scope_key, status
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
	`, ref).Scan(&scope.ID, &scope.Key, &scope.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &scope, nil
}

func resolveCapabilityTarget(ctx context.Context, q queryer, ref string) (*capabilityTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var target capabilityTarget
	err := q.QueryRowContext(ctx, `
		SELECT
			e.capability_endpoint_id,
			e.compact_address,
			e.status,
			p.provider_id,
			p.compact_address,
			p.status,
			p.node_id,
			n.node_key,
			n.status,
			e.risk_level,
			e.execution_authorization_level
		FROM capabilities.capability_endpoints e
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN nodes.nodes n ON n.node_id = p.node_id
		WHERE e.capability_endpoint_id = $1 OR e.compact_address = $1
	`, ref).Scan(
		&target.EndpointID,
		&target.CapabilityAddress,
		&target.EndpointStatus,
		&target.ProviderID,
		&target.ProviderAddress,
		&target.ProviderStatus,
		&target.TargetNodeID,
		&target.TargetNodeKey,
		&target.TargetNodeStatus,
		&target.RiskLevel,
		&target.ExecutionAuthorizationLevel,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}

func resolveEndpointID(ctx context.Context, q queryer, ref string) (string, error) {
	target, err := resolveCapabilityTarget(ctx, q, ref)
	if err != nil {
		return "", err
	}
	if target == nil {
		return "", sql.ErrNoRows
	}
	return target.EndpointID, nil
}

func resolveApprovalID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("approval ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT approval_id
		FROM policy.approvals
		WHERE approval_id = $1 OR approval_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveGrantID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("grant ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT grant_id
		FROM policy.grants
		WHERE grant_id = $1 OR grant_key = $1
	`, ref).Scan(&id)
	return id, err
}

func activeActorAuthorizationLevel(ctx context.Context, q queryer, actorID, nodeID string) (*int, error) {
	var level int
	err := q.QueryRowContext(ctx, `
		SELECT authorization_level
		FROM identity.actor_node_authorizations
		WHERE actor_id = $1
		  AND node_id = $2
		  AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
	`, actorID, nodeID).Scan(&level)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &level, nil
}

type grantCoverageTarget struct {
	ActorID                     string
	TargetNodeID                string
	ScopeID                     string
	CapabilityEndpointID        string
	RiskLevel                   string
	ExecutionAuthorizationLevel int
}

func findCoveringGrant(ctx context.Context, q queryer, target grantCoverageTarget) (*Grant, error) {
	rows, err := q.QueryContext(ctx, grantSelectSQL()+`
		WHERE granted_to_actor_id = $1
		  AND status = 'active'
		  AND expires_at > now()
		  AND (max_uses IS NULL OR uses_count < max_uses)
		  AND (max_authorization_level IS NULL OR max_authorization_level >= $2)
		  AND (
		    max_risk_level IS NULL OR
		    CASE max_risk_level
		      WHEN 'low' THEN 1
		      WHEN 'medium' THEN 2
		      WHEN 'high' THEN 3
		      WHEN 'critical' THEN 4
		      ELSE 0
		    END >=
		    CASE $3
		      WHEN 'low' THEN 1
		      WHEN 'medium' THEN 2
		      WHEN 'high' THEN 3
		      WHEN 'critical' THEN 4
		      ELSE 0
		    END
		  )
		ORDER BY expires_at ASC, created_at ASC
		LIMIT 50
	`, target.ActorID, target.ExecutionAuthorizationLevel, defaultString(target.RiskLevel, RiskLow))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		if !constraintAllows(grant.ScopeConstraints, target.ScopeID, "scope_id", "scope_ids") {
			continue
		}
		if !constraintAllows(grant.NodeConstraints, target.TargetNodeID, "target_node_id", "target_node_ids", "node_id", "node_ids") {
			continue
		}
		if !constraintAllows(grant.CapabilityConstraints, target.CapabilityEndpointID, "capability_endpoint_id", "capability_endpoint_ids") {
			continue
		}
		return &grant, nil
	}
	return nil, rows.Err()
}

func effectiveEventRequest(req requestctx.Context, actor *actorRecord, originNode *nodeRecord, scope *scopeRecord) requestctx.Context {
	out := req
	if actor != nil {
		out.ActorID = actor.ID
		out.ActorKey = actor.Key
	}
	if originNode != nil {
		out.OriginNodeID = originNode.ID
		out.OriginNodeKey = originNode.Key
	}
	if scope != nil {
		out.ScopeID = scope.ID
		out.ScopeKey = scope.Key
	}
	return out
}

func approvalMetadata(contextHash, correlationID string, requestMetadata json.RawMessage) map[string]any {
	out := map[string]any{
		"context_hash":   contextHash,
		"correlation_id": correlationID,
		"source":         "policy.explain",
	}
	if len(strings.TrimSpace(string(requestMetadata))) > 0 {
		out["request_metadata"] = json.RawMessage(requestMetadata)
	}
	return out
}

func constraintAllows(raw json.RawMessage, value string, keys ...string) bool {
	raw = objectOrDefault(raw)
	if string(raw) == "{}" {
		return true
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	if len(object) == 0 {
		return true
	}
	for _, key := range keys {
		item, ok := object[key]
		if !ok {
			continue
		}
		if stringConstraintAllows(item, value) {
			return true
		}
		return false
	}
	return false
}

func constraintStringValue(raw json.RawMessage, keys ...string) string {
	raw = objectOrDefault(raw)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range keys {
		item, ok := object[key]
		if !ok {
			continue
		}
		if text, ok := item.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func stringConstraintAllows(item any, value string) bool {
	switch typed := item.(type) {
	case string:
		return typed == value
	case []any:
		for _, candidate := range typed {
			if text, ok := candidate.(string); ok && text == value {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func validateObjectJSON(raw json.RawMessage, name string) error {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%s must be valid JSON object: %w", name, err)
	}
	if object == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	return nil
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

func jsonOrDefault(raw []byte, fallback string) json.RawMessage {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(raw)
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return strings.TrimSpace(fallback)
	}
	return value
}

func recordID(record *actorRecord) string {
	if record == nil {
		return ""
	}
	return record.ID
}

func nodeRecordID(record *nodeRecord) string {
	if record == nil {
		return ""
	}
	return record.ID
}

func optionalRecordID(record *scopeRecord) string {
	if record == nil {
		return ""
	}
	return record.ID
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPointerValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type queryExecer interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func decisionSelectSQL(prefix ...string) string {
	base := `
		SELECT policy_decision_id, decision_key, actor_id, origin_node_id,
		       target_node_id, scope_id, resource_kind, resource_id, operation,
		       capability_endpoint_id, provider_id, risk_level,
		       execution_authorization_level, actor_authorization_level,
		       decision, reason_code, safe_explanation, approval_id, grant_id,
		       context_hash, context_summary, created_at, expires_at, metadata
		FROM policy.decisions
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		RETURNING policy_decision_id, decision_key, actor_id, origin_node_id,
		          target_node_id, scope_id, resource_kind, resource_id, operation,
		          capability_endpoint_id, provider_id, risk_level,
		          execution_authorization_level, actor_authorization_level,
		          decision, reason_code, safe_explanation, approval_id, grant_id,
		          context_hash, context_summary, created_at, expires_at, metadata
		`
	}
	return base
}

func scanDecision(scanner rowScanner) (Decision, error) {
	var decision Decision
	var decisionKey sql.NullString
	var actorID sql.NullString
	var originNodeID sql.NullString
	var targetNodeID sql.NullString
	var scopeID sql.NullString
	var resourceID sql.NullString
	var capabilityEndpointID sql.NullString
	var providerID sql.NullString
	var riskLevel sql.NullString
	var executionAuthorizationLevel sql.NullInt64
	var actorAuthorizationLevel sql.NullInt64
	var approvalID sql.NullString
	var grantID sql.NullString
	var expiresAt sql.NullTime
	var contextSummary []byte
	var metadata []byte
	if err := scanner.Scan(
		&decision.PolicyDecisionID,
		&decisionKey,
		&actorID,
		&originNodeID,
		&targetNodeID,
		&scopeID,
		&decision.ResourceKind,
		&resourceID,
		&decision.Operation,
		&capabilityEndpointID,
		&providerID,
		&riskLevel,
		&executionAuthorizationLevel,
		&actorAuthorizationLevel,
		&decision.Decision,
		&decision.ReasonCode,
		&decision.SafeExplanation,
		&approvalID,
		&grantID,
		&decision.ContextHash,
		&contextSummary,
		&decision.CreatedAt,
		&expiresAt,
		&metadata,
	); err != nil {
		return Decision{}, err
	}
	decision.DecisionKey = nullStringPtr(decisionKey)
	decision.ActorID = nullStringPtr(actorID)
	decision.OriginNodeID = nullStringPtr(originNodeID)
	decision.TargetNodeID = nullStringPtr(targetNodeID)
	decision.ScopeID = nullStringPtr(scopeID)
	decision.ResourceID = nullStringPtr(resourceID)
	decision.CapabilityEndpointID = nullStringPtr(capabilityEndpointID)
	decision.ProviderID = nullStringPtr(providerID)
	decision.RiskLevel = nullStringPtr(riskLevel)
	decision.ExecutionAuthorizationLevel = nullIntPtr(executionAuthorizationLevel)
	decision.ActorAuthorizationLevel = nullIntPtr(actorAuthorizationLevel)
	decision.ApprovalID = nullStringPtr(approvalID)
	decision.GrantID = nullStringPtr(grantID)
	decision.ExpiresAt = nullTimePtr(expiresAt)
	decision.ContextSummary = jsonOrDefault(contextSummary, `{}`)
	decision.Metadata = jsonOrDefault(metadata, `{}`)
	return decision, nil
}

func approvalSelectSQL(prefix ...string) string {
	base := `
		SELECT approval_id, approval_key, requested_by_actor_id, approving_actor_id,
		       origin_node_id, target_node_id, scope_id, resource_kind, resource_id,
		       operation, capability_endpoint_id, provider_id, policy_decision_id,
		       risk_level, execution_authorization_level, request_reason, action_summary,
		       status, created_at, expires_at, decided_at, decision_reason,
		       resulting_grant_id, metadata
		FROM policy.approvals
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		RETURNING approval_id, approval_key, requested_by_actor_id, approving_actor_id,
		          origin_node_id, target_node_id, scope_id, resource_kind, resource_id,
		          operation, capability_endpoint_id, provider_id, policy_decision_id,
		          risk_level, execution_authorization_level, request_reason, action_summary,
		          status, created_at, expires_at, decided_at, decision_reason,
		          resulting_grant_id, metadata
		`
	}
	return base
}

func scanApproval(scanner rowScanner) (Approval, error) {
	var approval Approval
	var approvingActorID sql.NullString
	var originNodeID sql.NullString
	var targetNodeID sql.NullString
	var scopeID sql.NullString
	var resourceID sql.NullString
	var capabilityEndpointID sql.NullString
	var providerID sql.NullString
	var policyDecisionID sql.NullString
	var decidedAt sql.NullTime
	var decisionReason sql.NullString
	var resultingGrantID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&approval.ApprovalID,
		&approval.ApprovalKey,
		&approval.RequestedByActorID,
		&approvingActorID,
		&originNodeID,
		&targetNodeID,
		&scopeID,
		&approval.ResourceKind,
		&resourceID,
		&approval.Operation,
		&capabilityEndpointID,
		&providerID,
		&policyDecisionID,
		&approval.RiskLevel,
		&approval.ExecutionAuthorizationLevel,
		&approval.RequestReason,
		&approval.ActionSummary,
		&approval.Status,
		&approval.CreatedAt,
		&approval.ExpiresAt,
		&decidedAt,
		&decisionReason,
		&resultingGrantID,
		&metadata,
	); err != nil {
		return Approval{}, err
	}
	approval.ApprovingActorID = nullStringPtr(approvingActorID)
	approval.OriginNodeID = nullStringPtr(originNodeID)
	approval.TargetNodeID = nullStringPtr(targetNodeID)
	approval.ScopeID = nullStringPtr(scopeID)
	approval.ResourceID = nullStringPtr(resourceID)
	approval.CapabilityEndpointID = nullStringPtr(capabilityEndpointID)
	approval.ProviderID = nullStringPtr(providerID)
	approval.PolicyDecisionID = nullStringPtr(policyDecisionID)
	approval.DecidedAt = nullTimePtr(decidedAt)
	approval.DecisionReason = nullStringPtr(decisionReason)
	approval.ResultingGrantID = nullStringPtr(resultingGrantID)
	approval.Metadata = jsonOrDefault(metadata, `{}`)
	return approval, nil
}

func grantSelectSQL(prefix ...string) string {
	base := `
		SELECT grant_id, grant_key, grant_type, granted_by_actor_id, granted_to_actor_id,
		       approval_id, status, bypass_confirmation, max_risk_level,
		       max_authorization_level, scope_constraints, node_constraints,
		       capability_constraints, credential_constraints, object_constraints,
		       egress_constraints, max_uses, uses_count, audit_level, created_at,
		       expires_at, revoked_at, revoked_by_actor_id, revoked_reason, metadata
		FROM policy.grants
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		RETURNING grant_id, grant_key, grant_type, granted_by_actor_id, granted_to_actor_id,
		          approval_id, status, bypass_confirmation, max_risk_level,
		          max_authorization_level, scope_constraints, node_constraints,
		          capability_constraints, credential_constraints, object_constraints,
		          egress_constraints, max_uses, uses_count, audit_level, created_at,
		          expires_at, revoked_at, revoked_by_actor_id, revoked_reason, metadata
		`
	}
	return base
}

func scanGrant(scanner rowScanner) (Grant, error) {
	var grant Grant
	var approvalID sql.NullString
	var maxRiskLevel sql.NullString
	var maxAuthorizationLevel sql.NullInt64
	var maxUses sql.NullInt64
	var revokedAt sql.NullTime
	var revokedByActorID sql.NullString
	var revokedReason sql.NullString
	var scopeConstraints []byte
	var nodeConstraints []byte
	var capabilityConstraints []byte
	var credentialConstraints []byte
	var objectConstraints []byte
	var egressConstraints []byte
	var metadata []byte
	if err := scanner.Scan(
		&grant.GrantID,
		&grant.GrantKey,
		&grant.GrantType,
		&grant.GrantedByActorID,
		&grant.GrantedToActorID,
		&approvalID,
		&grant.Status,
		&grant.BypassConfirmation,
		&maxRiskLevel,
		&maxAuthorizationLevel,
		&scopeConstraints,
		&nodeConstraints,
		&capabilityConstraints,
		&credentialConstraints,
		&objectConstraints,
		&egressConstraints,
		&maxUses,
		&grant.UsesCount,
		&grant.AuditLevel,
		&grant.CreatedAt,
		&grant.ExpiresAt,
		&revokedAt,
		&revokedByActorID,
		&revokedReason,
		&metadata,
	); err != nil {
		return Grant{}, err
	}
	grant.ApprovalID = nullStringPtr(approvalID)
	grant.MaxRiskLevel = nullStringPtr(maxRiskLevel)
	grant.MaxAuthorizationLevel = nullIntPtr(maxAuthorizationLevel)
	grant.ScopeConstraints = jsonOrDefault(scopeConstraints, `{}`)
	grant.NodeConstraints = jsonOrDefault(nodeConstraints, `{}`)
	grant.CapabilityConstraints = jsonOrDefault(capabilityConstraints, `{}`)
	grant.CredentialConstraints = jsonOrDefault(credentialConstraints, `{}`)
	grant.ObjectConstraints = jsonOrDefault(objectConstraints, `{}`)
	grant.EgressConstraints = jsonOrDefault(egressConstraints, `{}`)
	grant.MaxUses = nullIntPtr(maxUses)
	grant.RevokedAt = nullTimePtr(revokedAt)
	grant.RevokedByActorID = nullStringPtr(revokedByActorID)
	grant.RevokedReason = nullStringPtr(revokedReason)
	grant.Metadata = jsonOrDefault(metadata, `{}`)
	return grant, nil
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	out := int(value.Int64)
	return &out
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

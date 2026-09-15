package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeprofiles"
	"loom.local/loom/internal/requestctx"
)

const (
	EnrollmentTokenActive  = "active"
	EnrollmentTokenUsed    = "used"
	EnrollmentTokenExpired = "expired"
	EnrollmentTokenRevoked = "revoked"

	EnrollmentRequestPending  = "pending"
	EnrollmentRequestApproved = "approved"
	EnrollmentRequestDenied   = "denied"

	NodeCredentialActive  = "active"
	NodeCredentialRevoked = "revoked"

	CredentialKindNodeTokenBootstrap = "node_token_bootstrap"
)

type EnrollmentToken struct {
	NodeEnrollmentTokenID string          `json:"node_enrollment_token_id"`
	TokenHint             string          `json:"token_hint"`
	RequestedByActorID    string          `json:"requested_by_actor_id"`
	Status                string          `json:"status"`
	CreatedAt             time.Time       `json:"created_at"`
	ExpiresAt             time.Time       `json:"expires_at"`
	UsedAt                *time.Time      `json:"used_at,omitempty"`
	Metadata              json.RawMessage `json:"metadata"`
}

type EnrollmentRequest struct {
	NodeEnrollmentRequestID string          `json:"node_enrollment_request_id"`
	NodeEnrollmentTokenID   *string         `json:"node_enrollment_token_id,omitempty"`
	RequestedNodeKey        string          `json:"requested_node_key"`
	RequestedDisplayName    string          `json:"requested_display_name"`
	RequestedNodeKind       string          `json:"requested_node_kind"`
	RequestedNodeRole       string          `json:"requested_node_role"`
	RequestedRuntimeClass   string          `json:"requested_runtime_class"`
	RequestedProfileJSON    json.RawMessage `json:"requested_profile_json"`
	RequestedByActorID      *string         `json:"requested_by_actor_id,omitempty"`
	ApprovedByActorID       *string         `json:"approved_by_actor_id,omitempty"`
	Status                  string          `json:"status"`
	ActivatedNodeID         *string         `json:"activated_node_id,omitempty"`
	NodeCredentialID        *string         `json:"node_credential_id,omitempty"`
	DenialReason            *string         `json:"denial_reason,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	ExpiresAt               time.Time       `json:"expires_at"`
	ApprovedAt              *time.Time      `json:"approved_at,omitempty"`
	DeniedAt                *time.Time      `json:"denied_at,omitempty"`
	Metadata                json.RawMessage `json:"metadata"`
}

type NodeCredential struct {
	NodeCredentialID string          `json:"node_credential_id"`
	NodeID           string          `json:"node_id"`
	CredentialKind   string          `json:"credential_kind"`
	CredentialHint   string          `json:"credential_hint"`
	Status           string          `json:"status"`
	IssuedByActorID  *string         `json:"issued_by_actor_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	LastUsedAt       *time.Time      `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time      `json:"revoked_at,omitempty"`
	RevokedReason    *string         `json:"revoked_reason,omitempty"`
	Metadata         json.RawMessage `json:"metadata"`
}

type CreateEnrollmentTokenInput struct {
	TTLSeconds int             `json:"ttl_seconds,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type CreateEnrollmentTokenResult struct {
	Token      EnrollmentToken `json:"token"`
	TokenValue string          `json:"token_value"`
}

type CreateEnrollmentRequestInput struct {
	EnrollmentToken       string          `json:"enrollment_token"`
	RequestedNodeKey      string          `json:"requested_node_key"`
	RequestedDisplayName  string          `json:"requested_display_name"`
	RequestedNodeKind     string          `json:"requested_node_kind,omitempty"`
	RequestedNodeRole     string          `json:"requested_node_role,omitempty"`
	RequestedRuntimeClass string          `json:"requested_runtime_class,omitempty"`
	RequestedProfileJSON  json.RawMessage `json:"requested_profile_json,omitempty"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

type ApproveEnrollmentInput struct {
	EnrollmentRequestRef string `json:"enrollment_request_ref"`
}

type DenyEnrollmentInput struct {
	EnrollmentRequestRef string `json:"enrollment_request_ref"`
	Reason               string `json:"reason,omitempty"`
}

type DecommissionNodeInput struct {
	NodeRef string `json:"node_ref,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type IssueNodeCredentialInput struct {
	NodeRef        string `json:"node_ref,omitempty"`
	RevokeExisting bool   `json:"revoke_existing,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type ApproveEnrollmentResult struct {
	Request         EnrollmentRequest `json:"request"`
	Node            Node              `json:"node"`
	Credential      NodeCredential    `json:"credential"`
	CredentialToken string            `json:"credential_token"`
}

type IssueNodeCredentialResult struct {
	Node               Node           `json:"node"`
	Credential         NodeCredential `json:"credential"`
	CredentialToken    string         `json:"credential_token"`
	RevokedCredentials int            `json:"revoked_credentials,omitempty"`
}

type DecommissionNodeResult struct {
	Node               Node   `json:"node"`
	RevokedCredentials int    `json:"revoked_credentials"`
	Reason             string `json:"reason,omitempty"`
}

type ListEnrollmentRequestsFilter struct {
	Limit  int
	Status string
}

func (s Service) CreateEnrollmentToken(ctx context.Context, req requestctx.Context, input CreateEnrollmentTokenInput) (CreateEnrollmentTokenResult, error) {
	ttl := time.Duration(input.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	tokenValue, err := newSecretToken("node_enroll")
	if err != nil {
		return CreateEnrollmentTokenResult{}, err
	}
	tokenHash, err := hashSecretToken(tokenValue)
	if err != nil {
		return CreateEnrollmentTokenResult{}, err
	}
	metadata := objectOrDefault(input.Metadata)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CreateEnrollmentTokenResult{}, err
	}
	defer tx.Rollback()

	token, err := scanEnrollmentToken(tx.QueryRowContext(ctx, `
		INSERT INTO security.node_enrollment_tokens (
			node_enrollment_token_id, token_hash, token_hint, requested_by_actor_id,
			status, expires_at, metadata
		)
		VALUES ($1, $2, $3, $4, 'active', $5, $6)
		RETURNING `+enrollmentTokenColumns()+`
	`, ids.NewNodeEnrollmentTokenID(), tokenHash, tokenHint(tokenValue), req.ActorID, time.Now().UTC().Add(ttl), metadata))
	if err != nil {
		return CreateEnrollmentTokenResult{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeNodeEnrollmentTokenCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "node_enrollment_token",
		TargetID:   token.NodeEnrollmentTokenID,
		Status:     token.Status,
		Result:     "created",
		Payload: map[string]any{
			"node_enrollment_token_id": token.NodeEnrollmentTokenID,
			"token_hint":               token.TokenHint,
			"expires_at":               token.ExpiresAt,
		},
		VisibilityClass: "security",
	}); err != nil {
		return CreateEnrollmentTokenResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateEnrollmentTokenResult{}, err
	}
	return CreateEnrollmentTokenResult{Token: token, TokenValue: tokenValue}, nil
}

func (s Service) CreateEnrollmentRequest(ctx context.Context, req requestctx.Context, input CreateEnrollmentRequestInput) (EnrollmentRequest, error) {
	input = normalizeEnrollmentRequestInput(input)
	if input.EnrollmentToken == "" {
		return EnrollmentRequest{}, fmt.Errorf("enrollment_token is required")
	}
	if input.RequestedNodeKey == "" {
		return EnrollmentRequest{}, fmt.Errorf("requested_node_key is required")
	}
	if input.RequestedDisplayName == "" {
		return EnrollmentRequest{}, fmt.Errorf("requested_display_name is required")
	}
	if err := validateObjectJSON(input.RequestedProfileJSON, "requested_profile_json"); err != nil {
		return EnrollmentRequest{}, err
	}
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return EnrollmentRequest{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return EnrollmentRequest{}, err
	}
	defer tx.Rollback()

	token, err := findActiveEnrollmentToken(ctx, tx, input.EnrollmentToken)
	if err != nil {
		return EnrollmentRequest{}, err
	}
	now := time.Now().UTC()
	if !token.ExpiresAt.After(now) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE security.node_enrollment_tokens
			SET status = 'expired'
			WHERE node_enrollment_token_id = $1
		`, token.NodeEnrollmentTokenID); err != nil {
			return EnrollmentRequest{}, err
		}
		return EnrollmentRequest{}, fmt.Errorf("enrollment token is expired")
	}

	enrollment, err := scanEnrollmentRequest(tx.QueryRowContext(ctx, `
		INSERT INTO security.node_enrollment_requests (
			node_enrollment_request_id, node_enrollment_token_id,
			requested_node_key, requested_display_name, requested_node_kind,
			requested_node_role, requested_runtime_class, requested_profile_json,
			requested_by_actor_id, status, expires_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''), 'pending', $10, $11)
		RETURNING `+enrollmentRequestColumns()+`
	`, ids.NewNodeEnrollmentRequestID(), token.NodeEnrollmentTokenID, input.RequestedNodeKey,
		input.RequestedDisplayName, input.RequestedNodeKind, input.RequestedNodeRole,
		input.RequestedRuntimeClass, objectOrDefault(input.RequestedProfileJSON),
		req.ActorID, now.Add(30*time.Minute), objectOrDefault(input.Metadata)))
	if err != nil {
		return EnrollmentRequest{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeNodeEnrollmentRequested,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "node_enrollment_request",
		TargetID:   enrollment.NodeEnrollmentRequestID,
		Status:     enrollment.Status,
		Result:     "pending",
		Payload: map[string]any{
			"node_enrollment_request_id": enrollment.NodeEnrollmentRequestID,
			"requested_node_key":         enrollment.RequestedNodeKey,
			"requested_node_kind":        enrollment.RequestedNodeKind,
		},
		VisibilityClass: "security",
	}); err != nil {
		return EnrollmentRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnrollmentRequest{}, err
	}
	return enrollment, nil
}

func (s Service) ApproveEnrollment(ctx context.Context, req requestctx.Context, input ApproveEnrollmentInput) (ApproveEnrollmentResult, error) {
	ref := strings.TrimSpace(input.EnrollmentRequestRef)
	if ref == "" {
		return ApproveEnrollmentResult{}, fmt.Errorf("enrollment_request_ref is required")
	}
	credentialToken, err := newSecretToken("node_cred")
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}
	credentialHash, err := hashSecretToken(credentialToken)
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}
	defer tx.Rollback()

	enrollment, err := resolveEnrollmentRequestTx(ctx, tx, ref)
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}
	if enrollment.Status != EnrollmentRequestPending {
		return ApproveEnrollmentResult{}, fmt.Errorf("enrollment request is not pending: %s", enrollment.Status)
	}
	if !enrollment.ExpiresAt.After(time.Now().UTC()) {
		return ApproveEnrollmentResult{}, fmt.Errorf("enrollment request is expired")
	}

	nodeID := ids.NewNodeID()
	node, err := scanNode(tx.QueryRowContext(ctx, `
		INSERT INTO nodes.nodes (
			node_id, node_key, display_name, node_kind, node_role, runtime_class,
			status, presence_state, enrollment_status, credential_status,
			owner_actor_id, home_scope_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'active', 'unknown', 'approved', 'active',
		        $7, $8, $9)
		RETURNING node_id, node_key, display_name, node_kind, node_role,
		          runtime_class, status, presence_state, last_heartbeat_at,
		          last_seen_at, runtime_version, enrollment_status,
		          credential_status, owner_actor_id, home_scope_id, metadata,
		          created_at, updated_at, retired_at
	`, nodeID, enrollment.RequestedNodeKey, enrollment.RequestedDisplayName,
		enrollment.RequestedNodeKind, enrollment.RequestedNodeRole,
		enrollment.RequestedRuntimeClass, req.ActorID, req.ScopeID,
		mustJSON(map[string]any{"enrollment_request_id": enrollment.NodeEnrollmentRequestID, "slice": "10"})))
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}
	if err := assignDefaultProfiles(ctx, tx, node.NodeID, node.NodeKind, node.NodeRole, node.RuntimeClass, req.ActorID); err != nil {
		return ApproveEnrollmentResult{}, err
	}
	if err := ensureApprovingActorAuthorization(ctx, tx, req.ActorID, node.NodeID); err != nil {
		return ApproveEnrollmentResult{}, err
	}

	credential, err := scanNodeCredential(tx.QueryRowContext(ctx, `
		INSERT INTO security.node_auth_credentials (
			node_credential_id, node_id, credential_kind, credential_hash,
			credential_hint, status, issued_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, $7)
		RETURNING `+nodeCredentialColumns()+`
	`, ids.NewNodeCredentialID(), node.NodeID, CredentialKindNodeTokenBootstrap,
		credentialHash, tokenHint(credentialToken), req.ActorID,
		mustJSON(map[string]any{"enrollment_request_id": enrollment.NodeEnrollmentRequestID, "slice": "10"})))
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}

	enrollment, err = scanEnrollmentRequest(tx.QueryRowContext(ctx, `
		UPDATE security.node_enrollment_requests
		SET status = 'approved',
		    approved_by_actor_id = $2,
		    activated_node_id = $3,
		    node_credential_id = $4,
		    approved_at = now()
		WHERE node_enrollment_request_id = $1
		RETURNING `+enrollmentRequestColumns()+`
	`, enrollment.NodeEnrollmentRequestID, req.ActorID, node.NodeID, credential.NodeCredentialID))
	if err != nil {
		return ApproveEnrollmentResult{}, err
	}
	if enrollment.NodeEnrollmentTokenID != nil {
		if _, err := tx.ExecContext(ctx, `
			UPDATE security.node_enrollment_tokens
			SET status = 'used', used_at = now()
			WHERE node_enrollment_token_id = $1
		`, *enrollment.NodeEnrollmentTokenID); err != nil {
			return ApproveEnrollmentResult{}, err
		}
	}

	for _, eventInput := range []events.AppendInput{
		{
			EventType:  events.TypeNodeEnrollmentApproved,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "node_enrollment_request",
			TargetID:   enrollment.NodeEnrollmentRequestID,
			Status:     enrollment.Status,
			Result:     "approved",
			Payload: map[string]any{
				"node_enrollment_request_id": enrollment.NodeEnrollmentRequestID,
				"node_id":                    node.NodeID,
				"node_key":                   node.NodeKey,
			},
			VisibilityClass: "security",
		},
		{
			EventType:  events.TypeNodeCredentialIssued,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "node_credential",
			TargetID:   credential.NodeCredentialID,
			Status:     credential.Status,
			Result:     "issued",
			Payload: map[string]any{
				"node_credential_id": credential.NodeCredentialID,
				"node_id":            node.NodeID,
				"credential_kind":    credential.CredentialKind,
				"credential_hint":    credential.CredentialHint,
			},
			VisibilityClass: "security",
		},
	} {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return ApproveEnrollmentResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return ApproveEnrollmentResult{}, err
	}
	return ApproveEnrollmentResult{
		Request:         enrollment,
		Node:            node,
		Credential:      credential,
		CredentialToken: credentialToken,
	}, nil
}

func ensureApprovingActorAuthorization(ctx context.Context, tx *sql.Tx, actorID, nodeID string) error {
	return ensureActorNodeAuthorization(ctx, tx, actorID, nodeID, "node_enrollment_approval", "11_part_2")
}

func ensureActorNodeAuthorization(ctx context.Context, tx *sql.Tx, actorID, nodeID, source, slice string) error {
	actorID = strings.TrimSpace(actorID)
	nodeID = strings.TrimSpace(nodeID)
	if actorID == "" || nodeID == "" {
		return fmt.Errorf("actor_id and node_id are required to grant node authorization")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO identity.actor_node_authorizations (
			authorization_id, actor_id, node_id, authorization_level, status, metadata
		)
		VALUES ($1, $2, $3, 5, 'active', $4)
		ON CONFLICT (actor_id, node_id)
		DO UPDATE SET
		    authorization_level = GREATEST(identity.actor_node_authorizations.authorization_level, EXCLUDED.authorization_level),
		    status = 'active',
		    expires_at = NULL,
		    metadata = identity.actor_node_authorizations.metadata || EXCLUDED.metadata
	`, ids.NewActorNodeAuthorizationID(), actorID, nodeID, mustJSON(map[string]any{
		"source": source,
		"slice":  slice,
	}))
	return err
}

func (s Service) IssueNodeCredential(ctx context.Context, req requestctx.Context, input IssueNodeCredentialInput) (IssueNodeCredentialResult, error) {
	ref := strings.TrimSpace(input.NodeRef)
	if ref == "" {
		return IssueNodeCredentialResult{}, fmt.Errorf("node_ref is required")
	}
	credentialToken, err := newSecretToken("node_cred")
	if err != nil {
		return IssueNodeCredentialResult{}, err
	}
	credentialHash, err := hashSecretToken(credentialToken)
	if err != nil {
		return IssueNodeCredentialResult{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "operator credential issue"
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IssueNodeCredentialResult{}, err
	}
	defer tx.Rollback()

	node, err := scanNode(tx.QueryRowContext(ctx, `
		SELECT node_id, node_key, display_name, node_kind, node_role,
		       runtime_class, status, presence_state, last_heartbeat_at,
		       last_seen_at, runtime_version, enrollment_status,
		       credential_status, owner_actor_id, home_scope_id, metadata,
		       created_at, updated_at, retired_at
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
		FOR UPDATE
	`, ref))
	if err != nil {
		return IssueNodeCredentialResult{}, err
	}
	if node.Status != "active" {
		return IssueNodeCredentialResult{}, fmt.Errorf("node is not active: %s", node.Status)
	}

	revoked := 0
	if input.RevokeExisting {
		result, err := tx.ExecContext(ctx, `
			UPDATE security.node_auth_credentials
			SET status = 'revoked',
			    revoked_at = now(),
			    revoked_reason = $2
			WHERE node_id = $1
			  AND status = 'active'
		`, node.NodeID, reason)
		if err != nil {
			return IssueNodeCredentialResult{}, err
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr == nil {
			revoked = int(rows)
		}
	}

	if err := ensureActorNodeAuthorization(ctx, tx, req.ActorID, node.NodeID, "node_credential_issue", "v0.8_main_node_agent"); err != nil {
		return IssueNodeCredentialResult{}, err
	}

	credential, err := scanNodeCredential(tx.QueryRowContext(ctx, `
		INSERT INTO security.node_auth_credentials (
			node_credential_id, node_id, credential_kind, credential_hash,
			credential_hint, status, issued_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, $7)
		RETURNING `+nodeCredentialColumns()+`
	`, ids.NewNodeCredentialID(), node.NodeID, CredentialKindNodeTokenBootstrap,
		credentialHash, tokenHint(credentialToken), req.ActorID,
		mustJSON(map[string]any{
			"source": "node_credential_issue",
			"reason": reason,
		})))
	if err != nil {
		return IssueNodeCredentialResult{}, err
	}

	node, err = scanNode(tx.QueryRowContext(ctx, `
		UPDATE nodes.nodes
		SET enrollment_status = 'approved',
		    credential_status = 'active',
		    updated_at = now()
		WHERE node_id = $1
		RETURNING node_id, node_key, display_name, node_kind, node_role,
		          runtime_class, status, presence_state, last_heartbeat_at,
		          last_seen_at, runtime_version, enrollment_status,
		          credential_status, owner_actor_id, home_scope_id, metadata,
		          created_at, updated_at, retired_at
	`, node.NodeID))
	if err != nil {
		return IssueNodeCredentialResult{}, err
	}

	if revoked > 0 {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeNodeCredentialRevoked,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "node",
			TargetID:   node.NodeID,
			Status:     node.CredentialStatus,
			Result:     "revoked_existing",
			Payload: map[string]any{
				"node_id":             node.NodeID,
				"node_key":            node.NodeKey,
				"revoked_credentials": revoked,
				"reason":              reason,
			},
			VisibilityClass: "security",
		}); err != nil {
			return IssueNodeCredentialResult{}, err
		}
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeNodeCredentialIssued,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "node_credential",
		TargetID:   credential.NodeCredentialID,
		Status:     credential.Status,
		Result:     "issued",
		Payload: map[string]any{
			"node_credential_id": credential.NodeCredentialID,
			"node_id":            node.NodeID,
			"node_key":           node.NodeKey,
			"credential_kind":    credential.CredentialKind,
			"credential_hint":    credential.CredentialHint,
			"revoked_existing":   revoked,
			"reason":             reason,
		},
		VisibilityClass: "security",
	}); err != nil {
		return IssueNodeCredentialResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return IssueNodeCredentialResult{}, err
	}
	return IssueNodeCredentialResult{
		Node:               node,
		Credential:         credential,
		CredentialToken:    credentialToken,
		RevokedCredentials: revoked,
	}, nil
}

func (s Service) DenyEnrollment(ctx context.Context, req requestctx.Context, input DenyEnrollmentInput) (EnrollmentRequest, error) {
	ref := strings.TrimSpace(input.EnrollmentRequestRef)
	if ref == "" {
		return EnrollmentRequest{}, fmt.Errorf("enrollment_request_ref is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return EnrollmentRequest{}, err
	}
	defer tx.Rollback()

	enrollment, err := resolveEnrollmentRequestTx(ctx, tx, ref)
	if err != nil {
		return EnrollmentRequest{}, err
	}
	if enrollment.Status != EnrollmentRequestPending {
		return EnrollmentRequest{}, fmt.Errorf("enrollment request is not pending: %s", enrollment.Status)
	}
	enrollment, err = scanEnrollmentRequest(tx.QueryRowContext(ctx, `
		UPDATE security.node_enrollment_requests
		SET status = 'denied',
		    approved_by_actor_id = $2,
		    denial_reason = nullif($3, ''),
		    denied_at = now()
		WHERE node_enrollment_request_id = $1
		RETURNING `+enrollmentRequestColumns()+`
	`, enrollment.NodeEnrollmentRequestID, req.ActorID, strings.TrimSpace(input.Reason)))
	if err != nil {
		return EnrollmentRequest{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeNodeEnrollmentDenied,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "node_enrollment_request",
		TargetID:   enrollment.NodeEnrollmentRequestID,
		Status:     enrollment.Status,
		Result:     "denied",
		Payload: map[string]any{
			"node_enrollment_request_id": enrollment.NodeEnrollmentRequestID,
			"requested_node_key":         enrollment.RequestedNodeKey,
		},
		VisibilityClass: "security",
	}); err != nil {
		return EnrollmentRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnrollmentRequest{}, err
	}
	return enrollment, nil
}

func (s Service) DecommissionNode(ctx context.Context, req requestctx.Context, input DecommissionNodeInput) (DecommissionNodeResult, error) {
	ref := strings.TrimSpace(input.NodeRef)
	if ref == "" {
		return DecommissionNodeResult{}, fmt.Errorf("node_ref is required")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "decommissioned by operator"
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DecommissionNodeResult{}, err
	}
	defer tx.Rollback()

	node, err := scanNode(tx.QueryRowContext(ctx, `
		SELECT node_id, node_key, display_name, node_kind, node_role,
		       runtime_class, status, presence_state, last_heartbeat_at,
		       last_seen_at, runtime_version, enrollment_status,
		       credential_status, owner_actor_id, home_scope_id, metadata,
		       created_at, updated_at, retired_at
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
		FOR UPDATE
	`, ref))
	if err != nil {
		return DecommissionNodeResult{}, err
	}
	credentialResult, err := tx.ExecContext(ctx, `
		UPDATE security.node_auth_credentials
		SET status = 'revoked',
		    revoked_at = now(),
		    revoked_reason = $2
		WHERE node_id = $1
		  AND status = 'active'
	`, node.NodeID, reason)
	if err != nil {
		return DecommissionNodeResult{}, err
	}
	revoked := 0
	if rows, rowsErr := credentialResult.RowsAffected(); rowsErr == nil {
		revoked = int(rows)
	}
	node, err = scanNode(tx.QueryRowContext(ctx, `
		UPDATE nodes.nodes
		SET status = 'retired',
		    presence_state = 'revoked',
		    enrollment_status = 'revoked',
		    credential_status = 'revoked',
		    retired_at = COALESCE(retired_at, now()),
		    updated_at = now()
		WHERE node_id = $1
		RETURNING node_id, node_key, display_name, node_kind, node_role,
		          runtime_class, status, presence_state, last_heartbeat_at,
		          last_seen_at, runtime_version, enrollment_status,
		          credential_status, owner_actor_id, home_scope_id, metadata,
		          created_at, updated_at, retired_at
	`, node.NodeID))
	if err != nil {
		return DecommissionNodeResult{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeNodeCredentialRevoked,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "node",
		TargetID:   node.NodeID,
		Status:     node.CredentialStatus,
		Result:     "decommissioned",
		Payload: map[string]any{
			"node_id":             node.NodeID,
			"node_key":            node.NodeKey,
			"revoked_credentials": revoked,
			"reason":              reason,
		},
		VisibilityClass: "security",
	}); err != nil {
		return DecommissionNodeResult{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeNodePresenceChanged,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "node",
		TargetID:   node.NodeID,
		Status:     node.PresenceState,
		Result:     "retired",
		Payload: map[string]any{
			"node_id":        node.NodeID,
			"node_key":       node.NodeKey,
			"presence_state": node.PresenceState,
			"reason":         reason,
		},
		VisibilityClass: "security",
	}); err != nil {
		return DecommissionNodeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DecommissionNodeResult{}, err
	}
	return DecommissionNodeResult{Node: node, RevokedCredentials: revoked, Reason: reason}, nil
}

func (s Service) ListEnrollmentRequests(ctx context.Context, filter ListEnrollmentRequestsFilter) ([]EnrollmentRequest, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := `SELECT ` + enrollmentRequestColumns() + ` FROM security.node_enrollment_requests WHERE true`
	args := []any{}
	if strings.TrimSpace(filter.Status) != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EnrollmentRequest{}
	for rows.Next() {
		request, err := scanEnrollmentRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, request)
	}
	return out, rows.Err()
}

func (s Service) GetEnrollmentRequest(ctx context.Context, ref string) (EnrollmentRequest, error) {
	return resolveEnrollmentRequestTx(ctx, s.DB, ref)
}

func findActiveEnrollmentToken(ctx context.Context, q queryer, tokenValue string) (EnrollmentToken, error) {
	hint := tokenHint(tokenValue)
	rows, err := q.QueryContext(ctx, `SELECT `+enrollmentTokenColumns()+`, token_hash
		FROM security.node_enrollment_tokens
		WHERE status = 'active' AND token_hint = $1
		ORDER BY created_at DESC
	`, hint)
	if err != nil {
		return EnrollmentToken{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		token, err := scanEnrollmentTokenWithHash(rows, &hash)
		if err != nil {
			return EnrollmentToken{}, err
		}
		if verifySecretToken(tokenValue, hash) {
			return token, nil
		}
	}
	if err := rows.Err(); err != nil {
		return EnrollmentToken{}, err
	}
	return EnrollmentToken{}, fmt.Errorf("enrollment token is invalid")
}

func resolveEnrollmentRequestTx(ctx context.Context, q queryer, ref string) (EnrollmentRequest, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return EnrollmentRequest{}, fmt.Errorf("enrollment request ref is required")
	}
	return scanEnrollmentRequest(q.QueryRowContext(ctx, `SELECT `+enrollmentRequestColumns()+`
		FROM security.node_enrollment_requests
		WHERE node_enrollment_request_id = $1
	`, ref))
}

func assignDefaultProfiles(ctx context.Context, q queryExecer, nodeID, nodeKind, nodeRole, runtimeClass, actorID string) error {
	assignment, err := nodeprofiles.Resolve(nodeprofiles.ResolveInput{
		NodeKind:     nodeKind,
		NodeRole:     nodeRole,
		RuntimeClass: runtimeClass,
	})
	if err != nil {
		return err
	}
	result, err := q.ExecContext(ctx, `
		INSERT INTO nodes.node_profile_assignments (
			node_id, authority_profile_id, runtime_profile_id, assigned_by_actor_id, metadata
		)
		SELECT $1, ap.authority_profile_id, rp.runtime_profile_id, nullif($4, ''), '{"slice":"v0.5","source":"node_profile_resolver"}'::jsonb
		FROM nodes.authority_profiles ap
		CROSS JOIN nodes.runtime_profiles rp
		WHERE ap.profile_key = $2
		  AND rp.profile_key = $3
		ON CONFLICT (node_id) DO UPDATE
		SET authority_profile_id = EXCLUDED.authority_profile_id,
		    runtime_profile_id = EXCLUDED.runtime_profile_id,
		    assigned_by_actor_id = EXCLUDED.assigned_by_actor_id,
		    assigned_at = now(),
		    metadata = EXCLUDED.metadata
	`, nodeID, assignment.AuthorityProfileKey, assignment.RuntimeProfileKey, actorID)
	if err != nil {
		return err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr == nil && rows == 0 {
		return fmt.Errorf("node profile assignment could not resolve authority=%s runtime=%s", assignment.AuthorityProfileKey, assignment.RuntimeProfileKey)
	}
	return nil
}

func normalizeEnrollmentRequestInput(input CreateEnrollmentRequestInput) CreateEnrollmentRequestInput {
	input.EnrollmentToken = strings.TrimSpace(input.EnrollmentToken)
	input.RequestedNodeKey = strings.TrimSpace(input.RequestedNodeKey)
	input.RequestedDisplayName = strings.TrimSpace(input.RequestedDisplayName)
	input.RequestedNodeKind = defaultString(input.RequestedNodeKind, "workspace")
	input.RequestedNodeRole = strings.TrimSpace(input.RequestedNodeRole)
	if input.RequestedNodeRole == "" {
		input.RequestedNodeRole = nodeprofiles.DefaultRoleForKind(input.RequestedNodeKind)
	}
	input.RequestedRuntimeClass = strings.TrimSpace(input.RequestedRuntimeClass)
	if input.RequestedRuntimeClass == "" {
		input.RequestedRuntimeClass = nodeprofiles.DefaultRuntimeClassFor(input.RequestedNodeKind, input.RequestedNodeRole)
	}
	input.RequestedProfileJSON = objectOrDefault(input.RequestedProfileJSON)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func enrollmentTokenColumns() string {
	return `
		node_enrollment_token_id, token_hint, requested_by_actor_id, status,
		created_at, expires_at, used_at, metadata`
}

func enrollmentRequestColumns() string {
	return `
		node_enrollment_request_id, node_enrollment_token_id, requested_node_key,
		requested_display_name, requested_node_kind, requested_node_role,
		requested_runtime_class, requested_profile_json, requested_by_actor_id,
		approved_by_actor_id, status, activated_node_id, node_credential_id,
		denial_reason, created_at, expires_at, approved_at, denied_at, metadata`
}

func nodeCredentialColumns() string {
	return `
		node_credential_id, node_id, credential_kind, credential_hint, status,
		issued_by_actor_id, created_at, expires_at, last_used_at, revoked_at,
		revoked_reason, metadata`
}

func scanEnrollmentToken(scanner nodeScanner) (EnrollmentToken, error) {
	return scanEnrollmentTokenWithHash(scanner, nil)
}

func scanEnrollmentTokenWithHash(scanner nodeScanner, hash *string) (EnrollmentToken, error) {
	var token EnrollmentToken
	var usedAt sql.NullTime
	var metadata []byte
	dest := []any{
		&token.NodeEnrollmentTokenID,
		&token.TokenHint,
		&token.RequestedByActorID,
		&token.Status,
		&token.CreatedAt,
		&token.ExpiresAt,
		&usedAt,
		&metadata,
	}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := scanner.Scan(dest...); err != nil {
		return EnrollmentToken{}, err
	}
	token.UsedAt = timePtr(usedAt)
	token.Metadata = jsonOrEmpty(metadata)
	return token, nil
}

func scanEnrollmentRequest(scanner nodeScanner) (EnrollmentRequest, error) {
	var request EnrollmentRequest
	var tokenID, requestedBy, approvedBy, activatedNodeID, credentialID, denialReason sql.NullString
	var approvedAt, deniedAt sql.NullTime
	var profileJSON, metadata []byte
	if err := scanner.Scan(
		&request.NodeEnrollmentRequestID,
		&tokenID,
		&request.RequestedNodeKey,
		&request.RequestedDisplayName,
		&request.RequestedNodeKind,
		&request.RequestedNodeRole,
		&request.RequestedRuntimeClass,
		&profileJSON,
		&requestedBy,
		&approvedBy,
		&request.Status,
		&activatedNodeID,
		&credentialID,
		&denialReason,
		&request.CreatedAt,
		&request.ExpiresAt,
		&approvedAt,
		&deniedAt,
		&metadata,
	); err != nil {
		return EnrollmentRequest{}, err
	}
	request.NodeEnrollmentTokenID = stringPtr(tokenID)
	request.RequestedByActorID = stringPtr(requestedBy)
	request.ApprovedByActorID = stringPtr(approvedBy)
	request.ActivatedNodeID = stringPtr(activatedNodeID)
	request.NodeCredentialID = stringPtr(credentialID)
	request.DenialReason = stringPtr(denialReason)
	request.ApprovedAt = timePtr(approvedAt)
	request.DeniedAt = timePtr(deniedAt)
	request.RequestedProfileJSON = jsonOrEmpty(profileJSON)
	request.Metadata = jsonOrEmpty(metadata)
	return request, nil
}

func scanNodeCredential(scanner nodeScanner) (NodeCredential, error) {
	var credential NodeCredential
	var issuedBy, revokedReason sql.NullString
	var expiresAt, lastUsedAt, revokedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&credential.NodeCredentialID,
		&credential.NodeID,
		&credential.CredentialKind,
		&credential.CredentialHint,
		&credential.Status,
		&issuedBy,
		&credential.CreatedAt,
		&expiresAt,
		&lastUsedAt,
		&revokedAt,
		&revokedReason,
		&metadata,
	); err != nil {
		return NodeCredential{}, err
	}
	credential.IssuedByActorID = stringPtr(issuedBy)
	credential.ExpiresAt = timePtr(expiresAt)
	credential.LastUsedAt = timePtr(lastUsedAt)
	credential.RevokedAt = timePtr(revokedAt)
	credential.RevokedReason = stringPtr(revokedReason)
	credential.Metadata = jsonOrEmpty(metadata)
	return credential, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queryExecer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

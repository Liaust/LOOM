package capabilities

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
)

const providerAdvertisementMessageKind = "provider.advertisement"

type ProviderAdvertisementInput struct {
	NodeRef         string             `json:"node_ref,omitempty"`
	CredentialToken string             `json:"credential_token,omitempty"`
	IdempotencyKey  string             `json:"idempotency_key,omitempty"`
	Provider        AdvertisedProvider `json:"provider"`
	Metadata        json.RawMessage    `json:"metadata,omitempty"`
}

type AdvertisedProvider struct {
	ProviderKey           string                    `json:"provider_key"`
	CompactAddress        string                    `json:"compact_address"`
	DisplayName           string                    `json:"display_name"`
	Description           string                    `json:"description,omitempty"`
	ProviderType          string                    `json:"provider_type"`
	ScopeRef              string                    `json:"scope_ref,omitempty"`
	Version               string                    `json:"version,omitempty"`
	RuntimeProfileJSON    json.RawMessage           `json:"runtime_profile_json,omitempty"`
	DocumentationRefsJSON json.RawMessage           `json:"documentation_refs_json,omitempty"`
	PackageRef            string                    `json:"package_ref,omitempty"`
	Health                ProviderHealthInput       `json:"health,omitempty"`
	Capabilities          []AdvertisedCapability    `json:"capabilities,omitempty"`
	UsageDocuments        []AdvertisedUsageDocument `json:"usage_documents,omitempty"`
	Metadata              json.RawMessage           `json:"metadata,omitempty"`
}

type AdvertisedCapability struct {
	EndpointName                string                    `json:"endpoint_name"`
	CompactAddress              string                    `json:"compact_address,omitempty"`
	ClassNamespace              string                    `json:"class_namespace"`
	ClassName                   string                    `json:"class_name"`
	ClassVersion                string                    `json:"class_version,omitempty"`
	DisplayName                 string                    `json:"display_name"`
	Description                 string                    `json:"description,omitempty"`
	Form                        string                    `json:"form"`
	InputSchemaJSON             json.RawMessage           `json:"input_schema_json,omitempty"`
	OutputSchemaJSON            json.RawMessage           `json:"output_schema_json,omitempty"`
	RiskLevel                   string                    `json:"risk_level"`
	ExecutionAuthorizationLevel int                       `json:"execution_authorization_level"`
	SideEffectsJSON             json.RawMessage           `json:"side_effects_json,omitempty"`
	PolicyRequirementsJSON      json.RawMessage           `json:"policy_requirements_json,omitempty"`
	CredentialRequirementsJSON  json.RawMessage           `json:"credential_requirements_json,omitempty"`
	ApprovalRequirementsJSON    json.RawMessage           `json:"approval_requirements_json,omitempty"`
	JobBehaviorJSON             json.RawMessage           `json:"job_behavior_json,omitempty"`
	SessionBehaviorJSON         json.RawMessage           `json:"session_behavior_json,omitempty"`
	StreamBehaviorJSON          json.RawMessage           `json:"stream_behavior_json,omitempty"`
	LeaseBehaviorJSON           json.RawMessage           `json:"lease_behavior_json,omitempty"`
	VersionLabel                string                    `json:"version_label,omitempty"`
	ImplementationHash          string                    `json:"implementation_hash,omitempty"`
	ManifestJSON                json.RawMessage           `json:"manifest_json,omitempty"`
	UsageDocuments              []AdvertisedUsageDocument `json:"usage_documents,omitempty"`
	Metadata                    json.RawMessage           `json:"metadata,omitempty"`
}

type AdvertisedUsageDocument struct {
	Title                string          `json:"title"`
	VersionLabel         string          `json:"version_label,omitempty"`
	BodyFormat           string          `json:"body_format,omitempty"`
	Body                 string          `json:"body"`
	SectionMapJSON       json.RawMessage `json:"section_map_json,omitempty"`
	VisibilityPolicyJSON json.RawMessage `json:"visibility_policy_json,omitempty"`
	SourceRef            string          `json:"source_ref,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

type ProviderAdvertisementFilter struct {
	Limit       int    `json:"limit,omitempty"`
	NodeRef     string `json:"node_ref,omitempty"`
	ProviderRef string `json:"provider_ref,omitempty"`
	Status      string `json:"status,omitempty"`
}

type ApproveProviderAdvertisementInput struct {
	Reason   string          `json:"reason,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type RejectProviderAdvertisementInput struct {
	Reason   string          `json:"reason"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func (s Service) AdvertiseProvider(ctx context.Context, req requestctx.Context, input ProviderAdvertisementInput) (ProviderAdvertisementInspection, error) {
	if strings.TrimSpace(input.CredentialToken) == "" {
		return ProviderAdvertisementInspection{}, fmt.Errorf("credential_token is required")
	}

	_, node, err := nodes.NewService(s.DB).AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	if strings.TrimSpace(input.NodeRef) != "" {
		nodeID, err := resolveNodeRef(ctx, s.DB, input.NodeRef)
		if err != nil {
			return ProviderAdvertisementInspection{}, fmt.Errorf("resolve advertised node: %w", err)
		}
		if nodeID != node.NodeID {
			return ProviderAdvertisementInspection{}, fmt.Errorf("credential does not belong to advertised node")
		}
	}

	rawPayload, advertisementHash, err := providerAdvertisementPayload(input)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	defer tx.Rollback()

	messageID, err := insertProviderAdvertisementMessageTx(ctx, tx, req, node.NodeID, input, rawPayload, advertisementHash)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	provider, endpoints, usageDocs, err := s.upsertAdvertisedProviderTx(ctx, tx, req, node.NodeID, input.Provider)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	validationSummary, _ := json.Marshal(map[string]any{
		"provider_key":     provider.ProviderKey,
		"compact_address":  provider.CompactAddress,
		"capability_count": len(input.Provider.Capabilities),
	})
	upsertSummary, _ := json.Marshal(map[string]any{
		"provider_id":          provider.ProviderID,
		"endpoint_count":       len(endpoints),
		"usage_document_count": len(usageDocs),
	})

	advertisement, err := insertProviderAdvertisementTx(ctx, tx, node.NodeID, provider.ProviderID, messageID, input, rawPayload, advertisementHash, validationSummary, upsertSummary)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeProviderAdvertisementRecv,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         provider.ScopeID,
		TargetKind:      "provider_advertisement",
		TargetID:        advertisement.ProviderAdvertisementID,
		Status:          advertisement.Status,
		Result:          "ok",
		Payload:         advertisementEventPayload(advertisement, provider),
		VisibilityClass: "security",
	}); err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeProviderAdvertisementValid,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         provider.ScopeID,
		TargetKind:      "provider_advertisement",
		TargetID:        advertisement.ProviderAdvertisementID,
		Status:          advertisement.Status,
		Result:          "pending_review",
		Payload:         advertisementEventPayload(advertisement, provider),
		VisibilityClass: "security",
	}); err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	if err := tx.Commit(); err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	return s.InspectProviderAdvertisement(ctx, advertisement.ProviderAdvertisementID)
}

func (s Service) ListProviderAdvertisements(ctx context.Context, filter ProviderAdvertisementFilter) ([]ProviderAdvertisement, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := `SELECT ` + providerAdvertisementColumns() + ` FROM capabilities.provider_advertisements WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		if !ValidProviderAdvertisementStatus(strings.TrimSpace(filter.Status)) {
			return nil, fmt.Errorf("unsupported advertisement status: %s", filter.Status)
		}
		add("status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.NodeRef) != "" {
		nodeID, err := resolveNodeRef(ctx, s.DB, filter.NodeRef)
		if err != nil {
			return nil, fmt.Errorf("resolve node: %w", err)
		}
		add("origin_node_id =", nodeID)
	}
	if strings.TrimSpace(filter.ProviderRef) != "" {
		providerID, err := resolveProviderRef(ctx, s.DB, filter.ProviderRef)
		if err != nil {
			return nil, fmt.Errorf("resolve provider: %w", err)
		}
		add("provider_id =", providerID)
	}

	query += fmt.Sprintf(" ORDER BY received_at DESC LIMIT %d", filter.Limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	advertisements := []ProviderAdvertisement{}
	for rows.Next() {
		ad, err := scanProviderAdvertisement(rows)
		if err != nil {
			return nil, err
		}
		advertisements = append(advertisements, ad)
	}
	return advertisements, rows.Err()
}

func (s Service) InspectProviderAdvertisement(ctx context.Context, ref string) (ProviderAdvertisementInspection, error) {
	adID, err := resolveProviderAdvertisementRef(ctx, s.DB, ref)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	ad, err := getProviderAdvertisement(ctx, s.DB, adID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	inspection := ProviderAdvertisementInspection{Advertisement: ad}
	if ad.ProviderID == nil {
		return inspection, nil
	}
	provider, err := getProvider(ctx, s.DB, *ad.ProviderID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	inspection.Provider = &provider
	endpoints, err := listProviderEndpoints(ctx, s.DB, provider.ProviderID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	inspection.Endpoints = endpoints
	docs, err := listUsageDocumentsForTarget(ctx, s.DB, UsageTargetKindProvider, provider.ProviderID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	for _, endpoint := range endpoints {
		endpointDocs, err := listUsageDocumentsForTarget(ctx, s.DB, UsageTargetKindCapabilityEndpoint, endpoint.CapabilityEndpointID)
		if err != nil {
			return ProviderAdvertisementInspection{}, err
		}
		docs = append(docs, endpointDocs...)
	}
	inspection.UsageDocuments = docs
	return inspection, nil
}

func (s Service) ApproveProviderAdvertisement(ctx context.Context, req requestctx.Context, ref string, input ApproveProviderAdvertisementInput) (ProviderAdvertisementInspection, error) {
	adID, err := resolveProviderAdvertisementRef(ctx, s.DB, ref)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	defer tx.Rollback()

	ad, err := getProviderAdvertisement(ctx, tx, adID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	if ad.Status == ProviderAdvertisementStatusApproved {
		if err := tx.Commit(); err != nil {
			return ProviderAdvertisementInspection{}, err
		}
		return s.InspectProviderAdvertisement(ctx, adID)
	}
	if ad.Status == ProviderAdvertisementStatusRejected {
		return ProviderAdvertisementInspection{}, fmt.Errorf("provider advertisement is rejected")
	}
	if ad.ProviderID == nil {
		return ProviderAdvertisementInspection{}, fmt.Errorf("provider advertisement has no provider")
	}

	provider, err := getProvider(ctx, tx, *ad.ProviderID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.providers
		SET status = 'active',
		    updated_at = now()
		WHERE provider_id = $1
	`, provider.ProviderID); err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	if err := activateAdvertisedEndpointsTx(ctx, tx, req.ActorID, provider.ProviderID); err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.usage_documents
		SET review_status = 'approved',
		    approved_by_actor_id = COALESCE(approved_by_actor_id, $2),
		    approved_at = COALESCE(approved_at, now()),
		    updated_at = now()
		WHERE (target_kind = 'provider' AND target_id = $1)
		   OR (target_kind = 'capability_endpoint' AND target_id IN (
		       SELECT capability_endpoint_id FROM capabilities.capability_endpoints WHERE provider_id = $1
		   ))
	`, provider.ProviderID, req.ActorID); err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	metadata := normalizeJSONObject(input.Metadata)
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.provider_advertisements
		SET status = 'approved',
		    reviewed_at = now(),
		    reviewed_by_actor_id = $2,
		    metadata = metadata || $3::jsonb
		WHERE provider_advertisement_id = $1
	`, ad.ProviderAdvertisementID, req.ActorID, metadata); err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	ad.Status = ProviderAdvertisementStatusApproved
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeProviderAdvertisementAppr,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         provider.ScopeID,
		TargetKind:      "provider_advertisement",
		TargetID:        ad.ProviderAdvertisementID,
		Status:          ProviderAdvertisementStatusApproved,
		Result:          "ok",
		Payload:         advertisementEventPayload(ad, provider),
		VisibilityClass: "security",
	}); err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	if err := tx.Commit(); err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	return s.InspectProviderAdvertisement(ctx, adID)
}

func (s Service) RejectProviderAdvertisement(ctx context.Context, req requestctx.Context, ref string, input RejectProviderAdvertisementInput) (ProviderAdvertisementInspection, error) {
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return ProviderAdvertisementInspection{}, fmt.Errorf("reason is required")
	}
	adID, err := resolveProviderAdvertisementRef(ctx, s.DB, ref)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	defer tx.Rollback()

	ad, err := getProviderAdvertisement(ctx, tx, adID)
	if err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	metadata := normalizeJSONObject(input.Metadata)
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.provider_advertisements
		SET status = 'rejected',
		    reviewed_at = now(),
		    reviewed_by_actor_id = $2,
		    rejection_reason = $3,
		    metadata = metadata || $4::jsonb
		WHERE provider_advertisement_id = $1
	`, ad.ProviderAdvertisementID, req.ActorID, reason, metadata); err != nil {
		return ProviderAdvertisementInspection{}, err
	}

	if ad.ProviderID != nil {
		provider, err := getProvider(ctx, tx, *ad.ProviderID)
		if err != nil {
			return ProviderAdvertisementInspection{}, err
		}
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:       events.TypeProviderAdvertisementRej,
			EventLevel:      "audit",
			Request:         req,
			ScopeID:         provider.ScopeID,
			TargetKind:      "provider_advertisement",
			TargetID:        ad.ProviderAdvertisementID,
			Status:          ProviderAdvertisementStatusRejected,
			Result:          "rejected",
			Payload:         advertisementEventPayload(ad, provider),
			VisibilityClass: "security",
		}); err != nil {
			return ProviderAdvertisementInspection{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return ProviderAdvertisementInspection{}, err
	}
	return s.InspectProviderAdvertisement(ctx, adID)
}

func (s Service) upsertAdvertisedProviderTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, nodeID string, advertised AdvertisedProvider) (Provider, []CapabilityEndpoint, []UsageDocument, error) {
	providerInput := RegisterProviderInput{
		ProviderKey:           advertised.ProviderKey,
		CompactAddress:        advertised.CompactAddress,
		DisplayName:           advertised.DisplayName,
		Description:           advertised.Description,
		ProviderType:          advertised.ProviderType,
		NodeRef:               nodeID,
		ScopeRef:              defaultString(advertised.ScopeRef, req.ScopeID),
		Version:               advertised.Version,
		Status:                ProviderStatusRegistered,
		RuntimeProfileJSON:    advertised.RuntimeProfileJSON,
		DocumentationRefsJSON: advertised.DocumentationRefsJSON,
		PackageRef:            advertised.PackageRef,
		Metadata:              advertised.Metadata,
	}
	normalizedProvider, err := normalizeProviderInput(ctx, tx, req, providerInput)
	if err != nil {
		return Provider{}, nil, nil, err
	}

	provider, err := upsertAdvertisedProviderTx(ctx, tx, req, normalizedProvider)
	if err != nil {
		return Provider{}, nil, nil, err
	}
	health, err := normalizeProviderHealthInput(advertised.Health)
	if err != nil {
		return Provider{}, nil, nil, err
	}
	if _, _, err := upsertProviderHealthTx(ctx, tx, provider.ProviderID, health); err != nil {
		return Provider{}, nil, nil, err
	}

	usageDocs := []UsageDocument{}
	for _, doc := range advertised.UsageDocuments {
		inserted, err := upsertAdvertisedUsageDocumentTx(ctx, tx, req, UsageTargetKindProvider, provider.ProviderID, provider.CompactAddress, doc)
		if err != nil {
			return Provider{}, nil, nil, err
		}
		usageDocs = append(usageDocs, inserted)
	}

	endpoints := []CapabilityEndpoint{}
	for _, advertisedCapability := range advertised.Capabilities {
		class, err := upsertAdvertisedClassTx(ctx, tx, advertisedCapability)
		if err != nil {
			return Provider{}, nil, nil, err
		}
		endpoint, err := upsertAdvertisedEndpointTx(ctx, tx, provider, class, advertisedCapability)
		if err != nil {
			return Provider{}, nil, nil, err
		}
		if _, err := upsertAdvertisedEndpointVersionTx(ctx, tx, req, endpoint, advertisedCapability); err != nil {
			return Provider{}, nil, nil, err
		}
		for _, doc := range advertisedCapability.UsageDocuments {
			inserted, err := upsertAdvertisedUsageDocumentTx(ctx, tx, req, UsageTargetKindCapabilityEndpoint, endpoint.CapabilityEndpointID, endpoint.CompactAddress, doc)
			if err != nil {
				return Provider{}, nil, nil, err
			}
			usageDocs = append(usageDocs, inserted)
		}
		endpoints = append(endpoints, endpoint)
	}
	return provider, endpoints, usageDocs, nil
}

func upsertAdvertisedProviderTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input RegisterProviderInput) (Provider, error) {
	var provider Provider
	err := tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.providers (
			provider_id, provider_key, compact_address, display_name, description,
			provider_type, node_id, scope_id, version, status, runtime_profile_json,
			documentation_refs_json, package_ref, created_by_actor_id, last_advertised_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'registered', $10, $11, nullif($12, ''), $13, now(), $14)
		ON CONFLICT (node_id, provider_key) DO UPDATE
		SET compact_address = EXCLUDED.compact_address,
		    display_name = EXCLUDED.display_name,
		    description = EXCLUDED.description,
		    provider_type = EXCLUDED.provider_type,
		    scope_id = EXCLUDED.scope_id,
		    version = EXCLUDED.version,
		    status = CASE
		        WHEN capabilities.providers.status = 'active' THEN 'active'
		        ELSE 'registered'
		    END,
		    runtime_profile_json = EXCLUDED.runtime_profile_json,
		    documentation_refs_json = EXCLUDED.documentation_refs_json,
		    package_ref = EXCLUDED.package_ref,
		    last_advertised_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+providerColumns(),
		ids.NewProviderID(),
		input.ProviderKey,
		input.CompactAddress,
		input.DisplayName,
		input.Description,
		input.ProviderType,
		input.NodeRef,
		input.ScopeRef,
		input.Version,
		input.RuntimeProfileJSON,
		input.DocumentationRefsJSON,
		input.PackageRef,
		req.ActorID,
		input.Metadata,
	).Scan(providerScanDest(&provider)...)
	if err != nil {
		return Provider{}, err
	}
	return normalizeScannedProvider(provider), nil
}

func upsertAdvertisedClassTx(ctx context.Context, tx *sql.Tx, cap AdvertisedCapability) (CapabilityClass, error) {
	input := RegisterCapabilityClassInput{
		Namespace:                     cap.ClassNamespace,
		Name:                          cap.ClassName,
		Version:                       cap.ClassVersion,
		DisplayName:                   cap.DisplayName,
		Description:                   cap.Description,
		Form:                          cap.Form,
		InputSchemaJSON:               cap.InputSchemaJSON,
		OutputSchemaJSON:              cap.OutputSchemaJSON,
		DefaultRiskLevel:              cap.RiskLevel,
		DefaultPolicyRequirementsJSON: cap.PolicyRequirementsJSON,
		Status:                        CapabilityClassStatusActive,
		Metadata:                      cap.Metadata,
	}
	normalized, err := normalizeCapabilityClassInput(input)
	if err != nil {
		return CapabilityClass{}, err
	}

	var class CapabilityClass
	err = tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.capability_classes (
			capability_class_id, namespace, name, version, display_name, description,
			form, input_schema_json, output_schema_json, default_risk_level,
			default_policy_requirements_json, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'active', $12)
		ON CONFLICT (namespace, name, version) DO UPDATE
		SET display_name = EXCLUDED.display_name,
		    description = EXCLUDED.description,
		    form = EXCLUDED.form,
		    input_schema_json = EXCLUDED.input_schema_json,
		    output_schema_json = EXCLUDED.output_schema_json,
		    default_risk_level = EXCLUDED.default_risk_level,
		    default_policy_requirements_json = EXCLUDED.default_policy_requirements_json,
		    status = CASE
		        WHEN capabilities.capability_classes.status = 'revoked' THEN 'revoked'
		        ELSE 'active'
		    END,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+capabilityClassColumns(),
		ids.NewCapabilityClassID(),
		normalized.Namespace,
		normalized.Name,
		normalized.Version,
		normalized.DisplayName,
		normalized.Description,
		normalized.Form,
		normalized.InputSchemaJSON,
		normalized.OutputSchemaJSON,
		normalized.DefaultRiskLevel,
		normalized.DefaultPolicyRequirementsJSON,
		normalized.Metadata,
	).Scan(capabilityClassScanDest(&class)...)
	if err != nil {
		return CapabilityClass{}, err
	}
	return normalizeScannedCapabilityClass(class), nil
}

func upsertAdvertisedEndpointTx(ctx context.Context, tx *sql.Tx, provider Provider, class CapabilityClass, cap AdvertisedCapability) (CapabilityEndpoint, error) {
	input := RegisterCapabilityEndpointInput{
		ProviderRef:                 provider.ProviderID,
		CapabilityClassRef:          class.CapabilityClassID,
		EndpointName:                cap.EndpointName,
		CompactAddress:              cap.CompactAddress,
		Form:                        cap.Form,
		InputSchemaJSON:             cap.InputSchemaJSON,
		OutputSchemaJSON:            cap.OutputSchemaJSON,
		RiskLevel:                   cap.RiskLevel,
		ExecutionAuthorizationLevel: cap.ExecutionAuthorizationLevel,
		SideEffectsJSON:             cap.SideEffectsJSON,
		PolicyRequirementsJSON:      cap.PolicyRequirementsJSON,
		CredentialRequirementsJSON:  cap.CredentialRequirementsJSON,
		ApprovalRequirementsJSON:    cap.ApprovalRequirementsJSON,
		JobBehaviorJSON:             cap.JobBehaviorJSON,
		SessionBehaviorJSON:         cap.SessionBehaviorJSON,
		StreamBehaviorJSON:          cap.StreamBehaviorJSON,
		LeaseBehaviorJSON:           cap.LeaseBehaviorJSON,
		Status:                      EndpointStatusRegistered,
		Metadata:                    cap.Metadata,
	}
	normalized, err := normalizeCapabilityEndpointInput(ctx, tx, input)
	if err != nil {
		return CapabilityEndpoint{}, err
	}

	var endpoint CapabilityEndpoint
	err = tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.capability_endpoints (
			capability_endpoint_id, provider_id, capability_class_id, endpoint_name,
			compact_address, form, input_schema_json, output_schema_json, risk_level,
			execution_authorization_level, side_effects_json, policy_requirements_json,
			credential_requirements_json, approval_requirements_json, job_behavior_json,
			session_behavior_json, stream_behavior_json, lease_behavior_json, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, 'registered', $19)
		ON CONFLICT (provider_id, endpoint_name) DO UPDATE
		SET capability_class_id = EXCLUDED.capability_class_id,
		    compact_address = EXCLUDED.compact_address,
		    form = EXCLUDED.form,
		    input_schema_json = EXCLUDED.input_schema_json,
		    output_schema_json = EXCLUDED.output_schema_json,
		    risk_level = EXCLUDED.risk_level,
		    execution_authorization_level = EXCLUDED.execution_authorization_level,
		    side_effects_json = EXCLUDED.side_effects_json,
		    policy_requirements_json = EXCLUDED.policy_requirements_json,
		    credential_requirements_json = EXCLUDED.credential_requirements_json,
		    approval_requirements_json = EXCLUDED.approval_requirements_json,
		    job_behavior_json = EXCLUDED.job_behavior_json,
		    session_behavior_json = EXCLUDED.session_behavior_json,
		    stream_behavior_json = EXCLUDED.stream_behavior_json,
		    lease_behavior_json = EXCLUDED.lease_behavior_json,
		    status = CASE
		        WHEN capabilities.capability_endpoints.status = 'active' THEN 'active'
		        ELSE 'registered'
		    END,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+capabilityEndpointColumns(),
		ids.NewCapabilityEndpointID(),
		normalized.ProviderRef,
		normalized.CapabilityClassRef,
		normalized.EndpointName,
		normalized.CompactAddress,
		normalized.Form,
		normalized.InputSchemaJSON,
		normalized.OutputSchemaJSON,
		normalized.RiskLevel,
		normalized.ExecutionAuthorizationLevel,
		normalized.SideEffectsJSON,
		normalized.PolicyRequirementsJSON,
		normalized.CredentialRequirementsJSON,
		normalized.ApprovalRequirementsJSON,
		normalized.JobBehaviorJSON,
		normalized.SessionBehaviorJSON,
		normalized.StreamBehaviorJSON,
		normalized.LeaseBehaviorJSON,
		normalized.Metadata,
	).Scan(capabilityEndpointScanDest(&endpoint)...)
	if err != nil {
		return CapabilityEndpoint{}, err
	}
	return normalizeScannedCapabilityEndpoint(endpoint), nil
}

func upsertAdvertisedEndpointVersionTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, endpoint CapabilityEndpoint, cap AdvertisedCapability) (EndpointVersion, error) {
	input := RegisterEndpointVersionInput{
		CapabilityEndpointRef:       endpoint.CapabilityEndpointID,
		VersionLabel:                cap.VersionLabel,
		ImplementationHash:          cap.ImplementationHash,
		ManifestJSON:                cap.ManifestJSON,
		InputSchemaJSON:             cap.InputSchemaJSON,
		OutputSchemaJSON:            cap.OutputSchemaJSON,
		RiskLevel:                   cap.RiskLevel,
		ExecutionAuthorizationLevel: cap.ExecutionAuthorizationLevel,
		PolicyRequirementsJSON:      cap.PolicyRequirementsJSON,
		CredentialRequirementsJSON:  cap.CredentialRequirementsJSON,
		ApprovalRequirementsJSON:    cap.ApprovalRequirementsJSON,
		Status:                      EndpointVersionStatusPendingReview,
		Metadata:                    cap.Metadata,
	}
	normalized, err := normalizeEndpointVersionInput(ctx, tx, input)
	if err != nil {
		return EndpointVersion{}, err
	}

	var version EndpointVersion
	err = tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.endpoint_versions (
			capability_endpoint_version_id, capability_endpoint_id, version_label,
			implementation_hash, manifest_json, input_schema_json, output_schema_json,
			risk_level, execution_authorization_level, policy_requirements_json,
			credential_requirements_json, approval_requirements_json, status,
			approved_by_actor_id, approved_at, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8, $9, $10, $11, $12, 'pending_review', nullif($13, ''), $14, $15)
		ON CONFLICT (capability_endpoint_id, version_label) DO UPDATE
		SET implementation_hash = EXCLUDED.implementation_hash,
		    manifest_json = EXCLUDED.manifest_json,
		    input_schema_json = EXCLUDED.input_schema_json,
		    output_schema_json = EXCLUDED.output_schema_json,
		    risk_level = EXCLUDED.risk_level,
		    execution_authorization_level = EXCLUDED.execution_authorization_level,
		    policy_requirements_json = EXCLUDED.policy_requirements_json,
		    credential_requirements_json = EXCLUDED.credential_requirements_json,
		    approval_requirements_json = EXCLUDED.approval_requirements_json,
		    status = CASE
		        WHEN capabilities.endpoint_versions.status = 'active' THEN 'active'
		        ELSE 'pending_review'
		    END,
		    metadata = EXCLUDED.metadata
		RETURNING `+endpointVersionColumns(),
		ids.NewCapabilityEndpointVersionID(),
		normalized.CapabilityEndpointRef,
		normalized.VersionLabel,
		normalized.ImplementationHash,
		normalized.ManifestJSON,
		normalized.InputSchemaJSON,
		normalized.OutputSchemaJSON,
		normalized.RiskLevel,
		normalized.ExecutionAuthorizationLevel,
		normalized.PolicyRequirementsJSON,
		normalized.CredentialRequirementsJSON,
		normalized.ApprovalRequirementsJSON,
		normalized.ApprovedByActorID,
		normalized.ApprovedAt,
		normalized.Metadata,
	).Scan(endpointVersionScanDest(&version)...)
	if err != nil {
		return EndpointVersion{}, err
	}
	return normalizeScannedEndpointVersion(version), nil
}

func upsertAdvertisedUsageDocumentTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, targetKind, targetID, targetAddress string, doc AdvertisedUsageDocument) (UsageDocument, error) {
	input := RegisterUsageDocumentInput{
		TargetKind:           targetKind,
		TargetID:             targetID,
		TargetAddress:        targetAddress,
		Title:                doc.Title,
		VersionLabel:         doc.VersionLabel,
		BodyFormat:           doc.BodyFormat,
		Body:                 doc.Body,
		SectionMapJSON:       doc.SectionMapJSON,
		VisibilityPolicyJSON: doc.VisibilityPolicyJSON,
		ReviewStatus:         UsageReviewStatusPendingReview,
		SourceKind:           UsageSourceKindManifest,
		SourceRef:            doc.SourceRef,
		Metadata:             doc.Metadata,
	}
	normalized, err := normalizeUsageDocumentInput(ctx, tx, input)
	if err != nil {
		return UsageDocument{}, err
	}
	var usageDoc UsageDocument
	err = tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.usage_documents (
			capability_usage_document_id, target_kind, target_id, target_address,
			title, version_label, body_format, body, section_map_json,
			visibility_policy_json, review_status, content_hash, source_kind,
			source_ref, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8, $9, $10, 'pending_review', $11, $12, nullif($13, ''), $14, $15)
		ON CONFLICT (target_kind, target_id, version_label, content_hash) DO UPDATE
		SET target_address = EXCLUDED.target_address,
		    title = EXCLUDED.title,
		    body_format = EXCLUDED.body_format,
		    body = EXCLUDED.body,
		    section_map_json = EXCLUDED.section_map_json,
		    visibility_policy_json = EXCLUDED.visibility_policy_json,
		    review_status = CASE
		        WHEN capabilities.usage_documents.review_status = 'approved' THEN 'approved'
		        ELSE 'pending_review'
		    END,
		    source_kind = EXCLUDED.source_kind,
		    source_ref = EXCLUDED.source_ref,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+usageDocumentColumns(),
		ids.NewCapabilityUsageDocumentID(),
		normalized.TargetKind,
		normalized.TargetID,
		normalized.TargetAddress,
		normalized.Title,
		normalized.VersionLabel,
		normalized.BodyFormat,
		normalized.Body,
		normalized.SectionMapJSON,
		normalized.VisibilityPolicyJSON,
		normalized.ContentHash,
		normalized.SourceKind,
		normalized.SourceRef,
		req.ActorID,
		normalized.Metadata,
	).Scan(usageDocumentScanDest(&usageDoc)...)
	if err != nil {
		return UsageDocument{}, err
	}
	return normalizeScannedUsageDocument(usageDoc), nil
}

func activateAdvertisedEndpointsTx(ctx context.Context, tx *sql.Tx, actorID, providerID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT ON (ev.capability_endpoint_id)
		       ev.capability_endpoint_version_id,
		       ev.capability_endpoint_id
		FROM capabilities.endpoint_versions ev
		JOIN capabilities.capability_endpoints e ON e.capability_endpoint_id = ev.capability_endpoint_id
		WHERE e.provider_id = $1
		  AND ev.status IN ('pending_review', 'active')
		ORDER BY ev.capability_endpoint_id, ev.created_at DESC
	`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type selectedVersion struct {
		versionID  string
		endpointID string
	}
	selected := []selectedVersion{}
	for rows.Next() {
		var item selectedVersion
		if err := rows.Scan(&item.versionID, &item.endpointID); err != nil {
			return err
		}
		selected = append(selected, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range selected {
		if _, err := tx.ExecContext(ctx, `
			UPDATE capabilities.endpoint_versions
			SET status = 'active',
			    approved_by_actor_id = COALESCE(approved_by_actor_id, $2),
			    approved_at = COALESCE(approved_at, now())
			WHERE capability_endpoint_version_id = $1
		`, item.versionID, actorID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE capabilities.capability_endpoints
			SET status = 'active',
			    active_endpoint_version_id = $2,
			    updated_at = now()
			WHERE capability_endpoint_id = $1
		`, item.endpointID, item.versionID); err != nil {
			return err
		}
	}
	return nil
}

func insertProviderAdvertisementMessageTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, nodeID string, input ProviderAdvertisementInput, rawPayload json.RawMessage, advertisementHash string) (string, error) {
	idempotencyKey := defaultString(input.IdempotencyKey, "provider-advertisement:"+advertisementHash)
	resultJSON, _ := json.Marshal(map[string]any{"advertisement_hash": advertisementHash})
	metadata, _ := json.Marshal(map[string]any{"source": "provider_advertisement", "correlation_id": req.CorrelationID})

	var messageID string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO communication.messages (
			communication_message_id, node_id, direction, kind, status,
			idempotency_key, correlation_id, payload_json, payload_hash,
			result_json, acked_at, metadata
		)
		VALUES ($1, $2, 'node_to_main', $3, 'acked', $4, nullif($5, ''), $6, $7, $8, now(), $9)
		ON CONFLICT (node_id, direction, idempotency_key) WHERE idempotency_key IS NOT NULL DO UPDATE
		SET payload_json = EXCLUDED.payload_json,
		    payload_hash = EXCLUDED.payload_hash,
		    result_json = EXCLUDED.result_json,
		    acked_at = COALESCE(communication.messages.acked_at, now()),
		    updated_at = now()
		RETURNING communication_message_id
	`, ids.NewCommunicationMessageID(), nodeID, providerAdvertisementMessageKind, idempotencyKey, req.CorrelationID, rawPayload, advertisementHash, resultJSON, metadata).Scan(&messageID)
	return messageID, err
}

func insertProviderAdvertisementTx(ctx context.Context, tx *sql.Tx, nodeID, providerID, messageID string, input ProviderAdvertisementInput, rawPayload json.RawMessage, advertisementHash string, validationSummary, upsertSummary json.RawMessage) (ProviderAdvertisement, error) {
	metadata := normalizeJSONObject(input.Metadata)
	var ad ProviderAdvertisement
	err := tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.provider_advertisements (
			provider_advertisement_id, origin_node_id, provider_id,
			communication_message_id, advertisement_hash, idempotency_key,
			status, raw_payload_json, validation_summary_json, upsert_summary_json,
			validated_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), 'pending_review', $7, $8, $9, now(), $10)
		ON CONFLICT (origin_node_id, advertisement_hash) DO UPDATE
		SET provider_id = EXCLUDED.provider_id,
		    communication_message_id = COALESCE(capabilities.provider_advertisements.communication_message_id, EXCLUDED.communication_message_id),
		    raw_payload_json = EXCLUDED.raw_payload_json,
		    validation_summary_json = EXCLUDED.validation_summary_json,
		    upsert_summary_json = EXCLUDED.upsert_summary_json,
		    validated_at = COALESCE(capabilities.provider_advertisements.validated_at, now()),
		    metadata = capabilities.provider_advertisements.metadata || EXCLUDED.metadata
		RETURNING `+providerAdvertisementColumns(),
		ids.NewProviderAdvertisementID(),
		nodeID,
		providerID,
		messageID,
		advertisementHash,
		input.IdempotencyKey,
		rawPayload,
		validationSummary,
		upsertSummary,
		metadata,
	).Scan(providerAdvertisementScanDest(&ad)...)
	if err != nil {
		return ProviderAdvertisement{}, err
	}
	return normalizeScannedProviderAdvertisement(ad), nil
}

func providerAdvertisementPayload(input ProviderAdvertisementInput) (json.RawMessage, string, error) {
	input.CredentialToken = ""
	input.Metadata = normalizeJSONObject(input.Metadata)
	input.Provider.RuntimeProfileJSON = normalizeJSONObject(input.Provider.RuntimeProfileJSON)
	input.Provider.DocumentationRefsJSON = normalizeJSONArray(input.Provider.DocumentationRefsJSON)
	input.Provider.Metadata = normalizeJSONObject(input.Provider.Metadata)
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, fmt.Sprintf("sha256:%x", sum), nil
}

func resolveProviderAdvertisementRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("provider advertisement ref is required")
	}
	var adID string
	err := q.QueryRowContext(ctx, `
		SELECT provider_advertisement_id
		FROM capabilities.provider_advertisements
		WHERE provider_advertisement_id = $1 OR advertisement_hash = $1
	`, ref).Scan(&adID)
	return adID, err
}

func getProviderAdvertisement(ctx context.Context, q queryer, adID string) (ProviderAdvertisement, error) {
	var ad ProviderAdvertisement
	err := q.QueryRowContext(ctx, `SELECT `+providerAdvertisementColumns()+` FROM capabilities.provider_advertisements WHERE provider_advertisement_id = $1`, adID).Scan(providerAdvertisementScanDest(&ad)...)
	if err != nil {
		return ProviderAdvertisement{}, err
	}
	return normalizeScannedProviderAdvertisement(ad), nil
}

func providerAdvertisementColumns() string {
	return `provider_advertisement_id, origin_node_id, provider_id,
	        communication_message_id, advertisement_hash, idempotency_key,
	        status, raw_payload_json, validation_summary_json, upsert_summary_json,
	        rejection_reason, received_at, validated_at, reviewed_at,
	        reviewed_by_actor_id, metadata`
}

func providerAdvertisementScanDest(ad *ProviderAdvertisement) []any {
	return []any{
		&ad.ProviderAdvertisementID,
		&ad.OriginNodeID,
		&ad.ProviderID,
		&ad.CommunicationMessageID,
		&ad.AdvertisementHash,
		&ad.IdempotencyKey,
		&ad.Status,
		&ad.RawPayloadJSON,
		&ad.ValidationSummaryJSON,
		&ad.UpsertSummaryJSON,
		&ad.RejectionReason,
		&ad.ReceivedAt,
		&ad.ValidatedAt,
		&ad.ReviewedAt,
		&ad.ReviewedByActorID,
		&ad.Metadata,
	}
}

func scanProviderAdvertisement(scanner rowScanner) (ProviderAdvertisement, error) {
	var ad ProviderAdvertisement
	if err := scanner.Scan(providerAdvertisementScanDest(&ad)...); err != nil {
		return ProviderAdvertisement{}, err
	}
	return normalizeScannedProviderAdvertisement(ad), nil
}

func normalizeScannedProviderAdvertisement(ad ProviderAdvertisement) ProviderAdvertisement {
	ad.RawPayloadJSON = jsonOrDefault(ad.RawPayloadJSON, `{}`)
	ad.ValidationSummaryJSON = jsonOrDefault(ad.ValidationSummaryJSON, `{}`)
	ad.UpsertSummaryJSON = jsonOrDefault(ad.UpsertSummaryJSON, `{}`)
	ad.Metadata = jsonOrDefault(ad.Metadata, `{}`)
	return ad
}

func advertisementEventPayload(ad ProviderAdvertisement, provider Provider) map[string]any {
	return map[string]any{
		"provider_advertisement_id": ad.ProviderAdvertisementID,
		"origin_node_id":            ad.OriginNodeID,
		"provider_id":               provider.ProviderID,
		"provider_key":              provider.ProviderKey,
		"compact_address":           provider.CompactAddress,
		"advertisement_hash":        ad.AdvertisementHash,
		"status":                    ad.Status,
	}
}

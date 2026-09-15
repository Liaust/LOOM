package capabilities

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/capabilityruntime"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

type RegisterProviderInput struct {
	ProviderKey           string          `json:"provider_key"`
	CompactAddress        string          `json:"compact_address"`
	DisplayName           string          `json:"display_name"`
	Description           string          `json:"description,omitempty"`
	ProviderType          string          `json:"provider_type"`
	NodeRef               string          `json:"node_ref,omitempty"`
	ScopeRef              string          `json:"scope_ref,omitempty"`
	Version               string          `json:"version,omitempty"`
	Status                string          `json:"status,omitempty"`
	RuntimeProfileJSON    json.RawMessage `json:"runtime_profile_json,omitempty"`
	DocumentationRefsJSON json.RawMessage `json:"documentation_refs_json,omitempty"`
	PackageRef            string          `json:"package_ref,omitempty"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

type ProviderHealthInput struct {
	HealthStatus       string          `json:"health_status"`
	AvailabilityStatus string          `json:"availability_status"`
	Message            string          `json:"message,omitempty"`
	DetailsJSON        json.RawMessage `json:"details_json,omitempty"`
	LastCheckedAt      *time.Time      `json:"last_checked_at,omitempty"`
	LastOKAt           *time.Time      `json:"last_ok_at,omitempty"`
}

type ProviderFilter struct {
	Limit                 int
	Offset                int
	NodeRef               string
	ScopeRef              string
	ProjectRef            string
	ProviderType          string
	Status                string
	Health                string
	RequireActiveEndpoint bool
}

type RegisterCapabilityClassInput struct {
	Namespace                     string          `json:"namespace"`
	Name                          string          `json:"name"`
	Version                       string          `json:"version,omitempty"`
	DisplayName                   string          `json:"display_name"`
	Description                   string          `json:"description,omitempty"`
	Form                          string          `json:"form"`
	InputSchemaJSON               json.RawMessage `json:"input_schema_json,omitempty"`
	OutputSchemaJSON              json.RawMessage `json:"output_schema_json,omitempty"`
	DefaultRiskLevel              string          `json:"default_risk_level"`
	DefaultPolicyRequirementsJSON json.RawMessage `json:"default_policy_requirements_json,omitempty"`
	Status                        string          `json:"status,omitempty"`
	Metadata                      json.RawMessage `json:"metadata,omitempty"`
}

type RegisterCapabilityEndpointInput struct {
	ProviderRef                 string          `json:"provider_ref"`
	CapabilityClassRef          string          `json:"capability_class_ref"`
	EndpointName                string          `json:"endpoint_name"`
	CompactAddress              string          `json:"compact_address"`
	Form                        string          `json:"form"`
	InputSchemaJSON             json.RawMessage `json:"input_schema_json,omitempty"`
	OutputSchemaJSON            json.RawMessage `json:"output_schema_json,omitempty"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	SideEffectsJSON             json.RawMessage `json:"side_effects_json,omitempty"`
	PolicyRequirementsJSON      json.RawMessage `json:"policy_requirements_json,omitempty"`
	CredentialRequirementsJSON  json.RawMessage `json:"credential_requirements_json,omitempty"`
	ApprovalRequirementsJSON    json.RawMessage `json:"approval_requirements_json,omitempty"`
	JobBehaviorJSON             json.RawMessage `json:"job_behavior_json,omitempty"`
	SessionBehaviorJSON         json.RawMessage `json:"session_behavior_json,omitempty"`
	StreamBehaviorJSON          json.RawMessage `json:"stream_behavior_json,omitempty"`
	LeaseBehaviorJSON           json.RawMessage `json:"lease_behavior_json,omitempty"`
	Status                      string          `json:"status,omitempty"`
	Metadata                    json.RawMessage `json:"metadata,omitempty"`
}

type RegisterEndpointVersionInput struct {
	CapabilityEndpointRef       string          `json:"capability_endpoint_ref"`
	VersionLabel                string          `json:"version_label"`
	ImplementationHash          string          `json:"implementation_hash,omitempty"`
	ManifestJSON                json.RawMessage `json:"manifest_json,omitempty"`
	InputSchemaJSON             json.RawMessage `json:"input_schema_json,omitempty"`
	OutputSchemaJSON            json.RawMessage `json:"output_schema_json,omitempty"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	PolicyRequirementsJSON      json.RawMessage `json:"policy_requirements_json,omitempty"`
	CredentialRequirementsJSON  json.RawMessage `json:"credential_requirements_json,omitempty"`
	ApprovalRequirementsJSON    json.RawMessage `json:"approval_requirements_json,omitempty"`
	Status                      string          `json:"status,omitempty"`
	ApprovedByActorID           string          `json:"approved_by_actor_id,omitempty"`
	ApprovedAt                  *time.Time      `json:"approved_at,omitempty"`
	Metadata                    json.RawMessage `json:"metadata,omitempty"`
}

type RegisterUsageDocumentInput struct {
	TargetKind           string          `json:"target_kind"`
	TargetID             string          `json:"target_id"`
	TargetAddress        string          `json:"target_address,omitempty"`
	Title                string          `json:"title"`
	VersionLabel         string          `json:"version_label,omitempty"`
	BodyFormat           string          `json:"body_format,omitempty"`
	Body                 string          `json:"body"`
	SectionMapJSON       json.RawMessage `json:"section_map_json,omitempty"`
	VisibilityPolicyJSON json.RawMessage `json:"visibility_policy_json,omitempty"`
	ReviewStatus         string          `json:"review_status,omitempty"`
	ContentHash          string          `json:"content_hash,omitempty"`
	SourceKind           string          `json:"source_kind,omitempty"`
	SourceRef            string          `json:"source_ref,omitempty"`
	ApprovedByActorID    string          `json:"approved_by_actor_id,omitempty"`
	ApprovedAt           *time.Time      `json:"approved_at,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

type CapabilityFilter struct {
	Limit              int
	ProviderRef        string
	ClassRef           string
	NodeRef            string
	ScopeRef           string
	ProjectRef         string
	Form               string
	Status             string
	Risk               string
	AuthorizationLevel int
}

type CapabilitySearchInput struct {
	Query       string `json:"query"`
	Limit       int    `json:"limit,omitempty"`
	ProviderRef string `json:"provider_ref,omitempty"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	Form        string `json:"form,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (s Service) RegisterProvider(ctx context.Context, req requestctx.Context, input RegisterProviderInput) (Provider, error) {
	normalized, err := normalizeProviderInput(ctx, s.DB, req, input)
	if err != nil {
		return Provider{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Provider{}, err
	}
	defer tx.Rollback()

	provider, err := insertProviderTx(ctx, tx, req, normalized)
	if err != nil {
		return Provider{}, err
	}
	if _, err := appendProviderRegisteredEvent(ctx, tx, req, provider); err != nil {
		return Provider{}, err
	}
	if err := tx.Commit(); err != nil {
		return Provider{}, err
	}
	return provider, nil
}

func (s Service) EnsureProvider(ctx context.Context, req requestctx.Context, input RegisterProviderInput) (Provider, bool, error) {
	normalized, err := normalizeProviderInput(ctx, s.DB, req, input)
	if err != nil {
		return Provider{}, false, err
	}
	var existingID string
	err = s.DB.QueryRowContext(ctx, `
		SELECT provider_id
		FROM capabilities.providers
		WHERE node_id = $1 AND provider_key = $2
	`, normalized.NodeRef, normalized.ProviderKey).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		provider, err := s.RegisterProvider(ctx, req, normalized)
		return provider, true, err
	}
	if err != nil {
		return Provider{}, false, err
	}
	var provider Provider
	err = s.DB.QueryRowContext(ctx, `
		UPDATE capabilities.providers
		SET compact_address = $2,
		    display_name = $3,
		    description = $4,
		    provider_type = $5,
		    scope_id = $6,
		    version = $7,
		    status = $8,
		    runtime_profile_json = $9,
		    documentation_refs_json = $10,
		    package_ref = nullif($11, ''),
		    metadata = $12,
		    updated_at = now()
		WHERE provider_id = $1
		RETURNING `+providerColumns(),
		existingID,
		normalized.CompactAddress,
		normalized.DisplayName,
		normalized.Description,
		normalized.ProviderType,
		normalized.ScopeRef,
		normalized.Version,
		normalized.Status,
		normalized.RuntimeProfileJSON,
		normalized.DocumentationRefsJSON,
		normalized.PackageRef,
		normalized.Metadata,
	).Scan(providerScanDest(&provider)...)
	if err != nil {
		return Provider{}, false, err
	}
	return normalizeScannedProvider(provider), false, nil
}

func (s Service) UpsertProviderHealth(ctx context.Context, req requestctx.Context, providerRef string, input ProviderHealthInput) (ProviderHealth, error) {
	providerID, err := s.ResolveProviderRef(ctx, providerRef)
	if err != nil {
		return ProviderHealth{}, err
	}
	normalized, err := normalizeProviderHealthInput(input)
	if err != nil {
		return ProviderHealth{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProviderHealth{}, err
	}
	defer tx.Rollback()

	old, found, err := getProviderHealthTx(ctx, tx, providerID)
	if err != nil {
		return ProviderHealth{}, err
	}

	health, _, err := upsertProviderHealthTx(ctx, tx, providerID, normalized)
	if err != nil {
		return ProviderHealth{}, err
	}
	if !found || changedProviderHealth(old, health) {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeProviderHealthUpdated,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "provider",
			TargetID:   providerID,
			Status:     health.HealthStatus,
			Result:     "ok",
			Payload: map[string]any{
				"provider_id":         providerID,
				"health_status":       health.HealthStatus,
				"availability_status": health.AvailabilityStatus,
				"created":             !found,
			},
			VisibilityClass: "internal",
		}); err != nil {
			return ProviderHealth{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ProviderHealth{}, err
	}
	return health, nil
}

func (s Service) ListProviders(ctx context.Context, filter ProviderFilter) ([]ProviderListItem, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		return nil, fmt.Errorf("provider offset must not be negative")
	}

	query := providerListSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.NodeRef) != "" {
		nodeID, err := resolveNodeRef(ctx, s.DB, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		add("p.node_id =", nodeID)
	}
	if strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := resolveScopeRef(ctx, s.DB, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("p.scope_id =", scopeID)
	}
	if strings.TrimSpace(filter.ProjectRef) != "" {
		scopeID, err := resolveProjectScopeRef(ctx, s.DB, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		add("p.scope_id =", scopeID)
	}
	if strings.TrimSpace(filter.ProviderType) != "" {
		add("p.provider_type =", strings.TrimSpace(filter.ProviderType))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("p.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.Health) != "" {
		add("COALESCE(h.health_status, 'unknown') =", strings.TrimSpace(filter.Health))
	}
	if filter.RequireActiveEndpoint {
		query += `
			AND EXISTS (
				SELECT 1
				FROM capabilities.capability_endpoints e
				WHERE e.provider_id = p.provider_id
				  AND e.status = 'active'
			)`
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY p.compact_address LIMIT $%d", len(args))
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []ProviderListItem{}
	for rows.Next() {
		item, err := scanProviderListItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) InspectProvider(ctx context.Context, providerRef string) (ProviderInspection, error) {
	providerID, err := s.ResolveProviderRef(ctx, providerRef)
	if err != nil {
		return ProviderInspection{}, err
	}
	provider, err := getProvider(ctx, s.DB, providerID)
	if err != nil {
		return ProviderInspection{}, err
	}
	health, found, err := getProviderHealth(ctx, s.DB, providerID)
	if err != nil {
		return ProviderInspection{}, err
	}
	endpoints, err := listProviderEndpoints(ctx, s.DB, providerID)
	if err != nil {
		return ProviderInspection{}, err
	}
	docs, err := listUsageDocumentsForTarget(ctx, s.DB, UsageTargetKindProvider, providerID)
	if err != nil {
		return ProviderInspection{}, err
	}
	var healthPtr *ProviderHealth
	if found {
		healthPtr = &health
	}
	return ProviderInspection{
		Provider:       provider,
		Health:         healthPtr,
		Endpoints:      endpoints,
		UsageDocuments: docs,
	}, nil
}

func (s Service) ResolveProviderRef(ctx context.Context, providerRef string) (string, error) {
	return resolveProviderRef(ctx, s.DB, providerRef)
}

func (s Service) RegisterCapabilityClass(ctx context.Context, req requestctx.Context, input RegisterCapabilityClassInput) (CapabilityClass, error) {
	normalized, err := normalizeCapabilityClassInput(input)
	if err != nil {
		return CapabilityClass{}, err
	}
	var class CapabilityClass
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO capabilities.capability_classes (
			capability_class_id, namespace, name, version, display_name,
			description, form, input_schema_json, output_schema_json,
			default_risk_level, default_policy_requirements_json, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
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
		normalized.Status,
		normalized.Metadata,
	).Scan(capabilityClassScanDest(&class)...)
	if err != nil {
		return CapabilityClass{}, err
	}
	_ = req
	return normalizeScannedCapabilityClass(class), nil
}

func (s Service) EnsureCapabilityClass(ctx context.Context, req requestctx.Context, input RegisterCapabilityClassInput) (CapabilityClass, bool, error) {
	normalized, err := normalizeCapabilityClassInput(input)
	if err != nil {
		return CapabilityClass{}, false, err
	}
	var existingID string
	err = s.DB.QueryRowContext(ctx, `
		SELECT capability_class_id
		FROM capabilities.capability_classes
		WHERE namespace = $1 AND name = $2 AND version = $3
	`, normalized.Namespace, normalized.Name, normalized.Version).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		class, err := s.RegisterCapabilityClass(ctx, req, normalized)
		return class, true, err
	}
	if err != nil {
		return CapabilityClass{}, false, err
	}
	var class CapabilityClass
	err = s.DB.QueryRowContext(ctx, `
		UPDATE capabilities.capability_classes
		SET display_name = $2,
		    description = $3,
		    form = $4,
		    input_schema_json = $5,
		    output_schema_json = $6,
		    default_risk_level = $7,
		    default_policy_requirements_json = $8,
		    status = $9,
		    metadata = $10,
		    updated_at = now(),
		    deprecated_at = CASE WHEN $9 = 'deprecated' THEN COALESCE(deprecated_at, now()) ELSE NULL END
		WHERE capability_class_id = $1
		RETURNING `+capabilityClassColumns(),
		existingID,
		normalized.DisplayName,
		normalized.Description,
		normalized.Form,
		normalized.InputSchemaJSON,
		normalized.OutputSchemaJSON,
		normalized.DefaultRiskLevel,
		normalized.DefaultPolicyRequirementsJSON,
		normalized.Status,
		normalized.Metadata,
	).Scan(capabilityClassScanDest(&class)...)
	if err != nil {
		return CapabilityClass{}, false, err
	}
	return normalizeScannedCapabilityClass(class), false, nil
}

func (s Service) RegisterCapabilityEndpoint(ctx context.Context, req requestctx.Context, input RegisterCapabilityEndpointInput) (CapabilityEndpoint, error) {
	normalized, err := normalizeCapabilityEndpointInput(ctx, s.DB, input)
	if err != nil {
		return CapabilityEndpoint{}, err
	}
	var endpoint CapabilityEndpoint
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO capabilities.capability_endpoints (
			capability_endpoint_id, provider_id, capability_class_id, endpoint_name,
			compact_address, form, input_schema_json, output_schema_json, risk_level,
			execution_authorization_level, side_effects_json, policy_requirements_json,
			credential_requirements_json, approval_requirements_json, job_behavior_json,
			session_behavior_json, stream_behavior_json, lease_behavior_json, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
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
		normalized.Status,
		normalized.Metadata,
	).Scan(capabilityEndpointScanDest(&endpoint)...)
	if err != nil {
		return CapabilityEndpoint{}, err
	}
	_ = req
	return normalizeScannedCapabilityEndpoint(endpoint), nil
}

func (s Service) EnsureCapabilityEndpoint(ctx context.Context, req requestctx.Context, input RegisterCapabilityEndpointInput) (CapabilityEndpoint, bool, error) {
	normalized, err := normalizeCapabilityEndpointInput(ctx, s.DB, input)
	if err != nil {
		return CapabilityEndpoint{}, false, err
	}
	var existingID string
	err = s.DB.QueryRowContext(ctx, `
		SELECT capability_endpoint_id
		FROM capabilities.capability_endpoints
		WHERE provider_id = $1 AND endpoint_name = $2
	`, normalized.ProviderRef, normalized.EndpointName).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		endpoint, err := s.RegisterCapabilityEndpoint(ctx, req, normalized)
		return endpoint, true, err
	}
	if err != nil {
		return CapabilityEndpoint{}, false, err
	}
	var endpoint CapabilityEndpoint
	err = s.DB.QueryRowContext(ctx, `
		UPDATE capabilities.capability_endpoints
		SET capability_class_id = $2,
		    compact_address = $3,
		    form = $4,
		    input_schema_json = $5,
		    output_schema_json = $6,
		    risk_level = $7,
		    execution_authorization_level = $8,
		    side_effects_json = $9,
		    policy_requirements_json = $10,
		    credential_requirements_json = $11,
		    approval_requirements_json = $12,
		    job_behavior_json = $13,
		    session_behavior_json = $14,
		    stream_behavior_json = $15,
		    lease_behavior_json = $16,
		    status = $17,
		    metadata = $18,
		    updated_at = now(),
		    deprecated_at = CASE WHEN $17 = 'deprecated' THEN COALESCE(deprecated_at, now()) ELSE NULL END
		WHERE capability_endpoint_id = $1
		RETURNING `+capabilityEndpointColumns(),
		existingID,
		normalized.CapabilityClassRef,
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
		normalized.Status,
		normalized.Metadata,
	).Scan(capabilityEndpointScanDest(&endpoint)...)
	if err != nil {
		return CapabilityEndpoint{}, false, err
	}
	return normalizeScannedCapabilityEndpoint(endpoint), false, nil
}

func (s Service) RegisterEndpointVersion(ctx context.Context, req requestctx.Context, input RegisterEndpointVersionInput) (EndpointVersion, error) {
	normalized, err := normalizeEndpointVersionInput(ctx, s.DB, input)
	if err != nil {
		return EndpointVersion{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return EndpointVersion{}, err
	}
	defer tx.Rollback()

	var version EndpointVersion
	err = tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.endpoint_versions (
			capability_endpoint_version_id, capability_endpoint_id, version_label,
			implementation_hash, manifest_json, input_schema_json, output_schema_json,
			risk_level, execution_authorization_level, policy_requirements_json,
			credential_requirements_json, approval_requirements_json, status,
			approved_by_actor_id, approved_at, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8, $9, $10, $11, $12, $13, nullif($14, ''), $15, $16)
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
		normalized.Status,
		normalized.ApprovedByActorID,
		normalized.ApprovedAt,
		normalized.Metadata,
	).Scan(endpointVersionScanDest(&version)...)
	if err != nil {
		return EndpointVersion{}, err
	}
	if version.Status == EndpointVersionStatusActive {
		if _, err := tx.ExecContext(ctx, `
			UPDATE capabilities.capability_endpoints
			SET active_endpoint_version_id = $1,
			    input_schema_json = $2,
			    output_schema_json = $3,
			    risk_level = $4,
			    execution_authorization_level = $5,
			    policy_requirements_json = $6,
			    credential_requirements_json = $7,
			    approval_requirements_json = $8,
			    updated_at = now()
			WHERE capability_endpoint_id = $9
		`,
			version.CapabilityEndpointVersionID,
			version.InputSchemaJSON,
			version.OutputSchemaJSON,
			version.RiskLevel,
			version.ExecutionAuthorizationLevel,
			version.PolicyRequirementsJSON,
			version.CredentialRequirementsJSON,
			version.ApprovalRequirementsJSON,
			version.CapabilityEndpointID,
		); err != nil {
			return EndpointVersion{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return EndpointVersion{}, err
	}
	_ = req
	return normalizeScannedEndpointVersion(version), nil
}

func (s Service) EnsureEndpointVersion(ctx context.Context, req requestctx.Context, input RegisterEndpointVersionInput) (EndpointVersion, bool, error) {
	normalized, err := normalizeEndpointVersionInput(ctx, s.DB, input)
	if err != nil {
		return EndpointVersion{}, false, err
	}
	var existingID string
	err = s.DB.QueryRowContext(ctx, `
		SELECT capability_endpoint_version_id
		FROM capabilities.endpoint_versions
		WHERE capability_endpoint_id = $1 AND version_label = $2
	`, normalized.CapabilityEndpointRef, normalized.VersionLabel).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		version, err := s.RegisterEndpointVersion(ctx, req, normalized)
		return version, true, err
	}
	if err != nil {
		return EndpointVersion{}, false, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return EndpointVersion{}, false, err
	}
	defer tx.Rollback()

	var version EndpointVersion
	err = tx.QueryRowContext(ctx, `
		UPDATE capabilities.endpoint_versions
		SET implementation_hash = nullif($2, ''),
		    manifest_json = $3,
		    input_schema_json = $4,
		    output_schema_json = $5,
		    risk_level = $6,
		    execution_authorization_level = $7,
		    policy_requirements_json = $8,
		    credential_requirements_json = $9,
		    approval_requirements_json = $10,
		    status = $11,
		    approved_by_actor_id = nullif($12, ''),
		    approved_at = $13,
		    metadata = $14,
		    deprecated_at = CASE WHEN $11 = 'deprecated' THEN COALESCE(deprecated_at, now()) ELSE NULL END
		WHERE capability_endpoint_version_id = $1
		RETURNING `+endpointVersionColumns(),
		existingID,
		normalized.ImplementationHash,
		normalized.ManifestJSON,
		normalized.InputSchemaJSON,
		normalized.OutputSchemaJSON,
		normalized.RiskLevel,
		normalized.ExecutionAuthorizationLevel,
		normalized.PolicyRequirementsJSON,
		normalized.CredentialRequirementsJSON,
		normalized.ApprovalRequirementsJSON,
		normalized.Status,
		normalized.ApprovedByActorID,
		normalized.ApprovedAt,
		normalized.Metadata,
	).Scan(endpointVersionScanDest(&version)...)
	if err != nil {
		return EndpointVersion{}, false, err
	}
	if version.Status == EndpointVersionStatusActive {
		if _, err := tx.ExecContext(ctx, `
			UPDATE capabilities.capability_endpoints
			SET active_endpoint_version_id = $1,
			    input_schema_json = $2,
			    output_schema_json = $3,
			    risk_level = $4,
			    execution_authorization_level = $5,
			    policy_requirements_json = $6,
			    credential_requirements_json = $7,
			    approval_requirements_json = $8,
			    updated_at = now()
			WHERE capability_endpoint_id = $9
		`,
			version.CapabilityEndpointVersionID,
			version.InputSchemaJSON,
			version.OutputSchemaJSON,
			version.RiskLevel,
			version.ExecutionAuthorizationLevel,
			version.PolicyRequirementsJSON,
			version.CredentialRequirementsJSON,
			version.ApprovalRequirementsJSON,
			version.CapabilityEndpointID,
		); err != nil {
			return EndpointVersion{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return EndpointVersion{}, false, err
	}
	_ = req
	return normalizeScannedEndpointVersion(version), false, nil
}

func (s Service) RegisterRuntimeBinding(ctx context.Context, req requestctx.Context, input RegisterRuntimeBindingInput) (RuntimeBindingInspection, error) {
	normalized, err := normalizeRuntimeBindingInput(ctx, s.DB, input)
	if err != nil {
		return RuntimeBindingInspection{}, err
	}
	binding, err := s.upsertRuntimeBinding(ctx, req, normalized)
	if err != nil {
		return RuntimeBindingInspection{}, err
	}
	return s.InspectRuntimeBinding(ctx, binding.RuntimeBindingID)
}

func (s Service) DisableRuntimeBinding(ctx context.Context, req requestctx.Context, bindingRef string, metadata json.RawMessage) (EndpointRuntimeBinding, error) {
	bindingID, err := s.resolveRuntimeBindingID(ctx, bindingRef)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	metadata = normalizeJSONObject(metadata)
	if err := validateJSONObject("metadata", metadata); err != nil {
		return EndpointRuntimeBinding{}, err
	}
	var binding EndpointRuntimeBinding
	err = s.DB.QueryRowContext(ctx, `
		UPDATE capabilities.endpoint_runtime_bindings
		SET status = $2,
		    disabled_at = COALESCE(disabled_at, now()),
		    metadata = metadata || $3::jsonb,
		    updated_at = now()
		WHERE runtime_binding_id = $1
		RETURNING `+runtimeBindingColumns(),
		bindingID,
		RuntimeBindingStatusDisabled,
		metadata,
	).Scan(runtimeBindingScanDest(&binding)...)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	_ = req
	return normalizeScannedRuntimeBinding(binding), nil
}

func (s Service) resolveRuntimeBindingID(ctx context.Context, bindingRef string) (string, error) {
	bindingRef = strings.TrimSpace(bindingRef)
	if bindingRef == "" {
		return "", fmt.Errorf("runtime binding ref is required")
	}
	if strings.HasPrefix(bindingRef, ids.CapabilityRuntimeBindingPrefix+"_") {
		var bindingID string
		err := s.DB.QueryRowContext(ctx, `
			SELECT runtime_binding_id
			FROM capabilities.endpoint_runtime_bindings
			WHERE runtime_binding_id = $1
		`, bindingRef).Scan(&bindingID)
		return bindingID, err
	}
	inspection, err := s.InspectRuntimeBinding(ctx, bindingRef)
	if err != nil {
		return "", err
	}
	return inspection.Binding.RuntimeBindingID, nil
}

func (s Service) ListRuntimeBindings(ctx context.Context, filter RuntimeBindingFilter) ([]RuntimeBindingInspection, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := runtimeBindingInspectionSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if ref := strings.TrimSpace(filter.CapabilityRef); ref != "" {
		endpointID, err := resolveEndpointRef(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("e.capability_endpoint_id =", endpointID)
	}
	if ref := strings.TrimSpace(filter.EndpointVersionRef); ref != "" {
		versionID, err := resolveEndpointVersionRef(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("b.capability_endpoint_version_id =", versionID)
	}
	if ref := strings.TrimSpace(filter.ProviderRef); ref != "" {
		providerID, err := resolveProviderRef(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("p.provider_id =", providerID)
	}
	if runtimeKind := strings.TrimSpace(filter.RuntimeKind); runtimeKind != "" {
		if !ValidRuntimeKind(runtimeKind) {
			return nil, fmt.Errorf("unsupported runtime_kind filter: %s", runtimeKind)
		}
		add("b.runtime_kind =", runtimeKind)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		if !ValidRuntimeBindingStatus(status) {
			return nil, fmt.Errorf("unsupported runtime binding status filter: %s", status)
		}
		add("b.status =", status)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY b.created_at DESC, e.compact_address LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []RuntimeBindingInspection{}
	for rows.Next() {
		item, err := scanRuntimeBindingInspection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) InspectRuntimeBinding(ctx context.Context, ref string) (RuntimeBindingInspection, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RuntimeBindingInspection{}, fmt.Errorf("runtime binding ref is required")
	}
	query := runtimeBindingInspectionSelectSQL()
	args := []any{}
	switch {
	case strings.HasPrefix(ref, ids.CapabilityRuntimeBindingPrefix+"_"):
		args = append(args, ref)
		query += ` WHERE b.runtime_binding_id = $1`
	case strings.HasPrefix(ref, ids.CapabilityEndpointVersionPrefix+"_"):
		versionID, err := resolveEndpointVersionRef(ctx, s.DB, ref)
		if err != nil {
			return RuntimeBindingInspection{}, err
		}
		args = append(args, versionID)
		query += ` WHERE b.capability_endpoint_version_id = $1`
	default:
		versionID, err := activeEndpointVersionIDForEndpointRef(ctx, s.DB, ref)
		if err != nil {
			return RuntimeBindingInspection{}, err
		}
		args = append(args, versionID)
		query += ` WHERE b.capability_endpoint_version_id = $1`
	}
	return scanRuntimeBindingInspection(s.DB.QueryRowContext(ctx, query, args...))
}

func (s Service) ActiveRuntimeBindingForEndpoint(ctx context.Context, endpointRef string) (EndpointRuntimeBinding, error) {
	versionID, err := activeEndpointVersionIDForEndpointRef(ctx, s.DB, endpointRef)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	return s.ActiveRuntimeBindingForEndpointVersion(ctx, versionID)
}

func (s Service) ActiveRuntimeBindingForEndpointVersion(ctx context.Context, versionRef string) (EndpointRuntimeBinding, error) {
	versionID, err := resolveEndpointVersionRef(ctx, s.DB, versionRef)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	binding, err := getRuntimeBindingForEndpointVersion(ctx, s.DB, versionID)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	return binding, nil
}

func (s Service) RegisterUsageDocument(ctx context.Context, req requestctx.Context, input RegisterUsageDocumentInput) (UsageDocument, error) {
	normalized, err := normalizeUsageDocumentInput(ctx, s.DB, input)
	if err != nil {
		return UsageDocument{}, err
	}

	var doc UsageDocument
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO capabilities.usage_documents (
			capability_usage_document_id, target_kind, target_id, target_address,
			title, version_label, body_format, body, section_map_json,
			visibility_policy_json, review_status, content_hash, source_kind,
			source_ref, created_by_actor_id, approved_by_actor_id, approved_at, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8, $9, $10, $11, $12, $13, nullif($14, ''), $15, nullif($16, ''), $17, $18)
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
		normalized.ReviewStatus,
		normalized.ContentHash,
		normalized.SourceKind,
		normalized.SourceRef,
		req.ActorID,
		normalized.ApprovedByActorID,
		normalized.ApprovedAt,
		normalized.Metadata,
	).Scan(usageDocumentScanDest(&doc)...)
	if err != nil {
		return UsageDocument{}, err
	}
	return normalizeScannedUsageDocument(doc), nil
}

func (s Service) EnsureUsageDocument(ctx context.Context, req requestctx.Context, input RegisterUsageDocumentInput) (UsageDocument, bool, error) {
	normalized, err := normalizeUsageDocumentInput(ctx, s.DB, input)
	if err != nil {
		return UsageDocument{}, false, err
	}

	var existingID string
	err = s.DB.QueryRowContext(ctx, `
		SELECT capability_usage_document_id
		FROM capabilities.usage_documents
		WHERE target_kind = $1
		  AND target_id = $2
		  AND source_kind = $3
		  AND source_ref IS NOT DISTINCT FROM nullif($4, '')
		ORDER BY updated_at DESC
		LIMIT 1
	`, normalized.TargetKind, normalized.TargetID, normalized.SourceKind, normalized.SourceRef).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		doc, err := s.RegisterUsageDocument(ctx, req, normalized)
		return doc, true, err
	}
	if err != nil {
		return UsageDocument{}, false, err
	}

	var doc UsageDocument
	err = s.DB.QueryRowContext(ctx, `
		UPDATE capabilities.usage_documents
		SET target_address = nullif($2, ''),
		    title = $3,
		    version_label = $4,
		    body_format = $5,
		    body = $6,
		    section_map_json = $7,
		    visibility_policy_json = $8,
		    review_status = $9,
		    content_hash = $10,
		    approved_by_actor_id = nullif($11, ''),
		    approved_at = $12,
		    deprecated_at = NULL,
		    metadata = $13,
		    updated_at = now()
		WHERE capability_usage_document_id = $1
		RETURNING `+usageDocumentColumns(),
		existingID,
		normalized.TargetAddress,
		normalized.Title,
		normalized.VersionLabel,
		normalized.BodyFormat,
		normalized.Body,
		normalized.SectionMapJSON,
		normalized.VisibilityPolicyJSON,
		normalized.ReviewStatus,
		normalized.ContentHash,
		normalized.ApprovedByActorID,
		normalized.ApprovedAt,
		normalized.Metadata,
	).Scan(usageDocumentScanDest(&doc)...)
	if err != nil {
		return UsageDocument{}, false, err
	}
	_ = req
	return normalizeScannedUsageDocument(doc), false, nil
}

func (s Service) ListCapabilities(ctx context.Context, filter CapabilityFilter) ([]CapabilityListItem, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := capabilityListSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.ProviderRef) != "" {
		providerID, err := s.ResolveProviderRef(ctx, filter.ProviderRef)
		if err != nil {
			return nil, err
		}
		add("e.provider_id =", providerID)
	}
	if strings.TrimSpace(filter.ClassRef) != "" {
		classID, err := resolveCapabilityClassRef(ctx, s.DB, filter.ClassRef)
		if err != nil {
			return nil, err
		}
		add("e.capability_class_id =", classID)
	}
	if strings.TrimSpace(filter.NodeRef) != "" {
		nodeID, err := resolveNodeRef(ctx, s.DB, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		add("p.node_id =", nodeID)
	}
	if strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := resolveScopeRef(ctx, s.DB, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("p.scope_id =", scopeID)
	}
	if strings.TrimSpace(filter.ProjectRef) != "" {
		scopeID, err := resolveProjectScopeRef(ctx, s.DB, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		add("p.scope_id =", scopeID)
	}
	if strings.TrimSpace(filter.Form) != "" {
		add("e.form =", strings.TrimSpace(filter.Form))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("e.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.Status) == EndpointStatusActive {
		add("p.status =", ProviderStatusActive)
	}
	if strings.TrimSpace(filter.Risk) != "" {
		add("e.risk_level =", strings.TrimSpace(filter.Risk))
	}
	if filter.AuthorizationLevel > 0 {
		add("e.execution_authorization_level =", filter.AuthorizationLevel)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY e.compact_address LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []CapabilityListItem{}
	for rows.Next() {
		item, err := scanCapabilityListItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) InspectCapability(ctx context.Context, capabilityRef string) (CapabilityInspection, error) {
	endpointID, err := s.ResolveEndpointRef(ctx, capabilityRef)
	if err != nil {
		return CapabilityInspection{}, err
	}
	endpoint, err := getCapabilityEndpoint(ctx, s.DB, endpointID)
	if err != nil {
		return CapabilityInspection{}, err
	}
	provider, err := getProvider(ctx, s.DB, endpoint.ProviderID)
	if err != nil {
		return CapabilityInspection{}, err
	}
	class, err := getCapabilityClass(ctx, s.DB, endpoint.CapabilityClassID)
	if err != nil {
		return CapabilityInspection{}, err
	}
	health, found, err := getProviderHealth(ctx, s.DB, provider.ProviderID)
	if err != nil {
		return CapabilityInspection{}, err
	}
	var version *EndpointVersion
	var runtimeBinding *EndpointRuntimeBinding
	if endpoint.ActiveEndpointVersionID != nil {
		active, err := getEndpointVersion(ctx, s.DB, *endpoint.ActiveEndpointVersionID)
		if err != nil {
			return CapabilityInspection{}, err
		}
		version = &active
		binding, err := getRuntimeBindingForEndpointVersion(ctx, s.DB, active.CapabilityEndpointVersionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return CapabilityInspection{}, err
		}
		if err == nil {
			runtimeBinding = &binding
		}
	}
	docs, err := listUsageDocumentsForTarget(ctx, s.DB, UsageTargetKindCapabilityEndpoint, endpointID)
	if err != nil {
		return CapabilityInspection{}, err
	}
	var healthPtr *ProviderHealth
	if found {
		healthPtr = &health
	}
	return CapabilityInspection{
		Endpoint:       endpoint,
		Class:          class,
		Provider:       provider,
		ProviderHealth: healthPtr,
		ActiveVersion:  version,
		RuntimeBinding: runtimeBinding,
		UsageDocuments: docs,
	}, nil
}

func (s Service) DisableCapabilityEndpoint(ctx context.Context, req requestctx.Context, endpointRef string, metadata json.RawMessage) (CapabilityEndpoint, error) {
	endpointID, err := s.ResolveEndpointRef(ctx, endpointRef)
	if err != nil {
		return CapabilityEndpoint{}, err
	}
	metadata = normalizeJSONObject(metadata)
	if err := validateJSONObject("metadata", metadata); err != nil {
		return CapabilityEndpoint{}, err
	}
	var endpoint CapabilityEndpoint
	err = s.DB.QueryRowContext(ctx, `
		UPDATE capabilities.capability_endpoints
		SET status = $2,
		    metadata = metadata || $3::jsonb,
		    updated_at = now()
		WHERE capability_endpoint_id = $1
		RETURNING `+capabilityEndpointColumns(),
		endpointID,
		EndpointStatusDisabled,
		metadata,
	).Scan(capabilityEndpointScanDest(&endpoint)...)
	if err != nil {
		return CapabilityEndpoint{}, err
	}
	_ = req
	return normalizeScannedCapabilityEndpoint(endpoint), nil
}

func (s Service) InspectUsageDocuments(ctx context.Context, capabilityRef string) ([]UsageDocument, error) {
	endpointID, err := s.ResolveEndpointRef(ctx, capabilityRef)
	if err != nil {
		return nil, err
	}
	return listUsageDocumentsForTarget(ctx, s.DB, UsageTargetKindCapabilityEndpoint, endpointID)
}

func (s Service) ResolveEndpointRef(ctx context.Context, capabilityRef string) (string, error) {
	return resolveEndpointRef(ctx, s.DB, capabilityRef)
}

func (s Service) SearchCapabilities(ctx context.Context, input CapabilitySearchInput) ([]CapabilityCandidate, error) {
	if strings.TrimSpace(input.Query) == "" {
		return []CapabilityCandidate{}, nil
	}
	if input.Limit <= 0 || input.Limit > 40 {
		input.Limit = 10
	}

	queryText := strings.TrimSpace(input.Query)
	searchPattern := "%" + queryText + "%"
	endpointStatus := defaultString(input.Status, EndpointStatusActive)
	if endpointStatus == EndpointStatusDisabled || endpointStatus == EndpointStatusRevoked || endpointStatus == EndpointStatusDeprecated {
		return []CapabilityCandidate{}, nil
	}
	args := []any{searchPattern, input.Limit, endpointStatus}
	where := `
		WHERE e.status = $3
		  AND p.status = 'active'
		  AND (
		      e.compact_address ILIKE $1
		   OR e.endpoint_name ILIKE $1
		   OR c.namespace ILIKE $1
		   OR c.name ILIKE $1
		   OR c.display_name ILIKE $1
		   OR c.description ILIKE $1
		   OR p.compact_address ILIKE $1
		   OR p.display_name ILIKE $1
		   OR p.provider_type ILIKE $1
		   OR doc.title ILIKE $1
		   OR doc.body ILIKE $1
		  )
	`
	add := func(condition string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(input.ProviderRef) != "" {
		providerID, err := s.ResolveProviderRef(ctx, input.ProviderRef)
		if err != nil {
			return nil, err
		}
		add("e.provider_id =", providerID)
	}
	if strings.TrimSpace(input.ScopeRef) != "" {
		scopeID, err := resolveScopeRef(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("p.scope_id =", scopeID)
	}
	if strings.TrimSpace(input.Form) != "" {
		add("e.form =", strings.TrimSpace(input.Form))
	}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT
			e.capability_endpoint_id,
			e.compact_address,
			e.provider_id,
			p.compact_address,
			COALESCE(h.health_status, 'unknown'),
			e.capability_class_id,
			c.namespace || '.' || c.name,
			c.display_name,
			c.description,
			COALESCE(
				NULLIF(doc.title || ': ' || left(regexp_replace(doc.body, '[[:space:]]+', ' ', 'g'), 220), ': '),
				c.description,
				''
			),
			e.form,
			e.risk_level,
			e.execution_authorization_level,
			CASE WHEN doc.capability_usage_document_id IS NOT NULL THEN ARRAY['usage_document']::text[] ELSE ARRAY[]::text[] END,
			(
				CASE WHEN e.compact_address ILIKE $1 THEN 100 ELSE 0 END +
				CASE WHEN e.endpoint_name ILIKE $1 THEN 80 ELSE 0 END +
				CASE WHEN c.namespace || '.' || c.name ILIKE $1 THEN 70 ELSE 0 END +
				CASE WHEN c.display_name ILIKE $1 THEN 60 ELSE 0 END +
				CASE WHEN c.description ILIKE $1 THEN 45 ELSE 0 END +
				CASE WHEN p.compact_address ILIKE $1 THEN 35 ELSE 0 END +
				CASE WHEN p.display_name ILIKE $1 THEN 30 ELSE 0 END +
				CASE WHEN p.provider_type ILIKE $1 THEN 25 ELSE 0 END +
				CASE WHEN doc.title ILIKE $1 THEN 65 ELSE 0 END +
				CASE WHEN doc.body ILIKE $1 THEN 40 ELSE 0 END +
				CASE WHEN p.compact_address LIKE 'main@%' THEN 5 ELSE 0 END +
				CASE WHEN COALESCE(h.health_status, 'unknown') = 'ok' THEN 5 ELSE 0 END
			)::double precision AS score,
			CASE
				WHEN e.compact_address ILIKE $1 THEN ARRAY['address']::text[]
				WHEN c.namespace || '.' || c.name ILIKE $1 OR c.display_name ILIKE $1 THEN ARRAY['class']::text[]
				WHEN doc.title ILIKE $1 OR doc.body ILIKE $1 THEN ARRAY['usage_document']::text[]
				WHEN p.compact_address ILIKE $1 OR p.display_name ILIKE $1 OR p.provider_type ILIKE $1 THEN ARRAY['provider']::text[]
				ELSE ARRAY['metadata']::text[]
			END AS match_reasons,
			e.metadata
		FROM capabilities.capability_endpoints e
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN capabilities.capability_classes c ON c.capability_class_id = e.capability_class_id
		LEFT JOIN capabilities.provider_health h ON h.provider_id = p.provider_id
		LEFT JOIN LATERAL (
			SELECT ud.capability_usage_document_id, ud.title, ud.body
			FROM capabilities.usage_documents ud
			WHERE ud.target_kind = 'capability_endpoint'
			  AND ud.target_id = e.capability_endpoint_id
			  AND ud.review_status = 'approved'
			ORDER BY
				CASE WHEN ud.title ILIKE $1 THEN 0 WHEN ud.body ILIKE $1 THEN 1 ELSE 2 END,
				ud.updated_at DESC,
				ud.title
			LIMIT 1
		) doc ON true
	`+where+`
		ORDER BY score DESC, e.compact_address
		LIMIT $2
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := []CapabilityCandidate{}
	for rows.Next() {
		var candidate CapabilityCandidate
		if err := rows.Scan(
			&candidate.CapabilityEndpointID,
			&candidate.CompactAddress,
			&candidate.ProviderID,
			&candidate.ProviderAddress,
			&candidate.ProviderHealth,
			&candidate.CapabilityClassID,
			&candidate.ClassName,
			&candidate.DisplayName,
			&candidate.Description,
			&candidate.MatchedUseSummary,
			&candidate.Form,
			&candidate.RiskLevel,
			&candidate.ExecutionAuthorizationLevel,
			(*stringSlice)(&candidate.Tags),
			&candidate.Score,
			(*stringSlice)(&candidate.MatchReasons),
			&candidate.Metadata,
		); err != nil {
			return nil, err
		}
		candidate.Metadata = jsonOrDefault(candidate.Metadata, `{}`)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func normalizeProviderInput(ctx context.Context, q queryer, req requestctx.Context, input RegisterProviderInput) (RegisterProviderInput, error) {
	input.ProviderKey = strings.TrimSpace(input.ProviderKey)
	input.CompactAddress = strings.TrimSpace(input.CompactAddress)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.ProviderType = strings.TrimSpace(input.ProviderType)
	input.Status = defaultString(input.Status, ProviderStatusRegistered)
	input.Version = defaultString(input.Version, "0.1.0")
	if input.NodeRef == "" {
		input.NodeRef = req.OriginNodeID
	}
	if input.ScopeRef == "" {
		input.ScopeRef = req.ScopeID
	}
	if input.ProviderKey == "" {
		return input, fmt.Errorf("provider_key is required")
	}
	if input.DisplayName == "" {
		return input, fmt.Errorf("display_name is required")
	}
	if !ValidProviderType(input.ProviderType) {
		return input, fmt.Errorf("unsupported provider_type: %s", input.ProviderType)
	}
	if !ValidProviderStatus(input.Status) {
		return input, fmt.Errorf("unsupported provider status: %s", input.Status)
	}
	address, err := ParseProviderAddress(input.CompactAddress)
	if err != nil {
		return input, err
	}
	if address.ProviderKey != input.ProviderKey {
		return input, fmt.Errorf("provider_key %q does not match address %q", input.ProviderKey, input.CompactAddress)
	}
	nodeID, err := resolveNodeRef(ctx, q, input.NodeRef)
	if err != nil {
		return input, fmt.Errorf("resolve provider node: %w", err)
	}
	scopeID, err := resolveScopeRef(ctx, q, input.ScopeRef)
	if err != nil {
		return input, fmt.Errorf("resolve provider scope: %w", err)
	}
	input.NodeRef = nodeID
	input.ScopeRef = scopeID
	input.RuntimeProfileJSON = normalizeJSONObject(input.RuntimeProfileJSON)
	input.DocumentationRefsJSON = normalizeJSONArray(input.DocumentationRefsJSON)
	input.Metadata = normalizeJSONObject(input.Metadata)
	return input, nil
}

func normalizeProviderHealthInput(input ProviderHealthInput) (ProviderHealthInput, error) {
	input.HealthStatus = defaultString(input.HealthStatus, HealthStatusUnknown)
	input.AvailabilityStatus = defaultString(input.AvailabilityStatus, AvailabilityStatusUnknown)
	input.Message = strings.TrimSpace(input.Message)
	if !ValidHealthStatus(input.HealthStatus) {
		return input, fmt.Errorf("unsupported health_status: %s", input.HealthStatus)
	}
	if !ValidAvailabilityStatus(input.AvailabilityStatus) {
		return input, fmt.Errorf("unsupported availability_status: %s", input.AvailabilityStatus)
	}
	input.DetailsJSON = normalizeJSONObject(input.DetailsJSON)
	return input, nil
}

func normalizeCapabilityClassInput(input RegisterCapabilityClassInput) (RegisterCapabilityClassInput, error) {
	input.Namespace = strings.TrimSpace(input.Namespace)
	input.Name = strings.TrimSpace(input.Name)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Version = defaultString(input.Version, "0.1.0")
	input.Form = strings.TrimSpace(input.Form)
	input.DefaultRiskLevel = strings.TrimSpace(input.DefaultRiskLevel)
	input.Status = defaultString(input.Status, CapabilityClassStatusActive)
	if input.Namespace == "" || input.Name == "" || input.DisplayName == "" {
		return input, fmt.Errorf("namespace, name, and display_name are required")
	}
	if !ValidCapabilityForm(input.Form) {
		return input, fmt.Errorf("unsupported capability form: %s", input.Form)
	}
	if !ValidRiskLevel(input.DefaultRiskLevel) {
		return input, fmt.Errorf("unsupported risk level: %s", input.DefaultRiskLevel)
	}
	if !ValidCapabilityClassStatus(input.Status) {
		return input, fmt.Errorf("unsupported class status: %s", input.Status)
	}
	input.InputSchemaJSON = normalizeJSONObject(input.InputSchemaJSON)
	input.OutputSchemaJSON = normalizeJSONObject(input.OutputSchemaJSON)
	input.DefaultPolicyRequirementsJSON = normalizeJSONObject(input.DefaultPolicyRequirementsJSON)
	input.Metadata = normalizeJSONObject(input.Metadata)
	if err := ValidateSchemaJSON(input.InputSchemaJSON); err != nil {
		return input, fmt.Errorf("input_schema_json: %w", err)
	}
	if err := ValidateSchemaJSON(input.OutputSchemaJSON); err != nil {
		return input, fmt.Errorf("output_schema_json: %w", err)
	}
	return input, nil
}

func normalizeCapabilityEndpointInput(ctx context.Context, q queryer, input RegisterCapabilityEndpointInput) (RegisterCapabilityEndpointInput, error) {
	providerID, err := resolveProviderRef(ctx, q, input.ProviderRef)
	if err != nil {
		return input, fmt.Errorf("resolve provider: %w", err)
	}
	classID, err := resolveCapabilityClassRef(ctx, q, input.CapabilityClassRef)
	if err != nil {
		return input, fmt.Errorf("resolve capability class: %w", err)
	}
	provider, err := getProvider(ctx, q, providerID)
	if err != nil {
		return input, err
	}

	input.ProviderRef = providerID
	input.CapabilityClassRef = classID
	input.EndpointName = strings.TrimSpace(input.EndpointName)
	input.CompactAddress = strings.TrimSpace(input.CompactAddress)
	input.Form = strings.TrimSpace(input.Form)
	input.RiskLevel = strings.TrimSpace(input.RiskLevel)
	input.Status = defaultString(input.Status, EndpointStatusRegistered)
	if input.EndpointName == "" {
		return input, fmt.Errorf("endpoint_name is required")
	}
	if input.CompactAddress == "" {
		input.CompactAddress = provider.CompactAddress + "." + input.EndpointName
	}
	address, err := ParseAddress(input.CompactAddress)
	if err != nil {
		return input, err
	}
	if address.ProviderKey != provider.ProviderKey {
		return input, fmt.Errorf("capability address provider %q does not match provider %q", address.ProviderKey, provider.ProviderKey)
	}
	if address.CapabilityName != input.EndpointName {
		return input, fmt.Errorf("endpoint_name %q does not match capability address %q", input.EndpointName, input.CompactAddress)
	}
	if !ValidCapabilityForm(input.Form) {
		return input, fmt.Errorf("unsupported capability form: %s", input.Form)
	}
	if !ValidRiskLevel(input.RiskLevel) {
		return input, fmt.Errorf("unsupported risk level: %s", input.RiskLevel)
	}
	if !ValidExecutionAuthorizationLevel(input.ExecutionAuthorizationLevel) {
		return input, fmt.Errorf("execution_authorization_level must be between 1 and 5")
	}
	if !ValidEndpointStatus(input.Status) {
		return input, fmt.Errorf("unsupported endpoint status: %s", input.Status)
	}
	input.InputSchemaJSON = normalizeJSONObject(input.InputSchemaJSON)
	input.OutputSchemaJSON = normalizeJSONObject(input.OutputSchemaJSON)
	input.SideEffectsJSON = normalizeJSONObject(input.SideEffectsJSON)
	input.PolicyRequirementsJSON = normalizeJSONObject(input.PolicyRequirementsJSON)
	input.CredentialRequirementsJSON = normalizeJSONObject(input.CredentialRequirementsJSON)
	input.ApprovalRequirementsJSON = normalizeJSONObject(input.ApprovalRequirementsJSON)
	input.JobBehaviorJSON = normalizeJSONObject(input.JobBehaviorJSON)
	input.SessionBehaviorJSON = normalizeJSONObject(input.SessionBehaviorJSON)
	input.StreamBehaviorJSON = normalizeJSONObject(input.StreamBehaviorJSON)
	input.LeaseBehaviorJSON = normalizeJSONObject(input.LeaseBehaviorJSON)
	input.Metadata = normalizeJSONObject(input.Metadata)
	if err := ValidateSchemaJSON(input.InputSchemaJSON); err != nil {
		return input, fmt.Errorf("input_schema_json: %w", err)
	}
	if err := ValidateSchemaJSON(input.OutputSchemaJSON); err != nil {
		return input, fmt.Errorf("output_schema_json: %w", err)
	}
	return input, nil
}

func normalizeEndpointVersionInput(ctx context.Context, q queryer, input RegisterEndpointVersionInput) (RegisterEndpointVersionInput, error) {
	endpointID, err := resolveEndpointRef(ctx, q, input.CapabilityEndpointRef)
	if err != nil {
		return input, fmt.Errorf("resolve endpoint: %w", err)
	}
	input.CapabilityEndpointRef = endpointID
	input.VersionLabel = defaultString(input.VersionLabel, "0.1.0")
	input.RiskLevel = strings.TrimSpace(input.RiskLevel)
	input.Status = defaultString(input.Status, EndpointVersionStatusPendingReview)
	if !ValidRiskLevel(input.RiskLevel) {
		return input, fmt.Errorf("unsupported risk level: %s", input.RiskLevel)
	}
	if !ValidExecutionAuthorizationLevel(input.ExecutionAuthorizationLevel) {
		return input, fmt.Errorf("execution_authorization_level must be between 1 and 5")
	}
	if !ValidEndpointVersionStatus(input.Status) {
		return input, fmt.Errorf("unsupported endpoint version status: %s", input.Status)
	}
	input.ManifestJSON = normalizeJSONObject(input.ManifestJSON)
	input.InputSchemaJSON = normalizeJSONObject(input.InputSchemaJSON)
	input.OutputSchemaJSON = normalizeJSONObject(input.OutputSchemaJSON)
	input.PolicyRequirementsJSON = normalizeJSONObject(input.PolicyRequirementsJSON)
	input.CredentialRequirementsJSON = normalizeJSONObject(input.CredentialRequirementsJSON)
	input.ApprovalRequirementsJSON = normalizeJSONObject(input.ApprovalRequirementsJSON)
	input.Metadata = normalizeJSONObject(input.Metadata)
	if err := ValidateSchemaJSON(input.InputSchemaJSON); err != nil {
		return input, fmt.Errorf("input_schema_json: %w", err)
	}
	if err := ValidateSchemaJSON(input.OutputSchemaJSON); err != nil {
		return input, fmt.Errorf("output_schema_json: %w", err)
	}
	return input, nil
}

func normalizeRuntimeBindingInput(ctx context.Context, q queryer, input RegisterRuntimeBindingInput) (RegisterRuntimeBindingInput, error) {
	versionID, err := resolveEndpointVersionRef(ctx, q, input.EndpointVersionRef)
	if err != nil {
		return input, fmt.Errorf("resolve endpoint version: %w", err)
	}
	input.EndpointVersionRef = versionID
	input.RuntimeKind = strings.TrimSpace(input.RuntimeKind)
	input.Status = defaultString(input.Status, RuntimeBindingStatusRegistered)
	input.ApprovedByActorID = strings.TrimSpace(input.ApprovedByActorID)
	if !ValidRuntimeKind(input.RuntimeKind) {
		return input, fmt.Errorf("unsupported runtime_kind: %s", input.RuntimeKind)
	}
	if !ValidRuntimeBindingStatus(input.Status) {
		return input, fmt.Errorf("unsupported runtime binding status: %s", input.Status)
	}
	input.RuntimeConfigJSON = normalizeJSONObject(input.RuntimeConfigJSON)
	input.InputMappingJSON = normalizeJSONObject(input.InputMappingJSON)
	input.OutputMappingJSON = normalizeJSONObject(input.OutputMappingJSON)
	input.Metadata = normalizeJSONObject(input.Metadata)
	if err := validateJSONObject("runtime_config_json", input.RuntimeConfigJSON); err != nil {
		return input, err
	}
	validation := capabilityruntime.NormalizeAndValidate(input.RuntimeKind, input.RuntimeConfigJSON, capabilityruntime.ValidationModeRegister)
	if !validation.Valid {
		return input, fmt.Errorf("runtime_config_json: %s", runtimeValidationMessage(validation.Errors))
	}
	if len(validation.Config) > 0 {
		input.RuntimeConfigJSON = validation.Config
	}
	if err := validateJSONObject("input_mapping_json", input.InputMappingJSON); err != nil {
		return input, err
	}
	if err := validateJSONObject("output_mapping_json", input.OutputMappingJSON); err != nil {
		return input, err
	}
	if err := validateJSONObject("metadata", input.Metadata); err != nil {
		return input, err
	}
	return input, nil
}

func normalizeUsageDocumentInput(ctx context.Context, q queryer, input RegisterUsageDocumentInput) (RegisterUsageDocumentInput, error) {
	input.TargetKind = strings.TrimSpace(input.TargetKind)
	input.TargetID = strings.TrimSpace(input.TargetID)
	input.Title = strings.TrimSpace(input.Title)
	input.VersionLabel = defaultString(input.VersionLabel, "0.1.0")
	input.BodyFormat = defaultString(input.BodyFormat, UsageDocumentFormatMarkdown)
	input.ReviewStatus = defaultString(input.ReviewStatus, UsageReviewStatusDraft)
	input.SourceKind = defaultString(input.SourceKind, UsageSourceKindManual)
	if !ValidUsageTargetKind(input.TargetKind) {
		return input, fmt.Errorf("unsupported usage-document target kind: %s", input.TargetKind)
	}
	if input.TargetID == "" || input.Title == "" || strings.TrimSpace(input.Body) == "" {
		return input, fmt.Errorf("target_id, title, and body are required")
	}
	if input.BodyFormat != UsageDocumentFormatMarkdown {
		return input, fmt.Errorf("unsupported usage-document body format: %s", input.BodyFormat)
	}
	if !ValidUsageReviewStatus(input.ReviewStatus) {
		return input, fmt.Errorf("unsupported usage-document review status: %s", input.ReviewStatus)
	}
	if !ValidUsageSourceKind(input.SourceKind) {
		return input, fmt.Errorf("unsupported usage-document source kind: %s", input.SourceKind)
	}
	if err := validateUsageTargetExists(ctx, q, input.TargetKind, input.TargetID); err != nil {
		return input, err
	}
	if strings.TrimSpace(input.ContentHash) == "" {
		input.ContentHash = contentHashForBody(input.Body)
	}
	input.SectionMapJSON = normalizeJSONObject(input.SectionMapJSON)
	input.VisibilityPolicyJSON = normalizeJSONObject(input.VisibilityPolicyJSON)
	input.Metadata = normalizeJSONObject(input.Metadata)
	return input, nil
}

func insertProviderTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input RegisterProviderInput) (Provider, error) {
	var provider Provider
	err := tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.providers (
			provider_id, provider_key, compact_address, display_name, description,
			provider_type, node_id, scope_id, version, status, runtime_profile_json,
			documentation_refs_json, package_ref, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, nullif($13, ''), $14, $15)
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
		input.Status,
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

func appendProviderRegisteredEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, provider Provider) (events.Event, error) {
	return events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeProviderRegistered,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    provider.ScopeID,
		TargetKind: "provider",
		TargetID:   provider.ProviderID,
		Status:     provider.Status,
		Result:     "ok",
		Payload: map[string]any{
			"provider_id":     provider.ProviderID,
			"provider_key":    provider.ProviderKey,
			"compact_address": provider.CompactAddress,
			"provider_type":   provider.ProviderType,
		},
		VisibilityClass: "internal",
	})
}

func upsertProviderHealthTx(ctx context.Context, tx *sql.Tx, providerID string, input ProviderHealthInput) (ProviderHealth, bool, error) {
	now := time.Now().UTC()
	checkedAt := input.LastCheckedAt
	if checkedAt == nil {
		checkedAt = &now
	}
	var health ProviderHealth
	err := tx.QueryRowContext(ctx, `
		INSERT INTO capabilities.provider_health (
			provider_id, health_status, availability_status, last_checked_at,
			last_ok_at, message, details_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (provider_id) DO UPDATE
		SET health_status = EXCLUDED.health_status,
		    availability_status = EXCLUDED.availability_status,
		    last_checked_at = EXCLUDED.last_checked_at,
		    last_ok_at = EXCLUDED.last_ok_at,
		    message = EXCLUDED.message,
		    details_json = EXCLUDED.details_json,
		    updated_at = now()
		RETURNING provider_id, health_status, availability_status, last_checked_at,
		          last_ok_at, message, details_json, updated_at
	`, providerID, input.HealthStatus, input.AvailabilityStatus, checkedAt, input.LastOKAt, input.Message, input.DetailsJSON).Scan(
		&health.ProviderID,
		&health.HealthStatus,
		&health.AvailabilityStatus,
		&health.LastCheckedAt,
		&health.LastOKAt,
		&health.Message,
		&health.DetailsJSON,
		&health.UpdatedAt,
	)
	if err != nil {
		return ProviderHealth{}, false, err
	}
	return normalizeScannedProviderHealth(health), false, nil
}

func changedProviderHealth(old ProviderHealth, current ProviderHealth) bool {
	return old.HealthStatus != current.HealthStatus ||
		old.AvailabilityStatus != current.AvailabilityStatus ||
		old.Message != current.Message ||
		string(old.DetailsJSON) != string(current.DetailsJSON)
}

func resolveProviderRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("provider ref is required")
	}
	query := `SELECT provider_id FROM capabilities.providers WHERE provider_id = $1 OR compact_address = $1 OR provider_key = $1 ORDER BY compact_address LIMIT 2`
	rows, err := q.QueryContext(ctx, query, ref)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var providerID string
		if err := rows.Scan(&providerID); err != nil {
			return "", err
		}
		ids = append(ids, providerID)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", sql.ErrNoRows
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("provider ref %q is ambiguous", ref)
	}
	return ids[0], nil
}

func resolveEndpointRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("capability ref is required")
	}
	var endpointID string
	err := q.QueryRowContext(ctx, `
		SELECT capability_endpoint_id
		FROM capabilities.capability_endpoints
		WHERE capability_endpoint_id = $1 OR compact_address = $1
	`, ref).Scan(&endpointID)
	if err != nil {
		return "", err
	}
	return endpointID, nil
}

func resolveEndpointVersionRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("endpoint version ref is required")
	}
	if strings.HasPrefix(ref, ids.CapabilityEndpointVersionPrefix+"_") {
		var versionID string
		err := q.QueryRowContext(ctx, `
			SELECT capability_endpoint_version_id
			FROM capabilities.endpoint_versions
			WHERE capability_endpoint_version_id = $1
		`, ref).Scan(&versionID)
		return versionID, err
	}
	return activeEndpointVersionIDForEndpointRef(ctx, q, ref)
}

func activeEndpointVersionIDForEndpointRef(ctx context.Context, q queryer, ref string) (string, error) {
	endpointID, err := resolveEndpointRef(ctx, q, ref)
	if err != nil {
		return "", err
	}
	var versionID sql.NullString
	err = q.QueryRowContext(ctx, `
		SELECT active_endpoint_version_id
		FROM capabilities.capability_endpoints
		WHERE capability_endpoint_id = $1
	`, endpointID).Scan(&versionID)
	if err != nil {
		return "", err
	}
	if !versionID.Valid || strings.TrimSpace(versionID.String) == "" {
		return "", fmt.Errorf("capability endpoint has no active endpoint version")
	}
	return versionID.String, nil
}

func resolveCapabilityClassRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("capability class ref is required")
	}
	var classID string
	err := q.QueryRowContext(ctx, `
		SELECT capability_class_id
		FROM capabilities.capability_classes
		WHERE capability_class_id = $1
		   OR namespace || '.' || name = $1
		   OR name = $1
		ORDER BY version DESC
		LIMIT 1
	`, ref).Scan(&classID)
	if err != nil {
		return "", err
	}
	return classID, nil
}

func resolveNodeRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("node ref is required")
	}
	var nodeID string
	err := q.QueryRowContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_id = $1 OR node_key = $1`, ref).Scan(&nodeID)
	return nodeID, err
}

func resolveScopeRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("scope ref is required")
	}
	var scopeID string
	err := q.QueryRowContext(ctx, `SELECT scope_id FROM scopes.scopes WHERE scope_id = $1 OR scope_key = $1 OR slug = $1`, ref).Scan(&scopeID)
	return scopeID, err
}

func resolveProjectScopeRef(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("project ref is required")
	}
	var scopeID string
	err := q.QueryRowContext(ctx, `
		SELECT p.project_scope_id
		FROM projects.projects p
		JOIN scopes.scopes s ON s.scope_id = p.project_scope_id
		WHERE p.project_id = $1
		   OR p.slug = $1
		   OR p.project_scope_id = $1
		   OR s.scope_key = $1
		   OR s.slug = $1
		LIMIT 1
	`, ref).Scan(&scopeID)
	return scopeID, err
}

func getProvider(ctx context.Context, q queryer, providerID string) (Provider, error) {
	var provider Provider
	err := q.QueryRowContext(ctx, `SELECT `+providerColumns()+` FROM capabilities.providers WHERE provider_id = $1`, providerID).Scan(providerScanDest(&provider)...)
	if err != nil {
		return Provider{}, err
	}
	return normalizeScannedProvider(provider), nil
}

func getProviderHealth(ctx context.Context, q queryer, providerID string) (ProviderHealth, bool, error) {
	health, found, err := getProviderHealthTx(ctx, q, providerID)
	return health, found, err
}

func getProviderHealthTx(ctx context.Context, q queryer, providerID string) (ProviderHealth, bool, error) {
	var health ProviderHealth
	err := q.QueryRowContext(ctx, `
		SELECT provider_id, health_status, availability_status, last_checked_at,
		       last_ok_at, message, details_json, updated_at
		FROM capabilities.provider_health
		WHERE provider_id = $1
	`, providerID).Scan(
		&health.ProviderID,
		&health.HealthStatus,
		&health.AvailabilityStatus,
		&health.LastCheckedAt,
		&health.LastOKAt,
		&health.Message,
		&health.DetailsJSON,
		&health.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return ProviderHealth{}, false, nil
	}
	if err != nil {
		return ProviderHealth{}, false, err
	}
	return normalizeScannedProviderHealth(health), true, nil
}

func getCapabilityClass(ctx context.Context, q queryer, classID string) (CapabilityClass, error) {
	var class CapabilityClass
	err := q.QueryRowContext(ctx, `SELECT `+capabilityClassColumns()+` FROM capabilities.capability_classes WHERE capability_class_id = $1`, classID).Scan(capabilityClassScanDest(&class)...)
	if err != nil {
		return CapabilityClass{}, err
	}
	return normalizeScannedCapabilityClass(class), nil
}

func getCapabilityEndpoint(ctx context.Context, q queryer, endpointID string) (CapabilityEndpoint, error) {
	var endpoint CapabilityEndpoint
	err := q.QueryRowContext(ctx, `SELECT `+capabilityEndpointColumns()+` FROM capabilities.capability_endpoints WHERE capability_endpoint_id = $1`, endpointID).Scan(capabilityEndpointScanDest(&endpoint)...)
	if err != nil {
		return CapabilityEndpoint{}, err
	}
	return normalizeScannedCapabilityEndpoint(endpoint), nil
}

func getEndpointVersion(ctx context.Context, q queryer, versionID string) (EndpointVersion, error) {
	var version EndpointVersion
	err := q.QueryRowContext(ctx, `SELECT `+endpointVersionColumns()+` FROM capabilities.endpoint_versions WHERE capability_endpoint_version_id = $1`, versionID).Scan(endpointVersionScanDest(&version)...)
	if err != nil {
		return EndpointVersion{}, err
	}
	return normalizeScannedEndpointVersion(version), nil
}

func runtimeValidationMessage(items []capabilityruntime.Diagnostic) string {
	if len(items) == 0 {
		return "runtime config is invalid"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Field) != "" {
			parts = append(parts, item.Field+": "+item.Message)
		} else {
			parts = append(parts, item.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func (s Service) upsertRuntimeBinding(ctx context.Context, req requestctx.Context, input RegisterRuntimeBindingInput) (EndpointRuntimeBinding, error) {
	var binding EndpointRuntimeBinding
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO capabilities.endpoint_runtime_bindings (
			runtime_binding_id, capability_endpoint_version_id, runtime_kind,
			runtime_config_json, input_mapping_json, output_mapping_json, status,
			created_by_actor_id, approved_by_actor_id, approved_at, disabled_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''), $10,
		        CASE WHEN $7 IN ('disabled', 'revoked') THEN now() ELSE NULL END, $11)
		ON CONFLICT (capability_endpoint_version_id) DO UPDATE
		SET runtime_kind = EXCLUDED.runtime_kind,
		    runtime_config_json = EXCLUDED.runtime_config_json,
		    input_mapping_json = EXCLUDED.input_mapping_json,
		    output_mapping_json = EXCLUDED.output_mapping_json,
		    status = EXCLUDED.status,
		    approved_by_actor_id = EXCLUDED.approved_by_actor_id,
		    approved_at = EXCLUDED.approved_at,
		    disabled_at = CASE
		        WHEN EXCLUDED.status IN ('disabled', 'revoked') THEN COALESCE(capabilities.endpoint_runtime_bindings.disabled_at, now())
		        ELSE NULL
		    END,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+runtimeBindingColumns(),
		ids.NewCapabilityRuntimeBindingID(),
		input.EndpointVersionRef,
		input.RuntimeKind,
		input.RuntimeConfigJSON,
		input.InputMappingJSON,
		input.OutputMappingJSON,
		input.Status,
		req.ActorID,
		input.ApprovedByActorID,
		input.ApprovedAt,
		input.Metadata,
	).Scan(runtimeBindingScanDest(&binding)...)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	return normalizeScannedRuntimeBinding(binding), nil
}

func getRuntimeBindingForEndpointVersion(ctx context.Context, q queryer, versionID string) (EndpointRuntimeBinding, error) {
	var binding EndpointRuntimeBinding
	err := q.QueryRowContext(ctx, `
		SELECT `+runtimeBindingColumns()+`
		FROM capabilities.endpoint_runtime_bindings
		WHERE capability_endpoint_version_id = $1
	`, versionID).Scan(runtimeBindingScanDest(&binding)...)
	if err != nil {
		return EndpointRuntimeBinding{}, err
	}
	return normalizeScannedRuntimeBinding(binding), nil
}

func getUsageDocument(ctx context.Context, q queryer, docID string) (UsageDocument, error) {
	var doc UsageDocument
	err := q.QueryRowContext(ctx, `SELECT `+usageDocumentColumns()+` FROM capabilities.usage_documents WHERE capability_usage_document_id = $1`, docID).Scan(usageDocumentScanDest(&doc)...)
	if err != nil {
		return UsageDocument{}, err
	}
	return normalizeScannedUsageDocument(doc), nil
}

func listProviderEndpoints(ctx context.Context, q queryer, providerID string) ([]CapabilityEndpoint, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+capabilityEndpointColumns()+` FROM capabilities.capability_endpoints WHERE provider_id = $1 ORDER BY compact_address`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	endpoints := []CapabilityEndpoint{}
	for rows.Next() {
		var endpoint CapabilityEndpoint
		if err := rows.Scan(capabilityEndpointScanDest(&endpoint)...); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, normalizeScannedCapabilityEndpoint(endpoint))
	}
	return endpoints, rows.Err()
}

func listUsageDocumentsForTarget(ctx context.Context, q queryer, targetKind, targetID string) ([]UsageDocument, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT `+usageDocumentColumns()+`
		FROM capabilities.usage_documents
		WHERE target_kind = $1 AND target_id = $2
		ORDER BY approved_at DESC NULLS LAST, updated_at DESC, title
	`, targetKind, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := []UsageDocument{}
	for rows.Next() {
		var doc UsageDocument
		if err := rows.Scan(usageDocumentScanDest(&doc)...); err != nil {
			return nil, err
		}
		docs = append(docs, normalizeScannedUsageDocument(doc))
	}
	return docs, rows.Err()
}

func validateUsageTargetExists(ctx context.Context, q queryer, targetKind, targetID string) error {
	var exists bool
	var query string
	switch targetKind {
	case UsageTargetKindProvider:
		query = `SELECT EXISTS (SELECT 1 FROM capabilities.providers WHERE provider_id = $1)`
	case UsageTargetKindCapabilityClass:
		query = `SELECT EXISTS (SELECT 1 FROM capabilities.capability_classes WHERE capability_class_id = $1)`
	case UsageTargetKindCapabilityEndpoint:
		query = `SELECT EXISTS (SELECT 1 FROM capabilities.capability_endpoints WHERE capability_endpoint_id = $1)`
	default:
		return nil
	}
	if err := q.QueryRowContext(ctx, query, targetID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("usage-document target %s/%s does not exist", targetKind, targetID)
	}
	return nil
}

func providerListSelectSQL() string {
	return `
		SELECT ` + prefixedProviderColumns("p") + `,
		       COALESCE(h.health_status, 'unknown'),
		       COALESCE(h.availability_status, 'unknown'),
		       COALESCE(h.details_json, '{}'::jsonb),
		       h.last_checked_at
		FROM capabilities.providers p
		LEFT JOIN capabilities.provider_health h ON h.provider_id = p.provider_id
	`
}

func capabilityListSelectSQL() string {
	return `
		SELECT ` + prefixedCapabilityEndpointColumns("e") + `,
		       p.compact_address,
		       p.provider_key,
		       COALESCE(h.health_status, 'unknown'),
		       c.namespace || '.' || c.name,
		       c.display_name,
		       c.description
		FROM capabilities.capability_endpoints e
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN capabilities.capability_classes c ON c.capability_class_id = e.capability_class_id
		LEFT JOIN capabilities.provider_health h ON h.provider_id = p.provider_id
	`
}

func runtimeBindingInspectionSelectSQL() string {
	return `
		SELECT ` + prefixedRuntimeBindingColumns("b") + `,
		       ` + prefixedEndpointVersionColumns("ev") + `,
		       ` + prefixedCapabilityEndpointColumns("e") + `,
		       ` + prefixedProviderColumns("p") + `,
		       ` + prefixedCapabilityClassColumns("c") + `
		FROM capabilities.endpoint_runtime_bindings b
		JOIN capabilities.endpoint_versions ev ON ev.capability_endpoint_version_id = b.capability_endpoint_version_id
		JOIN capabilities.capability_endpoints e ON e.capability_endpoint_id = ev.capability_endpoint_id
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN capabilities.capability_classes c ON c.capability_class_id = e.capability_class_id
	`
}

func providerColumns() string {
	return `provider_id, provider_key, compact_address, display_name, description,
	        provider_type, node_id, scope_id, version, status, runtime_profile_json,
	        documentation_refs_json, package_ref, created_by_actor_id, created_at,
	        updated_at, last_advertised_at, metadata`
}

func prefixedProviderColumns(prefix string) string {
	return prefix + `.provider_id, ` + prefix + `.provider_key, ` + prefix + `.compact_address, ` + prefix + `.display_name, ` + prefix + `.description,
	        ` + prefix + `.provider_type, ` + prefix + `.node_id, ` + prefix + `.scope_id, ` + prefix + `.version, ` + prefix + `.status, ` + prefix + `.runtime_profile_json,
	        ` + prefix + `.documentation_refs_json, ` + prefix + `.package_ref, ` + prefix + `.created_by_actor_id, ` + prefix + `.created_at,
	        ` + prefix + `.updated_at, ` + prefix + `.last_advertised_at, ` + prefix + `.metadata`
}

func providerScanDest(provider *Provider) []any {
	return []any{
		&provider.ProviderID,
		&provider.ProviderKey,
		&provider.CompactAddress,
		&provider.DisplayName,
		&provider.Description,
		&provider.ProviderType,
		&provider.NodeID,
		&provider.ScopeID,
		&provider.Version,
		&provider.Status,
		&provider.RuntimeProfileJSON,
		&provider.DocumentationRefsJSON,
		&provider.PackageRef,
		&provider.CreatedByActorID,
		&provider.CreatedAt,
		&provider.UpdatedAt,
		&provider.LastAdvertisedAt,
		&provider.Metadata,
	}
}

func scanProviderListItem(scanner rowScanner) (ProviderListItem, error) {
	var item ProviderListItem
	dest := providerScanDest(&item.Provider)
	dest = append(dest, &item.HealthStatus, &item.AvailabilityStatus, &item.HealthDetailsJSON, &item.LastCheckedAt)
	if err := scanner.Scan(dest...); err != nil {
		return ProviderListItem{}, err
	}
	item.Provider = normalizeScannedProvider(item.Provider)
	return item, nil
}

func capabilityClassColumns() string {
	return `capability_class_id, namespace, name, version, display_name, description,
	        form, input_schema_json, output_schema_json, default_risk_level,
	        default_policy_requirements_json, status, created_at, updated_at,
	        deprecated_at, metadata`
}

func prefixedCapabilityClassColumns(prefix string) string {
	return prefix + `.capability_class_id, ` + prefix + `.namespace, ` + prefix + `.name, ` + prefix + `.version, ` + prefix + `.display_name, ` + prefix + `.description,
	        ` + prefix + `.form, ` + prefix + `.input_schema_json, ` + prefix + `.output_schema_json, ` + prefix + `.default_risk_level,
	        ` + prefix + `.default_policy_requirements_json, ` + prefix + `.status, ` + prefix + `.created_at, ` + prefix + `.updated_at,
	        ` + prefix + `.deprecated_at, ` + prefix + `.metadata`
}

func capabilityClassScanDest(class *CapabilityClass) []any {
	return []any{
		&class.CapabilityClassID,
		&class.Namespace,
		&class.Name,
		&class.Version,
		&class.DisplayName,
		&class.Description,
		&class.Form,
		&class.InputSchemaJSON,
		&class.OutputSchemaJSON,
		&class.DefaultRiskLevel,
		&class.DefaultPolicyRequirementsJSON,
		&class.Status,
		&class.CreatedAt,
		&class.UpdatedAt,
		&class.DeprecatedAt,
		&class.Metadata,
	}
}

func capabilityEndpointColumns() string {
	return `capability_endpoint_id, provider_id, capability_class_id, active_endpoint_version_id,
	        endpoint_name, compact_address, form, input_schema_json, output_schema_json,
	        risk_level, execution_authorization_level, side_effects_json,
	        policy_requirements_json, credential_requirements_json, approval_requirements_json,
	        job_behavior_json, session_behavior_json, stream_behavior_json, lease_behavior_json,
	        status, created_at, updated_at, deprecated_at, metadata`
}

func prefixedCapabilityEndpointColumns(prefix string) string {
	return prefix + `.capability_endpoint_id, ` + prefix + `.provider_id, ` + prefix + `.capability_class_id, ` + prefix + `.active_endpoint_version_id,
	        ` + prefix + `.endpoint_name, ` + prefix + `.compact_address, ` + prefix + `.form, ` + prefix + `.input_schema_json, ` + prefix + `.output_schema_json,
	        ` + prefix + `.risk_level, ` + prefix + `.execution_authorization_level, ` + prefix + `.side_effects_json,
	        ` + prefix + `.policy_requirements_json, ` + prefix + `.credential_requirements_json, ` + prefix + `.approval_requirements_json,
	        ` + prefix + `.job_behavior_json, ` + prefix + `.session_behavior_json, ` + prefix + `.stream_behavior_json, ` + prefix + `.lease_behavior_json,
	        ` + prefix + `.status, ` + prefix + `.created_at, ` + prefix + `.updated_at, ` + prefix + `.deprecated_at, ` + prefix + `.metadata`
}

func capabilityEndpointScanDest(endpoint *CapabilityEndpoint) []any {
	return []any{
		&endpoint.CapabilityEndpointID,
		&endpoint.ProviderID,
		&endpoint.CapabilityClassID,
		&endpoint.ActiveEndpointVersionID,
		&endpoint.EndpointName,
		&endpoint.CompactAddress,
		&endpoint.Form,
		&endpoint.InputSchemaJSON,
		&endpoint.OutputSchemaJSON,
		&endpoint.RiskLevel,
		&endpoint.ExecutionAuthorizationLevel,
		&endpoint.SideEffectsJSON,
		&endpoint.PolicyRequirementsJSON,
		&endpoint.CredentialRequirementsJSON,
		&endpoint.ApprovalRequirementsJSON,
		&endpoint.JobBehaviorJSON,
		&endpoint.SessionBehaviorJSON,
		&endpoint.StreamBehaviorJSON,
		&endpoint.LeaseBehaviorJSON,
		&endpoint.Status,
		&endpoint.CreatedAt,
		&endpoint.UpdatedAt,
		&endpoint.DeprecatedAt,
		&endpoint.Metadata,
	}
}

func scanCapabilityListItem(scanner rowScanner) (CapabilityListItem, error) {
	var item CapabilityListItem
	dest := capabilityEndpointScanDest(&item.CapabilityEndpoint)
	dest = append(dest, &item.ProviderAddress, &item.ProviderKey, &item.ProviderHealth, &item.ClassName, &item.DisplayName, &item.Description)
	if err := scanner.Scan(dest...); err != nil {
		return CapabilityListItem{}, err
	}
	item.CapabilityEndpoint = normalizeScannedCapabilityEndpoint(item.CapabilityEndpoint)
	return item, nil
}

func endpointVersionColumns() string {
	return `capability_endpoint_version_id, capability_endpoint_id, version_label,
	        implementation_hash, manifest_json, input_schema_json, output_schema_json,
	        risk_level, execution_authorization_level, policy_requirements_json,
	        credential_requirements_json, approval_requirements_json, status,
	        approved_by_actor_id, approved_at, created_at, deprecated_at, metadata`
}

func prefixedEndpointVersionColumns(prefix string) string {
	return prefix + `.capability_endpoint_version_id, ` + prefix + `.capability_endpoint_id, ` + prefix + `.version_label,
	        ` + prefix + `.implementation_hash, ` + prefix + `.manifest_json, ` + prefix + `.input_schema_json, ` + prefix + `.output_schema_json,
	        ` + prefix + `.risk_level, ` + prefix + `.execution_authorization_level, ` + prefix + `.policy_requirements_json,
	        ` + prefix + `.credential_requirements_json, ` + prefix + `.approval_requirements_json, ` + prefix + `.status,
	        ` + prefix + `.approved_by_actor_id, ` + prefix + `.approved_at, ` + prefix + `.created_at, ` + prefix + `.deprecated_at, ` + prefix + `.metadata`
}

func endpointVersionScanDest(version *EndpointVersion) []any {
	return []any{
		&version.CapabilityEndpointVersionID,
		&version.CapabilityEndpointID,
		&version.VersionLabel,
		&version.ImplementationHash,
		&version.ManifestJSON,
		&version.InputSchemaJSON,
		&version.OutputSchemaJSON,
		&version.RiskLevel,
		&version.ExecutionAuthorizationLevel,
		&version.PolicyRequirementsJSON,
		&version.CredentialRequirementsJSON,
		&version.ApprovalRequirementsJSON,
		&version.Status,
		&version.ApprovedByActorID,
		&version.ApprovedAt,
		&version.CreatedAt,
		&version.DeprecatedAt,
		&version.Metadata,
	}
}

func runtimeBindingColumns() string {
	return `runtime_binding_id, capability_endpoint_version_id, runtime_kind,
	        runtime_config_json, input_mapping_json, output_mapping_json, status,
	        created_by_actor_id, approved_by_actor_id, approved_at, created_at,
	        updated_at, disabled_at, metadata`
}

func prefixedRuntimeBindingColumns(prefix string) string {
	return prefix + `.runtime_binding_id, ` + prefix + `.capability_endpoint_version_id, ` + prefix + `.runtime_kind,
	        ` + prefix + `.runtime_config_json, ` + prefix + `.input_mapping_json, ` + prefix + `.output_mapping_json, ` + prefix + `.status,
	        ` + prefix + `.created_by_actor_id, ` + prefix + `.approved_by_actor_id, ` + prefix + `.approved_at, ` + prefix + `.created_at,
	        ` + prefix + `.updated_at, ` + prefix + `.disabled_at, ` + prefix + `.metadata`
}

func runtimeBindingScanDest(binding *EndpointRuntimeBinding) []any {
	return []any{
		&binding.RuntimeBindingID,
		&binding.CapabilityEndpointVersionID,
		&binding.RuntimeKind,
		&binding.RuntimeConfigJSON,
		&binding.InputMappingJSON,
		&binding.OutputMappingJSON,
		&binding.Status,
		&binding.CreatedByActorID,
		&binding.ApprovedByActorID,
		&binding.ApprovedAt,
		&binding.CreatedAt,
		&binding.UpdatedAt,
		&binding.DisabledAt,
		&binding.Metadata,
	}
}

func scanRuntimeBindingInspection(scanner rowScanner) (RuntimeBindingInspection, error) {
	var item RuntimeBindingInspection
	dest := runtimeBindingScanDest(&item.Binding)
	dest = append(dest, endpointVersionScanDest(&item.EndpointVersion)...)
	dest = append(dest, capabilityEndpointScanDest(&item.Endpoint)...)
	dest = append(dest, providerScanDest(&item.Provider)...)
	dest = append(dest, capabilityClassScanDest(&item.Class)...)
	if err := scanner.Scan(dest...); err != nil {
		return RuntimeBindingInspection{}, err
	}
	item.Binding = normalizeScannedRuntimeBinding(item.Binding)
	item.EndpointVersion = normalizeScannedEndpointVersion(item.EndpointVersion)
	item.Endpoint = normalizeScannedCapabilityEndpoint(item.Endpoint)
	item.Provider = normalizeScannedProvider(item.Provider)
	item.Class = normalizeScannedCapabilityClass(item.Class)
	return item, nil
}

func usageDocumentColumns() string {
	return `capability_usage_document_id, target_kind, target_id, target_address,
	        title, version_label, body_format, body, section_map_json,
	        visibility_policy_json, review_status, content_hash, source_kind,
	        source_ref, created_by_actor_id, approved_by_actor_id, approved_at,
	        indexed_at, created_at, updated_at, deprecated_at, metadata`
}

func usageDocumentScanDest(doc *UsageDocument) []any {
	return []any{
		&doc.CapabilityUsageDocumentID,
		&doc.TargetKind,
		&doc.TargetID,
		&doc.TargetAddress,
		&doc.Title,
		&doc.VersionLabel,
		&doc.BodyFormat,
		&doc.Body,
		&doc.SectionMapJSON,
		&doc.VisibilityPolicyJSON,
		&doc.ReviewStatus,
		&doc.ContentHash,
		&doc.SourceKind,
		&doc.SourceRef,
		&doc.CreatedByActorID,
		&doc.ApprovedByActorID,
		&doc.ApprovedAt,
		&doc.IndexedAt,
		&doc.CreatedAt,
		&doc.UpdatedAt,
		&doc.DeprecatedAt,
		&doc.Metadata,
	}
}

func normalizeScannedProvider(provider Provider) Provider {
	provider.RuntimeProfileJSON = jsonOrDefault(provider.RuntimeProfileJSON, `{}`)
	provider.DocumentationRefsJSON = jsonOrDefault(provider.DocumentationRefsJSON, `[]`)
	provider.Metadata = jsonOrDefault(provider.Metadata, `{}`)
	return provider
}

func normalizeScannedProviderHealth(health ProviderHealth) ProviderHealth {
	health.DetailsJSON = jsonOrDefault(health.DetailsJSON, `{}`)
	return health
}

func normalizeScannedCapabilityClass(class CapabilityClass) CapabilityClass {
	class.InputSchemaJSON = jsonOrDefault(class.InputSchemaJSON, `{}`)
	class.OutputSchemaJSON = jsonOrDefault(class.OutputSchemaJSON, `{}`)
	class.DefaultPolicyRequirementsJSON = jsonOrDefault(class.DefaultPolicyRequirementsJSON, `{}`)
	class.Metadata = jsonOrDefault(class.Metadata, `{}`)
	return class
}

func normalizeScannedCapabilityEndpoint(endpoint CapabilityEndpoint) CapabilityEndpoint {
	endpoint.InputSchemaJSON = jsonOrDefault(endpoint.InputSchemaJSON, `{}`)
	endpoint.OutputSchemaJSON = jsonOrDefault(endpoint.OutputSchemaJSON, `{}`)
	endpoint.SideEffectsJSON = jsonOrDefault(endpoint.SideEffectsJSON, `{}`)
	endpoint.PolicyRequirementsJSON = jsonOrDefault(endpoint.PolicyRequirementsJSON, `{}`)
	endpoint.CredentialRequirementsJSON = jsonOrDefault(endpoint.CredentialRequirementsJSON, `{}`)
	endpoint.ApprovalRequirementsJSON = jsonOrDefault(endpoint.ApprovalRequirementsJSON, `{}`)
	endpoint.JobBehaviorJSON = jsonOrDefault(endpoint.JobBehaviorJSON, `{}`)
	endpoint.SessionBehaviorJSON = jsonOrDefault(endpoint.SessionBehaviorJSON, `{}`)
	endpoint.StreamBehaviorJSON = jsonOrDefault(endpoint.StreamBehaviorJSON, `{}`)
	endpoint.LeaseBehaviorJSON = jsonOrDefault(endpoint.LeaseBehaviorJSON, `{}`)
	endpoint.Metadata = jsonOrDefault(endpoint.Metadata, `{}`)
	return endpoint
}

func normalizeScannedEndpointVersion(version EndpointVersion) EndpointVersion {
	version.ManifestJSON = jsonOrDefault(version.ManifestJSON, `{}`)
	version.InputSchemaJSON = jsonOrDefault(version.InputSchemaJSON, `{}`)
	version.OutputSchemaJSON = jsonOrDefault(version.OutputSchemaJSON, `{}`)
	version.PolicyRequirementsJSON = jsonOrDefault(version.PolicyRequirementsJSON, `{}`)
	version.CredentialRequirementsJSON = jsonOrDefault(version.CredentialRequirementsJSON, `{}`)
	version.ApprovalRequirementsJSON = jsonOrDefault(version.ApprovalRequirementsJSON, `{}`)
	version.Metadata = jsonOrDefault(version.Metadata, `{}`)
	return version
}

func normalizeScannedRuntimeBinding(binding EndpointRuntimeBinding) EndpointRuntimeBinding {
	binding.RuntimeConfigJSON = jsonOrDefault(binding.RuntimeConfigJSON, `{}`)
	binding.InputMappingJSON = jsonOrDefault(binding.InputMappingJSON, `{}`)
	binding.OutputMappingJSON = jsonOrDefault(binding.OutputMappingJSON, `{}`)
	binding.Metadata = jsonOrDefault(binding.Metadata, `{}`)
	return binding
}

func normalizeScannedUsageDocument(doc UsageDocument) UsageDocument {
	doc.SectionMapJSON = jsonOrDefault(doc.SectionMapJSON, `{}`)
	doc.VisibilityPolicyJSON = jsonOrDefault(doc.VisibilityPolicyJSON, `{}`)
	doc.Metadata = jsonOrDefault(doc.Metadata, `{}`)
	return doc
}

func normalizeJSONObject(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func normalizeJSONArray(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`[]`)
	}
	return raw
}

func validateJSONObject(field string, raw json.RawMessage) error {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s must be a valid JSON object: %w", field, err)
	}
	if value == nil {
		return fmt.Errorf("%s must be a JSON object", field)
	}
	return nil
}

func jsonOrDefault(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(fallback)
	}
	return raw
}

func contentHashForBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return fmt.Sprintf("sha256:%x", sum)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

type stringSlice []string

func (s *stringSlice) Scan(value any) error {
	if value == nil {
		*s = nil
		return nil
	}
	switch raw := value.(type) {
	case string:
		*s = parsePostgresTextArray(raw)
		return nil
	case []byte:
		*s = parsePostgresTextArray(string(raw))
		return nil
	default:
		return fmt.Errorf("scan text array: unsupported type %T", value)
	}
}

func parsePostgresTextArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"`))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

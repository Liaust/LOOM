package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"loom.local/loom/internal/db"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/nodeprofiles"
	"loom.local/loom/internal/requestctx"
)

const (
	DefaultProductionSource        = "production-bootstrap"
	defaultProductionCorrelationID = "bootstrap-production"
)

type ProductionInput struct {
	NodeKey      string
	DisplayName  string
	NodeKind     string
	NodeRole     string
	RuntimeClass string

	OwnerActorKey    string
	OwnerDisplayName string
	OwnerActorKind   string

	ServiceActorKey    string
	ServiceDisplayName string

	SchedulerActorKey    string
	SchedulerDisplayName string

	SystemScopeKey  string
	SystemScopeSlug string
	SystemScopeName string

	NodeScopeKey  string
	NodeScopeSlug string
	NodeScopeName string

	OwnerScopeKey  string
	OwnerScopeSlug string
	OwnerScopeName string

	AuthorityProfileKey string
	RuntimeProfileKey   string

	InstallID     string
	PlanHash      string
	Source        string
	CorrelationID string
	Metadata      map[string]any
}

type ProductionStatusInput struct {
	NodeKey           string
	OwnerActorKey     string
	ServiceActorKey   string
	SchedulerActorKey string
	SystemScopeKey    string
	NodeScopeKey      string
	OwnerScopeKey     string

	AuthorityProfileKey string
	RuntimeProfileKey   string
}

type ProductionSummary struct {
	Ready   bool `json:"ready"`
	Created bool `json:"created"`

	OwnerActorID     string `json:"owner_actor_id,omitempty"`
	ServiceActorID   string `json:"service_actor_id,omitempty"`
	SchedulerActorID string `json:"scheduler_actor_id,omitempty"`
	MainNodeID       string `json:"main_node_id,omitempty"`
	SystemScopeID    string `json:"system_scope_id,omitempty"`
	NodeScopeID      string `json:"node_scope_id,omitempty"`
	OwnerScopeID     string `json:"owner_scope_id,omitempty"`

	AuthorityProfileKey string `json:"authority_profile_key,omitempty"`
	RuntimeProfileKey   string `json:"runtime_profile_key,omitempty"`

	BootstrapEventID    string   `json:"bootstrap_event_id,omitempty"`
	BootstrapEventCount int      `json:"bootstrap_event_count"`
	Missing             []string `json:"missing,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

func DefaultProductionInput() ProductionInput {
	return ProductionInput{
		NodeKey:      "main",
		DisplayName:  "Main node",
		NodeKind:     "main",
		NodeRole:     "main",
		RuntimeClass: nodeprofiles.RuntimeMainFull,

		OwnerActorKey:    "owner",
		OwnerDisplayName: "Owner",
		OwnerActorKind:   "human",

		ServiceActorKey:    "service:loomd",
		ServiceDisplayName: "LOOM daemon",

		SchedulerActorKey:    "scheduler:loom",
		SchedulerDisplayName: "LOOM scheduler",

		SystemScopeKey:  "system",
		SystemScopeSlug: "system",
		SystemScopeName: "System",

		NodeScopeKey:  "node-main",
		NodeScopeSlug: "node-main",
		NodeScopeName: "Main node",

		OwnerScopeKey:  "actor-owner",
		OwnerScopeSlug: "actor-owner",
		OwnerScopeName: "Owner actor",

		AuthorityProfileKey: nodeprofiles.AuthorityMainNodeDefault,
		RuntimeProfileKey:   nodeprofiles.RuntimeMainFull,
		Source:              DefaultProductionSource,
		CorrelationID:       defaultProductionCorrelationID,
		Metadata:            map[string]any{},
	}
}

func NormalizeProductionInput(input ProductionInput) ProductionInput {
	defaults := DefaultProductionInput()

	input.NodeKey = defaultString(strings.TrimSpace(input.NodeKey), defaults.NodeKey)
	input.DisplayName = defaultString(strings.TrimSpace(input.DisplayName), input.NodeKey)
	input.NodeKind = defaultString(normalizeToken(input.NodeKind), defaults.NodeKind)
	input.NodeRole = defaultString(normalizeToken(input.NodeRole), defaults.NodeRole)
	input.RuntimeClass = defaultString(normalizeToken(input.RuntimeClass), defaults.RuntimeClass)

	input.OwnerActorKey = defaultString(strings.TrimSpace(input.OwnerActorKey), defaults.OwnerActorKey)
	input.OwnerDisplayName = defaultString(strings.TrimSpace(input.OwnerDisplayName), defaults.OwnerDisplayName)
	input.OwnerActorKind = defaultString(normalizeToken(input.OwnerActorKind), defaults.OwnerActorKind)
	input.ServiceActorKey = defaultString(strings.TrimSpace(input.ServiceActorKey), defaults.ServiceActorKey)
	input.ServiceDisplayName = defaultString(strings.TrimSpace(input.ServiceDisplayName), defaults.ServiceDisplayName)
	input.SchedulerActorKey = defaultString(strings.TrimSpace(input.SchedulerActorKey), defaults.SchedulerActorKey)
	input.SchedulerDisplayName = defaultString(strings.TrimSpace(input.SchedulerDisplayName), defaults.SchedulerDisplayName)

	input.SystemScopeKey = defaultString(strings.TrimSpace(input.SystemScopeKey), defaults.SystemScopeKey)
	input.SystemScopeSlug = defaultString(slugToken(input.SystemScopeSlug), slugToken(input.SystemScopeKey))
	input.SystemScopeName = defaultString(strings.TrimSpace(input.SystemScopeName), defaults.SystemScopeName)

	input.NodeScopeKey = defaultString(strings.TrimSpace(input.NodeScopeKey), "node-"+slugToken(input.NodeKey))
	input.NodeScopeSlug = defaultString(slugToken(input.NodeScopeSlug), slugToken(input.NodeScopeKey))
	input.NodeScopeName = defaultString(strings.TrimSpace(input.NodeScopeName), input.DisplayName)

	input.OwnerScopeKey = defaultString(strings.TrimSpace(input.OwnerScopeKey), "actor-"+slugToken(input.OwnerActorKey))
	input.OwnerScopeSlug = defaultString(slugToken(input.OwnerScopeSlug), slugToken(input.OwnerScopeKey))
	input.OwnerScopeName = defaultString(strings.TrimSpace(input.OwnerScopeName), input.OwnerDisplayName+" actor")

	input.AuthorityProfileKey = defaultString(normalizeToken(input.AuthorityProfileKey), defaults.AuthorityProfileKey)
	input.RuntimeProfileKey = defaultString(normalizeToken(input.RuntimeProfileKey), defaults.RuntimeProfileKey)

	input.InstallID = strings.TrimSpace(input.InstallID)
	input.PlanHash = strings.TrimSpace(input.PlanHash)
	input.Source = defaultString(strings.TrimSpace(input.Source), defaults.Source)
	input.CorrelationID = defaultString(strings.TrimSpace(input.CorrelationID), defaults.CorrelationID)
	if input.Metadata == nil {
		input.Metadata = map[string]any{}
	}
	return input
}

func ValidateProductionInput(input ProductionInput) error {
	input.NodeKey = strings.TrimSpace(input.NodeKey)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.NodeKind = normalizeToken(input.NodeKind)
	input.NodeRole = normalizeToken(input.NodeRole)
	input.RuntimeClass = normalizeToken(input.RuntimeClass)
	input.OwnerActorKey = strings.TrimSpace(input.OwnerActorKey)
	input.OwnerActorKind = normalizeToken(input.OwnerActorKind)
	input.ServiceActorKey = strings.TrimSpace(input.ServiceActorKey)
	input.SchedulerActorKey = strings.TrimSpace(input.SchedulerActorKey)
	input.SystemScopeKey = strings.TrimSpace(input.SystemScopeKey)
	input.NodeScopeKey = strings.TrimSpace(input.NodeScopeKey)
	input.OwnerScopeKey = strings.TrimSpace(input.OwnerScopeKey)
	input.AuthorityProfileKey = normalizeToken(input.AuthorityProfileKey)
	input.RuntimeProfileKey = normalizeToken(input.RuntimeProfileKey)
	missing := []string{}
	for field, value := range map[string]string{
		"node_key":              input.NodeKey,
		"owner_actor_key":       input.OwnerActorKey,
		"owner_actor_kind":      input.OwnerActorKind,
		"service_actor_key":     input.ServiceActorKey,
		"scheduler_actor_key":   input.SchedulerActorKey,
		"system_scope_key":      input.SystemScopeKey,
		"node_scope_key":        input.NodeScopeKey,
		"owner_scope_key":       input.OwnerScopeKey,
		"authority_profile_key": input.AuthorityProfileKey,
		"runtime_profile_key":   input.RuntimeProfileKey,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing production bootstrap fields: %s", strings.Join(missing, ", "))
	}
	if input.NodeKind != "main" {
		return fmt.Errorf("production bootstrap requires node_kind main, got %q", input.NodeKind)
	}
	if input.NodeRole != "main" {
		return fmt.Errorf("production bootstrap requires node_role main, got %q", input.NodeRole)
	}
	if input.OwnerActorKind != "human" {
		return fmt.Errorf("production bootstrap requires owner_actor_kind human, got %q", input.OwnerActorKind)
	}
	if input.RuntimeClass != nodeprofiles.RuntimeMainFull {
		return fmt.Errorf("production bootstrap requires runtime_class %s, got %q", nodeprofiles.RuntimeMainFull, input.RuntimeClass)
	}
	assignment, err := nodeprofiles.Resolve(nodeprofiles.ResolveInput{
		NodeKind:     input.NodeKind,
		NodeRole:     input.NodeRole,
		RuntimeClass: input.RuntimeClass,
	})
	if err != nil {
		return err
	}
	if input.AuthorityProfileKey != assignment.AuthorityProfileKey {
		return fmt.Errorf("production bootstrap authority_profile %q is not compatible with %s/%s/%s", input.AuthorityProfileKey, input.NodeKind, input.NodeRole, input.RuntimeClass)
	}
	if input.RuntimeProfileKey != assignment.RuntimeProfileKey {
		return fmt.Errorf("production bootstrap runtime_profile %q is not compatible with %s/%s/%s", input.RuntimeProfileKey, input.NodeKind, input.NodeRole, input.RuntimeClass)
	}
	return nil
}

func (s Service) EnsureProductionBootstrap(ctx context.Context, input ProductionInput) (ProductionSummary, error) {
	input = NormalizeProductionInput(input)
	if err := ValidateProductionInput(input); err != nil {
		return ProductionSummary{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProductionSummary{}, err
	}
	defer tx.Rollback()

	owner, ownerCreated, err := ensureActorCompatible(ctx, tx, actorInput{
		Key:         input.OwnerActorKey,
		DisplayName: input.OwnerDisplayName,
		Kind:        input.OwnerActorKind,
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap": true,
			"owner":     true,
			"mode":      "production",
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	serviceActor, serviceCreated, err := ensureActorCompatible(ctx, tx, actorInput{
		Key:         input.ServiceActorKey,
		DisplayName: input.ServiceDisplayName,
		Kind:        "service",
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap": true,
			"service":   "loomd",
			"mode":      "production",
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	schedulerActor, schedulerCreated, err := ensureActorCompatible(ctx, tx, actorInput{
		Key:         input.SchedulerActorKey,
		DisplayName: input.SchedulerDisplayName,
		Kind:        "scheduler",
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap": true,
			"scheduler": "loom",
			"mode":      "production",
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	mainNode, nodeCreated, err := ensureNodeCompatible(ctx, tx, nodeInput{
		Key:          input.NodeKey,
		DisplayName:  input.DisplayName,
		Kind:         input.NodeKind,
		Role:         input.NodeRole,
		RuntimeClass: input.RuntimeClass,
		OwnerActorID: owner.ID,
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap":         true,
			"mode":              "production",
			"authority_profile": input.AuthorityProfileKey,
			"runtime_profile":   input.RuntimeProfileKey,
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	systemScope, systemCreated, err := ensureScopeCompatible(ctx, tx, scopeInput{
		Type:         "system",
		Key:          input.SystemScopeKey,
		Slug:         input.SystemScopeSlug,
		DisplayName:  input.SystemScopeName,
		OwnerActorID: serviceActor.ID,
		HomeNodeID:   mainNode.ID,
		CreatedBy:    serviceActor.ID,
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap": true,
			"mode":      "production",
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	nodeScope, nodeScopeCreated, err := ensureScopeCompatible(ctx, tx, scopeInput{
		Type:         "node",
		Key:          input.NodeScopeKey,
		Slug:         input.NodeScopeSlug,
		DisplayName:  input.NodeScopeName,
		OwnerActorID: owner.ID,
		HomeNodeID:   mainNode.ID,
		CreatedBy:    serviceActor.ID,
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap": true,
			"mode":      "production",
			"node_key":  input.NodeKey,
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	ownerScope, ownerScopeCreated, err := ensureScopeCompatible(ctx, tx, scopeInput{
		Type:         "actor",
		Key:          input.OwnerScopeKey,
		Slug:         input.OwnerScopeSlug,
		DisplayName:  input.OwnerScopeName,
		OwnerActorID: owner.ID,
		HomeNodeID:   mainNode.ID,
		CreatedBy:    serviceActor.ID,
		Metadata: productionMetadata(input, map[string]any{
			"bootstrap": true,
			"mode":      "production",
			"actor_key": input.OwnerActorKey,
		}),
	})
	if err != nil {
		return ProductionSummary{}, err
	}

	if err := updateBootstrapHomes(ctx, tx, mainNode.ID, owner.ID, serviceActor.ID, schedulerActor.ID, ownerScope.ID, systemScope.ID, nodeScope.ID); err != nil {
		return ProductionSummary{}, err
	}

	authOwnerCreated, err := ensureProductionAuthorization(ctx, tx, productionAuthorizationID("owner", input.OwnerActorKey, input.NodeKey), owner.ID, mainNode.ID, 5, input)
	if err != nil {
		return ProductionSummary{}, err
	}
	authServiceCreated, err := ensureProductionAuthorization(ctx, tx, productionAuthorizationID("service", input.ServiceActorKey, input.NodeKey), serviceActor.ID, mainNode.ID, 5, input)
	if err != nil {
		return ProductionSummary{}, err
	}
	authSchedulerCreated, err := ensureProductionAuthorization(ctx, tx, productionAuthorizationID("scheduler", input.SchedulerActorKey, input.NodeKey), schedulerActor.ID, mainNode.ID, 5, input)
	if err != nil {
		return ProductionSummary{}, err
	}

	profileChanged, err := ensureProductionProfileAssignment(ctx, tx, mainNode.ID, input.AuthorityProfileKey, input.RuntimeProfileKey, serviceActor.ID, input)
	if err != nil {
		return ProductionSummary{}, err
	}

	eventID, eventCount, eventCreated, err := ensureProductionBootstrapEvent(ctx, tx, input, serviceActor.ID, mainNode.ID, systemScope.ID)
	if err != nil {
		return ProductionSummary{}, err
	}

	created := ownerCreated || serviceCreated || schedulerCreated || nodeCreated ||
		systemCreated || nodeScopeCreated || ownerScopeCreated ||
		authOwnerCreated || authServiceCreated || authSchedulerCreated ||
		profileChanged || eventCreated

	if err := tx.Commit(); err != nil {
		return ProductionSummary{}, err
	}

	status, err := s.ProductionBootstrapStatus(ctx, ProductionStatusInputFromProductionInput(input))
	if err != nil {
		return ProductionSummary{}, err
	}
	status.Created = created
	if eventID != "" {
		status.BootstrapEventID = eventID
		status.BootstrapEventCount = eventCount
	}
	return status, nil
}

func (s Service) ProductionBootstrapStatus(ctx context.Context, input ProductionStatusInput) (ProductionSummary, error) {
	input = NormalizeProductionStatusInput(input)
	summary := ProductionSummary{}

	records := []struct {
		name  string
		query string
		args  []any
		dest  *string
	}{
		{"owner actor", "SELECT actor_id FROM identity.actors WHERE actor_key = $1 AND actor_kind = 'human'", []any{input.OwnerActorKey}, &summary.OwnerActorID},
		{"service actor", "SELECT actor_id FROM identity.actors WHERE actor_key = $1 AND actor_kind = 'service'", []any{input.ServiceActorKey}, &summary.ServiceActorID},
		{"scheduler actor", "SELECT actor_id FROM identity.actors WHERE actor_key = $1 AND actor_kind = 'scheduler'", []any{input.SchedulerActorKey}, &summary.SchedulerActorID},
		{"main node", "SELECT node_id FROM nodes.nodes WHERE node_key = $1 AND node_kind = 'main' AND node_role = 'main' AND runtime_class = 'main_full'", []any{input.NodeKey}, &summary.MainNodeID},
		{"system scope", "SELECT scope_id FROM scopes.scopes WHERE scope_key = $1 AND scope_type = 'system'", []any{input.SystemScopeKey}, &summary.SystemScopeID},
		{"node scope", "SELECT scope_id FROM scopes.scopes WHERE scope_key = $1 AND scope_type = 'node'", []any{input.NodeScopeKey}, &summary.NodeScopeID},
		{"owner scope", "SELECT scope_id FROM scopes.scopes WHERE scope_key = $1 AND scope_type = 'actor'", []any{input.OwnerScopeKey}, &summary.OwnerScopeID},
	}

	for _, record := range records {
		err := s.DB.QueryRowContext(ctx, record.query, record.args...).Scan(record.dest)
		if err == sql.ErrNoRows {
			summary.Missing = append(summary.Missing, record.name)
			continue
		}
		if err != nil {
			return ProductionSummary{}, err
		}
	}

	if summary.MainNodeID != "" {
		var authorityKey sql.NullString
		var runtimeKey sql.NullString
		err := s.DB.QueryRowContext(ctx, `
			SELECT ap.profile_key, rp.profile_key
			FROM nodes.node_profile_assignments assignment
			LEFT JOIN nodes.authority_profiles ap ON ap.authority_profile_id = assignment.authority_profile_id
			LEFT JOIN nodes.runtime_profiles rp ON rp.runtime_profile_id = assignment.runtime_profile_id
			WHERE assignment.node_id = $1
		`, summary.MainNodeID).Scan(&authorityKey, &runtimeKey)
		if err == sql.ErrNoRows {
			summary.Missing = append(summary.Missing, "node profile assignment")
		} else if err != nil {
			return ProductionSummary{}, err
		} else {
			summary.AuthorityProfileKey = authorityKey.String
			summary.RuntimeProfileKey = runtimeKey.String
			if summary.AuthorityProfileKey != input.AuthorityProfileKey || summary.RuntimeProfileKey != input.RuntimeProfileKey {
				summary.Missing = append(summary.Missing, "expected node profile assignment")
			}
		}
	}

	eventID, eventCount, err := productionBootstrapEventStatus(ctx, s.DB, input.NodeKey)
	if err != nil {
		return ProductionSummary{}, err
	}
	summary.BootstrapEventID = eventID
	summary.BootstrapEventCount = eventCount
	if eventCount == 0 {
		summary.Missing = append(summary.Missing, "production system.bootstrapped event")
	}
	if eventCount > 1 {
		summary.Warnings = append(summary.Warnings, "multiple production bootstrap events exist for node "+input.NodeKey)
	}

	summary.Ready = len(summary.Missing) == 0 && eventCount >= 1
	return summary, nil
}

func CheckProduction(ctx context.Context, dbURL string, input ProductionStatusInput) ProductionSummary {
	sqlDB, err := db.OpenSQL(ctx, dbURL)
	if err != nil {
		return ProductionSummary{Ready: false, Missing: []string{"database"}}
	}
	defer sqlDB.Close()

	summary, err := NewService(sqlDB).ProductionBootstrapStatus(ctx, input)
	if err != nil {
		return ProductionSummary{Ready: false, Missing: []string{"production_bootstrap_status"}}
	}
	return summary
}

func ProductionStatusInputFromProductionInput(input ProductionInput) ProductionStatusInput {
	input = NormalizeProductionInput(input)
	return ProductionStatusInput{
		NodeKey:             input.NodeKey,
		OwnerActorKey:       input.OwnerActorKey,
		ServiceActorKey:     input.ServiceActorKey,
		SchedulerActorKey:   input.SchedulerActorKey,
		SystemScopeKey:      input.SystemScopeKey,
		NodeScopeKey:        input.NodeScopeKey,
		OwnerScopeKey:       input.OwnerScopeKey,
		AuthorityProfileKey: input.AuthorityProfileKey,
		RuntimeProfileKey:   input.RuntimeProfileKey,
	}
}

func NormalizeProductionStatusInput(input ProductionStatusInput) ProductionStatusInput {
	defaults := DefaultProductionInput()
	input.NodeKey = defaultString(strings.TrimSpace(input.NodeKey), defaults.NodeKey)
	input.OwnerActorKey = defaultString(strings.TrimSpace(input.OwnerActorKey), defaults.OwnerActorKey)
	input.ServiceActorKey = defaultString(strings.TrimSpace(input.ServiceActorKey), defaults.ServiceActorKey)
	input.SchedulerActorKey = defaultString(strings.TrimSpace(input.SchedulerActorKey), defaults.SchedulerActorKey)
	input.SystemScopeKey = defaultString(strings.TrimSpace(input.SystemScopeKey), defaults.SystemScopeKey)
	input.NodeScopeKey = defaultString(strings.TrimSpace(input.NodeScopeKey), "node-"+slugToken(input.NodeKey))
	input.OwnerScopeKey = defaultString(strings.TrimSpace(input.OwnerScopeKey), "actor-"+slugToken(input.OwnerActorKey))
	input.AuthorityProfileKey = defaultString(normalizeToken(input.AuthorityProfileKey), defaults.AuthorityProfileKey)
	input.RuntimeProfileKey = defaultString(normalizeToken(input.RuntimeProfileKey), defaults.RuntimeProfileKey)
	return input
}

func ensureActorCompatible(ctx context.Context, tx *sql.Tx, input actorInput) (keyedRecord, bool, error) {
	record, created, err := ensureActor(ctx, tx, input)
	if err != nil || created {
		return record, created, err
	}
	var actorKind string
	if err := tx.QueryRowContext(ctx, `
		SELECT actor_kind
		FROM identity.actors
		WHERE actor_key = $1
	`, input.Key).Scan(&actorKind); err != nil {
		return keyedRecord{}, false, err
	}
	if actorKind != input.Kind {
		return keyedRecord{}, false, fmt.Errorf("actor %q exists with kind %q, expected %q", input.Key, actorKind, input.Kind)
	}
	return record, false, nil
}

func ensureNodeCompatible(ctx context.Context, tx *sql.Tx, input nodeInput) (keyedRecord, bool, error) {
	record, created, err := ensureNode(ctx, tx, input)
	if err != nil || created {
		return record, created, err
	}
	var kind, role, runtimeClass string
	if err := tx.QueryRowContext(ctx, `
		SELECT node_kind, node_role, runtime_class
		FROM nodes.nodes
		WHERE node_key = $1
	`, input.Key).Scan(&kind, &role, &runtimeClass); err != nil {
		return keyedRecord{}, false, err
	}
	if kind != input.Kind || role != input.Role || runtimeClass != input.RuntimeClass {
		return keyedRecord{}, false, fmt.Errorf("node %q exists with %s/%s/%s, expected %s/%s/%s", input.Key, kind, role, runtimeClass, input.Kind, input.Role, input.RuntimeClass)
	}
	return record, false, nil
}

func ensureScopeCompatible(ctx context.Context, tx *sql.Tx, input scopeInput) (keyedRecord, bool, error) {
	record, created, err := ensureScope(ctx, tx, input)
	if err != nil || created {
		return record, created, err
	}
	var scopeType string
	if err := tx.QueryRowContext(ctx, `
		SELECT scope_type
		FROM scopes.scopes
		WHERE scope_key = $1
	`, input.Key).Scan(&scopeType); err != nil {
		return keyedRecord{}, false, err
	}
	if scopeType != input.Type {
		return keyedRecord{}, false, fmt.Errorf("scope %q exists with type %q, expected %q", input.Key, scopeType, input.Type)
	}
	return record, false, nil
}

func updateBootstrapHomes(ctx context.Context, tx *sql.Tx, mainNodeID, ownerID, serviceActorID, schedulerActorID, ownerScopeID, systemScopeID, nodeScopeID string) error {
	updates := []struct {
		actorID string
		scopeID string
	}{
		{actorID: ownerID, scopeID: ownerScopeID},
		{actorID: serviceActorID, scopeID: systemScopeID},
		{actorID: schedulerActorID, scopeID: systemScopeID},
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE identity.actors
			SET home_node_id = $1, default_scope_id = $2, updated_at = now()
			WHERE actor_id = $3
		`, mainNodeID, update.scopeID, update.actorID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE nodes.nodes
		SET home_scope_id = $1, updated_at = now()
		WHERE node_id = $2
	`, nodeScopeID, mainNodeID)
	return err
}

func ensureProductionAuthorization(ctx context.Context, tx *sql.Tx, authorizationID, actorID, nodeID string, level int, input ProductionInput) (bool, error) {
	var existingLevel int
	var existingStatus string
	err := tx.QueryRowContext(ctx, `
		SELECT authorization_level, status
		FROM identity.actor_node_authorizations
		WHERE actor_id = $1
		  AND node_id = $2
	`, actorID, nodeID).Scan(&existingLevel, &existingStatus)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && existingLevel >= level && existingStatus == "active" {
		return false, nil
	}

	metadata, err := json.Marshal(productionMetadata(input, map[string]any{
		"bootstrap": true,
		"mode":      "production",
	}))
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO identity.actor_node_authorizations (
			authorization_id, actor_id, node_id, authorization_level, status, metadata
		)
		VALUES ($1, $2, $3, $4, 'active', $5::jsonb)
		ON CONFLICT (actor_id, node_id) DO UPDATE
		SET authorization_level = GREATEST(identity.actor_node_authorizations.authorization_level, EXCLUDED.authorization_level),
		    status = 'active',
		    metadata = identity.actor_node_authorizations.metadata || EXCLUDED.metadata
	`, authorizationID, actorID, nodeID, level, metadata)
	return err == nil, err
}

func ensureProductionProfileAssignment(ctx context.Context, tx *sql.Tx, nodeID, authorityProfileKey, runtimeProfileKey, serviceActorID string, input ProductionInput) (bool, error) {
	var existingAuthority, existingRuntime sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT ap.profile_key, rp.profile_key
		FROM nodes.node_profile_assignments assignment
		LEFT JOIN nodes.authority_profiles ap ON ap.authority_profile_id = assignment.authority_profile_id
		LEFT JOIN nodes.runtime_profiles rp ON rp.runtime_profile_id = assignment.runtime_profile_id
		WHERE assignment.node_id = $1
	`, nodeID).Scan(&existingAuthority, &existingRuntime)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && existingAuthority.String == authorityProfileKey && existingRuntime.String == runtimeProfileKey {
		return false, nil
	}

	metadata, err := json.Marshal(productionMetadata(input, map[string]any{
		"source": "production-bootstrap",
		"slice":  "v0.5",
	}))
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO nodes.node_profile_assignments (
			node_id, authority_profile_id, runtime_profile_id, assigned_by_actor_id, metadata
		)
		SELECT $1, ap.authority_profile_id, rp.runtime_profile_id, nullif($4, ''), $5::jsonb
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
	`, nodeID, authorityProfileKey, runtimeProfileKey, serviceActorID, metadata)
	if err != nil {
		return false, err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr == nil && rows == 0 {
		return false, fmt.Errorf("node profile assignment could not resolve authority=%s runtime=%s", authorityProfileKey, runtimeProfileKey)
	}
	return true, nil
}

func ensureProductionBootstrapEvent(ctx context.Context, tx *sql.Tx, input ProductionInput, serviceActorID, mainNodeID, systemScopeID string) (string, int, bool, error) {
	eventID, eventCount, err := productionBootstrapEventStatus(ctx, tx, input.NodeKey)
	if err != nil {
		return "", 0, false, err
	}
	if eventCount > 0 {
		return eventID, eventCount, false, nil
	}

	req := requestctx.Context{
		ActorID:       serviceActorID,
		ActorKey:      input.ServiceActorKey,
		OriginNodeID:  mainNodeID,
		OriginNodeKey: input.NodeKey,
		ScopeID:       systemScopeID,
		ScopeKey:      input.SystemScopeKey,
		CorrelationID: input.CorrelationID,
		FreshnessMode: "live_required",
		Source:        input.Source,
	}

	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeSystemBootstrapped,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         systemScopeID,
		TargetKind:      "system",
		TargetID:        systemScopeID,
		Status:          "created",
		Result:          "ok",
		Payload:         productionBootstrapPayload(input),
		VisibilityClass: "internal",
	})
	if err != nil {
		return "", 0, false, fmt.Errorf("append production bootstrap event: %w", err)
	}
	return event.EventID, 1, true, nil
}

type productionEventQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func productionBootstrapEventStatus(ctx context.Context, q productionEventQuerier, nodeKey string) (string, int, error) {
	var count int
	var eventID sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT count(*), (array_agg(event_id ORDER BY created_at ASC))[1]
		FROM events.events
		WHERE event_type = $1
		  AND payload->>'mode' = 'production'
		  AND payload->>'node_key' = $2
	`, events.TypeSystemBootstrapped, nodeKey).Scan(&count, &eventID)
	if err != nil {
		return "", 0, err
	}
	return eventID.String, count, nil
}

func productionBootstrapPayload(input ProductionInput) map[string]any {
	payload := map[string]any{
		"mode":                "production",
		"node_key":            input.NodeKey,
		"node_kind":           input.NodeKind,
		"node_role":           input.NodeRole,
		"runtime_class":       input.RuntimeClass,
		"owner_actor_key":     input.OwnerActorKey,
		"service_actor_key":   input.ServiceActorKey,
		"scheduler_actor_key": input.SchedulerActorKey,
		"system_scope_key":    input.SystemScopeKey,
	}
	if input.InstallID != "" {
		payload["setup_install_id"] = input.InstallID
	}
	if input.PlanHash != "" {
		payload["setup_plan_hash"] = input.PlanHash
	}
	if len(input.Metadata) > 0 {
		payload["metadata"] = input.Metadata
	}
	return payload
}

func productionMetadata(input ProductionInput, values map[string]any) map[string]any {
	out := make(map[string]any, len(values)+len(input.Metadata)+2)
	for key, value := range values {
		out[key] = value
	}
	if input.InstallID != "" {
		out["setup_install_id"] = input.InstallID
	}
	if input.PlanHash != "" {
		out["setup_plan_hash"] = input.PlanHash
	}
	for key, value := range input.Metadata {
		out[key] = value
	}
	return out
}

func productionAuthorizationID(prefix, actorKey, nodeKey string) string {
	return "auth_" + safeIdentifier(prefix) + "_" + safeIdentifier(actorKey) + "_" + safeIdentifier(nodeKey)
}

func safeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func slugToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

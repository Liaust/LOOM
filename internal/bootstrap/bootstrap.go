package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/db"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

type Summary struct {
	Ready               bool     `json:"ready"`
	Created             bool     `json:"created"`
	OwnerActorID        string   `json:"owner_actor_id,omitempty"`
	ServiceActorID      string   `json:"service_actor_id,omitempty"`
	SchedulerActorID    string   `json:"scheduler_actor_id,omitempty"`
	MainNodeID          string   `json:"main_node_id,omitempty"`
	SystemScopeID       string   `json:"system_scope_id,omitempty"`
	NodeScopeID         string   `json:"node_scope_id,omitempty"`
	ActorScopeID        string   `json:"actor_scope_id,omitempty"`
	BootstrapEventID    string   `json:"bootstrap_event_id,omitempty"`
	BootstrapEventCount int      `json:"bootstrap_event_count"`
	Missing             []string `json:"missing,omitempty"`
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func Check(ctx context.Context, dbURL string) Summary {
	sqlDB, err := db.OpenSQL(ctx, dbURL)
	if err != nil {
		return Summary{Ready: false, Missing: []string{"database"}}
	}
	defer sqlDB.Close()

	summary, err := NewService(sqlDB).BootstrapStatus(ctx)
	if err != nil {
		return Summary{Ready: false, Missing: []string{"bootstrap_status"}}
	}
	return summary
}

func (s Service) EnsureDevBootstrap(ctx context.Context) (Summary, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()

	owner, ownerCreated, err := ensureActor(ctx, tx, actorInput{
		Key:         "owner",
		DisplayName: "Owner",
		Kind:        "human",
		Metadata:    map[string]any{"bootstrap": true, "owner": true},
	})
	if err != nil {
		return Summary{}, err
	}

	serviceActor, serviceCreated, err := ensureActor(ctx, tx, actorInput{
		Key:         "service:loomd",
		DisplayName: "LOOM daemon",
		Kind:        "service",
		Metadata:    map[string]any{"bootstrap": true, "service": "loomd"},
	})
	if err != nil {
		return Summary{}, err
	}

	schedulerActor, schedulerCreated, err := ensureActor(ctx, tx, actorInput{
		Key:         "scheduler:loom",
		DisplayName: "LOOM scheduler",
		Kind:        "scheduler",
		Metadata:    map[string]any{"bootstrap": true, "scheduler": "loom"},
	})
	if err != nil {
		return Summary{}, err
	}

	devLowActor, devLowCreated, err := ensureActor(ctx, tx, actorInput{
		Key:         "agent:dev-low",
		DisplayName: "Dev Low Agent",
		Kind:        "agent",
		Metadata:    map[string]any{"bootstrap": true, "dev_fixture": true, "policy_fixture": true},
	})
	if err != nil {
		return Summary{}, err
	}

	mainNode, nodeCreated, err := ensureNode(ctx, tx, nodeInput{
		Key:          "main",
		DisplayName:  "Main node",
		Kind:         "server",
		Role:         "main",
		RuntimeClass: "main-node",
		OwnerActorID: owner.ID,
		Metadata:     map[string]any{"bootstrap": true},
	})
	if err != nil {
		return Summary{}, err
	}

	systemScope, systemCreated, err := ensureScope(ctx, tx, scopeInput{
		Type:         "system",
		Key:          "system",
		Slug:         "system",
		DisplayName:  "System",
		OwnerActorID: serviceActor.ID,
		HomeNodeID:   mainNode.ID,
		CreatedBy:    serviceActor.ID,
		Metadata:     map[string]any{"bootstrap": true},
	})
	if err != nil {
		return Summary{}, err
	}

	nodeScope, nodeScopeCreated, err := ensureScope(ctx, tx, scopeInput{
		Type:         "node",
		Key:          "node-main",
		Slug:         "node-main",
		DisplayName:  "Main node",
		OwnerActorID: owner.ID,
		HomeNodeID:   mainNode.ID,
		CreatedBy:    serviceActor.ID,
		Metadata:     map[string]any{"bootstrap": true, "node_key": "main"},
	})
	if err != nil {
		return Summary{}, err
	}

	actorScope, actorScopeCreated, err := ensureScope(ctx, tx, scopeInput{
		Type:         "actor",
		Key:          "actor-owner",
		Slug:         "actor-owner",
		DisplayName:  "Owner actor",
		OwnerActorID: owner.ID,
		HomeNodeID:   mainNode.ID,
		CreatedBy:    serviceActor.ID,
		Metadata:     map[string]any{"bootstrap": true, "actor_key": "owner"},
	})
	if err != nil {
		return Summary{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.actors
		SET home_node_id = $1, default_scope_id = $2, updated_at = now()
		WHERE actor_id = $3
	`, mainNode.ID, actorScope.ID, owner.ID); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.actors
		SET home_node_id = $1, default_scope_id = $2, updated_at = now()
		WHERE actor_id = $3
	`, mainNode.ID, systemScope.ID, serviceActor.ID); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.actors
		SET home_node_id = $1, default_scope_id = $2, updated_at = now()
		WHERE actor_id = $3
	`, mainNode.ID, systemScope.ID, schedulerActor.ID); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.actors
		SET home_node_id = $1, default_scope_id = $2, updated_at = now()
		WHERE actor_id = $3
	`, mainNode.ID, systemScope.ID, devLowActor.ID); err != nil {
		return Summary{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes.nodes
		SET home_scope_id = $1, updated_at = now()
		WHERE node_id = $2
	`, nodeScope.ID, mainNode.ID); err != nil {
		return Summary{}, err
	}

	authOwnerCreated, err := ensureAuthorization(ctx, tx, "auth_owner_main", owner.ID, mainNode.ID, 5)
	if err != nil {
		return Summary{}, err
	}
	authServiceCreated, err := ensureAuthorization(ctx, tx, "auth_service_loomd_main", serviceActor.ID, mainNode.ID, 5)
	if err != nil {
		return Summary{}, err
	}
	authSchedulerCreated, err := ensureAuthorization(ctx, tx, "auth_scheduler_loom_main", schedulerActor.ID, mainNode.ID, 5)
	if err != nil {
		return Summary{}, err
	}
	authDevLowCreated, err := ensureAuthorization(ctx, tx, "auth_agent_dev_low_main", devLowActor.ID, mainNode.ID, 1)
	if err != nil {
		return Summary{}, err
	}

	created := ownerCreated || serviceCreated || schedulerCreated || devLowCreated || nodeCreated || systemCreated || nodeScopeCreated || actorScopeCreated || authOwnerCreated || authServiceCreated || authSchedulerCreated || authDevLowCreated

	eventID, eventCreated, err := ensureBootstrapEvent(ctx, tx, serviceActor.ID, mainNode.ID, systemScope.ID)
	if err != nil {
		return Summary{}, err
	}
	created = created || eventCreated

	if err := tx.Commit(); err != nil {
		return Summary{}, err
	}

	status, err := s.BootstrapStatus(ctx)
	if err != nil {
		return Summary{}, err
	}
	status.Created = created
	if eventID != "" {
		status.BootstrapEventID = eventID
	}
	return status, nil
}

func (s Service) BootstrapStatus(ctx context.Context) (Summary, error) {
	summary := Summary{}

	records := []struct {
		name  string
		query string
		dest  *string
	}{
		{"owner actor", "SELECT actor_id FROM identity.actors WHERE actor_key = 'owner'", &summary.OwnerActorID},
		{"service actor", "SELECT actor_id FROM identity.actors WHERE actor_key = 'service:loomd'", &summary.ServiceActorID},
		{"scheduler actor", "SELECT actor_id FROM identity.actors WHERE actor_key = 'scheduler:loom'", &summary.SchedulerActorID},
		{"main node", "SELECT node_id FROM nodes.nodes WHERE node_key = 'main'", &summary.MainNodeID},
		{"system scope", "SELECT scope_id FROM scopes.scopes WHERE scope_key = 'system'", &summary.SystemScopeID},
		{"node scope", "SELECT scope_id FROM scopes.scopes WHERE scope_key = 'node-main'", &summary.NodeScopeID},
		{"actor scope", "SELECT scope_id FROM scopes.scopes WHERE scope_key = 'actor-owner'", &summary.ActorScopeID},
	}

	for _, record := range records {
		err := s.DB.QueryRowContext(ctx, record.query).Scan(record.dest)
		if err == sql.ErrNoRows {
			summary.Missing = append(summary.Missing, record.name)
			continue
		}
		if err != nil {
			return Summary{}, err
		}
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM events.events
		WHERE event_type = 'system.bootstrapped'
	`).Scan(&summary.BootstrapEventCount); err != nil {
		return Summary{}, err
	}
	if summary.BootstrapEventCount == 0 {
		summary.Missing = append(summary.Missing, "system.bootstrapped event")
	}

	summary.Ready = len(summary.Missing) == 0 && summary.BootstrapEventCount == 1
	return summary, nil
}

type keyedRecord struct {
	ID string
}

type actorInput struct {
	Key         string
	DisplayName string
	Kind        string
	Metadata    map[string]any
}

type nodeInput struct {
	Key          string
	DisplayName  string
	Kind         string
	Role         string
	RuntimeClass string
	OwnerActorID string
	Metadata     map[string]any
}

type scopeInput struct {
	Type         string
	Key          string
	Slug         string
	DisplayName  string
	OwnerActorID string
	HomeNodeID   string
	CreatedBy    string
	Metadata     map[string]any
}

func ensureActor(ctx context.Context, tx *sql.Tx, input actorInput) (keyedRecord, bool, error) {
	id := ids.NewActorID()
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return keyedRecord{}, false, err
	}

	var out keyedRecord
	err = tx.QueryRowContext(ctx, `
		INSERT INTO identity.actors (actor_id, actor_key, display_name, actor_kind, status, metadata)
		VALUES ($1, $2, $3, $4, 'active', $5)
		ON CONFLICT (actor_key) DO NOTHING
		RETURNING actor_id
	`, id, input.Key, input.DisplayName, input.Kind, metadata).Scan(&out.ID)
	if err == nil {
		return out, true, nil
	}
	if err != sql.ErrNoRows {
		return keyedRecord{}, false, err
	}

	err = tx.QueryRowContext(ctx, `SELECT actor_id FROM identity.actors WHERE actor_key = $1`, input.Key).Scan(&out.ID)
	return out, false, err
}

func ensureNode(ctx context.Context, tx *sql.Tx, input nodeInput) (keyedRecord, bool, error) {
	id := ids.NewNodeID()
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return keyedRecord{}, false, err
	}

	var out keyedRecord
	err = tx.QueryRowContext(ctx, `
		INSERT INTO nodes.nodes (
			node_id, node_key, display_name, node_kind, node_role,
			runtime_class, status, owner_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'active', $7, $8)
		ON CONFLICT (node_key) DO NOTHING
		RETURNING node_id
	`, id, input.Key, input.DisplayName, input.Kind, input.Role, input.RuntimeClass, input.OwnerActorID, metadata).Scan(&out.ID)
	if err == nil {
		return out, true, nil
	}
	if err != sql.ErrNoRows {
		return keyedRecord{}, false, err
	}

	err = tx.QueryRowContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_key = $1`, input.Key).Scan(&out.ID)
	return out, false, err
}

func ensureScope(ctx context.Context, tx *sql.Tx, input scopeInput) (keyedRecord, bool, error) {
	id := ids.NewScopeID()
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return keyedRecord{}, false, err
	}

	var out keyedRecord
	err = tx.QueryRowContext(ctx, `
		INSERT INTO scopes.scopes (
			scope_id, scope_type, scope_key, slug, display_name,
			owner_actor_id, home_node_id, status, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), 'active', nullif($8, ''), $9)
		ON CONFLICT (scope_key) DO NOTHING
		RETURNING scope_id
	`, id, input.Type, input.Key, input.Slug, input.DisplayName, input.OwnerActorID, input.HomeNodeID, input.CreatedBy, metadata).Scan(&out.ID)
	if err == nil {
		return out, true, nil
	}
	if err != sql.ErrNoRows {
		return keyedRecord{}, false, err
	}

	err = tx.QueryRowContext(ctx, `SELECT scope_id FROM scopes.scopes WHERE scope_key = $1`, input.Key).Scan(&out.ID)
	return out, false, err
}

func ensureAuthorization(ctx context.Context, tx *sql.Tx, authorizationID, actorID, nodeID string, level int) (bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO identity.actor_node_authorizations (
			authorization_id, actor_id, node_id, authorization_level, status, metadata
		)
		VALUES ($1, $2, $3, $4, 'active', '{"bootstrap":true}'::jsonb)
		ON CONFLICT (actor_id, node_id) DO NOTHING
		RETURNING authorization_id
	`, authorizationID, actorID, nodeID, level).Scan(&id)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func ensureBootstrapEvent(ctx context.Context, tx *sql.Tx, serviceActorID, mainNodeID, systemScopeID string) (string, bool, error) {
	var existing string
	err := tx.QueryRowContext(ctx, `
		SELECT event_id
		FROM events.events
		WHERE event_type = 'system.bootstrapped'
		ORDER BY created_at ASC
		LIMIT 1
	`).Scan(&existing)
	if err == nil {
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}

	req := requestctx.Context{
		ActorID:       serviceActorID,
		ActorKey:      "service:loomd",
		OriginNodeID:  mainNodeID,
		OriginNodeKey: "main",
		ScopeID:       systemScopeID,
		ScopeKey:      "system",
		CorrelationID: "bootstrap",
		FreshnessMode: "live_required",
		Source:        "dev-bootstrap",
	}

	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeSystemBootstrapped,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    systemScopeID,
		TargetKind: "system",
		TargetID:   systemScopeID,
		Status:     "created",
		Result:     "ok",
		Payload: map[string]any{
			"owner_actor_key":   "owner",
			"service_actor_key": "service:loomd",
			"main_node_key":     "main",
			"system_scope_key":  "system",
		},
		VisibilityClass: "internal",
	})
	if err != nil {
		return "", false, fmt.Errorf("append bootstrap event: %w", err)
	}
	return event.EventID, true, nil
}

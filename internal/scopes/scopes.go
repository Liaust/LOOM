package scopes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

type Scope struct {
	ScopeID          string          `json:"scope_id"`
	ScopeType        string          `json:"scope_type"`
	ScopeKey         string          `json:"scope_key"`
	Slug             string          `json:"slug"`
	DisplayName      string          `json:"display_name"`
	OwnerActorID     *string         `json:"owner_actor_id,omitempty"`
	HomeNodeID       *string         `json:"home_node_id,omitempty"`
	ParentScopeID    *string         `json:"parent_scope_id,omitempty"`
	Status           string          `json:"status"`
	CreatedByActorID *string         `json:"created_by_actor_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ArchivedAt       *time.Time      `json:"archived_at,omitempty"`
	Metadata         json.RawMessage `json:"metadata"`
}

type CreateInput struct {
	ScopeType   string `json:"scope_type"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	IfNotExists bool   `json:"if_not_exists"`
}

type CreateResult struct {
	Scope   Scope  `json:"scope"`
	Created bool   `json:"created"`
	EventID string `json:"event_id,omitempty"`
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) ListScopes(ctx context.Context, limit int) ([]Scope, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.DB.QueryContext(ctx, scopeSelectSQL()+` ORDER BY scope_type, slug LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scopes []Scope
	for rows.Next() {
		scope, err := scanScope(rows)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

func (s Service) GetScope(ctx context.Context, ref string) (Scope, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Scope{}, fmt.Errorf("scope ref is required")
	}

	row := s.DB.QueryRowContext(ctx, scopeSelectSQL()+`
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		ORDER BY
			CASE
				WHEN scope_id = $1 THEN 0
				WHEN scope_key = $1 THEN 1
				ELSE 2
			END
		LIMIT 1
	`, ref)

	return scanScope(row)
}

func (s Service) ResolveScopeRef(ctx context.Context, ref string) (string, error) {
	scope, err := s.GetScope(ctx, ref)
	if err != nil {
		return "", err
	}
	return scope.ScopeID, nil
}

func (s Service) CreateProjectScope(ctx context.Context, req requestctx.Context, input CreateInput) (CreateResult, error) {
	input.ScopeType = strings.TrimSpace(input.ScopeType)
	if input.ScopeType == "" {
		input.ScopeType = "project"
	}
	if input.ScopeType != "project" {
		return CreateResult{}, fmt.Errorf("Slice 2 only supports creating project scopes")
	}

	slug := strings.TrimSpace(input.Slug)
	if !slugPattern.MatchString(slug) {
		return CreateResult{}, fmt.Errorf("project scope slug must be lowercase URL-safe and 3-64 characters")
	}

	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = slug
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CreateResult{}, err
	}
	defer tx.Rollback()

	existing, err := getScopeTx(ctx, tx, "project:"+slug)
	if err == nil {
		if input.IfNotExists {
			return CreateResult{Scope: existing, Created: false}, tx.Commit()
		}
		return CreateResult{}, fmt.Errorf("scope already exists: %s", slug)
	}
	if err != sql.ErrNoRows {
		return CreateResult{}, err
	}

	scopeID := ids.NewScopeID()
	scopeKey := "project:" + slug
	var metadata = []byte(`{"slice":"2","project_record":"deferred_to_slice_3"}`)

	scope, err := insertScopeTx(ctx, tx, scopeID, "project", scopeKey, slug, displayName, req.ActorID, req.OriginNodeID, nil, req.ActorID, metadata)
	if err != nil {
		return CreateResult{}, err
	}

	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeScopeCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    scope.ScopeID,
		TargetKind: "scope",
		TargetID:   scope.ScopeID,
		Status:     "created",
		Result:     "ok",
		Payload: map[string]any{
			"scope_type": scope.ScopeType,
			"scope_key":  scope.ScopeKey,
			"slug":       scope.Slug,
		},
		VisibilityClass: "internal",
	})
	if err != nil {
		return CreateResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return CreateResult{}, err
	}

	return CreateResult{Scope: scope, Created: true, EventID: event.EventID}, nil
}

func insertScopeTx(
	ctx context.Context,
	tx *sql.Tx,
	scopeID,
	scopeType,
	scopeKey,
	slug,
	displayName,
	ownerActorID,
	homeNodeID string,
	parentScopeID *string,
	createdByActorID string,
	metadata []byte,
) (Scope, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO scopes.scopes (
			scope_id, scope_type, scope_key, slug, display_name, owner_actor_id,
			home_node_id, parent_scope_id, status, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8, 'active', nullif($9, ''), $10)
		RETURNING scope_id, scope_type, scope_key, slug, display_name, owner_actor_id,
		          home_node_id, parent_scope_id, status, created_by_actor_id,
		          created_at, updated_at, archived_at, metadata
	`,
		scopeID,
		scopeType,
		scopeKey,
		slug,
		displayName,
		ownerActorID,
		homeNodeID,
		parentScopeID,
		createdByActorID,
		metadata,
	)
	return scanScope(row)
}

func getScopeTx(ctx context.Context, tx *sql.Tx, ref string) (Scope, error) {
	row := tx.QueryRowContext(ctx, scopeSelectSQL()+`
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		LIMIT 1
	`, ref)
	return scanScope(row)
}

func scopeSelectSQL() string {
	return `
		SELECT scope_id, scope_type, scope_key, slug, display_name, owner_actor_id,
		       home_node_id, parent_scope_id, status, created_by_actor_id,
		       created_at, updated_at, archived_at, metadata
		FROM scopes.scopes
	`
}

type scopeScanner interface {
	Scan(dest ...any) error
}

func scanScope(scanner scopeScanner) (Scope, error) {
	var scope Scope
	var metadata []byte
	var ownerActorID sql.NullString
	var homeNodeID sql.NullString
	var parentScopeID sql.NullString
	var createdByActorID sql.NullString
	var archivedAt sql.NullTime
	if err := scanner.Scan(
		&scope.ScopeID,
		&scope.ScopeType,
		&scope.ScopeKey,
		&scope.Slug,
		&scope.DisplayName,
		&ownerActorID,
		&homeNodeID,
		&parentScopeID,
		&scope.Status,
		&createdByActorID,
		&scope.CreatedAt,
		&scope.UpdatedAt,
		&archivedAt,
		&metadata,
	); err != nil {
		return Scope{}, err
	}
	scope.OwnerActorID = stringPtr(ownerActorID)
	scope.HomeNodeID = stringPtr(homeNodeID)
	scope.ParentScopeID = stringPtr(parentScopeID)
	scope.CreatedByActorID = stringPtr(createdByActorID)
	scope.ArchivedAt = timePtr(archivedAt)
	scope.Metadata = json.RawMessage(metadata)
	if len(scope.Metadata) == 0 {
		scope.Metadata = json.RawMessage(`{}`)
	}
	return scope, nil
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
